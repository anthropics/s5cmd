package flags

import "flag"

// Shared benchmark flags to avoid redeclaring in multiple test files
var (
	BenchS3Only = flag.Bool("bench.s3only", false, "Run only S3 benchmarks")
	BenchGCSOnly = flag.Bool("bench.gcsonly", false, "Run only GCS benchmarks")
)