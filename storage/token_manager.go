package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/peak/s5cmd/v2/log"
	"github.com/peak/s5cmd/v2/useragent"
	"github.com/rogpeppe/go-internal/lockedfile"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	expiryGrace     = 5 * time.Minute
	refreshLoopWait = 30 * time.Second
)

// TokenManager is the interface for token management
type TokenManager interface {
	GetToken() (*oauth2.Token, error)
	Stop()
}

// tokenManagerImpl handles token fetching, caching, and refreshing in a background goroutine
type tokenManagerImpl struct {
	ctx       context.Context
	cancel    context.CancelFunc
	creds     *google.Credentials
	cacheFile string
	audience  string
	token     atomic.Pointer[oauth2.Token]
	mu        sync.Mutex
	updates   chan struct{}
	client    *retryablehttp.Client
}

// For testing - can be replaced in tests
var findDefaultCredentials = google.FindDefaultCredentials

// userAgentTransport adds the user agent to all requests
type userAgentTransport struct {
	base http.RoundTripper
}

func (t *userAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("User-Agent", useragent.GetGoogleAuthUserAgent())
	return t.base.RoundTrip(req)
}

// simpleMessage implements log.Message interface just passing through the message string
type simpleMessage struct {
	msg string
}

func (m *simpleMessage) String() string { return m.msg }
func (m *simpleMessage) JSON() string   { return fmt.Sprintf(`{"message":%q}`, m.msg) }

// leveledLogger adapts s5cmd's logging system to retryablehttp's LeveledLogger interface
type leveledLogger struct{}

func (l *leveledLogger) Error(msg string, keysAndValues ...interface{}) {
	log.Error(&simpleMessage{formatMessage(msg, keysAndValues...)})
}

func (l *leveledLogger) Info(msg string, keysAndValues ...interface{}) {
	log.Info(&simpleMessage{formatMessage(msg, keysAndValues...)})
}

func (l *leveledLogger) Debug(msg string, keysAndValues ...interface{}) {
	log.Debug(&simpleMessage{formatMessage(msg, keysAndValues...)})
}

func (l *leveledLogger) Warn(msg string, keysAndValues ...interface{}) {
	log.Warn(&simpleMessage{formatMessage(msg, keysAndValues...)})
}

// formatMessage formats a message with its key-value pairs
func formatMessage(msg string, keysAndValues ...interface{}) string {
	if len(keysAndValues) == 0 {
		return msg
	}

	var pairs []string
	for i := 0; i < len(keysAndValues); i += 2 {
		key := fmt.Sprint(keysAndValues[i])
		value := "<?>"
		if i+1 < len(keysAndValues) {
			value = fmt.Sprint(keysAndValues[i+1])
		}
		pairs = append(pairs, fmt.Sprintf("%s=%s", key, value))
	}
	return fmt.Sprintf("%s {%s}", msg, strings.Join(pairs, " "))
}

// NewTokenManager creates a new token manager and starts its refresh goroutine.
func NewTokenManager(ctx context.Context, baseClient *http.Client) (TokenManager, error) {
	// Create retry client for token operations with simplified settings
	client := retryablehttp.NewClient()
	client.Logger = &leveledLogger{} // Use our leveled logger adapter

	// Retries up to ~30m assuming timeouts on each attempt.
	client.RetryMax = 15
	client.RetryWaitMin = 1 * time.Second
	client.RetryWaitMax = 2 * time.Minute
	client.HTTPClient.Timeout = 10 * time.Second

	// Use baseClient's transport if provided
	if baseClient != nil {
		client.HTTPClient = baseClient
	}

	// Add user agent transport
	baseTransport := client.HTTPClient.Transport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}
	client.HTTPClient.Transport = &userAgentTransport{
		base: baseTransport,
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
		updates:   make(chan struct{}, 1),
	}

	// Try to load initial token from cache
	token, err := m.readTokenFromCache()
	if err == nil && token != nil && token.Valid() && time.Until(token.Expiry) >= 5*time.Minute {
		m.token.Store(token)
	} else {
		if err := m.refresh(); err != nil {
			log.Error(log.ErrorMessage{
				Command: "TokenRefresh",
				Err:     fmt.Sprintf("Failed to refresh token on startup: %v", err),
			})
		}
	}

	// Start refresh goroutine
	go m.refreshLoop()

	return m, nil
}

