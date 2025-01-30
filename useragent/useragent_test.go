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

func TestGetGoogleAuthUserAgent(t *testing.T) {
	// Save original env vars to restore later
	origPodName := os.Getenv("POD_NAME")
	origNamespace := os.Getenv("POD_NAMESPACE")
	origStatefulSet := os.Getenv("STATEFULSET_NAME")
	origNodeName := os.Getenv("NODE_NAME")
	defer func() {
		os.Setenv("POD_NAME", origPodName)
		os.Setenv("POD_NAMESPACE", origNamespace)
		os.Setenv("STATEFULSET_NAME", origStatefulSet)
		os.Setenv("NODE_NAME", origNodeName)
	}()

	// Clear environment variables
	os.Unsetenv("POD_NAME")
	os.Unsetenv("POD_NAMESPACE")
	os.Unsetenv("STATEFULSET_NAME")
	os.Unsetenv("NODE_NAME")

	// Test basic user agent without command or K8s info
	ua := GetGoogleAuthUserAgent()
	if !strings.HasPrefix(ua, "version:") {
		t.Errorf("User agent should start with version:, got %s", ua)
	}
	if !strings.Contains(ua, "invocation_id:") {
		t.Errorf("User agent should contain invocation_id:, got %s", ua)
	}

	// Test with command
	SetCommand("cp")
	ua = GetGoogleAuthUserAgent()
	if !strings.Contains(ua, "command:cp") {
		t.Errorf("User agent should contain command:cp, got %s", ua)
	}

	// Test with K8s env vars
	os.Setenv("POD_NAME", "test-pod")
	os.Setenv("POD_NAMESPACE", "test-ns")
	os.Setenv("STATEFULSET_NAME", "test-sts")
	os.Setenv("NODE_NAME", "test-node")

	ua = GetGoogleAuthUserAgent()
	// Get cache string and verify format
	ua = GetGoogleAuthUserAgent()
	
	// Extract and verify source
	source := regexp.MustCompile(`cache_source:(\w+)`).FindStringSubmatch(ua)
	if source == nil {
		t.Errorf("Cache source not found in UA: %s", ua)
	} else if !contains([]string{"HOME", "XDG", "ENV", "TEMP"}, source[1]) {
		t.Errorf("Cache source %s is not one of expected values", source[1])
	}

	// Extract and verify path hash
	pathHash := regexp.MustCompile(`cache_path:([a-f0-9]{8})`).FindStringSubmatch(ua)
	if pathHash == nil {
		t.Errorf("Cache path hash not found or invalid in UA: %s", ua)
	}

	// Extract and verify content hash
	contentHash := regexp.MustCompile(`cache_content:([a-f0-9]{8}|none)`).FindStringSubmatch(ua)
	if contentHash == nil {
		t.Errorf("Cache content hash not found or invalid in UA: %s", ua)
	}

	// Extract and verify status
	status := regexp.MustCompile(`cache_status:(writable|readonly|unavailable)`).FindStringSubmatch(ua)
	if status == nil {
		t.Errorf("Cache status not found or invalid in UA: %s", ua)
	}

	// Check for invocation ID
	invocationID := regexp.MustCompile(`invocation_id:([a-f0-9-]+)`).FindStringSubmatch(ua)
	if invocationID == nil {
		t.Errorf("Invocation ID not found or invalid in UA: %s", ua)
	}

	expectedParts := []string{
		"version:" + strings.TrimPrefix(version.Version, "v"),
		"command:cp",
		"pod:test-ns/test-pod",
		"statefulset:test-sts",
		"node:test-node",
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

	// Test default namespace when POD_NAMESPACE is not set
	os.Unsetenv("POD_NAMESPACE")
	ua = GetGoogleAuthUserAgent()
	if !strings.Contains(ua, "pod:default/test-pod") {
		t.Errorf("User agent should use default namespace when POD_NAMESPACE is not set, got %s", ua)
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
		parts := []string{
			fmt.Sprintf("version:%s", strings.TrimPrefix(version.Version, "v")),
		}
		if command != "" {
			parts = append(parts, fmt.Sprintf("command:%s", command))
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