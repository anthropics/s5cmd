package storage

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"golang.org/x/oauth2"
)

// mockTokenManager simulates a TokenManager that can fail a number of times
type mockTokenManager struct {
	failures  int
	remaining int
	token     *oauth2.Token
	t         *testing.T
	errorType string // can be "network", "oauth", or empty
}

func newMockTokenManager(t *testing.T, failures int, errorType string) TokenManager {
	return &mockTokenManager{
		failures:  failures,
		remaining: failures,
		token: &oauth2.Token{
			AccessToken: "test-token",
			TokenType:   "Bearer",
			Expiry:      time.Now().Add(1 * time.Hour),
		},
		t:         t,
		errorType: errorType,
	}
}

func (m *mockTokenManager) GetToken() (*oauth2.Token, error) {
	if m.remaining > 0 {
		m.remaining--
		var err error
		switch m.errorType {
		case "network":
			err = &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
		case "oauth":
			err = &oauth2.RetrieveError{Response: &http.Response{StatusCode: 401}}
		default:
			err = errors.New("simulated token fetch failure")
		}
		m.t.Logf("Token fetch failed: %v (remaining: %d)", err, m.remaining)
		return nil, err
	}
	m.t.Log("Token fetch succeeded")
	return m.token, nil
}

func (m *mockTokenManager) Stop() {
	// nothing to do in mock
}

// trackingHandler tracks the number of requests made
type trackingHandler struct {
	requests    int
	handler     http.Handler
	statusCodes []int // sequence of status codes to return
}

func (t *trackingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	t.requests++
	if len(t.statusCodes) > 0 {
		statusCode := t.statusCodes[0]
		t.statusCodes = t.statusCodes[1:]
		w.WriteHeader(statusCode)
		return
	}
	t.handler.ServeHTTP(w, r)
}

func TestStsRetryBehavior(t *testing.T) {
	// Create test S3 server
	s3Handler := &trackingHandler{
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	}
	server := httptest.NewServer(s3Handler)
	defer server.Close()

	testCases := []struct {
		name           string
		tokenFailures  int
		errorType      string
		statusCodes    []int
		maxRetries     int
		expectSuccess  bool
		expectRequests int
	}{
		{
			name:           "Success on first attempt",
			tokenFailures:  0,
			maxRetries:     3,
			expectSuccess:  true,
			expectRequests: 1,
		},
		{
			name:           "Success after one failure with retries",
			tokenFailures:  1,
			maxRetries:     3,
			expectSuccess:  true,
			expectRequests: 1, // Now expecting 1 because retry happens in token source
		},
		{
			name:           "Too many failures for retries",
			tokenFailures:  5,
			maxRetries:     3,
			expectSuccess:  false,
			expectRequests: 0, // Now expecting 0 because failures happen before request
		},
		{
			name:           "Network error retry",
			tokenFailures:  1,
			errorType:      "network",
			maxRetries:     3,
			expectSuccess:  true,
			expectRequests: 1, // Now expecting 1 because retry happens in token source
		},
		{
			name:           "OAuth error retry",
			tokenFailures:  1,
			errorType:      "oauth",
			maxRetries:     3,
			expectSuccess:  true,
			expectRequests: 1, // Now expecting 1 because retry happens in token source
		},
		{
			name:           "HTTP 429 retry",
			statusCodes:    []int{429, 200},
			maxRetries:     3,
			expectSuccess:  true,
			expectRequests: 2,
		},
		{
			name:           "HTTP 503 retry",
			statusCodes:    []int{503, 200},
			maxRetries:     3,
			expectSuccess:  true,
			expectRequests: 2,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Reset request counter and status codes
			s3Handler.requests = 0
			s3Handler.statusCodes = tc.statusCodes

			// Create mock token manager
			tokenManager := newMockTokenManager(t, tc.tokenFailures, tc.errorType)

			// Create custom round tripper that uses our mock token manager
			rt := &GoogleAuthRoundTripper{
				tokenManager: tokenManager,
				transport:    http.DefaultTransport,
			}

			// Create custom HTTP client with our round tripper
			client := &http.Client{Transport: rt}

			// Create AWS session with custom HTTP client
			sess, err := session.NewSession(&aws.Config{
				Endpoint:         aws.String(server.URL),
				Credentials:      credentials.NewStaticCredentials("test", "test", ""),
				HTTPClient:       client,
				S3ForcePathStyle: aws.Bool(true),
				MaxRetries:       aws.Int(tc.maxRetries),
				Region:           aws.String("us-west-2"),
			})
			if err != nil {
				t.Fatalf("Failed to create session: %v", err)
			}

			// Create S3 service client
			svc := s3.New(sess)

			// Attempt to list buckets
			_, err = svc.ListBucketsWithContext(context.Background(), &s3.ListBucketsInput{})

			// Verify behavior
			if tc.expectSuccess {
				if err != nil {
					t.Errorf("Expected success, got error: %v", err)
				}
			} else {
				if err == nil {
					t.Error("Expected error, got success")
				}
			}

			// Verify number of requests
			if s3Handler.requests != tc.expectRequests {
				t.Errorf("Expected %d requests, got %d", tc.expectRequests, s3Handler.requests)
			}
		})
	}
}
