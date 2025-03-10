package scenarios

import (
	"fmt"
	"time"

	"github.com/peak/s5cmd/v2/storage"
	"github.com/peak/s5cmd/v2/storage/url"
)

// BenchmarkScenario defines a specific benchmark configuration
type BenchmarkScenario struct {
	Name            string                  // Human-readable name
	Description     string                  // Description of what's being tested
	ObjectCount     int                     // Number of objects
	ObjectSize      int64                   // Size per object in bytes
	Commands        []string                // Command line arguments
	StorageLatency  map[string]time.Duration // Operation latencies
	Throughput      float64                 // MB/s for data operations
	ErrorRate       float64                 // 0.0-1.0 probability of error
	GCSTokenLatency time.Duration           // Latency for GCS token operations
}

// Predefined benchmark scenarios
var (
	// SmallFiles tests operations on many small files
	SmallFiles = &BenchmarkScenario{
		Name:        "SmallFiles",
		Description: "Many small files (10,000 x 1KB)",
		ObjectCount: 10000,
		ObjectSize:  1024, // 1KB
		Commands:    []string{"cp", "s3://test-bucket/small/", "s3://dest-bucket/small/"},
		StorageLatency: map[string]time.Duration{
			"List":  5 * time.Millisecond,
			"Stat":  2 * time.Millisecond,
			"Copy":  10 * time.Millisecond,
		},
		Throughput: 100.0, // 100 MB/s
	}

	// LargeFile tests operations on a single large file
	LargeFile = &BenchmarkScenario{
		Name:        "LargeFile",
		Description: "Single large file (1 x 5GB)",
		ObjectCount: 1,
		ObjectSize:  5 * 1024 * 1024 * 1024, // 5GB
		Commands:    []string{"cp", "s3://test-bucket/large/file", "s3://dest-bucket/large/file"},
		StorageLatency: map[string]time.Duration{
			"List":  5 * time.Millisecond,
			"Stat":  2 * time.Millisecond,
			"Copy":  200 * time.Millisecond,
		},
		Throughput: 100.0, // 100 MB/s
	}

	// MixedFiles tests operations on a mix of file sizes
	MixedFiles = &BenchmarkScenario{
		Name:        "MixedFiles",
		Description: "Mix of file sizes (100 x varying sizes)",
		ObjectCount: 100,
		ObjectSize:  0, // Varies by object
		Commands:    []string{"cp", "s3://test-bucket/mixed/", "s3://dest-bucket/mixed/"},
		StorageLatency: map[string]time.Duration{
			"List":  5 * time.Millisecond,
			"Stat":  2 * time.Millisecond,
			"Copy":  20 * time.Millisecond,
		},
		Throughput: 100.0, // 100 MB/s
	}

	// ErrorHandling tests retry behavior
	ErrorHandling = &BenchmarkScenario{
		Name:        "ErrorHandling",
		Description: "Operations with intermittent failures",
		ObjectCount: 100,
		ObjectSize:  1024 * 1024, // 1MB
		Commands:    []string{"cp", "--retry", "3", "s3://test-bucket/errors/", "s3://dest-bucket/errors/"},
		StorageLatency: map[string]time.Duration{
			"List":  5 * time.Millisecond,
			"Stat":  2 * time.Millisecond,
			"Copy":  20 * time.Millisecond,
		},
		Throughput: 100.0, // 100 MB/s
		ErrorRate:  0.2,   // 20% of operations fail
	}

	// GCSOperations tests GCS specific operations
	GCSOperations = &BenchmarkScenario{
		Name:        "GCSOperations",
		Description: "GCS operations with token refresh",
		ObjectCount: 100,
		ObjectSize:  1024 * 1024, // 1MB
		Commands:    []string{"cp", "gs://test-bucket/gcs/", "gs://dest-bucket/gcs/"},
		StorageLatency: map[string]time.Duration{
			"List":  5 * time.Millisecond,
			"Stat":  2 * time.Millisecond,
			"Copy":  20 * time.Millisecond,
		},
		Throughput:      100.0, // 100 MB/s
		GCSTokenLatency: 200 * time.Millisecond,
	}
)

// AllScenarios contains all the defined benchmark scenarios
var AllScenarios = []*BenchmarkScenario{
	SmallFiles,
	LargeFile,
	MixedFiles,
	ErrorHandling,
	GCSOperations,
}

// GenerateMixedObjects creates a slice of objects with varying sizes
func GenerateMixedObjects(prefix string, count int) []*storage.Object {
	objects := make([]*storage.Object, count)
	now := time.Now()

	// Create a mix of small, medium, and large files
	for i := 0; i < count; i++ {
		var size int64
		
		// Distribute sizes: 70% small, 20% medium, 10% large
		switch {
		case i < int(0.7*float64(count)):
			size = 1024 // 1KB
		case i < int(0.9*float64(count)):
			size = 1024 * 1024 // 1MB
		default:
			size = 10 * 1024 * 1024 // 10MB
		}

		objects[i] = &storage.Object{
			URL: &url.URL{
				Scheme: "s3",
				Bucket: "test-bucket",
				Path:   fmt.Sprintf("%s/file-%d", prefix, i),
			},
			Size:    size,
			ModTime: &now,
		}
	}

	return objects
}