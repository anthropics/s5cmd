package storage_bench

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/peak/s5cmd/v2/storage"
	"github.com/peak/s5cmd/v2/storage/url"
	"go.uber.org/mock/gomock"
	"golang.org/x/oauth2"
	
	"github.com/peak/s5cmd/v2/benchmark/scenarios"
	"github.com/peak/s5cmd/v2/benchmark/storage_bench/flags"
)

// TokenSourceMock implements the oauth2.TokenSource interface
type TokenSourceMock struct {
	RefreshLatency time.Duration
	RefreshCount   int
}

func (m *TokenSourceMock) Token() (*oauth2.Token, error) {
	// Simulate token fetch delay
	time.Sleep(m.RefreshLatency)
	m.RefreshCount++
	
	return &oauth2.Token{
		AccessToken: "mock-token",
		TokenType:   "Bearer",
		Expiry:      time.Now().Add(1 * time.Hour),
	}, nil
}

// For compatibility with benchmark code that expects a TokenManager
func (m *TokenSourceMock) GetToken() (*oauth2.Token, error) {
	return m.Token()
}

func (m *TokenSourceMock) Stop() {
	// No-op for mock
}

func BenchmarkGCSTokenRefresh(b *testing.B) {
	// Use environment variables for benchmark configuration if available
	tokenLatencyStr := os.Getenv("BENCH_TOKEN_LATENCY")
	if tokenLatencyStr != "" {
		// Parse token latency from environment
		duration, err := time.ParseDuration(tokenLatencyStr)
		if err == nil {
			b.Logf("Using token latency from environment: %v", duration)
		}
	}
	
	// Skip if only S3 benchmarks are requested
	if *flags.BenchS3Only {
		b.Skip("Skipping GCS benchmarks because -bench.s3only is set")
	}
	
	// Test different token refresh latencies
	latencies := []time.Duration{
		10 * time.Millisecond,  // Very fast
		50 * time.Millisecond,  // Fast
		200 * time.Millisecond, // Normal
		500 * time.Millisecond, // Slow
	}
	
	for _, latency := range latencies {
		b.Run(fmt.Sprintf("Latency_%dms", latency.Milliseconds()), func(b *testing.B) {
			// Create token manager with configured latency
			m := &TokenSourceMock{
				RefreshLatency: latency,
			}
			
			// Run benchmark
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Perform token fetch
				token, err := m.Token()
				if err != nil {
					b.Fatalf("Token fetch failed: %v", err)
				}
				
				// Verify token
				if token.AccessToken != "mock-token" {
					b.Fatalf("Incorrect token received")
				}
			}
		})
	}
}

func BenchmarkGCSAuthRoundTripper(b *testing.B) {
	// Skip if only S3 benchmarks are requested
	if *flags.BenchS3Only {
		b.Skip("Skipping GCS benchmarks because -bench.s3only is set")
	}
	
	// Simulates the GoogleAuthRoundTripper used by GCS operations
	
	// Create a basic mock token source
	tokenSource := &TokenSourceMock{
		RefreshLatency: 100 * time.Millisecond,
	}
	
	// Create test request
	req, _ := http.NewRequest("GET", "https://storage.googleapis.com/test-bucket/object", nil)
	
	// Run benchmark
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Token fetch (this is the most expensive part)
		token, err := tokenSource.Token()
		if err != nil {
			b.Fatalf("Token fetch failed: %v", err)
		}
		
		// Apply token to request
		token.SetAuthHeader(req)
		
		// Convert headers (simulating what GoogleAuthRoundTripper does)
		for key, values := range req.Header {
			oldKey := key
			newKey := fmt.Sprintf("x-goog-%s", oldKey)
			req.Header.Del(oldKey)
			req.Header[newKey] = values
		}
	}
}

