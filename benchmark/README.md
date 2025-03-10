
# s5cmd Performance Benchmarks

This directory contains two benchmarking systems for s5cmd:

1. **Python-based Integration Benchmarks** (`bench.py`): Tests real-world performance against actual S3/GCS buckets
2. **Go-based Synthetic Benchmarks** (`storage_bench/`, `command_bench/`): Synthetic benchmarks using mocked storage backends

## Go-based Synthetic Benchmarks

The Go benchmarks use the Go testing framework to enable quick performance testing without requiring real S3 or GCS buckets. These are ideal for identifying performance regressions during development.

### Running Go Benchmarks

#### Using the Command-Line Tool (Recommended)

For better output formatting and easy comparison, use the `benchrun` tool:

```bash
# Build the benchrun tool
go build -o benchrun ./benchmark/cmd/benchrun

# Run all benchmarks with default settings
./benchrun

# Run S3 benchmarks only with custom throughput
./benchrun -s3 -throughput 200

# Run GCS benchmarks with specific token fetch latency
./benchrun -gcs -token-latency 300ms

# Compare with previous benchmark run
./benchrun -compare
```

Output includes human-readable metrics:
- Time per operation (in s, ms, µs rather than raw nanoseconds)
- Throughput in appropriate units (GB/s, MB/s, KB/s)
- Objects processed per second
- API operation counts
- Token refresh metrics for GCS operations

#### Using Go Test Directly

To run all benchmarks:

```
go test -bench=. ./benchmark/...
```

To run specific benchmark types:

```
# Run only S3 storage benchmarks
go test -bench=. ./benchmark/storage_bench -run=^$ -bench=S3

# Run only GCS storage benchmarks
go test -bench=. ./benchmark/storage_bench -run=^$ -bench=GCS

# Run only command benchmarks
go test -bench=. ./benchmark/command_bench -run=^$
```

Customize behavior with flags:

```
# Test with higher simulated throughput
go test -bench=. ./benchmark/... -bench.throughput=200

# Test with GCS token latency of 100ms
go test -bench=. ./benchmark/... -bench.tokenlatency=100ms

# Test with 5% error rate
go test -bench=. ./benchmark/... -bench.errorrate=0.05

# Test with 20 workers
go test -bench=. ./benchmark/... -bench.workers=20

# Enable human-readable output
go test -bench=. ./benchmark/... -bench.human=true
```

### Go Benchmark Structure

- `storage_bench/`: Tests storage operations (List, Copy, Delete) for S3 and GCS
- `command_bench/`: Tests high-level commands like CP
- `scenarios/`: Defines benchmark scenarios (file sizes, counts, etc.)
- `cmd/benchrun/`: Command-line tool for running benchmarks with improved reporting

### Understanding Benchmark Metrics

The benchmark results now include several metrics to help identify performance issues:

#### Time Metrics
- **ns/op**: Base metric showing nanoseconds per operation (displayed in human-readable format)
- **s/op, ms/op, µs/op**: Human-readable time per operation in appropriate units

#### Throughput Metrics
- **MB/s**: Megabytes processed per second
- **GB/s**: For extremely high throughput operations
- **KB/s**: For slower operations

#### Operation Metrics
- **objects/s**: Number of objects processed per second
- **List/op, Copy/op, etc.**: Count of each operation type performed
- **deletes/s, batches/s**: Operation-specific metrics

#### GCS-specific Metrics
- **tokenRefreshes**: Number of token refresh operations performed
- **tokenOverhead%**: Percentage of total time spent on token refresh operations

When comparing benchmark runs with `-compare`, you'll see percentage changes with indicators:
- **🟢**: Performance improvement 
- **🔴**: Performance regression
- **→**: No significant change

## Python-based Integration Benchmarks

The Python benchmarks (`bench.py`) test real-world performance by working with actual S3/GCS buckets, comparing different builds of s5cmd.

### Required Tools
The Python benchmark requires the following tools:
- git
- go 
- hyperfine
- truncate

To run use the following syntax:
```
usage: bench.py [-h] [-s OLD NEW] [-w WARMUP] [-r RUNS] [-o OUTPUT_FILE_NAME] -b BUCKET [-l LOCAL_PATH] [-p PREFIX] [-hf HYPERFINE_EXTRA_FLAGS] [-sf S5CMD_EXTRA_FLAGS]

Compare performance of two different builds of s5cmd.

optional arguments:
  -h, --help            show this help message and exit
  -s OLD NEW, --s5cmd OLD NEW
                        Reference to old and new s5cmd.It can be a decimal indicating PR number,any of the version tags like v2.0.0 or any commit tag. Additionally it can be 'latest_release' or
                        'master'. (default: ('latest_release', 'master'))
  -w WARMUP, --warmup WARMUP
                        Number of program executions before the actual benchmark: (default: 2)
  -r RUNS, --runs RUNS  Number of runs to perform for each command (default: 10)
  -o OUTPUT_FILE_NAME, --output_file_name OUTPUT_FILE_NAME
                        Name of the output file (default: summary.md)
  -b BUCKET, --bucket BUCKET
                        Name of the bucket in remote (default: None)
  -l LOCAL_PATH, --local-path LOCAL_PATH
                        specify a local path for temporary files to be loaded. (default: None)
  -p PREFIX, --prefix PREFIX
                        Key prefix to be used while uploading to a specified bucket (default: s5cmd-benchmarks-)
  -hf HYPERFINE_EXTRA_FLAGS, --hyperfine-extra-flags HYPERFINE_EXTRA_FLAGS
                        hyperfine global extra flags. Write in between quotation marks and start with a space to avoid bugs. (default: None)
  -sf S5CMD_EXTRA_FLAGS, --s5cmd-extra-flags S5CMD_EXTRA_FLAGS
                        s5cmd global extra flags. Write in between quotation marks and start with a space to avoid bugs. (default: None)
```

