package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/peak/s5cmd/v2/log"
	"golang.org/x/oauth2"
)

// TestDirLockBasicFunctionality tests basic token caching functionality
func TestDirLockBasicFunctionality(t *testing.T) {
	log.Init("debug", false)
	tempDir, err := os.MkdirTemp("", "token-cache-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cacheFile := filepath.Join(tempDir, "token-cache.json")

	mockSource := &mockTokenSource{
		token: &oauth2.Token{
			AccessToken: "test-token",
			TokenType:   "Bearer",
			Expiry:      time.Now().Add(1 * time.Hour),
		},
	}

	source := &FileCachedTokenSource{
		underlying:  mockSource,
		cacheFile:   cacheFile,
		audience:    "test-audience",
		lockTimeout: 10 * time.Second,
	}

	// Test 1: Get token when cache doesn't exist
	token, err := source.Token()
	if err != nil {
		t.Fatalf("Failed to get token: %v", err)
	}
	if token.AccessToken != "test-token" {
		t.Errorf("Expected test-token, got %s", token.AccessToken)
	}

	// Test 2: Get token from cache
	mockSource.token.AccessToken = "new-token" // Change underlying token
	token, err = source.Token()
	if err != nil {
		t.Fatalf("Failed to get token: %v", err)
	}
	if token.AccessToken != "test-token" {
		t.Errorf("Expected cached test-token, got %s", token.AccessToken)
	}
}

// TestDirLockConcurrentAccess tests concurrent access to token cache
func TestDirLockConcurrentAccess(t *testing.T) {
	log.Init("debug", false)
	tempDir, err := os.MkdirTemp("", "token-cache-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cacheFile := filepath.Join(tempDir, "token-cache.json")

	mockSource := &mockTokenSource{
		token: &oauth2.Token{
			AccessToken: "test-token",
			TokenType:   "Bearer",
			Expiry:      time.Now().Add(1 * time.Hour),
		},
	}

	var wg sync.WaitGroup
	tokenCount := 0
	var mu sync.Mutex

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			source := &FileCachedTokenSource{
				underlying:  mockSource,
				cacheFile:   cacheFile,
				audience:    "test-audience",
				lockTimeout: 10 * time.Second,
			}
			token, err := source.Token()
			if err != nil {
				t.Errorf("Failed to get token: %v", err)
				return
			}
			mu.Lock()
			if token.AccessToken == "test-token" {
				tokenCount++
			}
			mu.Unlock()
		}()
	}

	wg.Wait()

	if tokenCount != 10 {
		t.Errorf("Expected 10 successful token fetches, got %d", tokenCount)
	}
}