// BenchmarkGCSList mirrors the S3List benchmark
func BenchmarkGCSList(b *testing.B) {
	// Skip if only S3 benchmarks are requested
	if *flags.BenchS3Only {
		b.Skip("Skipping GCS benchmarks because -bench.s3only is set")
	}
	
	// Run benchmarks for different file counts
	fileCounts := []int{10, 100, 1000, 10000}
	
	for _, count := range fileCounts {
		b.Run(gcsTestName(count), func(b *testing.B) {
			// Setup
			ctrl := gomock.NewController(b)
			defer ctrl.Finish()
			
			mockStorage := NewBenchStorage(ctrl)
			tokenSource := NewMockTokenSource()
			tokenSource.RefreshLatency = 200 * time.Millisecond
			
			// Prepare test objects
			objects := SimulateGSObjects("prefix", count, 1024) // 1KB files
			mockStorage.CreateMockObjectLister(objects)
			
			// Create URL for listing
			listURL := &url.URL{
				Scheme:  "gs",
				Bucket:  "test-bucket",
				Path:    "prefix",
			}
			
			// Calculate total size for throughput metrics
			var totalSize int64
			for _, obj := range objects {
				totalSize += obj.Size
			}
			
			// Override List operation to include token refresh
			mockStorage.MockStorage.EXPECT().
				List(gomock.Any(), gomock.Any(), gomock.Any()).
				DoAndReturn(func(ctx context.Context, u *url.URL, followSymlinks bool) <-chan *storage.Object {
					// For GCS, we add token fetch latency to operation
					// Make sure we track this count
					tokenSource.FetchCount++
					time.Sleep(tokenSource.RefreshLatency)
					
					// Create results channel
					ch := make(chan *storage.Object, len(objects))
					
					go func() {
						defer close(ch)
						defer mockStorage.recordOp("List")()
						
						// Send objects with simulated network delay
						for _, obj := range objects {
							// Check if context is cancelled
							select {
							case <-ctx.Done():
								return
							default:
								ch <- obj
								
								// Small delay between objects
								time.Sleep(1 * time.Millisecond)
							}
						}
					}()
					
					return ch
				}).AnyTimes()
			
			// Run benchmark
			b.ResetTimer()
			startTime := time.Now()
			
			for i := 0; i < b.N; i++ {
				ctx := context.Background()
				
				// Start listing operation
				objChan := mockStorage.List(ctx, listURL, false)
				
				// Consume all objects
				var objCount int
				for obj := range objChan {
					if obj.Err != nil {
						b.Fatalf("Error during listing: %v", obj.Err)
					}
					objCount++
				}
				
				if objCount != count {
					b.Fatalf("Expected %d objects, got %d", count, objCount)
				}
			}
			
			// Calculate and report metrics
			elapsed := time.Since(startTime)
			totalObjects := int64(b.N) * int64(count)
			objectsPerSec := float64(totalObjects) / elapsed.Seconds()
			
			// Calculate data throughput
			dataSizeTotal := int64(b.N) * totalSize
			dataThroughput := float64(dataSizeTotal) / elapsed.Seconds() / (1024 * 1024)
			
			// Calculate token metrics
			tokenOverheadTotal := time.Duration(tokenSource.FetchCount) * tokenSource.RefreshLatency
			tokenOverheadPct := float64(tokenOverheadTotal) / float64(elapsed) * 100
			
			// Report custom metrics
			b.ReportMetric(objectsPerSec, "objects/s")
			b.ReportMetric(dataThroughput, "MB/s")
			b.ReportMetric(float64(count), "objects/op")
			b.ReportMetric(float64(tokenSource.FetchCount), "tokenRefreshes")
			b.ReportMetric(tokenOverheadPct, "tokenOverhead%")
			
			// Report operation counts
			for op, opCount := range mockStorage.OpCount {
				b.ReportMetric(float64(opCount), op+"/op")
			}
		})
	}
}

