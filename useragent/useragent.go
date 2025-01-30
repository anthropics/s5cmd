package useragent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/peak/s5cmd/v2/version"
)

var (
	invocationID string
	command      string
)

func init() {
	invocationID = uuid.New().String()
}

// SetCommand sets the current command being executed
func SetCommand(cmd string) {
	command = cmd
}

// GetGoogleAuthUserAgent returns a detailed user agent string for Google STS calls
func GetGoogleAuthUserAgent() string {
	parts := []string{
		fmt.Sprintf("version:%s", strings.TrimPrefix(version.Version, "v")),
	}

	if command != "" {
		parts = append(parts, fmt.Sprintf("command:%s", command))
	}

	// Kubernetes information
	if podName := os.Getenv("POD_NAME"); podName != "" {
		namespace := os.Getenv("POD_NAMESPACE")
		if namespace == "" {
			namespace = "default"
		}
		parts = append(parts, fmt.Sprintf("pod:%s/%s", namespace, podName))
	}

	if statefulSet := os.Getenv("STATEFULSET_NAME"); statefulSet != "" {
		parts = append(parts, fmt.Sprintf("statefulset:%s", statefulSet))
	}

	if nodeName := os.Getenv("NODE_NAME"); nodeName != "" {
		parts = append(parts, fmt.Sprintf("node:%s", nodeName))
	}

	// Cache information - as separate fields
	source, pathHash, contentHash, status := getCacheInfo()
	parts = append(parts, fmt.Sprintf("cache_source:%s", source))
	parts = append(parts, fmt.Sprintf("cache_path:%s", pathHash))
	parts = append(parts, fmt.Sprintf("cache_content:%s", contentHash))
	parts = append(parts, fmt.Sprintf("cache_status:%s", status))

	parts = append(parts, fmt.Sprintf("invocation_id:%s", invocationID))

	return strings.Join(parts, " ")
}

// getCacheInfo returns cache location source and status
func getCacheInfo() (string, string, string, string) {
	var cachePath string
	var source string

	// Determine source of cache path
	if cachePath = os.Getenv("S5CMD_CACHE_PATH"); cachePath != "" {
		source = "ENV"
	} else if cacheHome := os.Getenv("XDG_CACHE_HOME"); cacheHome != "" {
		source = "XDG"
		cachePath = filepath.Join(cacheHome, "coo")
	} else if home := os.Getenv("HOME"); home != "" {
		source = "HOME"
		cachePath = filepath.Join(home, ".cache", "coo")
	} else {
		source = "TEMP"
		cachePath = filepath.Join(os.TempDir(), "s5cmd-cache")
	}

	// Get short hash of cache path
	hash := sha256.Sum256([]byte(cachePath))
	pathHash := hex.EncodeToString(hash[:])[:8]

	// Full cache file path
	cacheFile := filepath.Join(cachePath, "cached_s5cmd.json")

	// Get content hash if file exists
	contentHash := "none"
	if content, err := os.ReadFile(cacheFile); err == nil {
		h := sha256.Sum256(content)
		contentHash = hex.EncodeToString(h[:])[:8]
	}

	// Determine cache status
	status := "unavailable"
	if _, err := os.Stat(cachePath); err == nil {
		// Try to write a test file to verify permissions
		testFile := filepath.Join(cachePath, ".write-test")
		if err := os.WriteFile(testFile, []byte("test"), 0600); err == nil {
			status = "writable"
			os.Remove(testFile)
		} else {
			status = "readonly"
		}
	} else if os.IsNotExist(err) {
		// Try to create the directory to test writeability
		if err := os.MkdirAll(cachePath, 0700); err == nil {
			status = "writable"
		}
	}

	return source, pathHash, contentHash, status
}