package storage_bench

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"sync"
	"time"

	"github.com/peak/s5cmd/v2/storage"
	"github.com/peak/s5cmd/v2/storage/url"
	"go.uber.org/mock/gomock"
	"golang.org/x/oauth2"
)

// BenchStorage extends MockStorage to add timing and throttling
type BenchStorage struct {
	*storage.MockStorage
	
	// Configuration
	Latency     map[string]time.Duration // Operation latencies
	Throughput  float64                  // MB/s for data operations
	ErrorRate   float64                  // 0.0-1.0 probability of error
	
	// Metrics
	OpCount     map[string]int64
	BytesRead   int64
	BytesWritten int64
	mutex       sync.Mutex
}

// NewBenchStorage creates a new benchmark storage with realistic behavior
func NewBenchStorage(ctrl *gomock.Controller) *BenchStorage {
	mock := storage.NewMockStorage(ctrl)
	return &BenchStorage{
		MockStorage: mock,
		Latency: map[string]time.Duration{
			"List":  20 * time.Millisecond,
			"Stat":  10 * time.Millisecond,
			"Copy":  50 * time.Millisecond,
			"Delete": 30 * time.Millisecond,
		},
		Throughput: 100.0, // 100 MB/s
		OpCount:    make(map[string]int64),
	}
}

// recordOp tracks operation metrics
func (s *BenchStorage) recordOp(name string) func() {
	s.mutex.Lock()
	s.OpCount[name]++
	s.mutex.Unlock()
	
	start := time.Now()
	return func() {
		// Simulate operation latency
		latency := s.Latency[name]
		if latency > 0 {
			sleepTime := latency - time.Since(start)
			if sleepTime > 0 {
				time.Sleep(sleepTime)
			}
		}
	}
}

// SimulateS3Objects generates a list of S3 objects for testing
func SimulateS3Objects(prefix string, count int, sizeBytes int64) []*storage.Object {
	objects := make([]*storage.Object, count)
	now := time.Now()
	
	for i := 0; i < count; i++ {
		objURL := &url.URL{
			Scheme: "s3",
			Bucket: "test-bucket",
			Path:   fmt.Sprintf("%s/file-%d", prefix, i),
		}
		
		objects[i] = &storage.Object{
			URL:     objURL,
			Size:    sizeBytes,
			ModTime: &now,
			Type:    storage.ObjectType{},
		}
	}
	
	return objects
}

