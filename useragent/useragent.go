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
	invocationID = uuid.New().String()
)

// GetGoogleAuthUserAgent returns a detailed user agent string for Google STS calls
func GetGoogleAuthUserAgent() string {
	// Format version string
	versionStr := strings.TrimPrefix(version.Version, "v")
	if versionStr == "0.0.0" {
		// Use commit hash instead
		versionStr = version.GitCommit
	}

	parts := []string{
		fmt.Sprintf("s5cmd:%s", versionStr),
	}

	// Kubernetes/COO information
	if podName := os.Getenv("COO_POD_NAME"); podName != "" {
		namespace := firstNonEmpty(os.Getenv("COO_NAMESPACE"), "default")
		parts = append(parts, fmt.Sprintf("pod:%s/%s", namespace, podName))
	}

	if stsName := os.Getenv("COO_STS_NAME"); stsName != "" {
		parts = append(parts, fmt.Sprintf("statefulset:%s", stsName))
	}

	if commit := os.Getenv("COO_GIT_COMMIT"); commit != "" {
		parts = append(parts, fmt.Sprintf("commit:%s", commit))
	}

	// Cache information - as separate fields
	cacheInfo := getCacheInfo()
	parts = append(parts, fmt.Sprintf("cache_source:%s", cacheInfo.Source))
	parts = append(parts, fmt.Sprintf("cache_path:%s", cacheInfo.PathHash))
	parts = append(parts, fmt.Sprintf("cache_content:%s", cacheInfo.ContentHash))
	parts = append(parts, fmt.Sprintf("cache_status:%s", cacheInfo.Status))

	parts = append(parts, fmt.Sprintf("invocation_id:%s", invocationID))

	return strings.Join(parts, " ")
}

// CacheInfo holds information about the cache configuration and status
type CacheInfo struct {
	Source      string // Source of cache path configuration
	PathHash    string // Hash of the cache path
	ContentHash string // Hash of the cache content
	Status      string // Status of the cache (writable/readonly/disabled)
}

// firstNonEmpty returns the first non-empty string from the provided values
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func getCacheInfo() CacheInfo {
	// Check if caching is opted out
	if os.Getenv("S5CMD_FILE_CACHING_OPT_OUT") == "true" {
		return CacheInfo{
			Source:      "OPT_OUT",
			PathHash:    "none",
			ContentHash: "none",
			Status:      "disabled",
		}
	}

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

	return CacheInfo{
		Source:      source,
		PathHash:    pathHash,
		ContentHash: contentHash,
		Status:      status,
	}
}
