package benchmark

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
)

// BenchmarkResult holds parsed benchmark results
type BenchmarkResult struct {
	Name        string
	TimePerOp   float64  // in nanoseconds
	Throughput  float64  // in MB/s
	ObjectsPerS float64  // objects processed per second
	OpCounts    map[string]float64
	TokenMetrics map[string]float64
}

// PrintSummary prints a human-readable summary of benchmark results
func PrintSummary(results []BenchmarkResult) {
	// Create tabwriter for aligned output
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	
	// Print header
	fmt.Fprintln(w, "Benchmark\tTime\tThroughput\tObjects/s\tKey Operations")
	fmt.Fprintln(w, "---------\t----\t----------\t---------\t--------------")
	
	// Print each result
	for _, r := range results {
		// Format time based on scale
		timeStr := formatTime(r.TimePerOp)
		
		// Format throughput
		throughputStr := formatThroughput(r.Throughput)
		
		// Format objects per second
		objPerSecStr := fmt.Sprintf("%.0f", r.ObjectsPerS)
		
		// Format key operations
		opStr := formatOperations(r.OpCounts)
		
		// Print the row
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", 
			r.Name, timeStr, throughputStr, objPerSecStr, opStr)
	}
	
	// Flush the writer
	w.Flush()
	
	// Print token metrics if present in any result
	hasTokenMetrics := false
	for _, r := range results {
		if len(r.TokenMetrics) > 0 {
			hasTokenMetrics = true
			break
		}
	}
	
	if hasTokenMetrics {
		fmt.Println("\nGCS Token Metrics:")
		fmt.Println("------------------")
		
		w = tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "Benchmark\tToken Refreshes\tToken Overhead")
		fmt.Fprintln(w, "---------\t---------------\t--------------")
		
		for _, r := range results {
			if refreshes, ok := r.TokenMetrics["tokenRefreshes"]; ok {
				overhead := "N/A"
				if pct, exists := r.TokenMetrics["tokenOverhead%"]; exists {
					overhead = fmt.Sprintf("%.1f%%", pct)
				}
				
				fmt.Fprintf(w, "%s\t%.0f\t%s\n", r.Name, refreshes, overhead)
			}
		}
		
		w.Flush()
	}
}

// formatOperations formats operation counts in a concise way
func formatOperations(ops map[string]float64) string {
	// Only include the most important operations
	parts := []string{}
	
	priority := []string{"List", "Copy", "MultiDelete", "DeleteBatch"}
	
	for _, key := range priority {
		if val, ok := ops[key+"/op"]; ok && val > 0 {
			parts = append(parts, fmt.Sprintf("%s:%.0f", key, val))
		}
	}
	
	if len(parts) == 0 {
		return "N/A"
	}
	
	return strings.Join(parts, ", ")
}

// Add formatter functions within the benchmark package
func formatTime(value float64) string {
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

// formatThroughput formats throughput values with appropriate units
func formatThroughput(value float64) string {
	if value == 0 {
		return "N/A"
	}
	
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

// parseMetrics parses a benchmark result string to extract key metrics
func parseMetrics(result string) map[string]float64 {
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

// ParseBenchmarkOutput parses the Go benchmark output
func ParseBenchmarkOutput(output string) []BenchmarkResult {
	lines := strings.Split(output, "\n")
	var results []BenchmarkResult
	
	for _, line := range lines {
		// Only process benchmark lines with results
		if strings.HasPrefix(line, "Benchmark") && len(strings.Fields(line)) > 3 {
			// Process this benchmark line
			result := BenchmarkResult{
				OpCounts:     make(map[string]float64),
				TokenMetrics: make(map[string]float64),
			}
			
			// Get the benchmark name (the first field)
			fields := strings.Fields(line)
			result.Name = fields[0]
			
			// Extract and parse all numeric values from the line
			metrics := parseMetrics(line)
			
			// Extract key metrics
			result.TimePerOp = metrics["ns/op"]
			result.Throughput = metrics["MB/s"]
			result.ObjectsPerS = metrics["objects/s"]
			
			// Explicitly check for token metrics
			if val, ok := metrics["tokenRefreshes"]; ok {
				result.TokenMetrics["tokenRefreshes"] = val
			}
			if val, ok := metrics["tokenOverhead%"]; ok {
				result.TokenMetrics["tokenOverhead%"] = val
			}
			
			// Sort other metrics into categories
			for key, val := range metrics {
				if strings.HasSuffix(key, "/op") {
					result.OpCounts[key] = val
				} else if strings.Contains(key, "token") && 
					key != "tokenRefreshes" && key != "tokenOverhead%" {
					result.TokenMetrics[key] = val
				}
			}
			
			// Only add valid benchmark results that have at least time measurements
			if result.TimePerOp > 0 || result.Throughput > 0 || result.ObjectsPerS > 0 {
				results = append(results, result)
			}
		}
	}
	
	return results
}