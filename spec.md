# Technical Specification: Mock S3/GCS Server for s5cmd Testing

## Overview

This document outlines the technical specifications for a mock S3/GCS server to test s5cmd functionality, including edge cases like timeouts and failures. The server will simulate both S3 and GCS APIs, plus Google STS token authentication.

## 1. Core Requirements

- **API Compatibility**: Mock all S3/GCS APIs used by s5cmd
- **Authentication**: Support both AWS and Google authentication methods
- **Behavior Simulation**: Enable latency, errors, and bandwidth throttling
- **Testing Control**: Programmatically configure server behavior for tests
- **Isolation**: Run independently without affecting standard test suite

## 2. APIs to Mock

### S3/GCS API Operations

#### Bucket Operations
- `ListBuckets`
- `CreateBucket`
- `DeleteBucket` 
- `GetBucketVersioning`
- `PutBucketVersioning`

#### Object Operations
- `HeadObject` (metadata retrieval)
- `GetObject` (downloads)
- `PutObject` (uploads)
- `CopyObject`
- `DeleteObject`
- `DeleteObjects` (multi-delete)
- `SelectObjectContent` (queries)

#### List Operations
- `ListObjectsV2` (default listing)
- `ListObjectsV1` (for GCS compatibility)
- `ListObjectVersions` (versioned buckets)

#### Multipart Upload Operations
- `CreateMultipartUpload`
- `UploadPart`
- `CompleteMultipartUpload`
- `AbortMultipartUpload`

### Authentication APIs
- **AWS Signature V4**: `Authorization` header validation
- **Google OAuth**: Token generation, validation and refresh
- **STS Tokens**: Simulation of token acquisition and refresh

## 3. Architecture

```
mock-s3-gcs/
├── cmd/
│   └── mockserver/           # Executable entrypoint
├── internal/
│   ├── auth/                 # Authentication handling
│   ├── server/               # HTTP server implementation
│   ├── storage/              # In-memory object storage
│   ├── handlers/             # API endpoint handlers
│   ├── controller/           # Test control interface
│   └── behavior/             # Behavior simulation 
├── pkg/
│   ├── types/                # Shared type definitions
│   └── api/                  # Public API for test control
└── test/                     # Test integration examples
```

## 4. Implementation Details

### Server Core

```go
// Top-level server configuration
type MockServerConfig struct {
    Port                int
    EnableS3            bool
    EnableGCS           bool
    DefaultLatency      time.Duration
    DefaultErrorRate    float64
    RecordMode          bool          // Record all interactions for replay
    StorageDir          string        // Optional persistence location
    Debug               bool
}

// Core server implementation
type MockServer struct {
    config      MockServerConfig
    storage     *StorageEngine
    behaviors   *BehaviorController
    stats       *ServerStats
    server      *http.Server
    mu          sync.RWMutex
}

// Start the server with optional configuration
func NewMockServer(config MockServerConfig) (*MockServer, error) {
    // Initialize components
    // Start HTTP server
    // Return control interface
}
```

### Behavior Simulation

```go
// Operation behavior configuration
type OperationBehavior struct {
    Latency          time.Duration    // Artificial delay
    ErrorProbability float64          // Probability of error response (0-1)
    ErrorType        string           // Type of error to simulate
    MaxBandwidth     int64            // Bytes per second
    ChunkSize        int64            // For chunked responses
    FailAfter        int64            // Fail after X bytes (simulate partial failure)
    ConnectionDrop   bool             // Simulate connection drop
}

// Behavior controller for programmatic test control
type BehaviorController struct {
    // Maps operation name to behavior
    operations map[string]*OperationBehavior
    
    // Maps resource pattern to behavior
    resources  map[string]*OperationBehavior
    
    mu         sync.RWMutex
}

// Configure behavior for a specific S3/GCS operation
func (c *BehaviorController) ConfigureOperation(op string, behavior OperationBehavior) {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.operations[op] = &behavior
}

// Configure behavior for specific resources (buckets/objects)
func (c *BehaviorController) ConfigureResource(pattern string, behavior OperationBehavior) {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.resources[pattern] = &behavior
}
```

### Authentication Simulation