// TestDirLockStaleLock tests handling of stale locks
func TestDirLockStaleLock(t *testing.T) {
	log.Init("debug", false)
	tempDir, err := os.MkdirTemp("", "token-cache-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cacheFile := filepath.Join(tempDir, "token-cache.json")
	lockDir := cacheFile + ".lock"

	// Create a stale lock
	if err := os.Mkdir(lockDir, 0700); err != nil {
		t.Fatalf("Failed to create lock dir: %v", err)
	}
	staleTime := time.Now().Add(-5 * time.Minute)
	os.Chtimes(lockDir, staleTime, staleTime)

	mockSource := &mockTokenSource{
		token: &oauth2.Token{
			AccessToken: "test-token",
			TokenType:   "Bearer",
			Expiry:      time.Now().Add(1 * time.Hour),
		},
	}

	source := &FileCachedTokenSource{
		underlying:  mockSource,
		cacheFile:   cacheFile,
		audience:    "test-audience",
		lockTimeout: 1 * time.Minute,
	}

	token, err := source.Token()
	if err != nil {
		t.Fatalf("Failed to get token: %v", err)
	}
	if token.AccessToken != "test-token" {
		t.Errorf("Expected test-token, got %s", token.AccessToken)
	}

	// Check if stale lock was removed
	if _, err := os.Stat(lockDir); !os.IsNotExist(err) {
		t.Errorf("Expected stale lock to be removed")
	}
}

// TestDirLockCacheInvalidation tests cache invalidation scenarios
func TestDirLockCacheInvalidation(t *testing.T) {
	log.Init("debug", false)
	tempDir, err := os.MkdirTemp("", "token-cache-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cacheFile := filepath.Join(tempDir, "token-cache.json")

	mockSource := &mockTokenSource{
		token: &oauth2.Token{
			AccessToken: "new-token",
			TokenType:   "Bearer",
			Expiry:      time.Now().Add(1 * time.Hour),
		},
	}

	// Test 1: Expired token
	expiredToken := &oauth2.Token{
		AccessToken: "expired-token",
		TokenType:   "Bearer",
		Expiry:      time.Now().Add(-1 * time.Hour),
	}
	cacheData, _ := json.Marshal(CachedTokenInfo{
		Token:    expiredToken,
		Audience: "test-audience",
	})
	if err := os.WriteFile(cacheFile, cacheData, 0600); err != nil {
		t.Fatalf("Failed to write cache file: %v", err)
	}

	source := &FileCachedTokenSource{
		underlying:  mockSource,
		cacheFile:   cacheFile,
		audience:    "test-audience",
		lockTimeout: 10 * time.Second,
	}

	token, err := source.Token()
	if err != nil {
		t.Fatalf("Failed to get token: %v", err)
	}
	if token.AccessToken != "new-token" {
		t.Errorf("Expected new-token due to expiration, got %s", token.AccessToken)
	}

	// Test 2: Audience mismatch
	validToken := &oauth2.Token{
		AccessToken: "valid-token",
		TokenType:   "Bearer",
		Expiry:      time.Now().Add(1 * time.Hour),
	}
	cacheData, _ = json.Marshal(CachedTokenInfo{
		Token:    validToken,
		Audience: "wrong-audience",
	})
	if err := os.WriteFile(cacheFile, cacheData, 0600); err != nil {
		t.Fatalf("Failed to write cache file: %v", err)
	}

	token, err = source.Token()
	if err != nil {
		t.Fatalf("Failed to get token: %v", err)
	}
	if token.AccessToken != "new-token" {
		t.Errorf("Expected new-token due to audience mismatch, got %s", token.AccessToken)
	}
}

// TestDirLockAtomicWrite tests atomic write functionality
func TestDirLockAtomicWrite(t *testing.T) {
	log.Init("debug", false)
	tempDir, err := os.MkdirTemp("", "token-cache-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cacheFile := filepath.Join(tempDir, "token-cache.json")

	mockSource := &mockTokenSource{
		token: &oauth2.Token{
			AccessToken: "test-token",
			TokenType:   "Bearer",
			Expiry:      time.Now().Add(1 * time.Hour),
		},
	}

	source := &FileCachedTokenSource{
		underlying:  mockSource,
		cacheFile:   cacheFile,
		audience:    "test-audience",
		lockTimeout: 10 * time.Second,
	}

	// Get token to trigger write
	_, err = source.Token()
	if err != nil {
		t.Fatalf("Failed to get token: %v", err)
	}

	// Check for temp files
	tempFiles, err := filepath.Glob(cacheFile + ".tmp.*")
	if err != nil {
		t.Fatalf("Failed to glob temp files: %v", err)
	}
	if len(tempFiles) > 0 {
		t.Errorf("Expected no temp files after atomic write, found %d", len(tempFiles))
	}

	// Verify cache file exists and is valid
	data, err := os.ReadFile(cacheFile)
	if err != nil {
		t.Fatalf("Failed to read cache file: %v", err)
	}

	var cachedInfo CachedTokenInfo
	if err := json.Unmarshal(data, &cachedInfo); err != nil {
		t.Fatalf("Failed to unmarshal cache data: %v", err)
	}

	if cachedInfo.Token.AccessToken != "test-token" {
		t.Errorf("Expected test-token in cache, got %s", cachedInfo.Token.AccessToken)
	}
}

// TestDirLockTempFileCleanup tests cleanup of stale temp files
func TestDirLockTempFileCleanup(t *testing.T) {
	log.Init("debug", false)
	tempDir, err := os.MkdirTemp("", "token-cache-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cacheFile := filepath.Join(tempDir, "token-cache.json")

	// Create some old temp files
	for i := 0; i < 3; i++ {
		tempFile := fmt.Sprintf("%s.tmp.old%d", cacheFile, i)
		if err := os.WriteFile(tempFile, []byte("test"), 0600); err != nil {
			t.Fatalf("Failed to create temp file: %v", err)
		}
		oldTime := time.Now().Add(-2 * time.Hour)
		os.Chtimes(tempFile, oldTime, oldTime)
	}

	mockSource := &mockTokenSource{
		token: &oauth2.Token{
			AccessToken: "test-token",
			TokenType:   "Bearer",
			Expiry:      time.Now().Add(1 * time.Hour),
		},
	}

	source := &FileCachedTokenSource{
		underlying:  mockSource,
		cacheFile:   cacheFile,
		audience:    "test-audience",
		lockTimeout: 10 * time.Second,
	}

	source.cleanup()

	// Check if old temp files were removed
	tempFiles, err := filepath.Glob(cacheFile + ".tmp.*")
	if err != nil {
		t.Fatalf("Failed to glob temp files: %v", err)
	}
	if len(tempFiles) > 0 {
		t.Errorf("Expected all old temp files to be cleaned up, found %d", len(tempFiles))
	}
}

// TestDirLockNestedDirectory tests cache functionality in deeply nested paths
func TestDirLockNestedDirectory(t *testing.T) {
	log.Init("debug", false)
	tempDir, err := os.MkdirTemp("", "token-cache-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create a deeply nested path
	nestedPath := filepath.Join(tempDir, "level1", "level2", "level3", "level4")
	cacheFile := filepath.Join(nestedPath, "token-cache.json")

	mockSource := &mockTokenSource{
		token: &oauth2.Token{
			AccessToken: "test-token",
			TokenType:   "Bearer",
			Expiry:      time.Now().Add(1 * time.Hour),
		},
	}

	source := &FileCachedTokenSource{
		underlying:  mockSource,
		cacheFile:   cacheFile,
		audience:    "test-audience",
		lockTimeout: 10 * time.Second,
	}

	// Create parent directories first
	if err := os.MkdirAll(filepath.Dir(cacheFile), 0700); err != nil {
		t.Fatalf("Failed to create parent directories: %v", err)
	}

	_, err = source.Token()
	if err != nil {
		t.Fatalf("Failed to get token in nested directory: %v", err)
	}

	// Verify cache file was created in nested directory
	if _, err := os.Stat(cacheFile); os.IsNotExist(err) {
		t.Errorf("Cache file was not created in nested directory")
	}

	// Verify lock directory permissions
	lockDir := cacheFile + ".lock"
	if fi, err := os.Stat(lockDir); err == nil {
		if fi.Mode().Perm() != 0700 {
			t.Errorf("Lock directory has incorrect permissions: %v", fi.Mode().Perm())
		}
	}
}

// TestDirLockFallbackBehavior tests fallback to underlying source when locking fails
func TestDirLockFallbackBehavior(t *testing.T) {
	log.Init("debug", false)
	tempDir, err := os.MkdirTemp("", "token-cache-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cacheFile := filepath.Join(tempDir, "token-cache.json")
	lockDir := cacheFile + ".lock"

	// Create an unbreakable lock (regular file instead of directory to cause failure)
	if err := os.WriteFile(lockDir, []byte("lock"), 0600); err != nil {
		t.Fatalf("Failed to create lock file: %v", err)
	}

	mockSource := &mockTokenSource{
		token: &oauth2.Token{
			AccessToken: "fallback-token",
			TokenType:   "Bearer",
			Expiry:      time.Now().Add(1 * time.Hour),
		},
	}

	source := &FileCachedTokenSource{
		underlying:  mockSource,
		cacheFile:   cacheFile,
		audience:    "test-audience",
		lockTimeout: 1 * time.Second,
	}

	token, err := source.Token()
	if err != nil {
		t.Fatalf("Failed to get token: %v", err)
	}

	if token.AccessToken != "fallback-token" {
		t.Errorf("Expected fallback token, got %s", token.AccessToken)
	}
}

// TestDirLockCleanupAfterInterruption tests cleanup after process interruption
func TestDirLockCleanupAfterInterruption(t *testing.T) {
	log.Init("debug", false)
	tempDir, err := os.MkdirTemp("", "token-cache-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cacheFile := filepath.Join(tempDir, "token-cache.json")
	lockDir := cacheFile + ".lock"

	// Create a lock directory with old timestamp
	if err := os.Mkdir(lockDir, 0700); err != nil {
		t.Fatalf("Failed to create lock dir: %v", err)
	}
	oldTime := time.Now().Add(-10 * time.Minute)
	os.Chtimes(lockDir, oldTime, oldTime)

	mockSource := &mockTokenSource{
		token: &oauth2.Token{
			AccessToken: "test-token",
			TokenType:   "Bearer",
			Expiry:      time.Now().Add(1 * time.Hour),
		},
	}

	source := &FileCachedTokenSource{
		underlying:  mockSource,
		cacheFile:   cacheFile,
		audience:    "test-audience",
		lockTimeout: 5 * time.Minute,
	}

	token, err := source.Token()
	if err != nil {
		t.Fatalf("Failed to get token: %v", err)
	}

	if token.AccessToken != "test-token" {
		t.Errorf("Expected test token after cleanup, got %s", token.AccessToken)
	}

	// Verify old lock was removed
	if _, err := os.Stat(lockDir); !os.IsNotExist(err) {
		t.Errorf("Old lock directory was not removed")
	}
}

// TestDirLockLargeToken tests handling of large token payloads
func TestDirLockLargeToken(t *testing.T) {
	log.Init("debug", false)
	tempDir, err := os.MkdirTemp("", "token-cache-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cacheFile := filepath.Join(tempDir, "token-cache.json")

	// Create a large but predictable token
	largeToken := &oauth2.Token{
		AccessToken: strings.Repeat("X", 1024*1024), // 1MB of repeating X
		TokenType:   "Bearer",
		Expiry:      time.Now().Add(1 * time.Hour),
	}

	mockSource := &mockTokenSource{
		token: largeToken,
	}

	source := &FileCachedTokenSource{
		underlying:  mockSource,
		cacheFile:   cacheFile,
		audience:    "test-audience",
		lockTimeout: 10 * time.Second,
	}

	// Get and cache token
	token, err := source.Token()
	if err != nil {
		t.Fatalf("Failed to get large token: %v", err)
	}

	expectedLen := len(largeToken.AccessToken)
	if len(token.AccessToken) != expectedLen {
		t.Errorf("Large token was not handled correctly, expected %d bytes, got %d", 
			expectedLen, len(token.AccessToken))
	}

	// Create fresh token source with wrong token to ensure we read from file
	source2 := &FileCachedTokenSource{
		underlying:  &mockTokenSource{token: &oauth2.Token{AccessToken: "wrong"}},
		cacheFile:   cacheFile,
		audience:    "test-audience",
		lockTimeout: 10 * time.Second,
	}
	
	token2, err := source2.Token()
	if err != nil {
		t.Fatalf("Failed to read back large token: %v", err)
	}

	if len(token2.AccessToken) != expectedLen {
		t.Errorf("Large token read back incorrectly, expected %d bytes, got %d", 
			expectedLen, len(token2.AccessToken))
	}
	
	// Verify it's the cached token we get back
	if token2.AccessToken != largeToken.AccessToken {
		t.Errorf("Got wrong token back from cache")
	}
}