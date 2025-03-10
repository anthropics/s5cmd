package storage_bench

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/peak/s5cmd/v2/storage"
	"github.com/peak/s5cmd/v2/storage/url"
	"go.uber.org/mock/gomock"
	
	"github.com/peak/s5cmd/v2/benchmark/scenarios"
	"github.com/peak/s5cmd/v2/benchmark/storage_bench/flags"
)

func BenchmarkS3List(b *testing.B) {
	// Skip if only GCS benchmarks are requested
	if *flags.BenchGCSOnly {
		b.Skip("Skipping S3 benchmarks because -bench.gcsonly is set")
	}
	
	// Run benchmarks for different file counts
	fileCounts := []int{10, 100, 1000, 10000}
	
	for _, count := range fileCounts {
		b.Run(testName(count), func(b *testing.B) {
			// Setup
			ctrl := gomock.NewController(b)
			defer ctrl.Finish()
			
			mockStorage := NewBenchStorage(ctrl)
			
			// Prepare test objects
			objects := SimulateS3Objects("prefix", count, 1024) // 1KB files
			mockStorage.CreateMockObjectLister(objects)
			
			// Create URL for listing
			listURL := &url.URL{
				Scheme:  "s3",
				Bucket:  "test-bucket",
				Path:    "prefix",
			}
			
			// Calculate total size for throughput metrics
			var totalSize int64
			for _, obj := range objects {
				totalSize += obj.Size
			}
			
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
			
			// Calculate data throughput (assuming metadata is returned)
			dataSizeTotal := int64(b.N) * totalSize
			dataThroughput := float64(dataSizeTotal) / elapsed.Seconds() / (1024 * 1024)
			
			// Report custom metrics
			b.ReportMetric(objectsPerSec, "objects/s")
			b.ReportMetric(dataThroughput, "MB/s")
			b.ReportMetric(float64(count), "objects/op")
			
			// Report operation counts
			for op, opCount := range mockStorage.OpCount {
				b.ReportMetric(float64(opCount), op+"/op")
			}
		})
	}
}

func BenchmarkS3Copy(b *testing.B) {
	// Skip if only GCS benchmarks are requested
	if *flags.BenchGCSOnly {
		b.Skip("Skipping S3 benchmarks because -bench.gcsonly is set")
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
			
			// Configure throughput based on file size
			if fs.size > 10*1024*1024 {
				mockStorage.Throughput = 150.0 // Higher throughput for larger files
			}
			
			// Create source and destination URLs
			srcURL := &url.URL{
				Scheme: "s3",
				Bucket: "test-bucket",
				Path:   "source/file",
			}
			
			dstURL := &url.URL{
				Scheme: "s3",
				Bucket: "dest-bucket",
				Path:   "dest/file",
			}
			
			// Setup mock behavior for Copy
			mockStorage.MockStorage.EXPECT().
				Copy(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
				DoAndReturn(func(ctx context.Context, src, dst *url.URL, metadata storage.Metadata) error {
					defer mockStorage.recordOp("Copy")()
					
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
			
			// Report custom metrics
			b.ReportMetric(mbPerSec, "MB/s")
			b.ReportMetric(float64(totalBytes)/(1024*1024), "MB(total)")
			
			// Report operation counts
			for op, count := range mockStorage.OpCount {
				b.ReportMetric(float64(count), op+"/op")
			}
		})
	}
}

func BenchmarkS3Delete(b *testing.B) {
	// Skip if only GCS benchmarks are requested
	if *flags.BenchGCSOnly {
		b.Skip("Skipping S3 benchmarks because -bench.gcsonly is set")
	}
	
	// Run benchmarks for different file counts
	fileCounts := []int{1, 10, 100, 1000}
	
	for _, count := range fileCounts {
		b.Run(testName(count), func(b *testing.B) {
			// Setup
			ctrl := gomock.NewController(b)
			defer ctrl.Finish()
			
			mockStorage := NewBenchStorage(ctrl)
			
			// Prepare URLs for deletion
			urls := make([]*url.URL, count)
			for i := 0; i < count; i++ {
				urls[i] = &url.URL{
					Scheme: "s3",
					Bucket: "test-bucket",
					Path:   fmt.Sprintf("prefix/file-%d", i),
				}
			}
			
			// Setup mock behavior for MultiDelete
			mockStorage.SetupMultiDeleteOperation()
			
			// Setup dynamic URL channel for deletion
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
			
			// Assume 1KB per deletion metadata for throughput calculation
			metadataSize := int64(1024) // 1KB
			metadataThroughput := float64(totalDeleted*metadataSize) / elapsed.Seconds() / (1024 * 1024)
			
			// Report custom metrics
			b.ReportMetric(objectsPerSec, "deletes/s")
			b.ReportMetric(batchesPerSec, "batches/s")
			b.ReportMetric(metadataThroughput, "MB/s")
			b.ReportMetric(float64(count), "objects/op")
			
			// Report operation counts
			for op, opCount := range mockStorage.OpCount {
				b.ReportMetric(float64(opCount), op+"/op")
			}
		})
	}
}

func BenchmarkS3Scenario(b *testing.B) {
	// Skip if only GCS benchmarks are requested
	if *flags.BenchGCSOnly {
		b.Skip("Skipping S3 benchmarks because -bench.gcsonly is set")
	}
	
	// Test each predefined scenario
	for _, scenario := range scenarios.AllScenarios {
		// Skip GCS scenarios
		if scenario == scenarios.GCSOperations {
			continue
		}
		
		b.Run(scenario.Name, func(b *testing.B) {
			// Setup
			ctrl := gomock.NewController(b)
			defer ctrl.Finish()
			
			mockStorage := NewBenchStorage(ctrl)
			
			// Configure mock according to scenario
			mockStorage.Latency = scenario.StorageLatency
			mockStorage.Throughput = scenario.Throughput
			mockStorage.ErrorRate = scenario.ErrorRate
			
			// Generate test objects based on scenario
			var objects []*storage.Object
			if scenario == scenarios.MixedFiles {
				objects = scenarios.GenerateMixedObjects("mixed", scenario.ObjectCount)
			} else {
				// For SmallFiles scenario, limit the number of objects to process to avoid excessive runtime
				objCount := scenario.ObjectCount
				if scenario == scenarios.SmallFiles {
					objCount = 100 // Process only 100 objects instead of 10000
					b.Logf("Limiting SmallFiles scenario to %d objects", objCount)
				}
				objects = SimulateS3Objects(scenario.Name, objCount, scenario.ObjectSize)
			}
			
			// Setup mock behaviors
			mockStorage.CreateMockObjectLister(objects)
			mockStorage.SetupCopyOperation(objects, scenario.ObjectSize)
			
			// Run benchmark
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				runS3Scenario(b, mockStorage, scenario, objects)
			}
		})
	}
}

