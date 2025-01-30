package e2e

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"gotest.tools/v3/icmd"
)

// proxyTester creates test proxy servers for STS and GCS and manages test lifecycle
type proxyTester struct {
	t           *testing.T
	stsAttempts int32
	gcsAttempts int32
	stsServer   *httptest.Server
	gcsServer   *httptest.Server
	origVars    map[string]string
}

func newProxyTester(t *testing.T) *proxyTester {
	tester := &proxyTester{
		t:        t,
		origVars: make(map[string]string),
	}

	// Save original env vars
	varsToSave := []string{"S3_ENDPOINT_URL", "GOOGLE_AUDIENCE", "GOOGLE_OAUTH_TOKEN_URL"}
	for _, v := range varsToSave {
		tester.origVars[v] = os.Getenv(v)
	}

	// Create STS server
	tester.stsServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&tester.stsAttempts, 1)
		
		// Simulate failures for first 2 attempts
		if count <= 2 {
			t.Logf("STS request %d: simulating failure", count)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		// Succeed after 2 failures
		t.Logf("STS request %d: succeeding", count)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{
			"access_token": "test-token",
			"token_type": "Bearer",
			"expires_in": 3600
		}`)
	}))

	// Create GCS server
	tester.gcsServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&tester.gcsAttempts, 1)
		t.Logf("GCS request %d: auth header present: %v", count, r.Header.Get("Authorization") != "")
		
		// Check for proper authorization
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Logf("GCS request missing auth token")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		// Return empty bucket list
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"kind":"storage#buckets","items":[]}`)
	}))

	// Set test env vars
	os.Setenv("S3_ENDPOINT_URL", tester.gcsServer.URL)
	os.Setenv("GOOGLE_AUDIENCE", "test-audience")
	os.Setenv("GOOGLE_OAUTH_TOKEN_URL", tester.stsServer.URL+"/token")

	return tester
}

func (p *proxyTester) cleanup() {
	// Restore original env vars
	for k, v := range p.origVars {
		os.Setenv(k, v)
	}
	p.stsServer.Close()
	p.gcsServer.Close()
}

func TestSTSRetryBehavior(t *testing.T) {
	// We'll test all scenarios in a single test to avoid
	// multiple builds of the binary
	_, s5cmd := setup(t)

	tester := newProxyTester(t)
	defer tester.cleanup()

	testCases := []struct {
		name            string
		args            []string
		wantSTSAttempts int32
		wantGCSAttempts int32
		wantError       bool
	}{
		{
			name:            "List buckets with retry",
			args:            []string{"ls"},
			wantSTSAttempts: 3, // Expect 2 failures + 1 success
			wantGCSAttempts: 1, // After token success, one GCS call
			wantError:       false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Reset attempt counters
			atomic.StoreInt32(&tester.stsAttempts, 0)
			atomic.StoreInt32(&tester.gcsAttempts, 0)

			// Run command
			cmd := s5cmd(tc.args...)
			result := icmd.RunCmd(cmd)

			// Check attempts
			if gotSTS := atomic.LoadInt32(&tester.stsAttempts); gotSTS != tc.wantSTSAttempts {
				t.Errorf("STS attempts = %d, want %d", gotSTS, tc.wantSTSAttempts)
			}
			if gotGCS := atomic.LoadInt32(&tester.gcsAttempts); gotGCS != tc.wantGCSAttempts {
				t.Errorf("GCS attempts = %d, want %d", gotGCS, tc.wantGCSAttempts)
			}

			// Check error state
			if tc.wantError {
				result.Assert(t, icmd.Expected{ExitCode: 1})
			} else {
				result.Assert(t, icmd.Success)
			}
		})
	}
}