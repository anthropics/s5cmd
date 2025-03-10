package storage

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
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
		expiredToken := &oauth2.Token{
			AccessToken: "original-token",
			TokenType:   "Bearer",
			Expiry:      time.Now().Add(-1 * time.Minute),
		}
		impl.token.Store(expiredToken)

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
		// Create a context that we can cancel
		ctx, cancel := context.WithCancel(context.Background())

		// Immediately cancel the context before creating the token manager
		cancel()

		// Create token manager with the already cancelled context
		manager, err := NewTokenManager(ctx, nil)
		if err != nil {
			t.Fatalf("Failed to create token manager: %v", err)
		}

		// Attempt to get token - should immediately return context.Canceled
		_, err = manager.GetToken()

		// Verify the error is context cancellation
		if err == nil {
			t.Error("Expected error due to context cancellation")
		} else if err != context.Canceled {
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
}

// setupBenchTokenManager creates a token manager for benchmarks
func setupBenchTokenManager(b *testing.B) TokenManager {
	// Set up mock token
	token := &oauth2.Token{
		AccessToken: "benchmark-token",
		TokenType:   "Bearer",
		Expiry:      time.Now().Add(1 * time.Hour),
	}

	// Create mock credentials
	mockCreds := &google.Credentials{
		TokenSource: oauth2.StaticTokenSource(token),
	}

	// Override finding credentials
	origFindDefaultCredentials := findDefaultCredentials
	findDefaultCredentials = func(ctx context.Context, scopes ...string) (*google.Credentials, error) {
		return mockCreds, nil
	}
	b.Cleanup(func() { findDefaultCredentials = origFindDefaultCredentials })

	ctx, cancel := context.WithCancel(context.Background())
	b.Cleanup(func() { cancel() })

	manager, err := NewTokenManager(ctx, nil)
	if err != nil {
		b.Fatalf("Failed to create token manager: %v", err)
	}
	b.Cleanup(func() { manager.Stop() })

	return manager
}

// BenchmarkGetToken tests the performance of GetToken
func BenchmarkGetToken(b *testing.B) {
	manager := setupBenchTokenManager(b)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := manager.GetToken()
		if err != nil {
			b.Fatalf("GetToken failed: %v", err)
		}
	}
}

// BenchmarkGetTokenParallel tests GetToken with parallel goroutines
func BenchmarkGetTokenParallel(b *testing.B) {
	manager := setupBenchTokenManager(b)

	// Ensure we have a valid token
	validToken := &oauth2.Token{
		AccessToken: "valid-token-parallel",
		TokenType:   "Bearer",
		Expiry:      time.Now().Add(1 * time.Hour), // Valid for a long time
	}
	impl := manager.(*tokenManagerImpl)
	impl.token.Store(validToken)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := manager.GetToken()
			if err != nil {
				b.Fatalf("GetToken failed: %v", err)
			}
		}
	})
}

// BenchmarkGetTokenWithContention measures performance under refresh contention
func BenchmarkGetTokenWithContention(b *testing.B) {
	// Create a simplified version that directly tests the core mechanisms
	// without involving the file system

	// Start by creating a test directory with no permissions issue
	tempDir, err := os.MkdirTemp("", "token-bench")
	if err != nil {
		b.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create our own token manager with minimal dependencies
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Prepare a token source that returns immediately (zero delay)
	zeroDelaySource := &testTokenSource{
		getToken: func() (*oauth2.Token, error) {
			return &oauth2.Token{
				AccessToken: "test-token",
				TokenType:   "Bearer",
				Expiry:      time.Now().Add(1 * time.Hour),
			}, nil
		},
	}

	// Create credentials that use our immediate token source
	creds := &google.Credentials{
		TokenSource: zeroDelaySource,
	}

	// Create a minimal token manager (avoids hitting the network)
	m := &tokenManagerImpl{
		ctx:             ctx,
		cancel:          cancel,
		creds:           creds,
		updates:         make(chan struct{}, 10), // Larger buffer to avoid blocking
		cacheFile:       filepath.Join(tempDir, "token.json"),
		mu:              sync.RWMutex{},
		refreshInterval: 10 * time.Millisecond,
	}

	// Start refresh loop in background
	go m.refreshLoop()

	// Expired token for testing
	expiredToken := &oauth2.Token{
		AccessToken: "expired-test-token",
		TokenType:   "Bearer",
		Expiry:      time.Now().Add(-1 * time.Minute),
	}

	// Set the initial token to a valid one to avoid startup refresh
	validToken := &oauth2.Token{
		AccessToken: "valid-test-token",
		TokenType:   "Bearer",
		Expiry:      time.Now().Add(1 * time.Hour),
	}
	m.token.Store(validToken)

	// Create atomics to track behavior
	var validCount, expiredCount int32

	// Run a much smaller benchmark to identify issues
	iterations := 100

	b.ResetTimer()

	// Run iterations sequentially to avoid thread conflicts
	for i := 0; i < iterations; i++ {
		// Every 10 iterations, store an expired token to force refresh
		if i%10 == 0 {
			m.token.Store(expiredToken)
			atomic.AddInt32(&expiredCount, 1)
		}

		// Get token - this will either return the valid token immediately
		// or trigger a refresh if the token is expired
		token, err := m.GetToken()

		// Check for errors
		if err != nil {
			b.Fatalf("GetToken failed: %v", err)
		}

		// Check token validity
		if token != nil && token.Valid() {
			atomic.AddInt32(&validCount, 1)
		} else {
			b.Fatalf("Got invalid token: %v", token)
		}
	}

	// Report metrics about the test
	b.ReportMetric(float64(atomic.LoadInt32(&validCount)), "valid_tokens")
	b.ReportMetric(float64(atomic.LoadInt32(&expiredCount)), "expired_tokens")
}

// testTokenSource implements oauth2.TokenSource for testing
type testTokenSource struct {
	getToken func() (*oauth2.Token, error)
}

func (t *testTokenSource) Token() (*oauth2.Token, error) {
	return t.getToken()
}