```go
// Google auth token manager simulation
type GoogleAuthSimulator struct {
    tokens        map[string]*oauth2.Token
    refreshDelay  time.Duration
    errorRate     float64
    mu            sync.RWMutex
}

// Simulate token refresh with configurable delays/failures
func (g *GoogleAuthSimulator) SimulateTokenRefresh(ctx context.Context) (*oauth2.Token, error) {
    g.mu.Lock()
    defer g.mu.Unlock()
    
    // Check if should simulate error
    if rand.Float64() < g.errorRate {
        return nil, errors.New("simulated token refresh failure")
    }
    
    // Simulate refresh delay
    if g.refreshDelay > 0 {
        time.Sleep(g.refreshDelay)
    }
    
    token := &oauth2.Token{
        AccessToken: fmt.Sprintf("mock-token-%d", time.Now().Unix()),
        TokenType:   "Bearer",
        Expiry:      time.Now().Add(1 * time.Hour),
    }
    
    return token, nil
}
```

### Object Storage Engine

```go
// Core storage engine
type StorageEngine struct {
    buckets       map[string]*Bucket
    mu            sync.RWMutex
}

// Bucket representation
type Bucket struct {
    name          string
    creationDate  time.Time
    versioning    bool
    objects       map[string][]*Object  // Key -> versions (newest first)
    mu            sync.RWMutex
}

// Object representation
type Object struct {
    key           string
    data          []byte
    etag          string
    contentType   string
    metadata      map[string]string
    lastModified  time.Time
    versionID     string
    size          int64
    storageClass  string
    isDeleteMarker bool
}
```

## 5. Mock Time and Progressive Transfers

### Time Simulation

```go
// Virtual clock for time simulation
type VirtualClock struct {
    currentTime   time.Time
    speed         float64      // Time acceleration factor
    mu            sync.Mutex
}

// Get current virtual time
func (c *VirtualClock) Now() time.Time {
    c.mu.Lock()
    defer c.mu.Unlock()
    
    realElapsed := time.Since(c.lastRealTime)
    virtualElapsed := time.Duration(float64(realElapsed) * c.speed)
    c.currentTime = c.currentTime.Add(virtualElapsed)
    c.lastRealTime = time.Now()
    
    return c.currentTime
}

// Sleep in virtual time
func (c *VirtualClock) Sleep(d time.Duration) {
    if c.speed <= 0 {
        // Instant sleep
        c.mu.Lock()
        c.currentTime = c.currentTime.Add(d)
        c.mu.Unlock()
        return
    }
    
    // Real sleep adjusted by speed
    time.Sleep(time.Duration(float64(d) / c.speed))
}
```

### Progressive Transfer Simulation

```go
// Simulate slow transfer for large files
func simulateProgressiveTransfer(w http.ResponseWriter, data []byte, behavior *OperationBehavior) {
    totalSize := int64(len(data))
    
    // Set headers
    w.Header().Set("Content-Length", strconv.FormatInt(totalSize, 10))
    w.WriteHeader(http.StatusOK)
    
    // Calculate chunk parameters
    chunkSize := behavior.ChunkSize
    if chunkSize <= 0 {
        chunkSize = 1 * 1024 * 1024 // Default 1MB
    }
    
    bytesPerSecond := behavior.MaxBandwidth
    if bytesPerSecond <= 0 {
        bytesPerSecond = 10 * 1024 * 1024 // Default 10MB/s
    }
    
    // Calculate sleep time between chunks
    sleepPerChunk := time.Duration(float64(chunkSize) / float64(bytesPerSecond) * float64(time.Second))
    
    // Send data in chunks with controlled speed
    for offset := int64(0); offset < totalSize; offset += chunkSize {
        end := offset + chunkSize
        if end > totalSize {
            end = totalSize
        }
        
        // Check if we should simulate failure
        if behavior.FailAfter > 0 && offset >= behavior.FailAfter {
            // Close connection to simulate network failure
            if conn, ok := w.(http.Hijacker); ok {
                conn, _, _ := conn.Hijack()
                conn.Close()
            }
            return
        }
        
        // Write chunk
        if _, err := w.Write(data[offset:end]); err != nil {
            return
        }
        
        // Flush to ensure progressive delivery
        if f, ok := w.(http.Flusher); ok {
            f.Flush()
        }
        
        // Sleep to simulate bandwidth limit
        time.Sleep(sleepPerChunk)
    }
}
```

## 6. Implementation Plan

### Phase 1: Core Infrastructure (5 days)

**Task 1: Basic Server Setup (2 days)**
- Implement HTTP server with routing
- Create configuration system
- Build logging infrastructure

**Task 2: Storage Engine (3 days)**
- Implement in-memory storage for buckets and objects
- Create versioning support
- Implement data persistence (optional)

### Phase 2: S3 Basic Operations (7 days)

