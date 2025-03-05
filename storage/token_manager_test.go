package storage

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

func TestTokenManagerImpl(t *testing.T) {
	// Create temp directory for cache files
	tempDir, err := os.MkdirTemp("", "token-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Set HOME to temp dir for cache file testing
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)

	// Setup mock OAuth server
	var tokenCount int
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenCount++
		expiresIn := 3600 // 1 hour
		resp := map[string]interface{}{
			"access_token": "mock-token-" + string(rune(tokenCount+'0')),
			"token_type":   "Bearer",
			"expires_in":   expiresIn,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer mockServer.Close()

	// Create mock credentials JSON
	credsJSON := []byte(`{
		"type": "service_account",
		"audience": "test-audience",
		"token_url": "` + mockServer.URL + `"
	}`)

	// Create mock credentials
	mockCreds := &google.Credentials{
		JSON: credsJSON,
		TokenSource: oauth2.ReuseTokenSource(nil, oauth2.StaticTokenSource(&oauth2.Token{
			AccessToken: "initial-token",
			TokenType:   "Bearer",
			Expiry:      time.Now().Add(1 * time.Hour),
		})),
	}

	// Patch findDefaultCredentials to return our mock
	origFindDefaultCredentials := findDefaultCredentials
	findDefaultCredentials = func(ctx context.Context, scopes ...string) (*google.Credentials, error) {
		return mockCreds, nil
	}
	defer func() { findDefaultCredentials = origFindDefaultCredentials }()

	t.Run("GetToken returns valid token", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		manager, err := NewTokenManager(ctx, nil)
		if err != nil {
			t.Fatalf("Failed to create token manager: %v", err)
		}
		defer manager.Stop()

		token, err := manager.GetToken()
		if err != nil {
			t.Fatalf("GetToken failed: %v", err)
		}
		if token.AccessToken == "" {
			t.Error("Expected non-empty access token")
		}
		if !token.Valid() {
			t.Error("Expected valid token")
		}
	})

	t.Run("Token is cached to file", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		manager, err := NewTokenManager(ctx, nil)
		if err != nil {
			t.Fatalf("Failed to create token manager: %v", err)
		}
		defer manager.Stop()

		// Get initial token to trigger cache write
		_, err = manager.GetToken()
		if err != nil {
			t.Fatalf("GetToken failed: %v", err)
		}

		// Check cache file exists
		cacheFile := filepath.Join(tempDir, ".cache", "coo", "cached_s5cmd.json")
		if _, err := os.Stat(cacheFile); os.IsNotExist(err) {
			t.Error("Cache file was not created")
		}

		// Verify cache file content
		data, err := os.ReadFile(cacheFile)
		if err != nil {
			t.Fatalf("Failed to read cache file: %v", err)
		}

		var cachedInfo CachedTokenInfo
		if err := json.Unmarshal(data, &cachedInfo); err != nil {
			t.Fatalf("Failed to unmarshal cache data: %v", err)
		}

		if cachedInfo.Audience != "test-audience" {
			t.Errorf("Expected audience 'test-audience', got %q", cachedInfo.Audience)
		}
		if cachedInfo.Token == nil || cachedInfo.Token.AccessToken == "" {
			t.Error("Cache file did not contain valid token")
		}
	})

	t.Run("Token refresh before expiry", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		// Create test credentials for token tests
		manager, err := NewTokenManager(ctx, nil)
		if err != nil {
			t.Fatalf("Failed to create token manager: %v", err)
		}
		defer manager.Stop()

		// Force the token to be expired
		impl := manager.(*tokenManagerImpl)
		impl.token = &oauth2.Token{
			AccessToken: "original-token",
			TokenType:   "Bearer",
			Expiry:      time.Now().Add(-1 * time.Minute),
		}

		// Call refresh synchronously before releasing lock
		err = impl.refresh()
		if err != nil {
			t.Fatalf("Token refresh failed: %v", err)
		}

		// Get token - should be the refreshed token
		newToken, err := manager.GetToken()
		if err != nil {
			t.Fatalf("GetToken failed: %v", err)
		}

		if newToken.AccessToken == "original-token" {
			t.Error("Token was not refreshed")
		}
	})

	t.Run("Stop cancels background refresh", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		manager, err := NewTokenManager(ctx, nil)
		if err != nil {
			t.Fatalf("Failed to create token manager: %v", err)
		}

		// Get initial token
		_, err = manager.GetToken()
		if err != nil {
			t.Fatalf("GetToken failed: %v", err)
		}

		// Stop manager - implicitly waits for shutdown now
		manager.Stop()

		// Try to get token after stop
		_, err = manager.GetToken()
		if err == nil {
			t.Error("Expected error after stopping manager")
		}
		if err != context.Canceled {
			t.Errorf("Expected context.Canceled, got %v", err)
		}
	})

	t.Run("GetToken respects context cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		manager, err := NewTokenManager(ctx, nil)
		if err != nil {
			t.Fatalf("Failed to create token manager: %v", err)
		}
		defer manager.Stop()

		// Force token to be invalid
		impl := manager.(*tokenManagerImpl)
		impl.token = nil

		// Create a goroutine to cancel context after a short delay
		go func() {
			time.Sleep(100 * time.Millisecond)
			cancel()
		}()

		// Try to get token - should fail due to context cancellation
		_, err = manager.GetToken()
		if err == nil {
			t.Error("Expected error due to context cancellation")
		}
		if err != context.Canceled {
			t.Errorf("Expected context.Canceled, got %v", err)
		}
	})

	t.Run("Respects cache opt-out", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Set opt-out env var
		os.Setenv("S5CMD_FILE_CACHING_OPT_OUT", "1")
		defer os.Unsetenv("S5CMD_FILE_CACHING_OPT_OUT")

		manager, err := NewTokenManager(ctx, nil)
		if err != nil {
			t.Fatalf("Failed to create token manager: %v", err)
		}
		defer manager.Stop()

		// Cache path to check
		cacheFile := filepath.Join(tempDir, ".cache", "coo", "cached_s5cmd.json")

		// Make sure cache file is cleared before test
		os.Remove(cacheFile)

		// Get token to potentially trigger cache
		_, err = manager.GetToken()
		if err != nil {
			t.Fatalf("GetToken failed: %v", err)
		}

		// Check cache file does not exist - cache writing happens synchronously in the implementation
		if _, err := os.Stat(cacheFile); err == nil {
			t.Error("Cache file was created despite opt-out")
		} else if !os.IsNotExist(err) {
			t.Errorf("Unexpected error checking cache file: %v", err)
		}
	})

	t.Run("Jitter applies correct duration adjustments", func(t *testing.T) {
		// Save the original function
		originalRandFloat64 := randFloat64
		defer func() { randFloat64 = originalRandFloat64 }()
		
		tests := []struct {
			name           string
			base           time.Duration
			percent        float64
			mockRandValue  float64
			expectedResult time.Duration
		}{
			{
				name:           "No jitter (0%)",
				base:           time.Second,
				percent:        0,
				mockRandValue:  0.5, // This value doesn't matter when percent is 0
				expectedResult: time.Second,
			},
			{
				name:           "Negative jitter should be clamped to 0%",
				base:           time.Second,
				percent:        -0.2,
				mockRandValue:  0.5, // This value doesn't matter when percent is clamped to 0
				expectedResult: time.Second,
			},
			{
				name:           "Excessive jitter should be clamped to 100%",
				base:           time.Second,
				percent:        1.5,
				mockRandValue:  0, // This will produce a -1 after adjustment
				expectedResult: 0 * time.Second, // Base - 100%
			},
			{
				name:           "Excessive jitter should be clamped to 100% (upper bound)",
				base:           time.Second,
				percent:        1.5,
				mockRandValue:  1, // This will produce a +1 after adjustment
				expectedResult: 2 * time.Second, // Base + 100%
			},
			{
				name:           "50% jitter, minimum value",
				base:           time.Second,
				percent:        0.5,
				mockRandValue:  0, // This will produce a -0.5 after adjustment
				expectedResult: 500 * time.Millisecond, // Base - 50%
			},
			{
				name:           "50% jitter, median value",
				base:           time.Second,
				percent:        0.5,
				mockRandValue:  0.5, // This will produce a 0 after adjustment
				expectedResult: time.Second, // Base + 0%
			},
			{
				name:           "50% jitter, maximum value",
				base:           time.Second,
				percent:        0.5,
				mockRandValue:  1, // This will produce a +0.5 after adjustment
				expectedResult: 1500 * time.Millisecond, // Base + 50%
			},
		}
		
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				// Mock the random function to return a specific value
				randFloat64 = func() float64 { return tc.mockRandValue }
				
				// Call jitter with the test case values
				result := jitter(tc.base, tc.percent)
				
				// Verify the result matches the expected value
				if result != tc.expectedResult {
					t.Errorf("Expected %v, got %v", tc.expectedResult, result)
				}
			})
		}
	})
}