### Examples
```
./bench.py --bucket tempbucket  
```
Above command will compare `latest-release` to `master` with 2 warmup runs and 10 benchmark runs.  

```
./bench.py --bucket tempbucket --s5cmd v2.0.0 456 --warmup 2 --runs 10 
```
Above command will compare v2.0.0 to PR:456 with 2 warmup runs and 10 benchmark runs. 

```
./bench.py --bucket tempbucket --s5cmd v2.0.0 456 --warmup 2 --runs 10 -sf " --log error" -hf " --show-output"
```
When using `-hf` and `-sf` flags, use quotes like above and start with an empty space. If not started with an empty space, it might give an error. This is a known issue with `argparse` and [this](https://stackoverflow.com/questions/72129874/processing-arguments-for-subprocesses-using-argparse-expected-one-argument) discussion can be useful to understand the problem deeper.

### Example Output
```
./bench.py --bucket tempbucket --s5cmd master 478 --warmup 2 --runs 15
```

### Benchmark summary: 
|Scenarios | File Size | File Count |
|:---|:---|:---|
| small files | 1M | 10000 |
| large file | 10G | 1 |
| very large file | 300G | 1 |

|Scenario| Summary |
|:---|:---|
| upload small files | 'PR:478' ran 1.01 ± 0.02 times faster than 'master' |
| download small files | 'PR:478' ran 1.00 ± 0.01 times faster than 'master' |
| remove small files | 'master' ran 1.05 ± 0.41 times faster than 'PR:478' |
| upload large file | 'PR:478' ran 1.18 ± 0.23 times faster than 'master' |
| download large file | 'master' ran 1.05 ± 0.08 times faster than 'PR:478' |
| remove large file | 'master' ran 1.21 ± 0.39 times faster than 'PR:478' |
| upload very large file | 'PR:478' ran 1.01 times faster than 'master' |
| download very large file | 'master' ran 1.02 times faster than 'PR:478' |
| remove very large file | 'PR:478' ran 1.13 times faster than 'master' |

 ### Detailed summary: 
 |Scenario| Command | Mean [s] | Min [s] | Max [s] | Relative |
 |:---|:---|---:|---:|---:|---:|
| upload small files | `PR:478` | 9.117 ± 0.155 | 8.848 | 9.337 | 1.00 |
| upload small files | `master` | 9.252 ± 0.160 | 9.084 | 9.483 | 1.01 ± 0.02 |
| download small files | `PR:478` | 79.992 ± 0.091 | 79.879 | 80.177 | 1.00 |
| download small files | `master` | 79.993 ± 0.462 | 79.096 | 81.028 | 1.00 ± 0.01 |
 | remove small files | `PR:478` | 2.603 ± 0.435 | 2.308 | 3.245 | 1.05 ± 0.41 |
 | remove small files | `master` | 2.470 ± 0.878 | 2.012 | 3.787 | 1.00 |
 | upload large file | `PR:478` | 10.093 ± 1.491 | 9.043 | 14.222 | 1.00 |
 | upload large file | `master` | 11.876 ± 1.486 | 10.730 | 15.787 | 1.18 ± 0.23 |
 | download large file | `PR:478` | 27.689 ± 1.378 | 25.979 | 30.803 | 1.05 ± 0.08 |
 | download large file | `master` | 26.452 ± 1.667 | 24.891 | 29.375 | 1.00 |
 | remove large file | `PR:478` | 0.157 ± 0.029 | 0.122 | 0.210 | 1.21 ± 0.39 |
 | remove large file | `master` | 0.130 ± 0.034 | 0.090 | 0.220 | 1.00 |
 | upload very large file | `PR:478` | 270.462 | 270.462 | 270.462 | 1.00 |
 | upload very large file | `master` | 272.473 | 272.473 | 272.473 | 1.01 |
 | download very large file | `PR:478` | 2538.727 | 2538.727 | 2538.727 | 1.02 |
 | download very large file | `master` | 2501.010 | 2501.010 | 2501.010 | 1.00 |
 | remove very large file | `PR:478` | 1.011 | 1.011 | 1.011 | 1.00 |
 | remove very large file | `master` | 1.145 | 1.145 | 1.145 | 1.13 |
