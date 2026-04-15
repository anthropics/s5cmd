package e2e

import (
	"fmt"
	"testing"

	"gotest.tools/v3/icmd"
)

func TestStatS3Object(t *testing.T) {
	t.Parallel()

	const (
		filename = "file.txt"
		content  = "hello, stat"
	)

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)
	putFile(t, s3client, bucket, filename, content)

	src := fmt.Sprintf("s3://%v/%v", bucket, filename)

	cmd := s5cmd("stat", src)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)
	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: contains("%d s3://%s/%s", len(content), bucket, filename),
	})
}

func TestStatS3ObjectJSON(t *testing.T) {
	t.Parallel()

	const (
		filename = "file.txt"
		content  = "hello, stat json"
	)

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)
	putFile(t, s3client, bucket, filename, content)

	src := fmt.Sprintf("s3://%v/%v", bucket, filename)

	cmd := s5cmd("--json", "stat", src)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)
	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: match(fmt.Sprintf(`{"key":"s3:\/\/%s\/%s",.*"size":%d.*}`, bucket, filename, len(content))),
	}, jsonCheck(true))
}

func TestStatS3ObjectNotFound(t *testing.T) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	src := fmt.Sprintf("s3://%v/missing.txt", bucket)

	cmd := s5cmd("stat", src)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Expected{ExitCode: 1})
	assertLines(t, result.Stderr(), map[int]compareFunc{
		0: match(fmt.Sprintf(`ERROR "stat s3://%s/missing\.txt":.*not found`, bucket)),
	})
}

func TestStatS3MultipleObjects(t *testing.T) {
	t.Parallel()

	const (
		file1    = "first.txt"
		file2    = "second.txt"
		content1 = "first content"
		content2 = "second content, longer"
	)

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)
	putFile(t, s3client, bucket, file1, content1)
	putFile(t, s3client, bucket, file2, content2)

	src1 := fmt.Sprintf("s3://%v/%v", bucket, file1)
	src2 := fmt.Sprintf("s3://%v/%v", bucket, file2)

	cmd := s5cmd("stat", src1, src2)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)
	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: contains("%d s3://%s/%s", len(content1), bucket, file1),
		1: contains("%d s3://%s/%s", len(content2), bucket, file2),
	})
}

func TestStatValidation(t *testing.T) {
	t.Parallel()

	testcases := []struct {
		src      string
		name     string
		expected map[int]compareFunc
	}{
		{
			src:  "s3://%v/prefix/*",
			name: "wildcard rejected",
			expected: map[int]compareFunc{
				0: match(`ERROR "stat s3://(.*)/prefix/\*": remote source "s3://(.*)/prefix/\*" can not contain glob characters`),
			},
		},
		{
			src:  "s3://%v/prefix/",
			name: "prefix rejected",
			expected: map[int]compareFunc{
				0: match(`ERROR "stat s3://(.+)?/prefix/": remote source must be an object`),
			},
		},
		{
			src:  "%v/local.txt",
			name: "local file rejected",
			expected: map[int]compareFunc{
				0: match(`ERROR "stat (.*)/local\.txt": source must be a remote object`),
			},
		},
	}

	for _, tc := range testcases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s3client, s5cmd := setup(t)

			bucket := s3BucketFromTestName(t)
			createBucket(t, s3client, bucket)

			cmd := s5cmd("stat", fmt.Sprintf(tc.src, bucket))
			result := icmd.RunCmd(cmd)

			result.Assert(t, icmd.Expected{ExitCode: 1})
			assertLines(t, result.Stderr(), tc.expected)
		})
	}
}
