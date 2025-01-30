package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/peak/s5cmd/v2/log"
	"github.com/rogpeppe/go-internal/lockedfile"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// TokenManager is the interface for token management
type TokenManager interface {
	GetToken() (*oauth2.Token, error)
	Stop()
}

// tokenManagerImpl handles token fetching, caching, and refreshing in a background goroutine
type tokenManagerImpl struct {
	ctx       context.Context    // Context for lifecycle management
	cancel    context.CancelFunc // Cancel function to stop the manager
	creds     *google.Credentials
	cacheFile string
	audience  string
	token     *oauth2.Token
	mu        sync.RWMutex
	cond      *sync.Cond // Condition variable for token readiness
	client    *retryablehttp.Client
}

// For testing - can be replaced in tests
var findDefaultCredentials = google.FindDefaultCredentials

// SetUserAgent sets the user agent string for Google authentication requests
func SetUserAgent(ua string) {
	globalUserAgent = ua
}

// GetUserAgent returns the user agent string for Google authentication requests
func GetUserAgent() string {
	return globalUserAgent
}

var globalUserAgent string

// NewTokenManager creates a new token manager and starts its refresh goroutine.
func NewTokenManager(ctx context.Context, baseClient *http.Client) (TokenManager, error) {
	// Create retry client for token operations with simplified settings
	client := retryablehttp.NewClient()

	// Retries up to ~30m assuming timeouts on each attempt.
	client.RetryMax = 15
	client.RetryWaitMin = 1 * time.Second
	client.RetryWaitMax = 2 * time.Minute
	client.HTTPClient.Timeout = 10 * time.Second

	// Use baseClient's transport if provided
	if baseClient != nil {
		client.HTTPClient = baseClient
	}

	// Get credentials using the retry client
	tokenCtx := context.WithValue(ctx, oauth2.HTTPClient, client.StandardClient())
	creds, err := findDefaultCredentials(tokenCtx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return nil, err
	}

	// Determine audience from credentials
	content := map[string]interface{}{}
	json.Unmarshal(creds.JSON, &content)
	var audience string
	if aud, ok := content["audience"]; ok {
		audience = aud.(string)
	}

	// Determine cache file location
	cacheFile := filepath.Join(os.Getenv("HOME"), ".cache", "coo", "cached_s5cmd.json")

	mctx, cancel := context.WithCancel(ctx)
	m := &tokenManagerImpl{
		ctx:       mctx,
		cancel:    cancel,
		creds:     creds,
		cacheFile: cacheFile,
		audience:  audience,
		client:    client,
	}
	m.cond = sync.NewCond(&m.mu) // Initialize condition variable with mutex

	// Try to load initial token from cache
	token, err := m.readTokenFromCache()
	if err == nil && token != nil && token.Valid() && time.Until(token.Expiry) >= 5*time.Minute {
		m.token = token
	} else {
		// No valid token from cache, refresh immediately
		go func() {
			if err := m.refresh(); err != nil {
				log.Error(log.ErrorMessage{
					Command: "TokenRefresh",
					Err:     fmt.Sprintf("Failed to refresh token on startup: %v", err),
				})
			}
		}()
	}

	// Start refresh goroutine
	go m.refreshLoop()

	return m, nil
}

func (m *tokenManagerImpl) Stop() {
	m.cancel()
}

// GetToken returns the current token or blocks until one is available
func (m *tokenManagerImpl) GetToken() (*oauth2.Token, error) {
	// Check context first
	if err := m.ctx.Err(); err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Block until a valid token is available or context is canceled
	for m.token == nil || !m.token.Valid() {
		// Check context before waiting
		if err := m.ctx.Err(); err != nil {
			return nil, err
		}

		// Wait for signal that token has been refreshed
		m.cond.Wait()
	}

	return m.token, nil
}

// refresh gets a new token and updates both memory and file cache
func (m *tokenManagerImpl) refresh() error {
	ctx := context.WithValue(m.ctx, oauth2.HTTPClient, m.client.StandardClient())

	tokenSource := oauth2.ReuseTokenSourceWithExpiry(nil, &contextTokenSource{
		ctx: ctx,
		ts:  m.creds.TokenSource,
	}, time.Minute*5)

	token, err := tokenSource.Token()
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.token = token
	m.cond.Broadcast() // Signal that token is ready

	// Update file cache - always synchronous
	return m.writeTokenToCache(token)
}

func (m *tokenManagerImpl) refreshLoop() {
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-time.After(5 * time.Second): // Fixed refresh interval
			m.mu.RLock()
			token := m.token
			m.mu.RUnlock()

			// Skip refresh if token is still valid and not expiring soon
			if token != nil && token.Valid() && time.Until(token.Expiry) > 5*time.Minute {
				continue
			}

			// Try to refresh
			err := m.refresh()
			if err != nil {
				log.Error(log.ErrorMessage{
					Command: "TokenRefresh",
					Err:     fmt.Sprintf("Failed to refresh token: %v", err),
				})
			}
		}
	}
}

// ensureDirectoryExists creates the directory path for the cache file if it doesn't exist.
func (m *tokenManagerImpl) ensureDirectoryExists() error {
	if os.Getenv("S5CMD_FILE_CACHING_OPT_OUT") != "" {
		return nil
	}
	dir := filepath.Dir(m.cacheFile)
	return os.MkdirAll(dir, 0700)
}