**Task 3: Bucket Operations (2 days)**
- Implement ListBuckets, CreateBucket, DeleteBucket
- Add bucket versioning support

**Task 4: Object Operations (3 days)**
- Implement HeadObject, GetObject, PutObject
- Add DeleteObject and CopyObject

**Task 5: List Operations (2 days)**
- Implement ListObjectsV2 with pagination
- Add ListObjectsV1 for GCS compatibility
- Implement version listing

### Phase 3: Advanced Operations (8 days)

**Task 6: Multipart Uploads (3 days)**
- Implement complete multipart upload flow
- Add support for abort and resume

**Task 7: Authentication (3 days)**
- Implement AWS Signature V4 validation
- Create Google OAuth token simulation
- Add token refresh mechanics

**Task 8: Behavior Simulation (2 days)**
- Implement latency and bandwidth controls
- Add error injection system
- Create time simulation

### Phase 4: Testing Integration (5 days)

**Task 9: Test Control API (2 days)**
- Create programmatic control interface
- Implement scenario management
- Add state inspection API

**Task 10: s5cmd Test Integration (3 days)**
- Create specific test scenarios for s5cmd
- Implement test harness
- Add test result validation

## 7. Test Execution Strategy

### Test Integration

The mock server will be integrated into the testing workflow via:

```go
// Example test using mock server
//go:build integration
// +build integration

package e2e_test

import (
    "testing"
    "github.com/your-org/s5cmd-mock/pkg/api"
    "github.com/stretchr/testify/assert"
)

func TestLargeFileTimeout(t *testing.T) {
    // Start mock server
    server := api.NewMockServer(api.Config{
        Port: 9000,
        EnableS3: true,
        EnableGCS: true,
    })
    defer server.Stop()
    
    // Configure test scenario
    server.CreateBucket("testbucket")
    server.PutLargeObject("testbucket", "largefile.dat", 1024*1024*100) // 100MB
    
    // Configure slow download
    server.ConfigureOperation("GetObject", api.OperationBehavior{
        Latency: 0,
        MaxBandwidth: 1024*1024, // 1MB/s
    })
    
    // Run s5cmd
    cmd := exec.Command("s5cmd", 
        "cp", 
        "s3://testbucket/largefile.dat", 
        "local-file.dat")
    cmd.Env = append(os.Environ(), 
        "S3_ENDPOINT=http://localhost:9000",
        "AWS_ACCESS_KEY_ID=test", 
        "AWS_SECRET_ACCESS_KEY=test")
    
    output, err := cmd.CombinedOutput()
    assert.NoError(t, err)
    assert.Contains(t, string(output), "successfully copied")
}
```

### Run Configuration

To avoid running these tests during normal development:

1. **Dedicated Make Target**:
   ```
   .PHONY: test-integration
   test-integration:
       go test -tags=integration ./e2e/... -v
   ```

2. **Environment Flag**:
   ```
   if os.Getenv("S5CMD_RUN_INTEGRATION") != "true" {
       t.Skip("Skipping integration test. Set S5CMD_RUN_INTEGRATION=true to run")
   }
   ```

3. **CI Configuration**:
   - Run integration tests in dedicated CI job
   - Schedule longer tests for nightly builds

## 8. Operational Details

### Server Startup

```go
// Example mock server startup
package main

import (
    "github.com/your-org/s5cmd-mock/pkg/api"
)

func main() {
    server := api.NewMockServer(api.Config{
        Port: 9000,
        EnableS3: true,
        EnableGCS: true,
        Debug: true,
    })
    
    // Setup test data
    server.CreateBucket("testbucket")
    
    // Configure global behavior
    server.GlobalConfig(api.Behavior{
        DefaultLatency: 10 * time.Millisecond,
    })
    
    // Start server (blocks until Ctrl+C)
    server.Start()
}
```

### API Control Interface

The server will expose a REST API for test control:

```
GET /mock/status               # Server status
POST /mock/bucket              # Create bucket
GET /mock/bucket/{bucket}      # Get bucket info
POST /mock/object              # Create/update object
POST /mock/behavior            # Update behavior
POST /mock/reset               # Reset state
GET /mock/stats                # Get operation stats
```

## 9. Final Notes

This mock server will provide comprehensive testing capabilities for s5cmd, especially for edge cases that are difficult to test against real S3/GCS endpoints. It focuses on simulating the specific APIs and behaviors that s5cmd relies on, with special attention to timing and failure scenarios.

The server should be treated as a dedicated testing tool rather than a general-purpose S3/GCS implementation, with its behavior optimized for the specific testing needs of s5cmd.