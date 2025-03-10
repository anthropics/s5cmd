package benchmark

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

var (
	// Command line flags for benchmarks
	benchS3Only = flag.Bool("bench.s3only", false, "Run only S3 benchmarks")
	benchGCSOnly = flag.Bool("bench.gcsonly", false, "Run only GCS benchmarks")
	benchThroughput = flag.Float64("bench.throughput", 100.0, "Simulated throughput in MB/s")
	benchTokenLatency = flag.Duration("bench.tokenlatency", 200*time.Millisecond, "GCS token fetch latency")
	benchErrorRate = flag.Float64("bench.errorrate", 0.0, "Simulate errors at this rate (0.0-1.0)")
	benchWorkers = flag.Int("bench.workers", 10, "Number of worker goroutines")
	benchScenario = flag.String("bench.scenario", "all", "Run only a specific scenario (smallfiles, largefile, mixed, error, gcs)")
	benchHumanReadable = flag.Bool("bench.human", true, "Show human-readable output with units")
)

// TestMain is called before running tests
func TestMain(m *testing.M) {
	// Parse flags
	flag.Parse()
	
	// Print benchmark configuration
	if testing.Verbose() {
		fmt.Printf("Benchmark configuration:\n")
		fmt.Printf("  S3 Only: %v\n", *benchS3Only)
		fmt.Printf("  GCS Only: %v\n", *benchGCSOnly)
		fmt.Printf("  Throughput: %.1f MB/s\n", *benchThroughput)
		fmt.Printf("  Token Latency: %v\n", *benchTokenLatency)
		fmt.Printf("  Error Rate: %.2f\n", *benchErrorRate)
		fmt.Printf("  Workers: %d\n", *benchWorkers)
		fmt.Printf("  Scenario: %s\n", *benchScenario)
		fmt.Printf("  Human-readable: %v\n", *benchHumanReadable)
	}
	
	// Register our custom formatter for human-readable metrics
	// Comment out RegisterMetric which may not be available in all Go versions
	// if *benchHumanReadable {
	// 	testing.RegisterMetric("ns/op", formatTimeForTesting)
	// 	testing.RegisterMetric("MB/s", formatThroughputForTesting)
	// }
	
	// Run tests
	os.Exit(m.Run())
}

// formatTimeForTesting formats nanosecond durations in a human-readable way
// It converts raw nanoseconds to appropriate units (ns, μs, ms, s)
func formatTimeForTesting(value float64) string {
	const (
		nsPerMicro = 1000
		nsPerMilli = 1000 * 1000
		nsPerSec   = 1000 * 1000 * 1000
	)
	
	switch {
	case value < nsPerMicro:
		// Less than 1μs, display as nanoseconds
		return fmt.Sprintf("%.2f ns/op", value)
	case value < nsPerMilli:
		// Less than 1ms, display as microseconds
		return fmt.Sprintf("%.2f μs/op", value/nsPerMicro)
	case value < nsPerSec:
		// Less than 1s, display as milliseconds
		return fmt.Sprintf("%.2f ms/op", value/nsPerMilli)
	default:
		// Display as seconds
		return fmt.Sprintf("%.2f s/op", value/nsPerSec)
	}
}

// formatThroughputForTesting formats throughput values with appropriate units
func formatThroughputForTesting(value float64) string {
	if value < 1.0 {
		// Less than 1 MB/s, show as KB/s
		return fmt.Sprintf("%.2f KB/s", value*1024)
	} else if value > 1000.0 {
		// More than 1000 MB/s, show as GB/s
		return fmt.Sprintf("%.2f GB/s", value/1024)
	}
	// Otherwise show as MB/s
	return fmt.Sprintf("%.2f MB/s", value)
}

// FormatBytes formats a byte count in a human-readable form
func FormatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return strconv.FormatInt(bytes, 10) + " B"
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// ParseMetrics parses a benchmark result string to extract key metrics
func ParseMetrics(result string) map[string]float64 {
	metrics := make(map[string]float64)
	
	// Look for common benchmark patterns like "1234 ns/op" or "42.5 MB/s"
	parts := strings.Fields(result)
	for i := 0; i < len(parts)-1; i++ {
		if strings.Contains(parts[i+1], "/") || strings.Contains(parts[i+1], "%") {
			val, err := strconv.ParseFloat(parts[i], 64)
			if err == nil {
				metrics[parts[i+1]] = val
			}
		}
	}
	
	return metrics
}