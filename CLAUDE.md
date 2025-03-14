# Mock S3/GCS Server for s5cmd Testing

This document provides guidance for working on the Mock S3/GCS Server project, which is designed to test s5cmd functionality, particularly edge cases involving timeouts, failures, and other hard-to-test scenarios.

## Project Purpose

The Mock S3/GCS Server allows testing s5cmd against controlled environments where we can simulate:
- Network latency and bandwidth constraints
- Operation failures and timeouts
- Authentication issues
- Progressive large file transfers
- Partial failures and connection drops

This is particularly important for testing timeout-related issues like the bug where a 30-second timeout in the Google Authentication Client caused large file transfers to fail.

## Development Guidelines

### Project Structure

The project follows a modular architecture:
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

### Development Priorities

1. **Focus on API Compatibility**: Implement the exact AWS S3 and GCS APIs that s5cmd uses
2. **Prioritize Behavior Simulation**: Ensure the server can accurately simulate timeouts, bandwidth limits, and errors
3. **Build Incrementally**: Start with core operations (buckets, objects) before advanced features (multipart, versioning)
4. **Maintain Test Coverage**: Every component should have thorough unit tests

### AWS API Reference

For S3 API implementation, refer to these official AWS documents:
- [Amazon S3 REST API Introduction](https://docs.aws.amazon.com/AmazonS3/latest/API/Welcome.html)
- [S3 API Reference](https://docs.aws.amazon.com/AmazonS3/latest/API/API_Operations_Amazon_Simple_Storage_Service.html)

### Google Cloud Storage Reference

For GCS API implementation, refer to:
- [Google Cloud Storage JSON API Reference](https://cloud.google.com/storage/docs/json_api/v1)
- [XML API Reference](https://cloud.google.com/storage/docs/xml-api/overview)

## Testing Guidelines

### How to Run Integration Tests

Integration tests are designed to run separately from the standard test suite:

1. **Using Build Tags**:
   ```bash
   go test -tags=integration ./e2e/... -v
   ```

2. **Using Environment Variables**:
   ```bash
   S5CMD_RUN_INTEGRATION=true go test ./e2e/... -v
   ```

3. **Using Make Target** (once implemented):
   ```bash
   make test-integration
   ```

### Creating Test Scenarios

When creating test scenarios, focus on:

1. **Edge Cases**: Test timeouts, slow transfers, connection drops
2. **Real-World Issues**: Recreate reported bugs like the Google Authentication Client timeout
3. **Varied File Sizes**: Test with small files, large files (100MB+), and very large files (1GB+)
4. **Authentication**: Test with various authentication scenarios including token failures

Example test structure:
```go
func TestLargeFileTimeout(t *testing.T) {
    // 1. Set up mock server with specific behaviors
    // 2. Configure test scenario (bucket, objects, latency)
    // 3. Run s5cmd commands
    // 4. Verify expected behavior
}
```

## Common Commands

### Building and Running the Mock Server

```bash
# Build the server
cd cmd/mockserver && go build

# Run the server
./mockserver -port 9000 -debug

# Run with specific configuration
./mockserver -config config.json
```

### Testing Against the Mock Server

```bash
# Point s5cmd at the mock server
S3_ENDPOINT=http://localhost:9000 \
AWS_ACCESS_KEY_ID=test \
AWS_SECRET_ACCESS_KEY=test \
s5cmd cp testfile.dat s3://testbucket/testfile.dat

# Test GCS authentication
s5cmd --gcs-auth cp testfile.dat s3://testbucket/testfile.dat
```

## Implementation Tips

1. **Behavior Simulation**:
   - Use `time.Sleep()` to simulate latency
   - Implement controlled bandwidth using chunked responses with timed sleeps between chunks
   - Add jitter to make tests more realistic

2. **Virtual Time**:
   - Consider using a virtual clock for faster tests
   - Allow time acceleration for long operations

3. **Progressive Transfers**:
   - Large file transfers should stream data in chunks
   - Implement `http.Flusher` to ensure data is sent progressively

4. **Authentication**:
   - Mock token refresh with configurable delay
   - Simulate token expiration scenarios

5. **Error Injection**:
   - Allow errors to be injected at specific points
   - Support different error types (network, permission, timeout)

## Resources and References

- [AWS SDK for Go](https://github.com/aws/aws-sdk-go)
- [s5cmd storage implementation](https://github.com/peak/s5cmd/tree/master/storage)
- [S5cmd token manager](https://github.com/peak/s5cmd/blob/master/storage/token_manager.go)
- [MinIO S3 Server](https://github.com/minio/minio) (for reference)
- [S3Proxy](https://github.com/gaul/s3proxy) (for reference)

## Next Steps

1. Start by implementing the core infrastructure from Phase 1 in the [todo.md](todo.md) file
2. Develop the basic S3 operations that s5cmd uses most frequently
3. Focus on the behavior simulation aspects that will help test timeout scenarios
4. Integrate with the s5cmd test suite