// BenchmarkGCSCopy mirrors the S3Copy benchmark
func BenchmarkGCSCopy(b *testing.B) {
	// Skip if only S3 benchmarks are requested
	if *flags.BenchS3Only {
		b.Skip("Skipping GCS benchmarks because -bench.s3only is set")
	}
	
	// Test different file sizes
	fileSizes := []struct {
		name string
		size int64
	}{
		{"1KB", 1 * 1024},
		{"1MB", 1 * 1024 * 1024},
		{"10MB", 10 * 1024 * 1024},
		{"100MB", 100 * 1024 * 1024},
		{"1GB", 1 * 1024 * 1024 * 1024},
	}
	
	for _, fs := range fileSizes {
		b.Run(fs.name, func(b *testing.B) {
			// Setup
			ctrl := gomock.NewController(b)
			defer ctrl.Finish()
			
			mockStorage := NewBenchStorage(ctrl)
			tokenSource := NewMockTokenSource()
			tokenSource.RefreshLatency = 200 * time.Millisecond
			
			// Configure throughput based on file size
			if fs.size > 10*1024*1024 {
				mockStorage.Throughput = 150.0 // Higher throughput for larger files
			}
			
			// Create source and destination URLs
			srcURL := &url.URL{
				Scheme: "gs",
				Bucket: "test-bucket",
				Path:   "source/file",
			}
			
			dstURL := &url.URL{
				Scheme: "gs",
				Bucket: "dest-bucket",
				Path:   "dest/file",
			}
			
			// Setup mock behavior for Copy with token refresh
			mockStorage.MockStorage.EXPECT().
				Copy(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
				DoAndReturn(func(ctx context.Context, src, dst *url.URL, metadata storage.Metadata) error {
					defer mockStorage.recordOp("Copy")()
					
					// Simulate token refresh
					tokenSource.FetchCount++
					time.Sleep(tokenSource.RefreshLatency)
					
					// Simulate data transfer based on throughput
					bytesToTransfer := fs.size
					throughputBytesPerMs := (mockStorage.Throughput * 1024 * 1024) / 1000
					
					// Track bytes written
					mockStorage.mutex.Lock()
					mockStorage.BytesWritten += bytesToTransfer
					mockStorage.mutex.Unlock()
					
					// Simulate transfer time
					transferTime := time.Duration(float64(bytesToTransfer) / throughputBytesPerMs) * time.Millisecond
					time.Sleep(transferTime)
					
					return nil
				}).AnyTimes()
			
			// Run benchmark
			b.ResetTimer()
			startTime := time.Now()
			
			for i := 0; i < b.N; i++ {
				ctx := context.Background()
				
				// Execute copy operation
				err := mockStorage.Copy(ctx, srcURL, dstURL, storage.Metadata{})
				if err != nil {
					b.Fatalf("Copy operation failed: %v", err)
				}
			}
			
			// Calculate and report metrics
			elapsed := time.Since(startTime)
			totalBytes := int64(b.N) * fs.size
			bytesPerSec := float64(totalBytes) / elapsed.Seconds()
			mbPerSec := bytesPerSec / (1024 * 1024)
			
			// Calculate token metrics
			tokenOverheadTotal := time.Duration(tokenSource.FetchCount) * tokenSource.RefreshLatency
			tokenOverheadPct := float64(tokenOverheadTotal) / float64(elapsed) * 100
			
			// Report custom metrics
			b.ReportMetric(mbPerSec, "MB/s")
			b.ReportMetric(float64(totalBytes)/(1024*1024), "MB(total)")
			b.ReportMetric(float64(tokenSource.FetchCount), "tokenRefreshes")
			b.ReportMetric(tokenOverheadPct, "tokenOverhead%")
			
			// Report operation counts
			for op, count := range mockStorage.OpCount {
				b.ReportMetric(float64(count), op+"/op")
			}
		})
	}
}

// BenchmarkGCSDelete mirrors the S3Delete benchmark
func BenchmarkGCSDelete(b *testing.B) {
	// Skip if only S3 benchmarks are requested
	if *flags.BenchS3Only {
		b.Skip("Skipping GCS benchmarks because -bench.s3only is set")
	}
	
	// Run benchmarks for different file counts
	fileCounts := []int{1, 10, 100, 1000}
	
	for _, count := range fileCounts {
		b.Run(gcsTestName(count), func(b *testing.B) {
			// Setup
			ctrl := gomock.NewController(b)
			defer ctrl.Finish()
			
			mockStorage := NewBenchStorage(ctrl)
			tokenSource := NewMockTokenSource()
			tokenSource.RefreshLatency = 200 * time.Millisecond
			
			// Prepare URLs for deletion
			urls := make([]*url.URL, count)
			for i := 0; i < count; i++ {
				urls[i] = &url.URL{
					Scheme: "gs",
					Bucket: "test-bucket",
					Path:   fmt.Sprintf("prefix/file-%d", i),
				}
			}
			
			// Setup mock behavior for MultiDelete with token refresh
			mockStorage.MockStorage.EXPECT().
				MultiDelete(gomock.Any(), gomock.Any()).
				DoAndReturn(func(ctx context.Context, urlChan <-chan *url.URL) <-chan *storage.Object {
					// Collect URLs first to know the count
					var urls []*url.URL
					for u := range urlChan {
						urls = append(urls, u)
					}
					
					count := len(urls)
					resultCh := make(chan *storage.Object, count)
					
					go func() {
						defer close(resultCh)
						defer mockStorage.recordOp("MultiDelete")()
						
						// Simulate token refresh for GCS
						tokenSource.FetchCount++
						time.Sleep(tokenSource.RefreshLatency)
						
						// Simulate batch processing with appropriate delays
						batches := (count + 999) / 1000 // Allow up to 1000 keys per request
						for i := 0; i < batches; i++ {
							start := i * 1000
							end := (i + 1) * 1000
							if end > count {
								end = count
							}
							
							// Increment batch delete operation counter
							mockStorage.mutex.Lock()
							mockStorage.OpCount["DeleteBatch"]++
							mockStorage.mutex.Unlock()
							
							// Simulate batch delete latency
							time.Sleep(mockStorage.Latency["Delete"])
							
							// Return results
							for j := start; j < end; j++ {
								resultCh <- &storage.Object{URL: urls[j]}
							}
						}
					}()
					
					return resultCh
				}).AnyTimes()
			
			// Run benchmark
			b.ResetTimer()
			startTime := time.Now()
			
			for i := 0; i < b.N; i++ {
				ctx := context.Background()
				
				// Create URL channel
				urlChan := make(chan *url.URL, count)
				go func() {
					defer close(urlChan)
					for _, u := range urls {
						urlChan <- u
					}
				}()
				
				// Execute multi-delete operation
				resultChan := mockStorage.MultiDelete(ctx, urlChan)
				
				// Consume all results
				deletedCount := 0
				for result := range resultChan {
					if result.Err != nil {
						b.Fatalf("Delete operation failed: %v", result.Err)
					}
					deletedCount++
				}
				
				if deletedCount != count {
					b.Fatalf("Expected %d deletions, got %d", count, deletedCount)
				}
			}
			
			// Calculate and report metrics
			elapsed := time.Since(startTime)
			totalDeleted := int64(b.N) * int64(count)
			objectsPerSec := float64(totalDeleted) / elapsed.Seconds()
			batchesPerSec := float64(mockStorage.OpCount["DeleteBatch"]) / elapsed.Seconds()
			
			// Calculate token metrics
			tokenOverheadTotal := time.Duration(tokenSource.FetchCount) * tokenSource.RefreshLatency
			tokenOverheadPct := float64(tokenOverheadTotal) / float64(elapsed) * 100
			
			// Assume 1KB per deletion metadata for throughput calculation
			metadataSize := int64(1024) // 1KB
			metadataThroughput := float64(totalDeleted*metadataSize) / elapsed.Seconds() / (1024 * 1024)
			
			// Report custom metrics
			b.ReportMetric(objectsPerSec, "deletes/s")
			b.ReportMetric(batchesPerSec, "batches/s")
			b.ReportMetric(metadataThroughput, "MB/s")
			b.ReportMetric(float64(count), "objects/op")
			b.ReportMetric(float64(tokenSource.FetchCount), "tokenRefreshes")
			b.ReportMetric(tokenOverheadPct, "tokenOverhead%")
			
			// Report operation counts
			for op, opCount := range mockStorage.OpCount {
				b.ReportMetric(float64(opCount), op+"/op")
			}
		})
	}
}

// BenchmarkGCSScenario mirrors the S3Scenario benchmark
func BenchmarkGCSScenario(b *testing.B) {
	// Skip if only S3 benchmarks are requested
	if *flags.BenchS3Only {
		b.Skip("Skipping GCS benchmarks because -bench.s3only is set")
	}
	
	// Get the same scenarios used for S3 but adapt them for GCS
	gcsScenarios := getGCSScenarios()
	
	// Test each scenario
	for _, scenario := range gcsScenarios {
		b.Run(scenario.Name, func(b *testing.B) {
			// Setup
			ctrl := gomock.NewController(b)
			defer ctrl.Finish()
			
			mockStorage := NewBenchStorage(ctrl)
			tokenSource := NewMockTokenSource()
			
			// Configure mock according to scenario
			mockStorage.Latency = scenario.StorageLatency
			mockStorage.Throughput = scenario.Throughput
			mockStorage.ErrorRate = scenario.ErrorRate
			tokenSource.RefreshLatency = scenario.GCSTokenLatency
			
			// Generate test objects based on scenario
			var objects []*storage.Object
			if scenario.Name == "MixedFiles" {
				objects = scenarios.GenerateMixedObjects("mixed", scenario.ObjectCount)
				// Convert to GCS objects
				for _, obj := range objects {
					obj.URL.Scheme = "gs"
				}
			} else {
				// For SmallFiles scenario, limit the number of objects to process to avoid excessive runtime
				objCount := scenario.ObjectCount
				if scenario.Name == "SmallFiles" {
					objCount = 100 // Process only 100 objects instead of 10000
					b.Logf("Limiting %s scenario to %d objects", scenario.Name, objCount)
				}
				objects = SimulateGSObjects(scenario.Name, objCount, scenario.ObjectSize)
			}
			
			// Setup mock behaviors with token refresh
			mockStorage.CreateMockObjectLister(objects)
			mockStorage.SetupCopyOperation(objects, scenario.ObjectSize)
			
			// Override List operation to include token refresh
			mockStorage.MockStorage.EXPECT().
				List(gomock.Any(), gomock.Any(), gomock.Any()).
				DoAndReturn(func(ctx context.Context, u *url.URL, followSymlinks bool) <-chan *storage.Object {
					// For GCS, we add token fetch latency to operation
					tokenSource.FetchCount++
					time.Sleep(tokenSource.RefreshLatency)
					
					// Create results channel
					ch := make(chan *storage.Object, len(objects))
					
					go func() {
						defer close(ch)
						defer mockStorage.recordOp("List")()
						
						// Send objects with simulated network delay
						for _, obj := range objects {
							// Check if context is cancelled
							select {
							case <-ctx.Done():
								return
							default:
								ch <- obj
								
								// Small delay between objects
								time.Sleep(1 * time.Millisecond)
							}
						}
					}()
					
					return ch
				}).AnyTimes()
			
			// Override Copy operation to include token refresh
			mockStorage.MockStorage.EXPECT().
				Copy(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
				DoAndReturn(func(ctx context.Context, src, dst *url.URL, metadata storage.Metadata) error {
					defer mockStorage.recordOp("Copy")()
					
					// For GCS, we add token fetch latency to operation
					tokenSource.FetchCount++
					time.Sleep(tokenSource.RefreshLatency)
					
					// Simulate error based on error rate
					if mockStorage.ErrorRate > 0 && rand.Float64() < mockStorage.ErrorRate {
						return fmt.Errorf("simulated error")
					}
					
					// For regular operations, simulate throughput-based delay
					size := scenario.ObjectSize
					for _, obj := range objects {
						if obj.URL.Path == src.Path {
							size = obj.Size
							break
						}
					}
					
					// Prevent division by zero
					if mockStorage.Throughput <= 0 {
						mockStorage.Throughput = 1.0
					}
					
					// Calculate transfer time
					bytesPerMs := (mockStorage.Throughput * 1024 * 1024) / 1000
					sleepTime := time.Duration(float64(size) / bytesPerMs) * time.Millisecond
					
					// Apply minimum latency if transfer would be too fast
					minLatency := mockStorage.Latency["Copy"]
					if sleepTime < minLatency {
						sleepTime = minLatency
					}
					
					// Track bytes transferred
					mockStorage.mutex.Lock()
					mockStorage.BytesWritten += size
					mockStorage.mutex.Unlock()
					
					time.Sleep(sleepTime)
					return nil
				}).AnyTimes()
			
			// Run benchmark
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				runGCSScenario(b, mockStorage)
			}
			
			// Report token metrics
			b.ReportMetric(float64(tokenSource.FetchCount), "tokenRefreshes")
		})
	}
}

// Helper function to get GCS versions of the standard scenarios
func getGCSScenarios() []*scenarios.BenchmarkScenario {
	// Create copies of the standard scenarios but update for GCS
	gcsScenarios := make([]*scenarios.BenchmarkScenario, 0, len(scenarios.AllScenarios))
	
	for _, s := range scenarios.AllScenarios {
		// Skip the dedicated GCSOperations scenario as we're creating GCS versions of all scenarios
		if s == scenarios.GCSOperations {
			continue
		}
		
		// Create a copy of the scenario
		gcsScenario := &scenarios.BenchmarkScenario{
			Name:            s.Name,
			Description:     "GCS: " + s.Description,
			ObjectCount:     s.ObjectCount,
			ObjectSize:      s.ObjectSize,
			Throughput:      s.Throughput,
			ErrorRate:       s.ErrorRate,
			GCSTokenLatency: 200 * time.Millisecond, // Default token latency
			
			// Copy storage latency map
			StorageLatency: make(map[string]time.Duration),
		}
		
		// Copy storage latency map
		for k, v := range s.StorageLatency {
			gcsScenario.StorageLatency[k] = v
		}
		
		// Update commands to use gs:// instead of s3://
		cmds := make([]string, len(s.Commands))
		for i, cmd := range s.Commands {
			cmds[i] = strings.Replace(cmd, "s3://", "gs://", -1)
		}
		gcsScenario.Commands = cmds
		
		gcsScenarios = append(gcsScenarios, gcsScenario)
	}
	
	return gcsScenarios
}

func runGCSScenario(b *testing.B, mockStorage *BenchStorage) {
	ctx := context.Background()
	
	// Create URLs manually
	srcURL := &url.URL{
		Scheme: "gs",
		Bucket: "test-bucket",
		Path:   "gcs/",
	}
	
	dstURL := &url.URL{
		Scheme: "gs",
		Bucket: "dest-bucket",
		Path:   "gcs/",
	}
	
	// List source objects
	objChan := mockStorage.List(ctx, srcURL, false)
	
	// Copy each object
	var objCount int
	for obj := range objChan {
		if obj.Err != nil {
			b.Fatalf("Error during listing: %v", obj.Err)
		}
		
		// Create destination path
		srcObj := obj.URL.Clone()
		dstObj := dstURL.Clone()
		dstObj.Path = dstURL.Path + "/" + obj.URL.Base()
		
		// Execute copy
		err := mockStorage.Copy(ctx, srcObj, dstObj, storage.Metadata{})
		if err != nil {
			// For error scenarios, errors are expected
			if mockStorage.ErrorRate > 0 {
				continue
			}
			b.Fatalf("Copy operation failed: %v", err)
		}
		
		objCount++
	}
}

func gcsTestName(count int) string {
	return fmt.Sprintf("Count_%d", count)
}