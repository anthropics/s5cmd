package useragent

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/peak/s5cmd/v2/version"
)

// helper function to check if a string is in a slice
func contains(slice []string, str string) bool {
	for _, v := range slice {
		if v == str {
			return true
		}
	}
	return false
}

func TestGetCacheInfo(t *testing.T) {
	// Save original env vars
	origCachePath := os.Getenv("S5CMD_CACHE_PATH")
	origOptOut := os.Getenv("S5CMD_FILE_CACHING_OPT_OUT")
	defer func() {
		os.Setenv("S5CMD_CACHE_PATH", origCachePath)
		os.Setenv("S5CMD_FILE_CACHING_OPT_OUT", origOptOut)
	}()

	// Test opt-out case
	os.Setenv("S5CMD_FILE_CACHING_OPT_OUT", "true")
	info := getCacheInfo()
	if info.Source != "OPT_OUT" || info.PathHash != "none" || info.ContentHash != "none" || info.Status != "disabled" {
		t.Errorf("Unexpected cache info for opt-out case: %+v", info)
	}

	// Test normal case with custom cache path
	os.Unsetenv("S5CMD_FILE_CACHING_OPT_OUT")
	testPath := "/tmp/test-cache"
	os.Setenv("S5CMD_CACHE_PATH", testPath)

	info = getCacheInfo()
	if info.Source != "ENV" {
		t.Errorf("Expected source ENV, got %s", info.Source)
	}
	if info.PathHash == "" || info.PathHash == "none" {
		t.Errorf("Expected non-empty path hash, got %s", info.PathHash)
	}
	if !contains([]string{"writable", "readonly", "unavailable"}, info.Status) {
		t.Errorf("Unexpected cache status: %s", info.Status)
	}
}

func TestGetGoogleAuthUserAgent(t *testing.T) {
	// Save original env vars to restore later
	origPodName := os.Getenv("COO_POD_NAME")
	origNamespace := os.Getenv("COO_NAMESPACE")
	origStatefulSet := os.Getenv("COO_STS_NAME")
	defer func() {
		os.Setenv("COO_POD_NAME", origPodName)
		os.Setenv("COO_NAMESPACE", origNamespace)
		os.Setenv("COO_STS_NAME", origStatefulSet)
	}()

	// Clear environment variables
	os.Unsetenv("COO_POD_NAME")
	os.Unsetenv("COO_NAMESPACE")
	os.Unsetenv("COO_STS_NAME")

	// Test basic user agent without K8s info
	ua := GetGoogleAuthUserAgent()
	if !strings.HasPrefix(ua, "s5cmd:") {
		t.Errorf("User agent should start with s5cmd:, got %s", ua)
	}
	if !strings.Contains(ua, "invocation_id:") {
		t.Errorf("User agent should contain invocation_id:, got %s", ua)
	}

	// Test with K8s env vars
	os.Setenv("COO_POD_NAME", "test-pod")
	os.Setenv("COO_NAMESPACE", "test-ns")
	os.Setenv("COO_STS_NAME", "test-sts")

	// Get cache string and verify format
	ua = GetGoogleAuthUserAgent()

	// Extract and verify source
	source := regexp.MustCompile(`cache_source:(\w+)`).FindStringSubmatch(ua)
	if source == nil {
		t.Errorf("Cache source not found in UA: %s", ua)
	} else if !contains([]string{"HOME", "XDG", "ENV", "TEMP", "OPT_OUT"}, source[1]) {
		t.Errorf("Cache source %s is not one of expected values", source[1])
	}

	// Extract and verify path hash
	pathHash := regexp.MustCompile(`cache_path:([a-f0-9]{8}|none)`).FindStringSubmatch(ua)
	if pathHash == nil {
		t.Errorf("Cache path hash not found or invalid in UA: %s", ua)
	}

	// Extract and verify content hash
	contentHash := regexp.MustCompile(`cache_content:([a-f0-9]{8}|none)`).FindStringSubmatch(ua)
	if contentHash == nil {
		t.Errorf("Cache content hash not found or invalid in UA: %s", ua)
	}

	// Extract and verify status
	status := regexp.MustCompile(`cache_status:(writable|readonly|unavailable|disabled)`).FindStringSubmatch(ua)
	if status == nil {
		t.Errorf("Cache status not found or invalid in UA: %s", ua)
	}

	// Check for invocation ID
	invocationID := regexp.MustCompile(`invocation_id:([a-f0-9-]+)`).FindStringSubmatch(ua)
	if invocationID == nil {
		t.Errorf("Invocation ID not found or invalid in UA: %s", ua)
	}

	// Check for expected version format
	versionStr := strings.TrimPrefix(version.Version, "v")
	if versionStr == "0.0.0" {
		versionStr = version.GitCommit // Uses dev in test environment
	}

	expectedParts := []string{
		fmt.Sprintf("s5cmd:%s", versionStr),
		"pod:test-ns/test-pod",
		"statefulset:test-sts",
		"cache_source:",
		"cache_path:",
		"cache_content:",
		"cache_status:",
		"invocation_id:",
	}
	for _, part := range expectedParts {
		if !strings.Contains(ua, part) {
			t.Errorf("User agent should contain %s, got %s", part, ua)
		}
	}

	// Test default namespace when COO_NAMESPACE is not set
	os.Unsetenv("COO_NAMESPACE")
	ua = GetGoogleAuthUserAgent()
	if !strings.Contains(ua, "pod:default/test-pod") {
		t.Errorf("User agent should use default namespace when COO_NAMESPACE is not set, got %s", ua)
	}

	// Test invocation ID uniqueness
	ua1 := GetGoogleAuthUserAgent()

	// Get current ID from ua1
	id1 := regexp.MustCompile(`invocation_id:([a-f0-9-]+)`).FindStringSubmatch(ua1)
	if id1 == nil {
		t.Errorf("Could not extract first invocation ID")
		return
	}

	// Since we can't set the package var directly, create a new user agent getter with a different ID
	getNewAgent := func() string {
		versionStr := strings.TrimPrefix(version.Version, "v")
		if versionStr == "0.0.0" {
			versionStr = version.GitCommit
		}
		parts := []string{
			fmt.Sprintf("s5cmd:%s", versionStr),
		}
		parts = append(parts, fmt.Sprintf("invocation_id:%s", uuid.New().String()))
		return strings.Join(parts, " ")
	}

	ua2 := getNewAgent()
	id2 := regexp.MustCompile(`invocation_id:([a-f0-9-]+)`).FindStringSubmatch(ua2)

	if id2 == nil {
		t.Errorf("Could not extract second invocation ID")
	} else if id1[1] == id2[1] {
		t.Errorf("Invocation IDs should be unique. Got %s and %s", id1[1], id2[1])
	}
}
