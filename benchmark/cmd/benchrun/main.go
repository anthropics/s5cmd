package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	
	"github.com/peak/s5cmd/v2/benchmark"
)

func main() {
	// Command line flags
	s3Only := flag.Bool("s3", false, "Run only S3 benchmarks")
	gcsOnly := flag.Bool("gcs", false, "Run only GCS benchmarks")
	throughput := flag.Float64("throughput", 100.0, "Simulated throughput in MB/s")
	tokenLatency := flag.Duration("token-latency", 200*time.Millisecond, "GCS token fetch latency")
	errorRate := flag.Float64("error-rate", 0.0, "Simulate errors at this rate (0.0-1.0)")
	workers := flag.Int("workers", 10, "Number of worker goroutines")
	scenario := flag.String("scenario", "all", "Run only a specific scenario (smallfiles, largefile, mixed, error, gcs)")
	compare := flag.Bool("compare", false, "Compare with previous benchmark run (requires benchmark.out file)")
	debug := flag.Bool("debug", false, "Print verbose debug information")
	
	flag.Parse()
	
	// Configure logging based on debug flag
	logLevel := slog.LevelInfo
	if *debug {
		logLevel = slog.LevelDebug
		handler := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
			Level: logLevel,
		})
		slog.SetDefault(slog.New(handler))
	} else {
		handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: logLevel,
		})
		slog.SetDefault(slog.New(handler))
	}
	
	// Print user-friendly summary
	fmt.Println("Running benchmarks with parameters:")
	fmt.Printf("  Throughput: %.1f MB/s\n", *throughput)
	fmt.Printf("  Token Latency: %v\n", *tokenLatency)
	fmt.Printf("  Error Rate: %.2f\n", *errorRate)
	fmt.Printf("  Workers: %d\n", *workers)
	fmt.Printf("  Scenario: %s\n", *scenario)
	
	// Get current directory to ensure we execute from the module root
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Printf("Error getting current directory: %v\n", err)
		os.Exit(1)
	}
	
	// Change to the benchmark directory
	benchDir := filepath.Join(cwd, "benchmark", "storage_bench")
	
	// Build benchmark filter
	benchFilter := "."
	if *gcsOnly {
		benchFilter = "GCS"
	} else if *s3Only {
		benchFilter = "S3"
	}
	
	// Build command
	var cmd *exec.Cmd
	if *debug {
		cmd = exec.Command("go", "test", "-v", "-bench="+benchFilter, "-benchmem")
	} else {
		cmd = exec.Command("go", "test", "-bench="+benchFilter, "-benchmem")
	}
	
	// Set working directory
	cmd.Dir = benchDir
	
	// Set environment variables for the benchmark parameters
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("BENCH_THROUGHPUT=%f", *throughput),
		fmt.Sprintf("BENCH_TOKEN_LATENCY=%v", *tokenLatency),
		fmt.Sprintf("BENCH_ERROR_RATE=%f", *errorRate),
		fmt.Sprintf("BENCH_WORKERS=%d", *workers),
		fmt.Sprintf("BENCH_SCENARIO=%s", *scenario),
	)
	
	// Run the command
	slog.Debug("Running benchmark command", "cmd", cmd.String(), "dir", cmd.Dir)
	var benchOutput []byte
	if *debug {
		cmd.Stderr = os.Stderr
		benchOutput, err = cmd.Output()
	} else {
		benchOutput, err = cmd.CombinedOutput()
	}
	
	if err != nil {
		slog.Error("Benchmark command failed", "error", err)
		if !*debug && len(benchOutput) > 0 {
			// Print error output
			fmt.Println("\nCommand output:")
			fmt.Println(string(benchOutput))
		}
		os.Exit(1)
	}
	
	// Parse and display results
	slog.Debug("Benchmark raw output", "output", string(benchOutput))
	results := benchmark.ParseBenchmarkOutput(string(benchOutput))
	
	fmt.Println("\nBenchmark Summary:")
	fmt.Println("=================")
	benchmark.PrintSummary(results)
	
	if len(results) == 0 {
		fmt.Println("\nNo benchmark results found. This could be because:")
		fmt.Println("1. The benchmark filter didn't match any tests")
		fmt.Println("2. The benchmarks failed to run properly")
	}
	
	// Save benchmark output to file for future comparison
	err = os.WriteFile("benchmark.out", benchOutput, 0644)
	if err != nil {
		slog.Error("Error saving benchmark output", "error", err)
		fmt.Printf("Error saving benchmark output: %v\n", err)
	}
	
	// Compare with previous run if requested
	if *compare && fileExists("benchmark.prev") {
		fmt.Println("\nComparing with previous benchmark run:")
		fmt.Println("=====================================")
		
		prevOutput, err := os.ReadFile("benchmark.prev")
		if err != nil {
			fmt.Printf("Error reading previous benchmark file: %v\n", err)
			os.Exit(1)
		}
		
		prevResults := benchmark.ParseBenchmarkOutput(string(prevOutput))
		compareResults(results, prevResults)
	}
	
	// Rotate benchmark files
	if fileExists("benchmark.out") {
		// Move current to previous
		os.Rename("benchmark.out", "benchmark.prev")
	}
}

// fileExists checks if a file exists
func fileExists(filename string) bool {
	_, err := os.Stat(filename)
	return err == nil
}

// compareResults shows percentage changes between benchmark runs
func compareResults(current, previous []benchmark.BenchmarkResult) {
	// Create a map of previous results by name for easier lookup
	prevMap := make(map[string]benchmark.BenchmarkResult)
	for _, r := range previous {
		prevMap[r.Name] = r
	}
	
	// Create tabwriter for aligned output
	w := new(strings.Builder)
	fmt.Fprintln(w, "Benchmark\tTime\tThroughput\tObjects/s")
	fmt.Fprintln(w, "---------\t----\t----------\t---------")
	
	for _, curr := range current {
		if prev, ok := prevMap[curr.Name]; ok {
			// Calculate percentage changes with safeguards against division by zero
			var timeChange, throughputChange, objectsChange float64
			
			if prev.TimePerOp > 0 {
				timeChange = (curr.TimePerOp - prev.TimePerOp) / prev.TimePerOp * 100
			}
			
			if prev.Throughput > 0 {
				throughputChange = (curr.Throughput - prev.Throughput) / prev.Throughput * 100
			}
			
			if prev.ObjectsPerS > 0 {
				objectsChange = (curr.ObjectsPerS - prev.ObjectsPerS) / prev.ObjectsPerS * 100
			}
			
			// Format changes with direction indicators
			timeStr := formatChange(timeChange, true) // Negative is better for time
			throughputStr := formatChange(throughputChange, false) // Positive is better for throughput
			objectsStr := formatChange(objectsChange, false) // Positive is better for objects/s
			
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", 
				curr.Name, timeStr, throughputStr, objectsStr)
		}
	}
	
	// Print comparison
	fmt.Println(w.String())
}

// formatChange formats a percentage change with color indicators
func formatChange(pct float64, inversed bool) string {
	// If inversed is true, negative percentages are good (e.g., for time)
	// Otherwise, positive percentages are good (e.g., for throughput)
	
	if pct > 0 {
		if inversed {
			return fmt.Sprintf("🔴 +%.1f%%", pct) // Worse
		}
		return fmt.Sprintf("🟢 +%.1f%%", pct) // Better
	} else if pct < 0 {
		if inversed {
			return fmt.Sprintf("🟢 %.1f%%", pct) // Better
		}
		return fmt.Sprintf("🔴 %.1f%%", pct) // Worse
	}
	
	return "→ 0.0%" // No change
}