// readTokenFromCache attempts to read and parse an OAuth2 token from the cache file.
func (m *tokenManagerImpl) readTokenFromCache() (*oauth2.Token, error) {
	if m.cacheFile == "" {
		return nil, fmt.Errorf("no cache file configured")
	}

	if err := m.ensureDirectoryExists(); err != nil {
		return nil, fmt.Errorf("failed to create cache directory: %v", err)
	}

	data, err := lockedfile.Read(m.cacheFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read cached token: %v", err)
	}

	var cachedInfo CachedTokenInfo
	if err := json.Unmarshal(data, &cachedInfo); err != nil {
		return nil, fmt.Errorf("failed to unmarshal cached token info: %v", err)
	}

	if cachedInfo.Audience != m.audience {
		return nil, fmt.Errorf("cached token audience mismatch")
	}

	return cachedInfo.Token, nil
}

// writeTokenToCache marshals the given OAuth2 token to JSON and writes it to the cache file.
func (m *tokenManagerImpl) writeTokenToCache(token *oauth2.Token) error {
	if m.cacheFile == "" || os.Getenv("S5CMD_FILE_CACHING_OPT_OUT") != "" {
		return nil
	}

	if err := m.ensureDirectoryExists(); err != nil {
		return fmt.Errorf("failed to create cache directory: %v", err)
	}

	cachedInfo := CachedTokenInfo{
		Token:    token,
		Audience: m.audience,
	}

	data, err := json.Marshal(cachedInfo)
	if err != nil {
		return fmt.Errorf("failed to marshal token: %v", err)
	}

	err = lockedfile.Write(m.cacheFile, bytes.NewReader(data), 0600)
	if err != nil {
		return fmt.Errorf("failed to write cached token: %v", err)
	}

	return nil
}

// CachedTokenInfo holds token data and metadata for caching
type CachedTokenInfo struct {
	Token    *oauth2.Token `json:"token"`
	Audience string        `json:"audience"`
}

// contextTokenSource is a token source that passes a context to the underlying source
type contextTokenSource struct {
	ctx context.Context
	ts  oauth2.TokenSource
}

func (c *contextTokenSource) Token() (*oauth2.Token, error) {
	if withContext, ok := c.ts.(interface {
		TokenWithContext(context.Context) (*oauth2.Token, error)
	}); ok {
		return withContext.TokenWithContext(c.ctx)
	}
	return c.ts.Token()
}

// GoogleAuthRoundTripper handles authentication for Google Cloud Storage requests.
type GoogleAuthRoundTripper struct {
	tokenManager TokenManager
	transport    http.RoundTripper
	userAgent    string
}

// RoundTrip implements the http.RoundTripper interface.
func (c *GoogleAuthRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Always set our custom user agent for all calls
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}

	// Convert headers for all GCS calls
	for key, values := range req.Header {
		oldKey := key
		newKey := strings.Replace(strings.ToLower(oldKey), "x-amz", "x-goog", -1)
		for i := range values {
			values[i] = strings.Replace(values[i], "gs%3A//", "", -1)
		}
		req.Header.Del(oldKey)
		req.Header[newKey] = values
	}

	// Get token from manager
	token, err := c.tokenManager.GetToken()
	if err != nil {
		var retrieveErr *oauth2.RetrieveError
		if errors.As(err, &retrieveErr) {
			// Pass through existing OAuth2 errors
			return nil, err
		}

		// For authentication failures, use invalid_client
		if strings.Contains(err.Error(), "invalid_credential") ||
			strings.Contains(err.Error(), "unauthorized_client") {
			return nil, &oauth2.RetrieveError{
				Response:         nil,
				Body:             nil,
				ErrorCode:        "invalid_client",
				ErrorDescription: err.Error(),
			}
		}

		// Network errors are temporarily_unavailable
		return nil, &oauth2.RetrieveError{
			Response:         nil,
			Body:             nil,
			ErrorCode:        "temporarily_unavailable",
			ErrorDescription: err.Error(),
		}
	}

	token.SetAuthHeader(req)
	return c.transport.RoundTrip(req)
}

// newGoogleAuthenticationClient creates a new HTTP client with Google authentication.
func newGoogleAuthenticationClient(ctx context.Context, baseClient *http.Client) (*http.Client, error) {
	// Get base transport - use provided or default
	var baseTransport http.RoundTripper
	if baseClient != nil && baseClient.Transport != nil {
		baseTransport = baseClient.Transport
	} else {
		baseTransport = http.DefaultTransport
	}

	// Use provided client or create new one for token operations
	tokenClient := baseClient
	if tokenClient == nil {
		tokenClient = &http.Client{
			Transport: baseTransport,
			Timeout:   30 * time.Second,
		}
	}

	// Create token manager with background refresh
	tokenManager, err := NewTokenManager(ctx, tokenClient)
	if err != nil {
		return nil, err
	}

	// Build authenticated transport
	authTransport := &GoogleAuthRoundTripper{
		transport:    baseTransport,
		tokenManager: tokenManager,
		userAgent:    globalUserAgent,
	}

	// Create final client with auth transport
	return &http.Client{
		Transport: authTransport,
		Timeout:   30 * time.Second,
	}, nil
}