func (m *tokenManagerImpl) Stop() {
	m.cancel()
}

// GetToken returns the current token or blocks until one is available or context is canceled
func (m *tokenManagerImpl) GetToken() (*oauth2.Token, error) {
	if err := m.ctx.Err(); err != nil {
		return nil, err
	}

	// Fast path: try to get a valid token without any locks
	if tokenPtr := m.token.Load(); tokenPtr != nil && tokenPtr.Valid() {
		token := *tokenPtr
		return &token, nil
	}

	// Slow path: no valid token, need to wait or refresh
	for {
		if err := m.ctx.Err(); err != nil {
			return nil, err
		}
		
		m.mu.Lock()
		
		// Double-check after lock acquisition
		if tokenPtr := m.token.Load(); tokenPtr != nil && tokenPtr.Valid() {
			token := *tokenPtr
			m.mu.Unlock()
			return &token, nil
		}
		
		err := m.refresh()
		m.mu.Unlock()
		
		if err := m.ctx.Err(); err != nil {
			return nil, err
		}
		
		if err == nil {
			if tokenPtr := m.token.Load(); tokenPtr != nil && tokenPtr.Valid() {
				token := *tokenPtr
				return &token, nil
			}
		}

		select {
		case <-m.ctx.Done():
			return nil, m.ctx.Err()
		case <-m.updates:
			// Continue the loop
		}
	}
}

// refresh gets a new token and updates both memory and file cache while holding lock
func (m *tokenManagerImpl) refresh() error {
	if err := m.ensureDirectoryExists(); err != nil {
		return fmt.Errorf("failed to create cache directory: %v", err)
	}

	// Use file lock to coordinate with other processes
	file, err := lockedfile.OpenFile(m.cacheFile, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return fmt.Errorf("failed to get lock: %v", err)
	}
	defer file.Close()

	// First check if someone else already refreshed while we were waiting
	data, err := io.ReadAll(file)
	if err == nil && len(data) > 0 {
		var cachedInfo CachedTokenInfo
		if json.Unmarshal(data, &cachedInfo) == nil &&
			cachedInfo.Audience == m.audience &&
			cachedInfo.Token != nil &&
			cachedInfo.Token.Valid() &&
			time.Until(cachedInfo.Token.Expiry) > 5*time.Minute {
			// Use token that was refreshed by another process
			m.token.Store(cachedInfo.Token)
			
			// Notify waiters of new token
			select {
			case m.updates <- struct{}{}:
			default:
				// Channel full or no waiters, that's OK
			}
			
			return nil
		}
	}

	// Get new token
	ctx := context.WithValue(m.ctx, oauth2.HTTPClient, m.client.StandardClient())
	tokenSource := oauth2.ReuseTokenSourceWithExpiry(nil, &contextTokenSource{
		ctx: ctx,
		ts:  m.creds.TokenSource,
	}, time.Minute*5)

	newToken, err := tokenSource.Token()
	if err != nil {
		return err
	}

	// Store atomically
	m.token.Store(newToken)

	// Notify waiters of new token
	select {
	case m.updates <- struct{}{}:
	default:
		// Channel full or no waiters, that's OK
	}

	// Write new token to file cache
	cachedInfo := CachedTokenInfo{
		Token:    newToken,
		Audience: m.audience,
	}

	newData, err := json.Marshal(cachedInfo)
	if err != nil {
		return fmt.Errorf("failed to marshal token: %v", err)
	}

	// Truncate and write file
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	if _, err := file.Write(newData); err != nil {
		return err
	}

	return nil
}

func (m *tokenManagerImpl) refreshLoop() {
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-time.After(refreshLoopWait): // Fixed refresh interval
			tokenPtr := m.token.Load()
			
			// Skip refresh if token is still valid and not expiring soon
			if tokenPtr != nil && tokenPtr.Valid() && time.Until(tokenPtr.Expiry) > expiryGrace {
				continue
			}

			m.mu.Lock()
			err := m.refresh()
			m.mu.Unlock()
			
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
}

// RoundTrip implements the http.RoundTripper interface.
func (c *GoogleAuthRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
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
	}

	// Create final client with auth transport
	return &http.Client{
		Transport: authTransport,
		Timeout:   30 * time.Second,
	}, nil
}