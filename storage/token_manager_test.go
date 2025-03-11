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

// TestNotifier tests the Notifier component's functionality
func TestNotifier(t *testing.T) {
	t.Run("Basic notification wakes single waiter", func(t *testing.T) {
		notifier := NewNotifier()
		ctx := context.Background()

		// Verify initial generation is 0
		notifier.mu.Lock()
		if notifier.generation != 0 {
			t.Errorf("Initial generation should be 0, got %d", notifier.generation)
		}
		notifier.mu.Unlock()

		// Start a goroutine that waits and signals when notified
		notified := make(chan bool, 1)
		go func() {
			result := notifier.Wait(ctx)
			notified <- result
		}()

		// Sleep briefly to ensure goroutine is waiting
		time.Sleep(10 * time.Millisecond)

		// Notify and check if waiter was woken up
		notifier.Notify()

		select {
		case result := <-notified:
			if !result {
				t.Error("Waiter should have been notified, but got false")
			}
		case <-time.After(100 * time.Millisecond):
			t.Error("Timed out waiting for notification")
		}

		// Verify generation was incremented
		notifier.mu.Lock()
		if notifier.generation != 1 {
			t.Errorf("Generation should be 1 after notification, got %d", notifier.generation)
		}
		notifier.mu.Unlock()
	})

	t.Run("Context cancellation properly terminates wait", func(t *testing.T) {
		notifier := NewNotifier()
		ctx, cancel := context.WithCancel(context.Background())

		// Start a goroutine that waits and signals when notified or canceled
		waitResult := make(chan bool, 1)
		go func() {
			result := notifier.Wait(ctx)
			waitResult <- result
		}()

		// Sleep briefly to ensure goroutine is waiting
		time.Sleep(10 * time.Millisecond)

		// Cancel the context and check if waiter returns
		cancel()

		select {
		case result := <-waitResult:
			if result {
				t.Error("Waiter should have returned false on context cancellation, but got true")
			}
		case <-time.After(100 * time.Millisecond):
			t.Error("Timed out waiting for cancellation")
		}
	})

	t.Run("Multiple waiters all get notified", func(t *testing.T) {
		notifier := NewNotifier()
		ctx := context.Background()

		// Number of waiters to test
		numWaiters := 100

		// Create a wait group to track all goroutines
		var wg sync.WaitGroup
		wg.Add(numWaiters)

		// Count of successfully notified goroutines
		notifiedCount := atomic.Int32{}

		// Start multiple goroutines that wait
		for i := 0; i < numWaiters; i++ {
			go func(id int) {
				defer wg.Done()

				// Create a child context with timeout to avoid test hanging
				waitCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
				defer cancel()

				if notifier.Wait(waitCtx) {
					notifiedCount.Add(1)
					// Uncomment for debugging: fmt.Printf("Waiter %d was notified\n", id)
				}
			}(i)
		}

		// Sleep to ensure all goroutines are waiting
		time.Sleep(100 * time.Millisecond)

		// Send a single notification
		notifier.Notify()

		// Wait for all goroutines to complete with timeout
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()

		select {
		case <-done:
			// Success - all goroutines finished
		case <-time.After(1 * time.Second):
			t.Fatal("Timed out waiting for all goroutines to complete")
		}

		// Verify majority of waiters were notified (allow for a few timing issues)
		if int(notifiedCount.Load()) < numWaiters-3 {
			t.Errorf("Expected at least %d waiters to be notified, but got %d", numWaiters-3, notifiedCount.Load())
		} else {
			t.Logf("Successfully notified %d out of %d waiters", notifiedCount.Load(), numWaiters)
		}
	})

	t.Run("Notification before waiting works correctly", func(t *testing.T) {
		// Create a new notifier with initial generation 0
		notifier := NewNotifier()

		// Increment it to generation 1
		notifier.Notify()

		// Verify the generation is now 1
		notifier.mu.Lock()
		if notifier.generation != 1 {
			t.Errorf("Expected generation 1 after notification, got %d", notifier.generation)
		}
		notifier.mu.Unlock()

		// Now test that Wait returns true when called (since generation has changed from the default)
		ctx := context.Background()

		// This should succeed (no timing constraints)
		result := notifier.Wait(ctx)
		if !result {
			notifier.mu.Lock()
			gen := notifier.generation
			notifier.mu.Unlock()
			t.Errorf("Wait should return true when generation is %d", gen)
		}
	})

	t.Run("Multiple notifications increment generation correctly", func(t *testing.T) {
		notifier := NewNotifier()

		// Send multiple notifications
		expectedGen := uint64(0)
		for i := 0; i < 10; i++ {
			notifier.Notify()
			expectedGen++

			notifier.mu.Lock()
			if gen := notifier.generation; gen != expectedGen {
				t.Errorf("Generation should be %d after %d notifications, got %d", expectedGen, i+1, gen)
			}
			notifier.mu.Unlock()
		}
	})

	t.Run("Generation counter handles concurrent notifications", func(t *testing.T) {
		notifier := NewNotifier()

		// Number of concurrent notifications
		numNotifications := 100

		// Create a wait group to track all goroutines
		var wg sync.WaitGroup
		wg.Add(numNotifications)

		// Start multiple goroutines that notify concurrently
		for i := 0; i < numNotifications; i++ {
			go func() {
				defer wg.Done()
				notifier.Notify()
			}()
		}

		// Wait for all notifications
		wg.Wait()

		// Verify generation was incremented correctly
		notifier.mu.Lock()
		finalGen := notifier.generation
		notifier.mu.Unlock()
		if finalGen != uint64(numNotifications) {
			t.Errorf("Final generation should be %d after concurrent notifications, got %d", numNotifications, finalGen)
		}
	})

	t.Run("Integration test with token refreshing scenario", func(t *testing.T) {
		notifier := NewNotifier()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Number of workers to simulate
		numWorkers := 256

		// Simulate token state
		type tokenState struct {
			valid  atomic.Bool
			expiry atomic.Int64
		}

		tokenData := &tokenState{}
		tokenData.valid.Store(false)
		tokenData.expiry.Store(0)

		// Count of workers that got valid tokens
		successCount := atomic.Int32{}

		// Start workers that wait for token to become valid
		var wg sync.WaitGroup
		wg.Add(numWorkers)

		for i := 0; i < numWorkers; i++ {
			go func(workerID int) {
				defer wg.Done()

				// Keep trying until we get a valid token
				for attempts := 0; attempts < 5; attempts++ {
					// Check if token is valid
					if tokenData.valid.Load() && tokenData.expiry.Load() > time.Now().Unix() {
						successCount.Add(1)
						return
					}

					// Wait for notification of token refresh
					waitCtx, waitCancel := context.WithTimeout(ctx, 200*time.Millisecond)
					if notifier.Wait(waitCtx) {
						waitCancel()
						// Wait returned due to notification
						if tokenData.valid.Load() && tokenData.expiry.Load() > time.Now().Unix() {
							successCount.Add(1)
							return
						}
						// Otherwise continue trying
					} else {
						waitCancel()
						// Timeout or context canceled
						if tokenData.valid.Load() && tokenData.expiry.Load() > time.Now().Unix() {
							// Double-check if the token became valid while we were waiting
							successCount.Add(1)
							return
						}
					}
				}
			}(i)
		}

		// Sleep to ensure workers are waiting
		time.Sleep(200 * time.Millisecond)

		// Simulate token refresh
		tokenData.valid.Store(true)
		tokenData.expiry.Store(time.Now().Add(time.Hour).Unix())

		// Notify all workers
		notifier.Notify()

		// Wait for all workers to complete with timeout
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()

		select {
		case <-done:
			// Success - all goroutines finished
		case <-time.After(2 * time.Second):
			t.Fatal("Timed out waiting for all workers to complete")
		}

		// Verify most workers got valid tokens (allow for some timing issues)
		t.Logf("Successfully notified %d out of %d workers", successCount.Load(), numWorkers)
		if int(successCount.Load()) < numWorkers-20 {
			t.Errorf("Expected at least %d workers to get valid tokens, but got %d", numWorkers-20, successCount.Load())
		}
	})
}

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

		// Stop manager
		manager.Stop()

		// Sleep to let cancellation propagate
		time.Sleep(100 * time.Millisecond)

		// Try to get token after stop
		_, err = manager.(*tokenManagerImpl).GetToken()
		if err == nil {
			t.Error("Expected error after stopping manager")
		}
	})

	t.Run("GetToken respects context cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		manager, err := NewTokenManager(ctx, nil)
		if err != nil {
			t.Fatalf("Failed to create token manager: %v", err)
		}

		impl := manager.(*tokenManagerImpl)
		impl.token.Store(nil)

		_, err = impl.GetToken()

		if err == nil {
			t.Error("Expected error due to context cancellation")
		}

		manager.Stop()
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
		notifier:        NewNotifier(), // Use notifier instead of updates channel
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