// Helper functions

func testName(count int) string {
	return fmt.Sprintf("Count_%d", count)
}

func runS3Scenario(b *testing.B, mockStorage *BenchStorage, scenario *scenarios.BenchmarkScenario, objects []*storage.Object) {
	ctx := context.Background()
	
	// Extract source and destination from commands
	if len(scenario.Commands) < 3 {
		b.Fatalf("Invalid command format in scenario: %v", scenario.Commands)
	}
	
	command := scenario.Commands[0]
	src := scenario.Commands[1]
	dst := scenario.Commands[2]
	
	// Parse URLs
	srcURL, err := url.New(src)
	if err != nil {
		b.Fatalf("Invalid source URL: %v", err)
	}
	
	dstURL, err := url.New(dst)
	if err != nil {
		b.Fatalf("Invalid destination URL: %v", err)
	}
	
	// Execute operation based on command
	switch command {
	case "cp":
		// Limit the number of objects to process
		objLimit := len(objects)
		if objLimit > 1000 {
			objLimit = 1000 // Safety cap
		}
		
		// List source objects first
		objChan := mockStorage.List(ctx, srcURL, false)
		
		// Count objects and collect errors
		var objCount int
		for obj := range objChan {
			// Process only up to the limit to avoid excessive runtime
			if objCount >= objLimit {
				break
			}
			
			if obj.Err != nil {
				if obj.Err == storage.ErrNoObjectFound {
					// This is expected sometimes, just continue
					continue
				}
				b.Fatalf("Error during listing: %v", obj.Err)
			}
			
			// For each object, execute copy
			srcObj := obj.URL.Clone()
			dstObj := dstURL.Clone()
			dstObj.Path = dstURL.Path + "/" + obj.URL.Base()
			
			err := mockStorage.Copy(ctx, srcObj, dstObj, storage.Metadata{})
			if err != nil {
				// For error scenarios, we expect some failures
				if scenario.ErrorRate > 0 {
					continue
				}
				b.Fatalf("Copy operation failed: %v", err)
			}
			
			objCount++
		}
		
		if objCount == 0 {
			b.Fatalf("No objects were copied, check the scenario configuration")
		}
	default:
		b.Fatalf("Unsupported command in scenario: %s", command)
	}
}