// CreateMockObjectLister sets up expectations for List operation
func (s *BenchStorage) CreateMockObjectLister(objects []*storage.Object) {
	s.MockStorage.EXPECT().
		List(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, u *url.URL, followSymlinks bool) <-chan *storage.Object {
			defer s.recordOp("List")()
			
			// Create results channel
			ch := make(chan *storage.Object, len(objects))
			
			go func() {
				defer close(ch)
				
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
}

// MockReader provides a reader that simulates network throughput
type MockReader struct {
	data       []byte
	pos        int64
	throughput float64 // MB/s
	lastRead   time.Time
}

func NewMockReader(size int64, throughput float64) *MockReader {
	// Create a simple repeating pattern rather than allocating huge buffer
	pattern := []byte("ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
	return &MockReader{
		data:       pattern,
		throughput: throughput,
		lastRead:   time.Now(),
	}
}

func (r *MockReader) Read(p []byte) (n int, err error) {
	if r.pos >= int64(len(r.data)) {
		return 0, io.EOF
	}
	
	// Calculate how much we should read based on throughput
	elapsed := time.Since(r.lastRead).Seconds()
	maxBytes := int64(r.throughput * 1024 * 1024 * elapsed)
	
	// Cap at buffer size
	if maxBytes > int64(len(p)) {
		maxBytes = int64(len(p))
	}
	
	// Fill with pattern data
	var bytesRead int
	for i := 0; i < int(maxBytes); i++ {
		p[i] = r.data[int(r.pos)%len(r.data)]
		r.pos++
		bytesRead++
	}
	
	// If we actually wrote data, update the timestamp
	if bytesRead > 0 {
		r.lastRead = time.Now()
	} else {
		// We need to throttle, sleep a bit
		time.Sleep(10 * time.Millisecond)
	}
	
	return bytesRead, nil
}

// MockTokenSource implements a fake token source for GCS testing
type MockTokenSource struct {
	RefreshLatency time.Duration
	FetchCount     int
	mu             sync.Mutex
}

func NewMockTokenSource() *MockTokenSource {
	return &MockTokenSource{
		RefreshLatency: 200 * time.Millisecond,
	}
}

func (m *MockTokenSource) Token() (*oauth2.Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	m.FetchCount++
	
	// Simulate token fetch latency
	time.Sleep(m.RefreshLatency)
	
	// Return a mock token that expires in 1 hour
	return &oauth2.Token{
		AccessToken: "mock-token",
		TokenType:   "Bearer",
		Expiry:      time.Now().Add(1 * time.Hour),
	}, nil
}

// For compatibility with benchmark code
func (m *MockTokenSource) GetToken() (*oauth2.Token, error) {
	return m.Token()
}

func (m *MockTokenSource) Stop() {
	// No-op for mock
}

// SetupCopyOperation configures the mock storage for the Copy operation
func (s *BenchStorage) SetupCopyOperation(objects []*storage.Object, objectSize int64) {
	s.MockStorage.EXPECT().
		Copy(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, src, dst *url.URL, metadata storage.Metadata) error {
			defer s.recordOp("Copy")()
			
			// Simulate error based on error rate
			if s.ErrorRate > 0 && rand.Float64() < s.ErrorRate {
				return fmt.Errorf("simulated error")
			}
			
			// For regular operations, simulate throughput-based delay
			size := objectSize
			for _, obj := range objects {
				if obj.URL.Path == src.Path {
					size = obj.Size
					break
				}
			}
			
			// Prevent division by zero
			if s.Throughput <= 0 {
				s.Throughput = 1.0
			}
			
			// Calculate transfer time
			bytesPerMs := (s.Throughput * 1024 * 1024) / 1000
			sleepTime := time.Duration(float64(size) / bytesPerMs) * time.Millisecond
			
			// Apply minimum latency if transfer would be too fast
			minLatency := s.Latency["Copy"]
			if sleepTime < minLatency {
				sleepTime = minLatency
			}
			
			// Track bytes transferred
			s.mutex.Lock()
			s.BytesWritten += size
			s.mutex.Unlock()
			
			time.Sleep(sleepTime)
			return nil
		}).AnyTimes()
}

// SetupMultiDeleteOperation configures the mock storage for the MultiDelete operation
func (s *BenchStorage) SetupMultiDeleteOperation() {
	s.MockStorage.EXPECT().
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
				defer s.recordOp("MultiDelete")()
				
				// Simulate batch processing with appropriate delays
				batches := (count + 999) / 1000 // S3 allows up to 1000 keys per request
				for i := 0; i < batches; i++ {
					start := i * 1000
					end := (i + 1) * 1000
					if end > count {
						end = count
					}
					
					// Increment batch delete operation counter
					s.mutex.Lock()
					s.OpCount["DeleteBatch"]++
					s.mutex.Unlock()
					
					// Simulate batch delete latency
					time.Sleep(s.Latency["Delete"])
					
					// Return results
					for j := start; j < end; j++ {
						resultCh <- &storage.Object{URL: urls[j]}
					}
				}
			}()
			
			return resultCh
		}).AnyTimes()
}

// SimulateGSObjects generates a list of GCS objects for testing
func SimulateGSObjects(prefix string, count int, sizeBytes int64) []*storage.Object {
	objects := make([]*storage.Object, count)
	now := time.Now()
	
	for i := 0; i < count; i++ {
		objURL := &url.URL{
			Scheme: "gs",
			Bucket: "test-bucket",
			Path:   fmt.Sprintf("%s/file-%d", prefix, i),
		}
		
		objects[i] = &storage.Object{
			URL:     objURL,
			Size:    sizeBytes,
			ModTime: &now,
			Type:    storage.ObjectType{},
		}
	}
	
	return objects
}