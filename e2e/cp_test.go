// Package e2e includes end-to-end testing of s5cmd commands.
//
// All test cases include a comment for which test cases are covered in the
// following format:
//
// dir/: directory
// file: local file
// bucket: s3 bucket
// prefix/: s3 prefix
// prefix-without-slash: s3 prefix without a trailing slash
// object: s3 object name
// *: match all objects
// *.ext: match partial objects
//
// dir2: another directory
// file2: another local file
// object2: another s3 object name
// prefix2/: another s3 prefix
// bucket2: another s3 bucket
//
// For example, finding the test case that covers uploading all files in a
// directory to an s3 prefix: "cp dir/* s3://bucket/prefix/".
package e2e

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/fs"
	"gotest.tools/v3/icmd"
)

type testCase struct {
	name    string
	storage string
}

var testCases = []testCase{
	{name: "AWS", storage: "s3"},
	{name: "GCP", storage: "gs"},
}

func TestCopySingleS3ObjectToLocal(t *testing.T) {
	t.Parallel()
	t.Run("SingleS3ObjectToLocal", func(t *testing.T) {
		for _, tc := range testCases {
			tc := tc
			t.Run(tc.storage, func(t *testing.T) {
				runTestCopySingleS3ObjectToLocal(t, &tc)
			})
		}
	})
}

func runTestCopySingleS3ObjectToLocal(t *testing.T, tc *testCase) {
	t.Parallel()

	const (
		fileContent = "this is a file content"
	)

	testcases := []struct {
		name        string
		src         string
		dst         string
		expected    fs.PathOp
		expectedDst string
	}{
		{
			name:        "cp " + tc.storage + "://bucket/object .",
			src:         "file1.txt",
			dst:         ".",
			expected:    fs.WithFile("file1.txt", fileContent, fs.WithMode(0644)),
			expectedDst: "file1.txt",
		},
		{
			name:        "cp " + tc.storage + "://bucket/object file",
			src:         "file1.txt",
			dst:         "file1.txt",
			expected:    fs.WithFile("file1.txt", fileContent, fs.WithMode(0644)),
			expectedDst: "file1.txt",
		},
		{
			name:        "cp " + tc.storage + "://bucket/object dir/",
			src:         "file1.txt",
			dst:         "dir/",
			expected:    fs.WithDir("dir", fs.WithFile("file1.txt", fileContent, fs.WithMode(0644))),
			expectedDst: "dir/file1.txt",
		},
		{
			name:        "cp " + tc.storage + "://bucket/object dir/file",
			src:         "file1.txt",
			dst:         "dir/file1.txt",
			expected:    fs.WithDir("dir", fs.WithFile("file1.txt", fileContent, fs.WithMode(0644))),
			expectedDst: "dir/file1.txt",
		},
		// Cases with adjacent slashes. Expected behavior is to remove all duplicate slashes in local files.
		{
			name:        "cp " + tc.storage + "://bucket//a/b///c////object .",
			src:         "/a/b///c////file1.txt",
			dst:         ".",
			expected:    fs.WithFile("file1.txt", fileContent, fs.WithMode(0644)),
			expectedDst: "file1.txt",
		},
		{
			name:        "cp " + tc.storage + "://bucket//a/b///c////object file",
			src:         "/a/b///c////file1.txt",
			dst:         "file1.txt",
			expected:    fs.WithFile("file1.txt", fileContent, fs.WithMode(0644)),
			expectedDst: "file1.txt",
		},
		{
			name:        "cp " + tc.storage + "://bucket//a/b///c////object dir/",
			src:         "/a/b///c////file1.txt",
			dst:         "dir/",
			expected:    fs.WithDir("dir", fs.WithFile("file1.txt", fileContent, fs.WithMode(0644))),
			expectedDst: "dir/file1.txt",
		},
		{
			name:        "cp " + tc.storage + "://bucket//a/b///c////object dir/file",
			src:         "/a/b///c////file1.txt",
			dst:         "dir/file1.txt",
			expected:    fs.WithDir("dir", fs.WithFile("file1.txt", fileContent, fs.WithMode(0644))),
			expectedDst: "dir/file1.txt",
		},
	}

	for _, tc := range testcases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			bucket := s3BucketFromTestName(t)

			s3client, s5cmd := setup(t)
			createBucket(t, s3client, bucket)

			putFile(t, s3client, bucket, tc.src, fileContent)

			src := fmt.Sprintf("s3://%v/%v", bucket, tc.src)
			cmd := s5cmd("cp", src, tc.dst)
			result := icmd.RunCmd(cmd)

			result.Assert(t, icmd.Success)
			expectedOutput := fmt.Sprintf("cp s3://%v/%v %v", bucket, tc.src, tc.expectedDst)
			assertLines(t, result.Stdout(), map[int]compareFunc{
				0: equals(expectedOutput),
			})

			// assert local filesystem
			expected := fs.Expected(t, tc.expected)
			assert.Assert(t, fs.Equal(cmd.Dir, expected))

			// assert s3 object
			assert.Assert(t, ensureS3Object(s3client, bucket, tc.src, fileContent))
		})
	}
}

func TestCopySingleObjectToLocalJSON(t *testing.T) {
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.storage, func(t *testing.T) {
			t.Parallel()
			runTestCopySingleObjectToLocalJSON(t, &tc)
		})
	}
}

// --json cp s3://bucket/object .
func runTestCopySingleObjectToLocalJSON(t *testing.T, tc *testCase) {
	t.Run(tc.name, func(t *testing.T) {
		s3client, s5cmd := setup(t)
		bucket := s3BucketFromTestName(t)
		createBucket(t, s3client, bucket)

		const (
			filename = "testfile1.txt"
			content  = "this is a file content"
		)

		putFile(t, s3client, bucket, filename, content)

		cmd := s5cmd("--json", "cp", tc.storage+"://"+bucket+"/"+filename, ".")
		result := icmd.RunCmd(cmd)
		result.Assert(t, icmd.Success)

		jsonText := ` { "operation": "cp", "success": true, "source": "%s://%v/testfile1.txt", "destination": "testfile1.txt", "object": { "type": "file", "size": 22 } } `
		assertLines(t, result.Stdout(), map[int]compareFunc{
			0: json(jsonText, tc.storage, bucket),
		}, jsonCheck(true))

		// assert local filesystem
		expected := fs.Expected(t, fs.WithFile(filename, content, fs.WithMode(0644)))
		assert.Assert(t, fs.Equal(cmd.Dir, expected))

		// assert s3 object
		assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
	})
}

// cp s3://bucket/object *
func TestCopySingleS3ObjectToLocalWithDestinationWildcard(t *testing.T) {
	t.Parallel()

	for _, tc := range testCases {
		t.Run(tc.storage, func(t *testing.T) {
			t.Parallel()
			s3client, s5cmd := setup(t)
			bucket := s3BucketFromTestName(t)
			createBucket(t, s3client, bucket)

			const (
				filename = "testfile1.txt"
				content  = "this is a file content"
			)

			putFile(t, s3client, bucket, filename, content)

			cmd := s5cmd("cp", tc.storage+"://"+bucket+"/"+filename, "*")
			result := icmd.RunCmd(cmd)
			result.Assert(t, icmd.Expected{ExitCode: 1})

			// ignore stdout. we expect error logs from stderr.
			assertLines(t, result.Stderr(), map[int]compareFunc{
				0: equals(`ERROR "cp %v://%v/%v *": target "*" can not contain glob characters`, tc.storage, bucket, filename),
			})

			// assert local filesystem
			expected := fs.Expected(t)
			assert.Assert(t, fs.Equal(cmd.Dir, expected))
			// assert s3 object
			assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
		})
	}
}

// cp s3://bucket/prefix/ dir/

func TestCopyPrefixToLocalMustReturnError(t *testing.T) {
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.storage, func(t *testing.T) {
			t.Parallel()

			bucket := s3BucketFromTestName(t)

			s3client, s5cmd := setup(t)
			createBucket(t, s3client, bucket)

			const filename = "testfile1.txt"
			putFile(t, s3client, bucket, filename, "")

			cmd := s5cmd("cp", tc.storage+"://"+bucket+"/", ".")
			result := icmd.RunCmd(cmd)

			result.Assert(t, icmd.Expected{
				ExitCode: 1,
				Err:      "Please provide a destination which is not a directory or use the --recursive flag",
			})
		})
	}
}

func runTestCopyPrefixToLocalMustReturnError(t *testing.T, tc *testCase) {
	t.Run(tc.name, func(t *testing.T) {
		t.Parallel()

		bucket := s3BucketFromTestName(t)

		s3client, s5cmd := setup(t)
		createBucket(t, s3client, bucket)

		cmd := s5cmd("cp", tc.storage+"://"+bucket+"/prefix/", "localpath/")
		result := icmd.RunCmd(cmd)

		result.Assert(t, icmd.Expected{ExitCode: 1})

		assertLines(t, result.Stderr(), map[int]compareFunc{
			0: equals("ERROR " + tc.storage + " prefix /prefix/ can not be a directory destination"),
		})
	})
}

// cp --flatten s3://bucket/* dir/ (flat source hiearchy)

func TestCopyMultipleFlatObjectsToLocalJSON(t *testing.T) {
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.storage, func(t *testing.T) {
			t.Parallel()
			runTestCopyMultipleFlatObjectsToLocalJSON(t, &tc)
		})
	}
}

func runTestCopyMultipleFlatObjectsToLocalJSON(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	filesToContent := map[string]string{
		"testfile1.txt":            "this is a test file 1",
		"a/readme.md":              "this is a readme file",
		"a/filename-with-hypen.gz": "file has hypen in its name",
		"b/another_test_file.txt":  "yet another txt file. yatf.",
	}

	for filename, content := range filesToContent {
		putFile(t, s3client, bucket, filename, content)
	}

	cmd := s5cmd("--json", "cp", "--flatten", tc.storage+"://"+bucket+"/*", ".")
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: json(` { "operation": "cp", "success": true, "source": "%v://%v/a/filename-with-hypen.gz", "destination": "filename-with-hypen.gz", "object": { "type": "file", "size": 26 } }`, tc.storage, bucket),
		1: json(` { "operation": "cp", "success": true, "source": "%v://%v/a/readme.md", "destination": "readme.md", "object": { "type": "file", "size": 22 } }`, tc.storage, bucket),
		2: json(` { "operation": "cp", "success": true, "source": "%v://%v/b/another_test_file.txt", "destination": "another_test_file.txt", "object": { "type": "file", "size": 27 } }`, tc.storage, bucket),
		3: json(` { "operation": "cp", "success": true, "source": "%v://%v/testfile1.txt", "destination": "testfile1.txt", "object": { "type": "file", "size": 21 } }`, tc.storage, bucket),
	}, sortInput(true), jsonCheck(true))

	// assert local filesystem
	// expect flattened directory structure
	var expectedFiles = []fs.PathOp{
		fs.WithFile("testfile1.txt", "this is a test file 1"),
		fs.WithFile("readme.md", "this is a readme file"),
		fs.WithFile("filename-with-hypen.gz", "file has hypen in its name"),
		fs.WithFile("another_test_file.txt", "yet another txt file. yatf."),
	}
	expected := fs.Expected(t, expectedFiles...)
	assert.Assert(t, fs.Equal(cmd.Dir, expected))

	// assert s3 objects
	for filename, content := range filesToContent {
		assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
	}
}

// cp --flatten s3://bucket/*.txt dir/

func TestCopyMultipleFlatS3ObjectsToLocalWithPartialMatching(t *testing.T) {
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.storage, func(t *testing.T) {
			t.Parallel()
			runTestCopyMultipleFlatS3ObjectsToLocalWithPartialMatching(t, &tc)
		})
	}
}

func runTestCopyMultipleFlatS3ObjectsToLocalWithPartialMatching(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	filesToContent := map[string]string{
		"testfile1.txt":            "this is a test file 1",
		"a/readme.md":              "this is a readme file",
		"a/filename-with-hypen.gz": "file has hypen in its name",
		"b/another_test_file.txt":  "yet another txt file. yatf.",
	}

	for filename, content := range filesToContent {
		putFile(t, s3client, bucket, filename, content)
	}

	cmd := s5cmd("cp", "--flatten", tc.storage+"://"+bucket+"/*.txt", ".")
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stderr(), map[int]compareFunc{})

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals("cp " + tc.storage + "://" + bucket + "/b/another_test_file.txt another_test_file.txt"),
		1: equals("cp " + tc.storage + "://" + bucket + "/testfile1.txt testfile1.txt"),
	}, sortInput(true))

	// assert local filesystem
	// expect flattened directory structure
	var expectedFiles = []fs.PathOp{
		fs.WithFile("testfile1.txt", "this is a test file 1"),
		fs.WithFile("another_test_file.txt", "yet another txt file. yatf."),
	}
	expected := fs.Expected(t, expectedFiles...)
	assert.Assert(t, fs.Equal(cmd.Dir, expected))

	// assert s3 objects
	for filename, content := range filesToContent {
		assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
	}
}

// cp s3://bucket/*/*.txt dir/

func TestCopyMultipleFlatNestedS3ObjectsToLocalWithPartialMatching(t *testing.T) {
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.storage, func(t *testing.T) {
			t.Parallel()
			runTestCopyMultipleFlatNestedS3ObjectsToLocalWithPartialMatching(t, &tc)
		})
	}
}

func runTestCopyMultipleFlatNestedS3ObjectsToLocalWithPartialMatching(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	filesToContent := map[string]string{
		"testfile1.txt":            "this is a test file 1",
		"a/readme.md":              "this is a readme file",
		"a/filename-with-hypen.gz": "file has hypen in its name",
		"b/another_test_file.txt":  "yet another txt file. yatf.",
	}

	for filename, content := range filesToContent {
		putFile(t, s3client, bucket, filename, content)
	}

	cmd := s5cmd("cp", tc.storage+"://"+bucket+"/*/*.txt", ".")
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stderr(), map[int]compareFunc{})

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals("cp " + tc.storage + "://" + bucket + "/b/another_test_file.txt b/another_test_file.txt"),
	})

	// assert local filesystem
	expected := fs.Expected(t, fs.WithDir("b", fs.WithMode(0755),
		fs.WithFile("another_test_file.txt", "yet another txt file. yatf.")))
	assert.Assert(t, fs.Equal(cmd.Dir, expected))

	// assert s3 objects
	for filename, content := range filesToContent {
		assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
	}
}

// --json cp --flatten s3://bucket/* .

func TestCopyMultipleFlatS3ObjectsToLocalJSON(t *testing.T) {
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.storage, func(t *testing.T) {
			t.Parallel()
			runTestCopyMultipleFlatS3ObjectsToLocalJSON(t, &tc)
		})
	}
}

func runTestCopyMultipleFlatS3ObjectsToLocalJSON(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	filesToContent := map[string]string{
		"testfile1.txt":            "this is a test file 1",
		"a/readme.md":              "this is a readme file",
		"a/filename-with-hypen.gz": "file has hypen in its name",
		"b/another_test_file.txt":  "yet another txt file. yatf.",
	}

	for filename, content := range filesToContent {
		putFile(t, s3client, bucket, filename, content)
	}

	cmd := s5cmd("--json", "cp", "--flatten", tc.storage+"://"+bucket+"/*", ".")
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: json(` { "operation": "cp", "success": true, "source": "%v://%v/a/filename-with-hypen.gz", "destination": "filename-with-hypen.gz", "object": { "type": "file", "size": 26 } }`, tc.storage, bucket),
		1: json(` { "operation": "cp", "success": true, "source": "%v://%v/a/readme.md", "destination": "readme.md", "object": { "type": "file", "size": 22 } }`, tc.storage, bucket),
		2: json(` { "operation": "cp", "success": true, "source": "%v://%v/b/another_test_file.txt", "destination": "another_test_file.txt", "object": { "type": "file", "size": 27 } }`, tc.storage, bucket),
		3: json(` { "operation": "cp", "success": true, "source": "%v://%v/testfile1.txt", "destination": "testfile1.txt", "object": { "type": "file", "size": 21 } }`, tc.storage, bucket),
	}, sortInput(true), jsonCheck(true))

	// assert local filesystem
	var expectedFiles = []fs.PathOp{
		fs.WithFile("testfile1.txt", "this is a test file 1"),
		fs.WithFile("readme.md", "this is a readme file"),
		fs.WithFile("filename-with-hypen.gz", "file has hypen in its name"),
		fs.WithFile("another_test_file.txt", "yet another txt file. yatf."),
	}
	expected := fs.Expected(t, expectedFiles...)
	assert.Assert(t, fs.Equal(cmd.Dir, expected))

	// assert remote objects
	for filename, content := range filesToContent {
		assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
	}
}

// cp s3://bucket/* dir/ (nested source hierarchy)

func TestCopyMultipleNestedS3ObjectsToLocal(t *testing.T) {
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.storage, func(t *testing.T) {
			t.Parallel()
			runTestCopyMultipleNestedS3ObjectsToLocal(t, &tc)
		})
	}
}

func runTestCopyMultipleNestedS3ObjectsToLocal(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	filesToContent := map[string]string{
		"testfile1.txt":               "this is a test file 1",
		"a/readme.md":                 "this is a readme file",
		"a/b/filename-with-hypen.gz":  "file has hypen in its name",
		"b/another_test_file.txt":     "yet another txt file. yatf.",
		"c/d/e/another_test_file.txt": "yet another txt file. yatf.",
	}

	for filename, content := range filesToContent {
		putFile(t, s3client, bucket, filename, content)
	}

	cmd := s5cmd("cp", tc.storage+"://"+bucket+"/*", ".")
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %v://%v/a/b/filename-with-hypen.gz a/b/filename-with-hypen.gz`, tc.storage, bucket),
		1: equals(`cp %v://%v/a/readme.md a/readme.md`, tc.storage, bucket),
		2: equals(`cp %v://%v/b/another_test_file.txt b/another_test_file.txt`, tc.storage, bucket),
		3: equals(`cp %v://%v/c/d/e/another_test_file.txt c/d/e/another_test_file.txt`, tc.storage, bucket),
		4: equals(`cp %v://%v/testfile1.txt testfile1.txt`, tc.storage, bucket),
	}, sortInput(true))

	// assert local filesystem
	var expectedFiles = []fs.PathOp{
		fs.WithFile("testfile1.txt", "this is a test file 1"),
		fs.WithDir(
			"a",
			fs.WithFile("readme.md", "this is a readme file"),
			fs.WithDir(
				"b",
				fs.WithFile("filename-with-hypen.gz", "file has hypen in its name"),
			),
		),
		fs.WithDir(
			"b",
			fs.WithFile("another_test_file.txt", "yet another txt file. yatf."),
		),
		fs.WithDir(
			"c",
			fs.WithDir(
				"d",
				fs.WithDir(
					"e",
					fs.WithFile("another_test_file.txt", "yet another txt file. yatf."),
				),
			),
		),
	}
	expected := fs.Expected(t, expectedFiles...)
	assert.Assert(t, fs.Equal(cmd.Dir, expected))

	// assert remote objects
	for filename, content := range filesToContent {
		assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
	}
}

// cp s3://bucket/*/*.ext dir/

func TestCopyMultipleNestedS3ObjectsToLocalWithPartial(t *testing.T) {
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.storage, func(t *testing.T) {
			t.Parallel()
			runTestCopyMultipleNestedS3ObjectsToLocalWithPartial(t, &tc)
		})
	}
}

func runTestCopyMultipleNestedS3ObjectsToLocalWithPartial(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	filesToContent := map[string]string{
		"testfile1.txt":               "this is a test file 1",
		"a/readme.md":                 "this is a readme file",
		"a/b/filename-with-hypen.gz":  "file has hypen in its name",
		"b/another_test_file.txt":     "yet another txt file. yatf.",
		"c/d/e/another_test_file.txt": "yet another txt file. yatf.",
	}

	for filename, content := range filesToContent {
		putFile(t, s3client, bucket, filename, content)
	}

	cmd := s5cmd("cp", tc.storage+"://"+bucket+"/*/*.txt", ".")
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %v://%v/b/another_test_file.txt b/another_test_file.txt`, tc.storage, bucket),
		1: equals(`cp %v://%v/c/d/e/another_test_file.txt c/d/e/another_test_file.txt`, tc.storage, bucket),
	}, sortInput(true))

	// assert local filesystem
	var expectedFiles = []fs.PathOp{
		fs.WithDir(
			"b",
			fs.WithFile("another_test_file.txt", "yet another txt file. yatf."),
		),
		fs.WithDir(
			"c",
			fs.WithDir(
				"d",
				fs.WithDir(
					"e",
					fs.WithFile("another_test_file.txt", "yet another txt file. yatf."),
				),
			),
		),
	}
	expected := fs.Expected(t, expectedFiles...)
	assert.Assert(t, fs.Equal(cmd.Dir, expected))

	// assert remote objects
	for filename, content := range filesToContent {
		assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
	}
}

// cp s3://bucket/* dir/ (dir/ doesn't exist)

func TestCopyMultipleS3ObjectsToGivenLocalDirectory(t *testing.T) {
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.storage, func(t *testing.T) {
			t.Parallel()
			runTestCopyMultipleS3ObjectsToGivenLocalDirectory(t, &tc)
		})
	}
}

func runTestCopyMultipleS3ObjectsToGivenLocalDirectory(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	filesToContent := map[string]string{
		"testfile1.txt":               "this is a test file 1",
		"a/readme.md":                 "this is a readme file",
		"a/b/filename-with-hypen.gz":  "file has hypen in its name",
		"b/another_test_file.txt":     "yet another txt file. yatf.",
		"c/d/e/another_test_file.txt": "yet another txt file. yatf.",
	}

	for filename, content := range filesToContent {
		putFile(t, s3client, bucket, filename, content)
	}

	const localDir = "given-dir"

	cmd := s5cmd("cp", tc.storage+"://"+bucket+"/*", localDir+"/")
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %v://%v/a/b/filename-with-hypen.gz %v/a/b/filename-with-hypen.gz`, tc.storage, bucket, localDir),
		1: equals(`cp %v://%v/a/readme.md %v/a/readme.md`, tc.storage, bucket, localDir),
		2: equals(`cp %v://%v/b/another_test_file.txt %v/b/another_test_file.txt`, tc.storage, bucket, localDir),
		3: equals(`cp %v://%v/c/d/e/another_test_file.txt %v/c/d/e/another_test_file.txt`, tc.storage, bucket, localDir),
		4: equals(`cp %v://%v/testfile1.txt %v/testfile1.txt`, tc.storage, bucket, localDir),
	}, sortInput(true))

	// assert local filesystem
	var expectedFiles = []fs.PathOp{
		fs.WithDir(
			localDir,
			fs.WithFile("testfile1.txt", "this is a test file 1"),
			fs.WithDir(
				"a",
				fs.WithFile("readme.md", "this is a readme file"),
				fs.WithDir(
					"b",
					fs.WithFile("filename-with-hypen.gz", "file has hypen in its name"),
				),
			),
			fs.WithDir(
				"b",
				fs.WithFile("another_test_file.txt", "yet another txt file. yatf."),
			),
			fs.WithDir(
				"c",
				fs.WithDir(
					"d",
					fs.WithDir(
						"e",
						fs.WithFile("another_test_file.txt", "yet another txt file. yatf."),
					),
				),
			),
		),
	}
	expected := fs.Expected(t, expectedFiles...)
	assert.Assert(t, fs.Equal(cmd.Dir, expected))

	// assert remote objects
	for filename, content := range filesToContent {
		assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
	}
}

// cp dir/file s3://bucket/

func TestCopySingleFileToS3(t *testing.T) {
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.storage, func(t *testing.T) {
			t.Parallel()
			runTestCopySingleFileToS3(t, &tc)
		})
	}
}

func runTestCopySingleFileToS3(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const (
		// make sure that Put reads the file header and guess Content-Type correctly.
		filename = "index"
		content  = `
<html lang="en">
	<head>
	<meta charset="utf-8">
	<body>
		<div id="foo">
			<div class="bar"></div>
		</div>
		<div id="baz">
			<style data-hey="naber"></style>
		</div>
	</body>
</html>
`
		expectedContentType        = "text/html; charset=utf-8"
		expectedContentDisposition = "inline"
	)

	workdir := fs.NewDir(t, bucket, fs.WithFile(filename, content))
	defer workdir.Remove()

	srcpath := workdir.Join(filename)
	dstpath := fmt.Sprintf("%v://%v/", tc.storage, bucket)
	contentDisposition := "inline"

	srcpath = filepath.ToSlash(srcpath)
	cmd := s5cmd("cp", "--content-disposition", contentDisposition, srcpath, dstpath)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: suffix(`cp %v %v%v`, srcpath, dstpath, filename),
	})

	// assert local filesystem
	expected := fs.Expected(t, fs.WithFile(filename, content))
	assert.Assert(t, fs.Equal(workdir.Path(), expected))

	// assert S3/GCS
	assert.Assert(t, ensureS3Object(s3client, bucket, filename, content, ensureContentType(expectedContentType), ensureContentDisposition(expectedContentDisposition)))
}

func TestCopySingleFileToS3WithAllMetadataFlags(t *testing.T) {
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.storage, func(t *testing.T) {
			t.Parallel()
			runTestCopySingleFileToS3WithAllMetadataFlags(t, &tc)
		})
	}
}

func runTestCopySingleFileToS3WithAllMetadataFlags(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const (
		filename             = "index"
		content              = `testfilecontent`
		cacheControl         = "public, max-age=3600"
		expires              = "2025-01-01T00:00:00Z"
		storageClass         = "STANDARD_IA"
		ContentType          = "text/html; charset=utf-8"
		ContentDisposition   = "inline"
		ContentEncoding      = "utf-8"
		EncryptionMethod     = "aws:kms"
		EncryptionKeyID      = "1234abcd-12ab-34cd-56ef-1234567890ab"
		contentType          = "text/html"
		contentDisposition   = "inline"
		contentEncoding      = "gzip"
		contentLanguage      = "en"
		expectedContentType  = contentType
		expectedEncoding     = contentEncoding
		expectedLanguage     = contentLanguage
		expectedCacheControl = cacheControl
		expectedDisposition  = contentDisposition
		expectedStorageClass = "STANDARD"
	)

	// expected expires flag is the parsed version of the date in RFC3339 format
	parsedTime, err := time.Parse(time.RFC3339, expires)
	if err != nil {
		t.Fatal(err)
	}

	expectedExpires := parsedTime.Format(http.TimeFormat)

	workdir := fs.NewDir(t, bucket, fs.WithFile(filename, content))
	defer workdir.Remove()

	srcpath := workdir.Join(filename)
	dstpath := fmt.Sprintf("%v://%v/", tc.storage, bucket)

	srcpath = filepath.ToSlash(srcpath)
	cmd := s5cmd("cp", "--no-guess-mime-type", "--content-type", contentType, "--content-disposition", contentDisposition, "--content-encoding", contentEncoding, "--content-language", contentLanguage, "--cache-control", cacheControl, srcpath, dstpath)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)
	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: prefix("cp "),
	})

	// assert local filesystem
	expected := fs.Expected(t, fs.WithFile(filename, content))
	assert.Assert(t, fs.Equal(workdir.Path(), expected))

	// assert S3/GCS
	assert.Assert(t, ensureS3Object(s3client, bucket, filename, content,
		ensureExpires(expectedExpires),
		ensureCacheControl(cacheControl),
		ensureStorageClass(storageClass),
		ensureContentType(ContentType),
		ensureContentDisposition(ContentDisposition),
		ensureContentEncoding(ContentEncoding),
		ensureEncryptionMethod(EncryptionMethod),
		ensureEncryptionKeyID(EncryptionKeyID),
	))
}

// cp dir/file s3://bucket/ --metadata key1=val1 --metadata key2=val2 ...

func TestCopySingleFileToS3WithArbitraryMetadata(t *testing.T) {
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.storage, func(t *testing.T) {
			t.Parallel()
			runTestCopySingleFileToS3WithArbitraryMetadata(t, &tc)
		})
	}
}

func runTestCopySingleFileToS3WithArbitraryMetadata(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const (
		filename = "myfile.txt"
		content  = "file content"
	)

	workdir := fs.NewDir(t, "somedir", fs.WithFile(filename, content))
	defer workdir.Remove()

	srcpath := workdir.Join(filename)
	dstpath := fmt.Sprintf("%v://%v/", tc.storage, bucket)

	srcpath = filepath.ToSlash(srcpath)
	cmd := s5cmd("cp", "--metadata", "key1=val1", "--metadata", "key2=val2", srcpath, dstpath)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: suffix(`cp %v %v%v`, srcpath, dstpath, filename),
	})

	// assert local filesystem
	expected := fs.Expected(t, fs.WithFile(filename, content))
	assert.Assert(t, fs.Equal(workdir.Path(), expected))

	// assert S3/GCS
	assert.Assert(t, ensureS3Object(
		s3client, bucket, filename, content,
		ensureArbitraryMetadata(map[string]*string{"key1": strPtr("val1"), "key2": strPtr("val2")}),
	))
}

// cp s3://bucket2/obj2 s3://bucket1/obj1 --metadata key1=val1 --metadata key2=val2 ...

func TestCopyS3ToS3WithArbitraryMetadata(t *testing.T) {
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.storage, func(t *testing.T) {
			t.Parallel()
			runTestCopyS3ToS3WithArbitraryMetadata(t, &tc)
		})
	}
}

func runTestCopyS3ToS3WithArbitraryMetadata(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket1 := s3BucketFromTestName(t)
	bucket2 := bucket1 + "-other"
	createBucket(t, s3client, bucket1)
	createBucket(t, s3client, bucket2)

	const (
		filename = "testfile1.txt"
		content  = "this is a file content"
	)

	// put a file into bucket2
	putFile(t, s3client, bucket2, filename, content)

	cmd := s5cmd(
		"cp",
		fmt.Sprintf("%v://%v/%v", tc.storage, bucket2, filename),
		fmt.Sprintf("%v://%v/copied-%v", tc.storage, bucket1, filename),
		"--metadata", "key1=val1",
		"--metadata", "key2=val2",
	)

	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: suffix(`cp %v://%v/%v %v://%v/copied-%v`, tc.storage, bucket2, filename, tc.storage, bucket1, filename),
	})

	// assert local filesystem (should not be modified)
	assertLines(t, result.Stdout(), map[int]compareFunc{})

	// assert s3 object in bucket1
	assert.Assert(t, ensureS3Object(
		s3client, bucket1, fmt.Sprintf("copied-%v", filename), content,
		ensureArbitraryMetadata(map[string]*string{"key1": strPtr("val1"), "key2": strPtr("val2")}),
	))

	// assert s3 object in bucket2 still exists
	assert.Assert(t, ensureS3Object(s3client, bucket2, filename, content))
}

func TestCopySingleFileToS3WithAdjacentSlashes(t *testing.T) {
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.storage, func(t *testing.T) {
			t.Parallel()
			runTestCopySingleFileToS3WithAdjacentSlashes(t, &tc)
		})
	}
}

func runTestCopySingleFileToS3WithAdjacentSlashes(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const (
		filename        = "file1.txt"
		content         = "some content"
		subdir          = "subdir/anotherslash//"
		expectedContent = content
	)

	workdir := fs.NewDir(t, "test-dir", fs.WithFile(filename, content))
	defer workdir.Remove()

	srcpath := workdir.Join(filename)
	dstpath := fmt.Sprintf("%v://%v/%v//", tc.storage, bucket, subdir)

	srcpath = filepath.ToSlash(srcpath)
	cmd := s5cmd("cp", srcpath, dstpath)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: suffix(`cp %v %v%v%v`, srcpath, dstpath, subdir, filename),
	})

	// assert local filesystem
	expected := fs.Expected(t, fs.WithFile(filename, content))
	assert.Assert(t, fs.Equal(workdir.Path(), expected))

	// assert s3 object
	assert.Assert(t, ensureS3Object(s3client, bucket, path.Join(subdir, filename), expectedContent))
}

// --json cp dir/file s3://bucket

func TestCopySingleFileToS3JSON(t *testing.T) {
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.storage, func(t *testing.T) {
			t.Parallel()
			runTestCopySingleFileToS3JSON(t, &tc)
		})
	}
}

func runTestCopySingleFileToS3JSON(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const (
		filename = "testfile1.txt"
		content  = "this is a file content"
	)

	workdir := fs.NewDir(t, "somedir", fs.WithFile(filename, content))
	defer workdir.Remove()

	srcpath := workdir.Join(filename)
	dstpath := fmt.Sprintf("%v://%v/", tc.storage, bucket)

	srcpath = filepath.ToSlash(srcpath)
	cmd := s5cmd("--json", "cp", srcpath, dstpath)
	result := icmd.RunCmd(cmd)
	result.Assert(t, icmd.Success)

	jsonText := ` { "operation": "cp", "success": true, "source": "%v", "destination": "%v://%v/testfile1.txt", "object": { "type": "file", "size": 22 } } `
	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: json(jsonText, srcpath, tc.storage, bucket),
	}, jsonCheck(true))

	// assert local filesystem
	expected := fs.Expected(t, fs.WithFile(filename, content))
	assert.Assert(t, fs.Equal(workdir.Path(), expected))

	// assert s3 object
	assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
}

// cp dir/ s3://bucket/

func TestCopyDirToS3(t *testing.T) {

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyDirToS3(t, &tc)
		})
	}
}

func runTestCopyDirToS3(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const (
		filename = "file1.txt"
		content  = "this is a file"
	)

	folderLayout := []fs.PathOp{
		fs.WithFile(filename, content),
		fs.WithDir(
			"subfolder",
			fs.WithFile(filename, content),
		),
	}

	workdir := fs.NewDir(t, t.Name(), folderLayout...)
	defer workdir.Remove()

	srcpath := filepath.ToSlash(workdir.Path())
	dstpath := fmt.Sprintf("%v://%v/", tc.storage, bucket)

	cmd := s5cmd("cp", "--raw", srcpath+"/"+filename, dstpath)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	// assert only the single file was copied
	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals("cp %v/%v %v%v", srcpath, filename, dstpath, filename),
	})

	// assert s3 objects
	err := ensureS3Object(s3client, bucket, filename, content)
	if err != nil {
		t.Fatalf("%v does not exist in s3", filename)
	}

	// assert only the file was uploaded and not the whole directory
	err = ensureS3Object(s3client, bucket, "subfolder/"+filename, content)
	assertError(t, err, errS3NoSuchKey)

	// assert local filesystem
	expected := fs.Expected(t, folderLayout...)
	assert.Assert(t, fs.Equal(workdir.Path(), expected))
}

// cp dir/{file, folderWithBackslash} s3://bucket

func TestCopyDirBackslashedToS3(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyDirBackslashedToS3(t, &tc)
		})
	}
}

func runTestCopyDirBackslashedToS3(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const (
		filename = "file1.txt"
		content  = "this is a file"
	)

	folderLayout := []fs.PathOp{
		fs.WithFile("folder/file", content),
		fs.WithDir("foldernotherfolder"),
	}

	workdir := fs.NewDir(t, t.Name(), folderLayout...)
	defer workdir.Remove()

	srcpath := filepath.ToSlash(workdir.Path())
	dstpath := fmt.Sprintf("%v://%v/", tc.storage, bucket)

	cmd := s5cmd("cp", srcpath+"/folder", dstpath)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals("cp %v/folder/file %vfolder/file", srcpath, dstpath),
	})

	// assert s3 objects
	err := ensureS3Object(s3client, bucket, "folder/file", content)
	if err != nil {
		t.Fatalf("%v does not exist in s3", "folder/file")
	}

	// assert only the file was uploaded and empty subfolder was skipped
	err = ensureS3Object(s3client, bucket, "folder/anotherfolder/", "")
	assertError(t, err, errS3NoSuchKey)

	// assert local filesystem
	expected := fs.Expected(t, folderLayout...)
	assert.Assert(t, fs.Equal(workdir.Path(), expected))
}

// cp --storage-class=GLACIER file s3://bucket/

func TestCopySingleFileToS3WithStorageClassGlacier(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopySingleFileToS3WithStorageClassGlacier(t, &tc)
		})
	}
}

func runTestCopySingleFileToS3WithStorageClassGlacier(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const (
		filename = "testfile1.txt"
		content  = "this is a file content"
	)

	workdir := fs.NewDir(t, t.Name(), fs.WithFile(filename, content))
	defer workdir.Remove()

	cmd := s5cmd("cp", "--storage-class", "GLACIER", filename, fmt.Sprintf("%s://%s/", tc.storage, bucket))

	// store current working directory to set back
	// when command finishes
	previousCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// change working directory
	err = os.Chdir(workdir.Path())
	if err != nil {
		t.Fatal(err)
	}
	// set back working directory
	defer os.Chdir(previousCWD)

	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %v %s://%v/%v`, filename, tc.storage, bucket, filename),
	})

	// assert local filesystem
	expected := fs.Expected(t, fs.WithFile(filename, content))
	assert.Assert(t, fs.Equal(workdir.Path(), expected))

	// assert s3 object
	assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))

	if tc.storage == "s3" {
		// assert s3 object storage class
		// TODO(rr): check
		// assert.Assert(t, ensureS3ObjectStorageClass(s3client, bucket, filename, "GLACIER"))
	}
}

// cp --flatten dir/ s3://bucket/

func TestFlattenCopyDirToS3(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestFlattenCopyDirToS3(t, &tc)
		})
	}
}

func runTestFlattenCopyDirToS3(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const (
		subfolder = "subfolder"
		filename  = "file1.txt"
		content   = "this is a file"
	)

	folderLayout := []fs.PathOp{
		fs.WithDir(
			subfolder,
			fs.WithFile(filename, content),
		),
	}

	workdir := fs.NewDir(t, t.Name(), folderLayout...)
	defer workdir.Remove()

	cmd := s5cmd("cp", "--flatten", filepath.Join(workdir.Path(), subfolder), fmt.Sprintf("%s://%s/", tc.storage, bucket))
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %s/%s %s://%s/%s`, subfolder, filename, tc.storage, bucket, filename),
	})

	// assert s3 object
	err := ensureS3Object(s3client, bucket, filename, content)
	if err != nil {
		t.Fatalf("%s not uploaded to s3: %v", filename, err)
	}

	err = ensureS3Object(s3client, bucket, "subfolder/", "")
	assertError(t, err, errS3NoSuchKey)

	// assert local filesystem
	expected := fs.Expected(t, folderLayout...)
	assert.Assert(t, fs.Equal(workdir.Path(), expected))
}

// cp dir/* s3://bucket/

func TestCopyMultipleFilesToS3Bucket(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyMultipleFilesToS3Bucket(t, &tc)
		})
	}
}

func runTestCopyMultipleFilesToS3Bucket(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	filesToContent := map[string]string{
		"testfile1.txt":             "content1",
		"readme.md":                 "this is a readme",
		"filename-with-symbols-@$%": "some contents",
	}

	var files []fs.PathOp
	for filename, content := range filesToContent {
		op := fs.WithFile(filename, content)
		files = append(files, op)
	}

	workdir := fs.NewDir(t, t.Name(), files...)
	defer workdir.Remove()
	workdirPath := filepath.ToSlash(workdir.Path())

	cmd := s5cmd("cp", workdirPath+"/*", fmt.Sprintf("%s://%s/", tc.storage, bucket))
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals("cp %v/filename-with-symbols-@$%% %v://%v/filename-with-symbols-@$%%", workdirPath, tc.storage, bucket),
		1: equals("cp %v/readme.md %v://%v/readme.md", workdirPath, tc.storage, bucket),
		2: equals("cp %v/testfile1.txt %v://%v/testfile1.txt", workdirPath, tc.storage, bucket),
	})

	for filename, content := range filesToContent {
		assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
	}

	// assert local filesystem
	expected := fs.Expected(t, files...)
	assert.Assert(t, fs.Equal(workdirPath, expected))
}

// cp parent/*/name.txt s3://bucket/newfolder

func TestCopyMultipleFilesWithWildcardedDirectoryToS3Bucket(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyMultipleFilesWithWildcardedDirectoryToS3Bucket(t, &tc)
		})
	}
}

func runTestCopyMultipleFilesWithWildcardedDirectoryToS3Bucket(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const (
		subfolder1 = "subfolder1"
		subfolder2 = "subfolder2"
		filename   = "file.txt"
		content    = "this is a test file"
	)

	folderLayout := []fs.PathOp{
		fs.WithDir(subfolder1,
			fs.WithFile(filename, content),
		),
		fs.WithDir(subfolder2,
			fs.WithFile(filename, content),
		),
	}

	workdir := fs.NewDir(t, t.Name(), folderLayout...)
	defer workdir.Remove()

	cmd := s5cmd("cp", filepath.Join(workdir.Path(), "*", filename), fmt.Sprintf("%s://%s/", tc.storage, bucket))
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %s/%s/%s %s://%s/%s/%s`, workdir.Path(), subfolder1, filename, tc.storage, bucket, subfolder1, filename),
		1: equals(`cp %s/%s/%s %s://%s/%s/%s`, workdir.Path(), subfolder2, filename, tc.storage, bucket, subfolder2, filename),
	})

	assert.Assert(t, ensureS3Object(s3client, bucket, filepath.Join(subfolder1, filename), content))
	assert.Assert(t, ensureS3Object(s3client, bucket, filepath.Join(subfolder2, filename), content))
}

// cp parent/c*/name.txt s3://bucket/newfolder

func TestCopyMultipleFilesEndWildcardedToS3Bucket(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyMultipleFilesEndWildcardedToS3Bucket(t, &tc)
		})
	}
}

func runTestCopyMultipleFilesEndWildcardedToS3Bucket(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const (
		subfolder1 = "subfolder1"
		subfolder2 = "sub2"
		filename   = "file.txt"
		content    = "this is a test file"
	)

	folderLayout := []fs.PathOp{
		fs.WithDir(subfolder1,
			fs.WithFile(filename, content),
		),
		fs.WithDir(subfolder2,
			fs.WithFile(filename, content),
		),
	}

	workdir := fs.NewDir(t, t.Name(), folderLayout...)
	defer workdir.Remove()

	cmd := s5cmd("cp", filepath.Join(workdir.Path(), "s*", filename), fmt.Sprintf("%s://%s/", tc.storage, bucket))
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %s/%s/%s %s://%s/%s/%s`, workdir.Path(), subfolder1, filename, tc.storage, bucket, subfolder1, filename),
		1: equals(`cp %s/%s/%s %s://%s/%s/%s`, workdir.Path(), subfolder2, filename, tc.storage, bucket, subfolder2, filename),
	})

	assert.Assert(t, ensureS3Object(s3client, bucket, filepath.Join(subfolder1, filename), content))
	assert.Assert(t, ensureS3Object(s3client, bucket, filepath.Join(subfolder2, filename), content))
}

// cp parent/c*1/name.txt s3://bucket/newfolder

func TestCopyMultipleFilesMiddleWildcardedDirectoryToS3Bucket(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyMultipleFilesMiddleWildcardedDirectoryToS3Bucket(t, &tc)
		})
	}
}

func runTestCopyMultipleFilesMiddleWildcardedDirectoryToS3Bucket(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const (
		subfolder1 = "sub1"
		subfolder2 = "sub2"
		filename   = "file.txt"
		content    = "this is a test file"
	)

	folderLayout := []fs.PathOp{
		fs.WithDir(subfolder1,
			fs.WithFile(filename, content),
		),
		fs.WithDir(subfolder2,
			fs.WithFile(filename, content),
		),
	}

	workdir := fs.NewDir(t, t.Name(), folderLayout...)
	defer workdir.Remove()

	cmd := s5cmd("cp", filepath.Join(workdir.Path(), "sub*1", filename), fmt.Sprintf("%s://%s/", tc.storage, bucket))
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %s/%s/%s %s://%s/%s/%s`, workdir.Path(), subfolder1, filename, tc.storage, bucket, subfolder1, filename),
	})

	assert.Assert(t, ensureS3Object(s3client, bucket, filepath.Join(subfolder1, filename), content))
}

// cp --flatten dir/* s3://bucket/

func TestFlattenCopyMultipleFilesToS3Bucket(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestFlattenCopyMultipleFilesToS3Bucket(t, &tc)
		})
	}
}

func runTestFlattenCopyMultipleFilesToS3Bucket(t *testing.T, tc *testCase) {
	t.Parallel()

	const (
		subfolder1 = "subfolder1"
		subfolder2 = "subfolder2"
		filename1  = "file1.txt"
		filename2  = "file2.txt"
		content    = "this is a test file"
	)

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	folderLayout := []fs.PathOp{
		fs.WithDir(subfolder1,
			fs.WithFile(filename1, content),
		),
		fs.WithDir(subfolder2,
			fs.WithFile(filename2, content),
		),
	}

	workdir := fs.NewDir(t, t.Name(), folderLayout...)
	defer workdir.Remove()

	cmd := s5cmd("cp", "--flatten", filepath.Join(workdir.Path(), "*"), fmt.Sprintf("%s://%s/", tc.storage, bucket))
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %s/%s/%s %s://%s/%s`, workdir.Path(), subfolder1, filename1, tc.storage, bucket, filename1),
		1: equals(`cp %s/%s/%s %s://%s/%s`, workdir.Path(), subfolder2, filename2, tc.storage, bucket, filename2),
	})

	assert.Assert(t, ensureS3Object(s3client, bucket, filename1, content))
	assert.Assert(t, ensureS3Object(s3client, bucket, filename2, content))
}

// cp dir/* s3://bucket/prefix (error)

func TestCopyMultipleFilesToS3WithPrefixWithoutSlash(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyMultipleFilesToS3WithPrefixWithoutSlash(t, &tc)
		})
	}
}

func runTestCopyMultipleFilesToS3WithPrefixWithoutSlash(t *testing.T, tc *testCase) {
	t.Parallel()

	const (
		subfolder1 = "subfolder1"
		subfolder2 = "subfolder2"
		filename1  = "file1.txt"
		filename2  = "file2.txt"
		content    = "this is a test file"
	)

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	folderLayout := []fs.PathOp{
		fs.WithDir(subfolder1,
			fs.WithFile(filename1, content),
		),
		fs.WithDir(subfolder2,
			fs.WithFile(filename2, content),
		),
	}

	workdir := fs.NewDir(t, t.Name(), folderLayout...)
	defer workdir.Remove()

	dstName := "prefix"
	cmd := s5cmd("cp", filepath.Join(workdir.Path(), "*"), fmt.Sprintf("%s://%s/%s", tc.storage, bucket, dstName))
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Expected{ExitCode: 1})

	assertLines(t, result.Stderr(), map[int]compareFunc{
		0: equals(`ERROR "cp %s %s://%s/%s": target %q is not a directory`, filepath.Join(workdir.Path(), "*"), tc.storage, bucket, dstName, fmt.Sprintf("%s://%s/%s", tc.storage, bucket, dstName)),
	})
}

// cp prefix* s3://bucket/

func TestCopyDirectoryWithGlobCharactersToS3Bucket(t *testing.T) {
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyDirectoryWithGlobCharactersToS3Bucket(t, &tc)
		})
	}
}

func runTestCopyDirectoryWithGlobCharactersToS3Bucket(t *testing.T, tc *testCase) {
	s3client, s5cmd := setup(t)
	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const (
		globChar  = "special?char"
		filename1 = "file1.txt"
		filename2 = "file2.txt"
		content   = "this is a test file"
	)

	folderLayout := []fs.PathOp{
		fs.WithDir(globChar,
			fs.WithFile(filename1, content),
			fs.WithFile(filename2, content),
		),
	}

	workdir := fs.NewDir(t, t.Name(), folderLayout...)
	defer workdir.Remove()

	cmd := s5cmd("cp", filepath.Join(workdir.Path(), fmt.Sprintf("%s*", globChar)), fmt.Sprintf("%s://%s", tc.storage, bucket))
	result := icmd.RunCmd(cmd)
	result.Assert(t, icmd.Success)
	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %s/%s/%s %s://%s/%s/%s`, workdir.Path(), globChar, filename1, tc.storage, bucket, globChar, filename1),
		1: equals(`cp %s/%s/%s %s://%s/%s/%s`, workdir.Path(), globChar, filename2, tc.storage, bucket, globChar, filename2),
	}, sortInput(true))

	// TODO(rr)
	// assert.Assert(t, ensureS3Dir(s3client, bucket, globChar))
	assert.Assert(t, ensureS3Object(s3client, bucket, filepath.Join(globChar, filename1), content))
	assert.Assert(t, ensureS3Object(s3client, bucket, filepath.Join(globChar, filename2), content))
}

// cp dir/* s3://bucket/prefix/

func TestCopyMultipleFilesToS3WithPrefixWithSlash(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyMultipleFilesToS3WithPrefixWithSlash(t, &tc)
		})
	}
}

func runTestCopyMultipleFilesToS3WithPrefixWithSlash(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	const (
		bucketPrefix = "prefix"
		filename1    = "testfile1.txt"
		filename2    = "testfile2.txt"
		content      = "this is a test file"
	)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	workdir := fs.NewDir(t, t.Name(),
		fs.WithFile(filename1, content),
		fs.WithFile(filename2, content),
	)
	defer workdir.Remove()

	// cp dir/* s3://bucket/prefix/
	cmd := s5cmd("cp", workdir.Path()+"/*", fmt.Sprintf("%s://%s/%s/", tc.storage, bucket, bucketPrefix))
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %s %s://%s/%s/%s`, filename1, tc.storage, bucket, bucketPrefix, filename1),
		1: equals(`cp %s %s://%s/%s/%s`, filename2, tc.storage, bucket, bucketPrefix, filename2),
	})

	// assert s3 objects
	assert.Assert(t, ensureS3Object(s3client, bucket, bucketPrefix+"/"+filename1, content))
	assert.Assert(t, ensureS3Object(s3client, bucket, bucketPrefix+"/"+filename2, content))
}

// cp --flatten dir/* s3://bucket/prefix/

func TestFlattenCopyMultipleFilesToS3WithPrefixWithSlash(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestFlattenCopyMultipleFilesToS3WithPrefixWithSlash(t, &tc)
		})
	}
}

func runTestFlattenCopyMultipleFilesToS3WithPrefixWithSlash(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	const (
		bucketPrefix = "prefix"
		subfolder1   = "subfolder1"
		subfolder2   = "subfolder2"
		filename1    = "testfile1.txt"
		filename2    = "testfile2.txt"
		content      = "this is a test file"
	)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	workdir := fs.NewDir(t, t.Name(),
		fs.WithDir(subfolder1,
			fs.WithFile(filename1, content),
		),
		fs.WithDir(subfolder2,
			fs.WithFile(filename2, content),
		),
	)
	defer workdir.Remove()

	cmd := s5cmd("cp", "--flatten", workdir.Path()+"/*", fmt.Sprintf("%s://%s/%s/", tc.storage, bucket, bucketPrefix))
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %s/%s/%s %s://%s/%s/%s`, workdir.Path(), subfolder1, filename1, tc.storage, bucket, bucketPrefix, filename1),
		1: equals(`cp %s/%s/%s %s://%s/%s/%s`, workdir.Path(), subfolder2, filename2, tc.storage, bucket, bucketPrefix, filename2),
	})

	assert.Assert(t, ensureS3Object(s3client, bucket, bucketPrefix+"/"+filename1, content))
	assert.Assert(t, ensureS3Object(s3client, bucket, bucketPrefix+"/"+filename2, content))
}

// cp dir/ s3://bucket/prefix/

func TestCopyLocalDirectoryToS3WithPrefixWithSlash(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyLocalDirectoryToS3WithPrefixWithSlash(t, &tc)
		})
	}
}

func runTestCopyLocalDirectoryToS3WithPrefixWithSlash(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const (
		bucketPrefix = "prefix"
		dirname      = "testdir"
		filename1    = "testfile1.txt"
		filename2    = "testfile2.txt"
		content      = "this is a test file"
	)

	dirLayout := []fs.PathOp{
		fs.WithFile(filename1, content),
		fs.WithFile(filename2, content),
	}

	workdir := fs.NewDir(t, "somedir", fs.WithDir(dirname, dirLayout...))
	defer workdir.Remove()

	// cp dir/ s3://bucket/prefix/
	cmd := s5cmd("cp", filepath.Join(workdir.Path(), dirname)+"/", fmt.Sprintf("%s://%s/%s/", tc.storage, bucket, bucketPrefix))
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %s %s://%s/%s/%s/`, dirname, tc.storage, bucket, bucketPrefix, dirname),
	})

	// assert s3 objects
	assert.Assert(t, ensureS3Object(s3client, bucket, bucketPrefix+"/"+dirname+"/"+filename1, content))
	assert.Assert(t, ensureS3Object(s3client, bucket, bucketPrefix+"/"+dirname+"/"+filename2, content))
}

// cp --flatten dir/ s3://bucket/prefix/

func TestFlattenCopyLocalDirectoryToS3WithPrefixWithSlash(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestFlattenCopyLocalDirectoryToS3WithPrefixWithSlash(t, &tc)
		})
	}
}

func runTestFlattenCopyLocalDirectoryToS3WithPrefixWithSlash(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const (
		bucketPrefix = "prefix"
		dirname      = "testdir"
		filename1    = "testfile1.txt"
		filename2    = "testfile2.txt"
		content      = "this is a test file"
	)

	dirLayout := []fs.PathOp{
		fs.WithFile(filename1, content),
		fs.WithFile(filename2, content),
	}

	workdir := fs.NewDir(t, "somedir", fs.WithDir(dirname, dirLayout...))
	defer workdir.Remove()

	// cp --flatten dir/ s3://bucket/prefix/
	cmd := s5cmd("cp", "--flatten", filepath.Join(workdir.Path(), dirname)+"/", fmt.Sprintf("%s://%s/%s/", tc.storage, bucket, bucketPrefix))
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %s/%s %s://%s/%s/%s`, dirname, filename1, tc.storage, bucket, bucketPrefix, filename1),
		1: equals(`cp %s/%s %s://%s/%s/%s`, dirname, filename2, tc.storage, bucket, bucketPrefix, filename2),
	})

	// assert s3 objects
	assert.Assert(t, ensureS3Object(s3client, bucket, bucketPrefix+"/"+filename1, content))
	assert.Assert(t, ensureS3Object(s3client, bucket, bucketPrefix+"/"+filename2, content))
}

// cp dir/ s3://bucket/prefix

func TestCopyLocalDirectoryToS3WithPrefixWithoutSlash(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyLocalDirectoryToS3WithPrefixWithoutSlash(t, &tc)
		})
	}
}

func runTestCopyLocalDirectoryToS3WithPrefixWithoutSlash(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const (
		bucketPrefix = "prefix"
		dirname      = "testdir"
		filename1    = "testfile1.txt"
		filename2    = "testfile2.txt"
		content      = "this is a test file"
	)

	dirLayout := []fs.PathOp{
		fs.WithFile(filename1, content),
		fs.WithFile(filename2, content),
	}

	workdir := fs.NewDir(t, "somedir", fs.WithDir(dirname, dirLayout...))
	defer workdir.Remove()

	// cp dir/ s3://bucket/prefix
	cmd := s5cmd("cp", filepath.Join(workdir.Path(), dirname)+"/", fmt.Sprintf("%s://%s/%s", tc.storage, bucket, bucketPrefix))
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %s %s://%s/%s`, dirname, tc.storage, bucket, bucketPrefix),
	})

	// assert s3 objects
	assert.Assert(t, ensureS3Object(s3client, bucket, bucketPrefix+"/"+filename1, content))
	assert.Assert(t, ensureS3Object(s3client, bucket, bucketPrefix+"/"+filename2, content))
}

// cp s3://bucket/object s3://bucket/object2

func TestCopySingleObjectToObject(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopySingleObjectToObject(t, &tc)
		})
	}
}

func runTestCopySingleObjectToObject(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const (
		filename    = "testfile1.txt"
		dstfilename = "copy_" + filename
		content     = "this is a file content"
	)

	putFile(t, s3client, bucket, filename, content)

	src := fmt.Sprintf("%s://%v/%v", tc.storage, bucket, filename)
	dst := fmt.Sprintf("%s://%v/%v", tc.storage, bucket, dstfilename)

	cmd := s5cmd("cp", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %v %v`, src, dst),
	})

	// assert source object
	assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))

	// assert destination object
	assert.Assert(t, ensureS3Object(s3client, bucket, dstfilename, content))
}

// --json cp s3://bucket/object s3://bucket2/object

func TestCopySingleS3ObjectToS3JSON(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopySingleS3ObjectToS3JSON(t, &tc)
		})
	}
}

func runTestCopySingleS3ObjectToS3JSON(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	dstbucket := s3BucketFromTestName(t)
	createBucket(t, s3client, dstbucket)

	const (
		filename = "testfile1.txt"
		content  = "this is a file content"
	)

	putFile(t, s3client, bucket, filename, content)

	cmd := s5cmd("--json", "cp", tc.storage+"://"+bucket+"/"+filename, tc.storage+"://"+dstbucket+"/"+filename)
	result := icmd.RunCmd(cmd)
	result.Assert(t, icmd.Success)

	jsonText := ` { "operation": "cp", "success": true, "source": "%s://%v/testfile1.txt", "destination": "%s://%v/testfile1.txt", "object": { "type": "file", "size": 22 } } `
	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: json(jsonText, tc.storage, bucket, tc.storage, dstbucket),
	}, jsonCheck(true))

	// assert source object
	assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))

	// assert destination object
	assert.Assert(t, ensureS3Object(s3client, dstbucket, filename, content))
}

// cp s3://bucket/object s3://bucket2/

func TestCopySingleS3ObjectIntoAnotherBucketWithPrefix(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopySingleS3ObjectIntoAnotherBucketWithPrefix(t, &tc)
		})
	}
}

func runTestCopySingleS3ObjectIntoAnotherBucketWithPrefix(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	srcbucket := s3BucketFromTestNameWithPrefix(t, "src")
	dstbucket := s3BucketFromTestNameWithPrefix(t, "dst")

	createBucket(t, s3client, srcbucket)
	createBucket(t, s3client, dstbucket)

	const (
		filename = "testfile1.txt"
		content  = "this is a file content"
		prefix   = "prefix/"
	)

	putFile(t, s3client, srcbucket, filename, content)

	src := fmt.Sprintf("%s://%v/%v", tc.storage, srcbucket, filename)
	dst := fmt.Sprintf("%s://%v/%v", tc.storage, dstbucket, prefix)

	cmd := s5cmd("cp", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %v %v%v`, src, dst, filename),
	})

	// assert source object
	assert.Assert(t, ensureS3Object(s3client, srcbucket, filename, content))

	// assert destination object
	assert.Assert(t, ensureS3Object(s3client, dstbucket, prefix+filename, content))
}

func runCopySingleS3ObjectIntoAnotherBucketWithPrefix(t *testing.T, tc *testCase) {
	t.Parallel()

	srcbucket := s3BucketFromTestNameWithPrefix(t, "src")
	dstbucket := s3BucketFromTestNameWithPrefix(t, "dst")

	s3client, s5cmd := setup(t)

	createBucket(t, s3client, srcbucket)
	createBucket(t, s3client, dstbucket)

	const (
		filename = "testfile1.txt"
		content  = "this is a file content"
		prefix   = "prefix/"
	)

	putFile(t, s3client, srcbucket, filename, content)

	src := fmt.Sprintf("%s://%v/%v", tc.storage, srcbucket, filename)
	dst := fmt.Sprintf("%s://%v/%v", tc.storage, dstbucket, prefix)

	cmd := s5cmd("cp", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %v %v%v`, src, dst, filename),
	})

	// assert s3 source object
	assert.Assert(t, ensureS3Object(s3client, srcbucket, filename, content))

	// assert s3 destination object
	assert.Assert(t, ensureS3Object(s3client, dstbucket, prefix+filename, content))
}

// cp --flatten s3://bucket/object s3://bucket2/

func TestFlattenCopySingleS3ObjectIntoAnotherBucket(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runFlattenCopySingleObjectIntoAnotherBucket(t, &tc)
		})
	}
}

func runFlattenCopySingleObjectIntoAnotherBucket(t *testing.T, tc *testCase) {
	t.Parallel()

	srcbucket := s3BucketFromTestNameWithPrefix(t, "src")
	dstbucket := s3BucketFromTestNameWithPrefix(t, "dst")

	s3client, s5cmd := setup(t)

	createBucket(t, s3client, srcbucket)
	createBucket(t, s3client, dstbucket)

	const (
		filename1 = "testfile1.txt"
		filename2 = "nested/nested/testfile2.txt"
		content   = "this is a file content"
	)

	putFile(t, s3client, srcbucket, filename1, content)
	putFile(t, s3client, srcbucket, filename2, content)

	src := fmt.Sprintf("%s://%v/", tc.storage, srcbucket)
	dst := fmt.Sprintf("%s://%v/", tc.storage, dstbucket)

	cmd := s5cmd("cp", "--flatten", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %v%v %v%v`, src, filename1, dst, filename1),
		1: equals(`cp %v%v %v%v`, src, filename2, dst, "testfile2.txt"),
	}, sortInput(true))

	// assert s3 source objects
	assert.Assert(t, ensureS3Object(s3client, srcbucket, filename1, content))
	assert.Assert(t, ensureS3Object(s3client, srcbucket, filename2, content))

	// assert s3 destination objects
	assert.Assert(t, ensureS3Object(s3client, dstbucket, filename1, content))
	assert.Assert(t, ensureS3Object(s3client, dstbucket, "testfile2.txt", content))
	// TODO(rr): check
	// assert.Assert(t, notS3Object(s3client, dstbucket, filename2))
}

// cp s3://bucket/object s3://bucket2/object

func TestCopySingleS3ObjectIntoAnotherBucketWithObjName(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopySingleS3ObjectIntoAnotherBucketWithObjName(t, &tc)
		})
	}
}

func runTestCopySingleS3ObjectIntoAnotherBucketWithObjName(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	srcbucket := s3BucketFromTestNameWithPrefix(t, "src")
	dstbucket := s3BucketFromTestNameWithPrefix(t, "dst")

	createBucket(t, s3client, srcbucket)
	createBucket(t, s3client, dstbucket)

	const (
		filename   = "testfile1.txt"
		dstObjName = "dstObj.txt"
		content    = "this is a file content"
	)

	putFile(t, s3client, srcbucket, filename, content)

	src := fmt.Sprintf("%s://%v/%v", tc.storage, srcbucket, filename)
	dst := fmt.Sprintf("%s://%v/%v", tc.storage, dstbucket, dstObjName)

	cmd := s5cmd("cp", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %v %v`, src, dst),
	})

	// assert source object
	assert.Assert(t, ensureS3Object(s3client, srcbucket, filename, content))

	// assert destination object
	assert.Assert(t, ensureS3Object(s3client, dstbucket, dstObjName, content))
}

// cp s3://bucket/object s3://bucket2/prefix/

// cp s3://bucket/* s3://dstbucket/

func TestCopyAllObjectsIntoAnotherBucketIncludingSpecialCharacter(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCopyAllObjectsIntoAnotherBucketIncludingSpecialCharacter(t, &tc)
		})
	}
}

func runCopyAllObjectsIntoAnotherBucketIncludingSpecialCharacter(t *testing.T, tc *testCase) {
	t.Parallel()

	srcbucket := s3BucketFromTestNameWithPrefix(t, "src")
	dstbucket := s3BucketFromTestNameWithPrefix(t, "dst")

	s3client, s5cmd := setup(t)

	createBucket(t, s3client, srcbucket)
	createBucket(t, s3client, dstbucket)

	filenames := []string{
		"testfile1.txt",
		"file with space.txt",
		"中文",
		"$dollarSign",
		"file with quotes'\"",
		"`backticks`.txt`",
		"(parenthesis){braces}[brackets]",
	}

	for _, filename := range filenames {
		putFile(t, s3client, srcbucket, filename, filename)
	}

	src := fmt.Sprintf("%s://%v/*", tc.storage, srcbucket)
	dst := fmt.Sprintf("%s://%v/", tc.storage, dstbucket)

	cmd := s5cmd("cp", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %v%v %v%v`, src, "testfile1.txt", dst, "testfile1.txt"),
		1: equals(`cp %v%v %v%v`, src, "file with space.txt", dst, "file with space.txt"),
		2: equals(`cp %v%v %v%v`, src, "中文", dst, "中文"),
		3: equals(`cp %v%v %v%v`, src, "$dollarSign", dst, "$dollarSign"),
		4: equals(`cp %v%v %v%v`, src, "file with quotes'\"", dst, "file with quotes'\""),
		5: equals(`cp %v%v %v%v`, src, "`backticks`.txt`", dst, "`backticks`.txt`"),
		6: equals(`cp %v%v %v%v`, src, "(parenthesis){braces}[brackets]", dst, "(parenthesis){braces}[brackets]"),
	}, sortInput(true))

	for _, filename := range filenames {
		content := filename
		// assert s3 source objects
		assert.Assert(t, ensureS3Object(s3client, srcbucket, filename, content))
		// assert s3 destination objects
		assert.Assert(t, ensureS3Object(s3client, dstbucket, filename, content))
	}
}

// cp s3://bucket/* s3://bucket/prefix/

func TestCopyMultipleS3ObjectsToS3WithPrefix(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyMultipleS3ObjectsToS3WithPrefix(t, &tc)
		})
	}
}

func runTestCopyMultipleS3ObjectsToS3WithPrefix(t *testing.T, tc *testCase) {
	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	filesToContent := map[string]string{
		"testfile1.txt":            "this is a test file 1",
		"readme.md":                "this is a readme file",
		"b/filename-with-hypen.gz": "file has hypen in its name",
		"a/another_test_file.txt":  "yet another txt file. yatf.",
	}

	for filename, content := range filesToContent {
		putFile(t, s3client, bucket, filename, content)
	}

	src := fmt.Sprintf("%s://%v/*", tc.storage, bucket)
	dst := fmt.Sprintf("%s://%v/dst", tc.storage, bucket)

	cmd := s5cmd("cp", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %v/a/another_test_file.txt %v/a/another_test_file.txt`, src, dst),
		1: equals(`cp %v/b/filename-with-hypen.gz %v/b/filename-with-hypen.gz`, src, dst),
		2: equals(`cp %v/readme.md %v/readme.md`, src, dst),
		3: equals(`cp %v/testfile1.txt %v/testfile1.txt`, src, dst),
	}, sortInput(true))

	// assert s3 source objects
	for filename, content := range filesToContent {
		assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
	}

	// assert s3 destination objects
	for filename, content := range filesToContent {
		assert.Assert(t, ensureS3Object(s3client, bucket, "dst/"+filename, content))
	}
}

// cp --flatten s3://bucket/* s3://bucket/prefix/

func TestFlattenCopyMultipleS3ObjectsToS3WithPrefix(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestFlattenCopyMultipleS3ObjectsToS3WithPrefix(t, &tc)
		})
	}
}

func runTestFlattenCopyMultipleS3ObjectsToS3WithPrefix(t *testing.T, tc *testCase) {
	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	filesToContent := map[string]string{
		"testfile1.txt":            "this is a test file 1",
		"readme.md":                "this is a readme file",
		"b/filename-with-hypen.gz": "file has hypen in its name",
		"a/another_test_file.txt":  "yet another txt file. yatf.",
	}

	for filename, content := range filesToContent {
		putFile(t, s3client, bucket, filename, content)
	}

	src := fmt.Sprintf("%s://%v/*", tc.storage, bucket)
	dst := fmt.Sprintf("%s://%v/dst/", tc.storage, bucket)

	cmd := s5cmd("cp", "--flatten", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %v/a/another_test_file.txt %vanother_test_file.txt`, src, dst),
		1: equals(`cp %v/b/filename-with-hypen.gz %vfilename-with-hypen.gz`, src, dst),
		2: equals(`cp %v/readme.md %vreadme.md`, src, dst),
		3: equals(`cp %v/testfile1.txt %vtestfile1.txt`, src, dst),
	}, sortInput(true))

	// assert s3 source objects
	for filename, content := range filesToContent {
		assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
	}

	// assert flattened s3 destination objects
	assert.Assert(t, ensureS3Object(s3client, bucket, "dst/another_test_file.txt", filesToContent["a/another_test_file.txt"]))
	assert.Assert(t, ensureS3Object(s3client, bucket, "dst/filename-with-hypen.gz", filesToContent["b/filename-with-hypen.gz"]))
	assert.Assert(t, ensureS3Object(s3client, bucket, "dst/readme.md", filesToContent["readme.md"]))
	assert.Assert(t, ensureS3Object(s3client, bucket, "dst/testfile1.txt", filesToContent["testfile1.txt"]))
}

// cp s3://bucket/* s3://bucket/prefix

func TestCopyMultipleS3ObjectsToS3WithPrefixWithoutSlash(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s3client, s5cmd := setup(t)

			bucket := s3BucketFromTestName(t)
			createBucket(t, s3client, bucket)

			filesToContent := map[string]string{
				"testfile1.txt":            "this is a test file 1",
				"readme.md":                "this is a readme file",
				"b/filename-with-hypen.gz": "file has hypen in its name",
				"a/another_test_file.txt":  "yet another txt file. yatf.",
			}

			for filename, content := range filesToContent {
				putFile(t, s3client, bucket, filename, content)
			}

			src := fmt.Sprintf("%s://%v/*", tc.storage, bucket)
			dst := fmt.Sprintf("%s://%v/dst", tc.storage, bucket)

			cmd := s5cmd("cp", src, dst)
			result := icmd.RunCmd(cmd)

			result.Assert(t, icmd.Success)

			// expect a failure for copying objects without trailing slash
			assertLines(t, result.Stderr(), map[int]compareFunc{
				0: contains(`"/dst" is not a directory`),
			})

			// assert s3 source objects
			for filename, content := range filesToContent {
				assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
			}
		})
	}
}

// --json cp s3://bucket/* s3://bucket/prefix/

func TestCopyMultipleS3ObjectsToS3JSON(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyMultipleS3ObjectsToS3JSON(t, &tc)
		})
	}
}

func runTestCopyMultipleS3ObjectsToS3JSON(t *testing.T, tc *testCase) {
	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	filesToContent := map[string]string{
		"testfile1.txt":            "this is a test file 1",
		"readme.md":                "this is a readme file",
		"b/filename-with-hypen.gz": "file has hypen in its name",
		"a/another_test_file.txt":  "yet another txt file. yatf.",
	}

	for filename, content := range filesToContent {
		putFile(t, s3client, bucket, filename, content)
	}

	src := fmt.Sprintf("%s://%v/*", tc.storage, bucket)
	dst := fmt.Sprintf("%s://%v/dst/", tc.storage, bucket)

	cmd := s5cmd("--json", "cp", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	// assert s3 source objects
	for filename, content := range filesToContent {
		assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
	}

	// assert s3 destination objects
	dstobj := "dst/"
	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: json(`{"operation":"cp","success":true,"source":"%v/a/another_test_file.txt","destination":"%v/a/another_test_file.txt","object":{"type":"file","size":%d}}`, src, dstobj+bucket, len(filesToContent["a/another_test_file.txt"])),
		1: json(`{"operation":"cp","success":true,"source":"%v/b/filename-with-hypen.gz","destination":"%v/b/filename-with-hypen.gz","object":{"type":"file","size":%d}}`, src, dstobj+bucket, len(filesToContent["b/filename-with-hypen.gz"])),
		2: json(`{"operation":"cp","success":true,"source":"%v/readme.md","destination":"%v/readme.md","object":{"type":"file","size":%d}}`, src, dstobj+bucket, len(filesToContent["readme.md"])),
		3: json(`{"operation":"cp","success":true,"source":"%v/testfile1.txt","destination":"%v/testfile1.txt","object":{"type":"file","size":%d}}`, src, dstobj+bucket, len(filesToContent["testfile1.txt"])),
	}, jsonCheck(true))

	for filename, content := range filesToContent {
		assert.Assert(t, ensureS3Object(s3client, bucket, "dst/"+filename, content))
	}
}

// cp -u -s s3://bucket/prefix/* s3://bucket/prefix2/

func TestCopyMultipleS3ObjectsToS3_Issue70(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCopyMultipleS3ObjectsToS3_Issue70(t, &tc)
		})
	}
}

func runCopyMultipleS3ObjectsToS3_Issue70(t *testing.T, tc *testCase) {
	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	filesToContent := map[string]string{
		"file1.txt": "this is a test file 1",
		"file2.txt": "this is a test file 2",
		"file3.txt": "this is a test file 3",
	}

	for filename, content := range filesToContent {
		putFile(t, s3client, bucket, "obj/"+filename, content)
	}

	t.Run("copy without -s", func(t *testing.T) {
		src := fmt.Sprintf("%s://%s/obj/*", tc.storage, bucket)
		dst := fmt.Sprintf("%s://%s/dst/", tc.storage, bucket)

		cmd := s5cmd("-u", "cp", src, dst)
		result := icmd.RunCmd(cmd)

		result.Assert(t, icmd.Success)

		assertLines(t, result.Stderr(), map[int]compareFunc{
			0: equals(""),
		})

		// assert s3 source objects
		for filename, content := range filesToContent {
			assert.Assert(t, ensureS3Object(s3client, bucket, "obj/"+filename, content))
		}

		// assert s3 destination objects
		for filename, content := range filesToContent {
			assert.Assert(t, ensureS3Object(s3client, bucket, "dst/"+filename, content))
		}
	})

	t.Run("copy with -s", func(t *testing.T) {
		src := fmt.Sprintf("%s://%s/obj/*", tc.storage, bucket)
		dst := fmt.Sprintf("%s://%s/dst2/", tc.storage, bucket)

		cmd := s5cmd("-u", "-s", "cp", src, dst)
		result := icmd.RunCmd(cmd)

		result.Assert(t, icmd.Success)

		assertLines(t, result.Stderr(), map[int]compareFunc{
			0: equals(""),
		})

		// assert s3 source objects
		for filename, content := range filesToContent {
			assert.Assert(t, ensureS3Object(s3client, bucket, "obj/"+filename, content))
		}

		// assert s3 destination objects
		assert.Assert(t, ensureS3Object(s3client, bucket, "dst2/file1.txt", filesToContent["file1.txt"]))
		assert.Assert(t, ensureS3Object(s3client, bucket, "dst2/file2.txt", filesToContent["file2.txt"]))
		assert.Assert(t, ensureS3Object(s3client, bucket, "dst2/file3.txt", filesToContent["file3.txt"]))
	})
}

// cp s3://bucket/object dir/ (dirobject exists)

func TestCopyS3ObjectToLocalWithTheSameFilename(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s3client, s5cmd := setup(t)
			bucket := s3BucketFromTestName(t)
			createBucket(t, s3client, bucket)

			const (
				filename = "testfile1.txt"
				content  = "this is a file content"
			)

			workdir := fs.NewDir(t, t.Name(), fs.WithDir(filename, fs.WithMode(0550)))
			defer workdir.Remove()

			putFile(t, s3client, bucket, filename, content)

			cmd := s5cmd("cp", tc.storage+"://"+bucket+"/"+filename, filename+"/")
			result := icmd.RunCmd(cmd, withWorkingDir(workdir))
			result.Assert(t, icmd.Expected{ExitCode: 1})

			assertLines(t, result.Stderr(), map[int]compareFunc{
				0: equals(`cp command failed: stat s3://test-copy-s-3-object-to-local-with-the-same-filename/testfile1.txt/testfile1.txt: not a directory`),
			})

			// assert local filesystem
			expected := fs.Expected(t, fs.WithDir(filename, fs.WithMode(0550)))
			assert.Assert(t, fs.Equal(workdir.Path(), expected))

			// assert s3 object
			assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
		})
	}
}

// -log=debug cp -n s3://bucket/object .

func TestCopyS3ToLocalWithSameFilenameWithNoClobber(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyS3ToLocalWithSameFilenameWithNoClobber(t, &tc)
		})
	}
}

func runTestCopyS3ToLocalWithSameFilenameWithNoClobber(t *testing.T, tc *testCase) {
	s3client, s5cmd := setup(t)
	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const (
		filename = "testfile1.txt"
		content  = "this is a file content"
	)

	workdir := fs.NewDir(t, t.Name(), fs.WithDir(filename, fs.WithMode(0550)))
	defer workdir.Remove()

	putFile(t, s3client, bucket, filename, content)

	cmd := s5cmd("-log=debug", "cp", "-n", tc.storage+"://"+bucket+"/"+filename, ".")
	result := icmd.RunCmd(cmd, withWorkingDir(workdir))
	result.Assert(t, icmd.Expected{ExitCode: 1})

	assertLines(t, result.Stderr(), map[int]compareFunc{
		0: contains(`DEBUG "cp" "-n" "` + tc.storage + `://` + bucket + `/testfile1.txt" "."`),
		1: equals("cp command failed: object testfile1.txt already exists; use -f to overwrite"),
	}, strictLineCheck(false))

	// assert local filesystem
	expected := fs.Expected(t, fs.WithDir(filename, fs.WithMode(0550)))
	assert.Assert(t, fs.Equal(workdir.Path(), expected))

	// assert s3 object
	assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
}

// cp -n -s s3://bucket/object dir/

func TestCopyS3ToLocalWithSameFilenameOverrideIfSizeDiffers(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyS3ToLocalWithSameFilenameOverrideIfSizeDiffers(t, &tc)
		})
	}
}

func runTestCopyS3ToLocalWithSameFilenameOverrideIfSizeDiffers(t *testing.T, tc *testCase) {
	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const filename = "testfile1.txt"

	workdir := fs.NewDir(t, t.Name(), fs.WithFile(filename, "othercontent"))
	defer workdir.Remove()

	const content = "this is a file content"
	putFile(t, s3client, bucket, filename, content)

	cmd := s5cmd("cp", "-n", "-s", tc.storage+"://"+bucket+"/"+filename, filename+"/")
	result := icmd.RunCmd(cmd, withWorkingDir(workdir))
	result.Assert(t, icmd.Success)

	assertLines(t, result.Stderr(), map[int]compareFunc{
		0: equals(""),
	})

	// assert local filesystem
	expected := fs.Expected(t, fs.WithFile(filename, content))
	assert.Assert(t, fs.Equal(workdir.Path(), expected))

	// assert s3 object
	assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
}

// cp -n -u s3://bucket/object dir/ (source is newer)
func TestCopyS3ToLocalWithSameFilenameOverrideIfSourceIsNewer(t *testing.T) {
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyS3ToLocalWithSameFilenameOverrideIfSourceIsNewer(t, &tc)
		})
	}
}

func runTestCopyS3ToLocalWithSameFilenameOverrideIfSourceIsNewer(t *testing.T, tc *testCase) {
	t.Parallel()

	bucket := s3BucketFromTestName(t)
	s3client, s5cmd := setup(t)

	const (
		filename        = "testfile1.txt"
		content         = "this is the content"
		expectedContent = content + "\n"
	)

	now := time.Now().UTC()
	timestamp := fs.WithTimestamps(
		now.Add(-time.Minute), // access time
		now.Add(-time.Minute), // mod time
	)
	workdir := fs.NewDir(t, t.Name(), fs.WithFile(filename, content, timestamp))
	defer workdir.Remove()

	createBucket(t, s3client, bucket)
	// upload a modified version of the file. also uploaded file is newer than
	// the file on local fs.
	putFile(t, s3client, bucket, filename, expectedContent)

	cmd := s5cmd("cp", "-n", "-u", tc.storage+"://"+bucket+"/"+filename, ".")
	result := icmd.RunCmd(cmd, withWorkingDir(workdir))

	// '-n' prevents overriding the file, but '-s' overrides '-n' if the file
	// size differs.
	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp `+tc.storage+`://%v/%v %v`, bucket, filename, filename),
	})

	expected := fs.Expected(t, fs.WithFile(filename, expectedContent))
	assert.Assert(t, fs.Equal(workdir.Path(), expected))
}

// cp -n -u s3://bucket/object dir/ (source is older)
func TestCopyS3ToLocalWithSameFilenameDontOverrideIfS3ObjectIsOlder(t *testing.T) {
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyS3ToLocalWithSameFilenameDontOverrideIfS3ObjectIsOlder(t, &tc)
		})
	}
}

func runTestCopyS3ToLocalWithSameFilenameDontOverrideIfS3ObjectIsOlder(t *testing.T, tc *testCase) {
	t.Parallel()

	bucket := s3BucketFromTestName(t)
	s3client, s5cmd := setup(t)

	const (
		filename = "testfile1.txt"
		content  = "this is the content"
	)

	createBucket(t, s3client, bucket)
	// upload a modified version of the file.
	putFile(t, s3client, bucket, filename, content+"\n")

	// file on the fs is newer than the file on s3. expect an 'dont override'
	// behaviour.
	now := time.Now().UTC()
	timestamp := fs.WithTimestamps(
		now.Add(time.Minute), // access time
		now.Add(time.Minute), // mod time
	)
	workdir := fs.NewDir(t, t.Name(), fs.WithFile(filename, content, timestamp))
	defer workdir.Remove()

	cmd := s5cmd("--log=debug", "cp", "-n", "-u", tc.storage+"://"+bucket+"/"+filename, ".")
	result := icmd.RunCmd(cmd, withWorkingDir(workdir))

	// '-n' prevents overriding the file, but '-s' overrides '-n' if the file
	// size differs.
	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`DEBUG "cp %v://%v/%v %v": object is newer or same age`, tc.storage, bucket, filename, filename),
	})
	assertLines(t, result.Stderr(), map[int]compareFunc{})
	expected := fs.Expected(t, fs.WithFile(filename, content))
	assert.Assert(t, fs.Equal(workdir.Path(), expected))
}

// cp -u -s s3://bucket/prefix/* dir/

func TestCopyS3ToLocal_Issue70(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyS3ToLocal_Issue70(t, &tc)
		})
	}
}

func runTestCopyS3ToLocal_Issue70(t *testing.T, tc *testCase) {
	s3client, s5cmd := setup(t)
	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	workdir := fs.NewDir(t, t.Name())
	defer workdir.Remove()

	filesToContent := map[string]string{
		"testfile1.txt": "content1",
		"testfile2.txt": "content2",
		"testfile3.txt": "content3",
	}

	for filename, content := range filesToContent {
		putFile(t, s3client, bucket, filename, content)
	}

	cmd := s5cmd("cp", "-n", "-u", "-s", tc.storage+"://"+bucket+"/*", ".")
	result := icmd.RunCmd(cmd, withWorkingDir(workdir))
	result.Assert(t, icmd.Success)

	assertLines(t, result.Stderr(), map[int]compareFunc{
		0: equals(""),
	})

	expected := fs.Expected(t, fs.WithFile("testfile1.txt", "content1"),
		fs.WithFile("testfile2.txt", "content2"),
		fs.WithFile("testfile3.txt", "content3"))
	assert.Assert(t, fs.Equal(workdir.Path(), expected))

	for filename, content := range filesToContent {
		assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
	}
}

// cp file s3://bucket (bucket/file exists)

func TestCopyLocalFileToS3WithTheSameFilename(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyLocalFileToS3WithTheSameFilename(t, &tc)
		})
	}
}

func runTestCopyLocalFileToS3WithTheSameFilename(t *testing.T, tc *testCase) {
	s3client, s5cmd := setup(t)
	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const filename = "testfile1.txt"
	const content = "this is a file content"

	putFile(t, s3client, bucket, filename, content)

	// Local copy of the file with same name but different content
	dir := t.TempDir()
	localFilename := filepath.Join(dir, filename)
	localContent := "this is the local content"
	err := ioutil.WriteFile(localFilename, []byte(localContent), 0644)
	assert.NilError(t, err)

	cmd := s5cmd("cp", localFilename, tc.storage+"://"+bucket)
	result := icmd.RunCmd(cmd)
	result.Assert(t, icmd.Success)

	assertLines(t, result.Stderr(), map[int]compareFunc{
		0: equals(""),
	})

	// assert s3 object
	assert.Assert(t, ensureS3Object(s3client, bucket, filename, localContent))
}

// -log=debug cp -n file s3://bucket (bucket/file exists)

func TestCopyLocalFileToS3WithSameFilenameWithNoClobber(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyLocalFileToS3WithSameFilenameWithNoClobber(t, &tc)
		})
	}
}

func runTestCopyLocalFileToS3WithSameFilenameWithNoClobber(t *testing.T, tc *testCase) {
	s3client, s5cmd := setup(t)
	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const filename = "testfile1.txt"
	const content = "this is a file content"

	putFile(t, s3client, bucket, filename, content)

	// Local copy of the file with same name but different content
	dir := t.TempDir()
	localFilename := filepath.Join(dir, filename)
	localContent := "this is the local content"
	err := ioutil.WriteFile(localFilename, []byte(localContent), 0644)
	assert.NilError(t, err)

	cmd := s5cmd("-log=debug", "cp", "-n", localFilename, tc.storage+"://"+bucket)
	result := icmd.RunCmd(cmd)
	result.Assert(t, icmd.Success)

	assertLines(t, result.Stderr(), map[int]compareFunc{
		0: contains(`"%v/%v" not overwritten`, bucket, filename),
	})

	// assert s3 object not modified
	assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
}

// cp -n file s3://bucket

func TestCopyLocalFileToS3WithNoClobber(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyLocalFileToS3WithNoClobber(t, &tc)
		})
	}
}

func runTestCopyLocalFileToS3WithNoClobber(t *testing.T, tc *testCase) {
	s3client, s5cmd := setup(t)
	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const filename = "testfile1.txt"

	dir := t.TempDir()
	localFilename := filepath.Join(dir, filename)
	err := ioutil.WriteFile(localFilename, []byte("content"), 0644)
	assert.NilError(t, err)

	cmd := s5cmd("cp", "-n", localFilename, tc.storage+"://"+bucket)
	result := icmd.RunCmd(cmd)
	result.Assert(t, icmd.Success)

	assertLines(t, result.Stderr(), map[int]compareFunc{
		0: equals(""),
	})

	assert.Assert(t, ensureS3Object(s3client, bucket, filename, "content"))

	// Overwrite local file and try again with -n
	err = ioutil.WriteFile(localFilename, []byte("newcontent"), 0644)
	assert.NilError(t, err)

	cmd = s5cmd("cp", "-n", localFilename, tc.storage+"://"+bucket)
	result = icmd.RunCmd(cmd)
	result.Assert(t, icmd.Success)

	assertLines(t, result.Stderr(), map[int]compareFunc{
		0: equals(""),
	})

	// With -n, expect original content
	assert.Assert(t, ensureS3Object(s3client, bucket, filename, "content"))
}

// cp -n -s file s3://bucket (bucket/file exists)

func TestCopyLocalFileToS3WithSameFilenameOverrideIfSizeDiffers(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyLocalFileToS3WithSameFilenameOverrideIfSizeDiffers(t, &tc)
		})
	}
}

func runTestCopyLocalFileToS3WithSameFilenameOverrideIfSizeDiffers(t *testing.T, tc *testCase) {
	s3client, s5cmd := setup(t)
	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const filename = "testfile1.txt"
	const content = "this is a file content"

	putFile(t, s3client, bucket, filename, content)

	// Local copy of the file with same name but different size
	dir := t.TempDir()
	localFilename := filepath.Join(dir, filename)
	localContent := content + " with additional content"
	err := ioutil.WriteFile(localFilename, []byte(localContent), 0644)
	assert.NilError(t, err)

	cmd := s5cmd("cp", "-n", "-s", localFilename, tc.storage+"://"+bucket)
	result := icmd.RunCmd(cmd)
	result.Assert(t, icmd.Success)

	assertLines(t, result.Stderr(), map[int]compareFunc{
		0: equals(""),
	})

	// With -s, expect updated content
	assert.Assert(t, ensureS3Object(s3client, bucket, filename, localContent))
}

// cp -n -u file s3://bucket (bucket/file exists, source is newer)

func TestCopyLocalFileToS3WithSameFilenameOverrideIfSourceIsNewer(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyLocalFileToS3WithSameFilenameOverrideIfSourceIsNewer(t, &tc)
		})
	}
}

func runTestCopyLocalFileToS3WithSameFilenameOverrideIfSourceIsNewer(t *testing.T, tc *testCase) {
	s3client, s5cmd := setup(t)
	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const filename = "testfile1.txt"
	const content = "this is a file content"

	putFile(t, s3client, bucket, filename, content)

	// Local copy of the file with same name but newer
	dir := t.TempDir()
	localFilename := filepath.Join(dir, filename)
	err := ioutil.WriteFile(localFilename, []byte(content), 0644)
	assert.NilError(t, err)
	// ensure local file is newer
	futuretime := time.Now().Add(1 * time.Hour)
	assert.NilError(t, os.Chtimes(localFilename, futuretime, futuretime))

	cmd := s5cmd("cp", "-n", "-u", localFilename, tc.storage+"://"+bucket)
	result := icmd.RunCmd(cmd)
	result.Assert(t, icmd.Success)

	assertLines(t, result.Stderr(), map[int]compareFunc{
		0: equals(""),
	})

	// With -u, expect updated content
	assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
}

// cp -n -u file s3://bucket (bucket/file exists, source is older)
func TestCopyLocalFileToS3WithSameFilenameDontOverrideIfS3ObjectIsOlder(t *testing.T) {
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyLocalFileToS3WithSameFilenameDontOverrideIfS3ObjectIsOlder(t, &tc)
		})
	}
}

func runTestCopyLocalFileToS3WithSameFilenameDontOverrideIfS3ObjectIsOlder(t *testing.T, tc *testCase) {
	t.Parallel()

	bucket := s3BucketFromTestName(t)

	s3client, s5cmd := setup(t)

	const (
		filename        = "testfile1.txt"
		content         = "this is the content"
		expectedContent = content + "\n"
	)

	createBucket(t, s3client, bucket)
	// upload a modified version of the file. also uploaded file is newer than
	// the file on local fs.
	putFile(t, s3client, bucket, filename, content)

	now := time.Now().UTC()
	timestamp := fs.WithTimestamps(
		now.Add(-time.Minute), // access time
		now.Add(-time.Minute), // mod time
	)
	workdir := fs.NewDir(t, t.Name(), fs.WithFile(filename, expectedContent, timestamp))
	defer workdir.Remove()

	cmd := s5cmd("--log=debug", "cp", "-n", "-u", filename, tc.storage+"://"+bucket)
	result := icmd.RunCmd(cmd, withWorkingDir(workdir))

	// '-n' prevents overriding the file, but '-u' overrides '-n' if the file
	// modtime differs.
	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`DEBUG "cp %v %v://%v/%v": object is newer or same age`, tc.storage, filename, bucket, filename),
	})

	assertLines(t, result.Stderr(), map[int]compareFunc{})

	assert.NilError(t, ensureS3Object(s3client, bucket, filename, content))
}

// cp file s3://bucket/

func TestCopyLocalFileToS3WithFilePermissions(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyLocalFileToS3WithFilePermissions(t, &tc)
		})
	}
}

func runTestCopyLocalFileToS3WithFilePermissions(t *testing.T, tc *testCase) {
	s3client, s5cmd := setup(t)
	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const (
		filename           = "testfile1.txt"
		expectedPermission = "0664"
	)

	dir := t.TempDir()
	localFile := filepath.Join(dir, filename)
	err := ioutil.WriteFile(localFile, []byte("content"), 0664)
	assert.NilError(t, err)

	cmd := s5cmd("cp", localFile, tc.storage+"://"+bucket+"/")
	result := icmd.RunCmd(cmd)
	result.Assert(t, icmd.Success)

	cmd = s5cmd("ls", "-s", tc.storage+"://"+bucket+"/"+filename)
	result = icmd.RunCmd(cmd)
	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: suffix(expectedPermission + " content " + filename),
	})
}

// cp file s3://bucket/object

func TestCopyLocalFileToS3WithCustomName(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyLocalFileToS3WithCustomName(t, &tc)
		})
	}
}

func runTestCopyLocalFileToS3WithCustomName(t *testing.T, tc *testCase) {
	s3client, s5cmd := setup(t)
	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const (
		filename = "testfile1.txt"
		content  = "this is a file content"
		destname = "custom-name.txt"
	)

	dir := t.TempDir()
	localFile := filepath.Join(dir, filename)
	err := ioutil.WriteFile(localFile, []byte(content), 0644)
	assert.NilError(t, err)

	cmd := s5cmd("cp", localFile, tc.storage+"://"+bucket+"/"+destname)
	result := icmd.RunCmd(cmd)
	result.Assert(t, icmd.Success)

	assertLines(t, result.Stderr(), map[int]compareFunc{
		0: equals(""),
	})

	// assert object was copied with custom name
	assert.Assert(t, ensureS3Object(s3client, bucket, destname, content))
}

// cp file s3://bucket/prefix/

func TestCopyLocalFileToS3WithPrefix(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyLocalFileToS3WithPrefix(t, &tc)
		})
	}
}

func runTestCopyLocalFileToS3WithPrefix(t *testing.T, tc *testCase) {
	s3client, s5cmd := setup(t)
	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const (
		filename = "testfile1.txt"
		content  = "this is a file content"
		destname = "prefix/testfile1.txt"
	)

	dir := t.TempDir()
	localFile := filepath.Join(dir, filename)
	err := ioutil.WriteFile(localFile, []byte(content), 0644)
	assert.NilError(t, err)

	cmd := s5cmd("cp", localFile, tc.storage+"://"+bucket+"/prefix/")
	result := icmd.RunCmd(cmd)
	result.Assert(t, icmd.Success)

	assertLines(t, result.Stderr(), map[int]compareFunc{
		0: equals(""),
	})

	// assert object was copied with prefix+filename
	assert.Assert(t, ensureS3Object(s3client, bucket, destname, content))
}

// cp file s3://bucket

func TestMultipleLocalFileToS3Bucket(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestMultipleLocalFileToS3Bucket(t, &tc)
		})
	}
}

func runTestMultipleLocalFileToS3Bucket(t *testing.T, tc *testCase) {
	s3client, s5cmd := setup(t)
	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	filesToContent := map[string]string{
		"file1.txt": "file1 content",
		"file2.txt": "file2 content",
		"file3.txt": "file3 content",
	}

	filesToUpload := []string{}
	for file, content := range filesToContent {
		byt := []byte(content)
		err := ioutil.WriteFile(file, byt, 0644)
		assert.NilError(t, err)
		filesToUpload = append(filesToUpload, file)
	}

	cmd := s5cmd(append([]string{"cp"}, append(filesToUpload, tc.storage+"://"+bucket)...)...)
	result := icmd.RunCmd(cmd)
	result.Assert(t, icmd.Success)

	assertLines(t, result.Stderr(), map[int]compareFunc{
		0: equals(""),
	})

	// assert all uploaded objects
	for file, content := range filesToContent {
		assert.Assert(t, ensureS3Object(s3client, bucket, file, content))
	}
}

// cp * s3://bucket/prefix/
func TestCopyMultipleLocalNestedFilesToS3(t *testing.T) {
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyMultipleLocalNestedFilesToS3(t, &tc)
		})
	}
}

func runTestCopyMultipleLocalNestedFilesToS3(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	// nested folder layout
	//
	// ├─a
	// │ ├─readme.md
	// │ └─file1.txt
	// └─b
	//   └─c
	//     └─file2.txt
	//
	// after `s5cmd cp * s3://bucket/prefix/`, expect:
	//
	// prefix
	//  ├─a
	//  │ ├─readme.md
	//  │ └─file1.txt
	//  └─b
	//    └─c
	//      └─file2.txt

	folderLayout := []fs.PathOp{
		fs.WithDir(
			"a",
			fs.WithFile("file1.txt", "file1"),
			fs.WithFile("readme.md", "readme"),
		),
		fs.WithDir(
			"b",
			fs.WithDir(
				"c",
				fs.WithFile("file2.txt", "file2"),
			),
		),
	}

	workdir := fs.NewDir(t, t.Name(), folderLayout...)
	defer workdir.Remove()

	dst := fmt.Sprintf("%v://%v/prefix/", tc.storage, bucket)

	cmd := s5cmd("cp", "*", dst)
	result := icmd.RunCmd(cmd, withWorkingDir(workdir))

	result.Assert(t, icmd.Success)
	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals("cp a/file1.txt %va/file1.txt", dst),
		1: equals("cp a/readme.md %va/readme.md", dst),
		2: equals("cp b/c/file2.txt %vb/c/file2.txt", dst),
	}, sortInput(true))
	// assert local filesystem
	expected := fs.Expected(t, folderLayout...)
	assert.Assert(t, fs.Equal(workdir.Path(), expected))
	// assert s3 objects
	assert.Assert(t, ensureS3Object(s3client, bucket, "prefix/a/readme.md", "readme"))
	assert.Assert(t, ensureS3Object(s3client, bucket, "prefix/a/file1.txt", "file1"))
	assert.Assert(t, ensureS3Object(s3client, bucket, "prefix/b/c/file2.txt", "file2"))
}

// cp --no-follow-symlinks my_link s3://bucket/prefix/

func TestCopyLinkToASingleFileWithFollowSymlinkDisabled(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyLinkToASingleFileWithFollowSymlinkDisabled(t, &tc)
		})
	}
}

func runTestCopyLinkToASingleFileWithFollowSymlinkDisabled(t *testing.T, tc *testCase) {
	s3client, s5cmd := setup(t)
	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const (
		filename        = "file.txt"
		linkToFile      = "my_link"
		expectedContent = "this is a test file"
	)

	workdir := t.TempDir()

	filePath := filepath.Join(workdir, filename)
	err := ioutil.WriteFile(filePath, []byte(expectedContent), 0644)
	assert.NilError(t, err)

	linkPath := filepath.Join(workdir, linkToFile)
	err = os.Symlink(filePath, linkPath)
	assert.NilError(t, err)

	cmd := s5cmd("--no-follow-symlinks", "cp", linkPath, tc.storage+"://"+bucket+"/prefix/")
	result := icmd.RunCmd(cmd)
	result.Assert(t, icmd.Success)
}

// cp * s3://bucket/prefix/

func TestCopyWithFollowSymlink(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyWithFollowSymlink(t, &tc)
		})
	}
}

func runTestCopyWithFollowSymlink(t *testing.T, tc *testCase) {
	s3client, s5cmd := setup(t)
	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const (
		filename        = "file.txt"
		linkToFile      = "my_link"
		expectedContent = "this is a test file"
	)

	workdir := t.TempDir()

	filePath := filepath.Join(workdir, filename)
	err := ioutil.WriteFile(filePath, []byte(expectedContent), 0644)
	assert.NilError(t, err)

	linkPath := filepath.Join(workdir, linkToFile)
	err = os.Symlink(filePath, linkPath)
	assert.NilError(t, err)

	cmd := s5cmd("cp", workdir+"/", tc.storage+"://"+bucket+"/prefix/")
	result := icmd.RunCmd(cmd)
	result.Assert(t, icmd.Success)

	// assert link object was uploaded
	assert.Assert(t, ensureS3Object(s3client, bucket, "prefix/"+linkToFile, ""))

	// assert the original file was uploaded
	assert.Assert(t, ensureS3Object(s3client, bucket, "prefix/"+filename, expectedContent))
}

func TestCopyErrorWhenGivenObjectIsNotFoundUsingWildcard(t *testing.T) {
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyErrorWhenGivenObjectIsNotFoundUsingWildcard(t, &tc)
		})
	}
}

func runTestCopyErrorWhenGivenObjectIsNotFoundUsingWildcard(t *testing.T, tc *testCase) {
	_, s5cmd := setup(t)
	cmd := s5cmd("cp", tc.storage+"://bucket/*.txt", ".")
	result := icmd.RunCmd(cmd)
	result.Assert(t, icmd.Expected{ExitCode: 1})
	assertLines(t, result.Stderr(), map[int]compareFunc{
		0: equals(`ERROR "cp %v://bucket/*.txt ." object not found`, tc.storage),
	}, sortInput(true))
}

// cp --no-follow-symlinks * s3://bucket/prefix/

func TestCopyWithNoFollowSymlink(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyWithNoFollowSymlink(t, &tc)
		})
	}
}

func runTestCopyWithNoFollowSymlink(t *testing.T, tc *testCase) {
	s3client, s5cmd := setup(t)
	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const (
		filename   = "file.txt"
		linkToFile = "my_link"
	)

	workdir := t.TempDir()

	filePath := filepath.Join(workdir, filename)
	err := ioutil.WriteFile(filePath, []byte("file content"), 0644)
	assert.NilError(t, err)

	linkPath := filepath.Join(workdir, linkToFile)
	err = os.Symlink(filePath, linkPath)
	assert.NilError(t, err)

	cmd := s5cmd("--no-follow-symlinks", "cp", workdir+"/", tc.storage+"://"+bucket+"/prefix/")
	result := icmd.RunCmd(cmd)
	result.Assert(t, icmd.Success)

	// assert link object was uploaded
	assert.Assert(t, ensureS3Object(s3client, bucket, "prefix/"+linkToFile, ""))

	// assert the original file was uploaded
	assert.Assert(t, ensureS3Object(s3client, bucket, "prefix/"+filename, "file content"))
}

// --dry-run cp dir/ s3://bucket/
func TestCopyDirToS3DryRun(t *testing.T) {
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyDirToS3DryRun(t, &tc)
		})
	}
}

func runTestCopyDirToS3DryRun(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)
	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	folderLayout := []fs.PathOp{
		fs.WithFile("file1.txt", "content"),
		fs.WithDir(
			"c",
			fs.WithFile("file2.txt", "content"),
		),
	}

	workdir := fs.NewDir(t, t.Name(), folderLayout...)
	defer workdir.Remove()

	srcpath := filepath.ToSlash(workdir.Path())
	dstpath := fmt.Sprintf("%v://%v/", tc.storage, bucket)

	cmd := s5cmd("--dry-run", "cp", workdir.Path()+"/", dstpath)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %v/c/file2.txt %vc/file2.txt`, srcpath, dstpath),
		1: equals(`cp %v/file1.txt %vfile1.txt`, srcpath, dstpath),
	}, sortInput(true))

	// assert no change in s3
	objs := []string{"c/file2.txt", "file1.txt"}
	for _, obj := range objs {
		err := ensureS3Object(s3client, bucket, obj, "content")
		assertError(t, err, errS3NoSuchKey)
	}

	// assert local filesystem
	expected := fs.Expected(t, folderLayout...)
	assert.Assert(t, fs.Equal(workdir.Path(), expected))
}

// --dry-run cp s3://bucket/* dir/

func TestCopyS3ToDirDryRun(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyS3ToDirDryRun(t, &tc)
		})
	}
}

func runTestCopyS3ToDirDryRun(t *testing.T, tc *testCase) {
	s3client, s5cmd := setup(t)
	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const (
		filesCnt = 10
	)

	expectedFiles := make([]string, filesCnt)
	for i := 0; i < filesCnt; i++ {
		filename := fmt.Sprintf("file%d", i)
		putFile(t, s3client, bucket, filename, filename)
		expectedFiles[i] = filename
	}

	dir, err := os.MkdirTemp("", "")
	assert.NilError(t, err)
	defer os.RemoveAll(dir)

	cmd := s5cmd("--dry-run", "cp", tc.storage+"://"+bucket+"/*", dir+"/")
	result := icmd.RunCmd(cmd)
	result.Assert(t, icmd.Success)

	// expect a line of output for each file
	assert.Equal(t, len(result.Stdout()), filesCnt)

	// assert no files were copied
	dirEntries, err := os.ReadDir(dir)
	assert.NilError(t, err)
	assert.Equal(t, 0, len(dirEntries))
}

func TestCopyLocalObjectstoS3WithRawFlag(t *testing.T) {
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyLocalObjectstoS3WithRawFlag(t, &tc)
		})
	}
}

func runTestCopyLocalObjectstoS3WithRawFlag(t *testing.T, tcCsp *testCase) {
	t.Parallel()

	testcases := []struct {
		name             string
		src              []fs.PathOp
		wantedFile       string
		expectedFiles    []string
		nonExpectedFiles []string
		rawFlag          string
	}{
		{
			name: "cp --raw file*.txt " + tcCsp.storage + "://bucket/",
			src: []fs.PathOp{
				fs.WithFile("file*.txt", "content"),
				fs.WithFile("file*1.txt", "content"),
				fs.WithFile("file*file.txt", "content"),
				fs.WithFile("file*2.txt", "content"),
			},
			wantedFile:       "file*.txt",
			expectedFiles:    []string{"file*.txt"},
			nonExpectedFiles: []string{"file*1.txt", "file*file.txt", "file*2.txt"},
			rawFlag:          "--raw",
		},
		{
			name: "cp  file*.txt " + tcCsp.storage + "://bucket/",
			src: []fs.PathOp{
				fs.WithFile("file*.txt", "content"),
				fs.WithFile("file*1.txt", "content"),
				fs.WithFile("file*file.txt", "content"),
				fs.WithFile("file*2.txt", "content"),
			},
			wantedFile:       "file*.txt",
			expectedFiles:    []string{"file*.txt", "file*1.txt", "file*file.txt", "file*2.txt"},
			nonExpectedFiles: []string{},
			rawFlag:          "",
		},
		{
			name: "cp  a*/file*.txt " + tcCsp.storage + "://bucket/",
			src: []fs.PathOp{
				fs.WithDir(
					"a*",
					fs.WithFile("file*.txt", "content"),
					fs.WithFile("file*1.txt", "content"),
				),
				fs.WithDir(
					"a*b",
					fs.WithFile("file*2.txt", "content"),
					fs.WithFile("file*3.txt", "content"),
				),

				fs.WithFile("file4.txt", "content"),
			},
			wantedFile:       "a*/file*.txt",
			expectedFiles:    []string{"file*.txt"}, // when full path entered, the base part is uploaded.
			nonExpectedFiles: []string{"a*/file*.txt", "a*/file*1.txt", "a*b/file*2.txt", "a*/file*3.txt", "file*4.txt", "file*1.txt", "file*2.txt", "file*3.txt"},
			rawFlag:          "--raw",
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			bucket := s3BucketFromTestName(t)

			s3client, s5cmd := setup(t)

			createBucket(t, s3client, bucket)

			workdir := fs.NewDir(t, "copy-raw-test", tc.src...)
			defer workdir.Remove()

			srcpath := filepath.ToSlash(workdir.Join(tc.wantedFile))
			dst := fmt.Sprintf(tcCsp.storage+"://%v", bucket)

			cmd := s5cmd("cp", srcpath, dst)
			if tc.rawFlag != "" {
				cmd = s5cmd("cp", tc.rawFlag, srcpath, dst)
			}

			result := icmd.RunCmd(cmd)
			result.Assert(t, icmd.Success)

			for _, obj := range tc.expectedFiles {
				err := ensureS3Object(s3client, bucket, obj, "content")
				if err != nil {
					t.Fatalf("%s is not exist in s3\n", obj)
				}
			}

			for _, obj := range tc.nonExpectedFiles {
				err := ensureS3Object(s3client, bucket, obj, "content")
				assertError(t, err, errS3NoSuchKey)
			}

			// assert filesystem
			expected := fs.Expected(t, tc.src...)
			assert.Assert(t, fs.Equal(workdir.Path(), expected))
		})
	}
}

// When folder is uploaded with --raw flag, it only uploads file with given name.
func TestCopyS3ObjectstoLocalWithRawFlag(t *testing.T) {
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyS3ObjectstoLocalWithRawFlag(t, &tc)
		})
	}
}

func runTestCopyS3ObjectstoLocalWithRawFlag(t *testing.T, tcCsp *testCase) {
	t.Parallel()
	const fileContent = "this is a file content"
	testcases := []struct {
		name           string
		src            []string
		wantedFile     string
		expectedOutput string
		expectedFiles  []fs.PathOp
		rawFlag        string
	}{
		{
			name:           "cp --raw file*.txt " + tcCsp.storage + "://bucket/",
			src:            []string{"file*.txt", "file*1.txt", "file*2.txt"},
			wantedFile:     "file*.txt",
			expectedOutput: "cp s3://bucket/file*.txt file*txt",
			rawFlag:        "--raw",
			expectedFiles: []fs.PathOp{
				fs.WithFile("file*.txt", fileContent),
			},
		},
		{
			name:       "cp  file*.txt " + tcCsp.storage + "://bucket/",
			src:        []string{"file*.txt", "file*1.txt", "file*2.txt"},
			wantedFile: "file*.txt",
			rawFlag:    "",
			expectedFiles: []fs.PathOp{
				fs.WithFile("file*.txt", fileContent),
				fs.WithFile("file*1.txt", fileContent),
				fs.WithFile("file*2.txt", fileContent),
			},
		},
		{
			name:       "cp  a*/file.txt " + tcCsp.storage + "://bucket/",
			src:        []string{"a*/file*.txt", "a*b/file1.txt", "a*c/file2.txt"},
			wantedFile: "a*/file*.txt",
			rawFlag:    "--raw",
			expectedFiles: []fs.PathOp{
				fs.WithFile("file*.txt", fileContent),
			},
		},
		{
			name:       "cp  a*/file.txt " + tcCsp.storage + "://bucket/",
			src:        []string{"a*/file.txt", "a*/file1.txt", "a*/file2.txt"},
			wantedFile: "a*/file.txt",
			rawFlag:    "",
			expectedFiles: []fs.PathOp{
				fs.WithDir(
					"a*",
					fs.WithFile("file.txt", fileContent),
				),
			},
		},
	}

	for _, tc := range testcases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			bucket := s3BucketFromTestName(t)

			s3client, s5cmd := setup(t)

			createBucket(t, s3client, bucket)

			for _, filename := range tc.src {
				putFile(t, s3client, bucket, filename, fileContent)

			}

			cmd := s5cmd("cp", tcCsp.storage+"://"+bucket+"/"+tc.wantedFile, ".")
			if tc.rawFlag != "" {
				cmd = s5cmd("cp", "--raw", tcCsp.storage+"://"+bucket+"/"+tc.wantedFile, ".")
			}

			result := icmd.RunCmd(cmd)

			result.Assert(t, icmd.Success)

			// assert local file system
			expected := fs.Expected(t, tc.expectedFiles...)
			assert.Assert(t, fs.Equal(cmd.Dir, expected))

			// assert s3 object
			for _, filename := range tc.src {
				assert.Assert(t, ensureS3Object(s3client, bucket, filename, fileContent))
			}
		})
	}
}

func TestCopyMultipleS3ObjectsToS3WithRawMode(t *testing.T) {
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rawTestCopyMultipleS3ObjectsToS3WithRawMode(t, &tc)
		})
	}
}

func rawTestCopyMultipleS3ObjectsToS3WithRawMode(t *testing.T, tcCsp *testCase) {
	t.Parallel()

	srcbucket := s3BucketFromTestNameWithPrefix(t, "src")
	dstbucket := s3BucketFromTestNameWithPrefix(t, "dst")

	s3client, s5cmd := setup(t)

	createBucket(t, s3client, srcbucket)
	createBucket(t, s3client, dstbucket)

	filesToContent := map[string]string{
		"file*.txt":      "this is a test file 1",
		"file*1.txt":     "this is a test file 2",
		"file*.py":       "this is a test python file",
		"file*/file.txt": "this is a test file with prefix",
	}

	for filename, content := range filesToContent {
		putFile(t, s3client, srcbucket, filename, content)
	}

	src := fmt.Sprintf(tcCsp.storage+"://%v/file*.txt", srcbucket)
	dst := fmt.Sprintf(tcCsp.storage+"://%v", dstbucket)

	cmd := s5cmd("cp", "--raw", src, dst)
	result := icmd.RunCmd(cmd)
	result.Assert(t, icmd.Success)
	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals("cp %v %v/file*.txt", src, dst),
	})

	// assert s3 source objects
	for filename, content := range filesToContent {
		assert.Assert(t, ensureS3Object(s3client, srcbucket, filename, content))
	}

	expectedFiles := map[string]string{
		"file*.txt": "this is a test file 1",
	}
	// assert s3 objects in destination.
	for filename, content := range expectedFiles {
		assert.Assert(t, ensureS3Object(s3client, dstbucket, filename, content))
	}
}

// cp --raw s3://srcbucket/file* s3://dstbucket
func TestCopyMultipleS3ObjectsWithPrefixToS3WithRawMode(t *testing.T) {
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyMultipleS3ObjectsWithPrefixToS3WithRawMode(t, &tc)
		})
	}
}

func runTestCopyMultipleS3ObjectsWithPrefixToS3WithRawMode(t *testing.T, tc *testCase) {
	t.Parallel()

	srcbucket := s3BucketFromTestNameWithPrefix(t, "src")
	dstbucket := s3BucketFromTestNameWithPrefix(t, "dst")

	s3client, s5cmd := setup(t)

	createBucket(t, s3client, srcbucket)
	createBucket(t, s3client, dstbucket)

	filesToContent := map[string]string{
		"file*/file.txt":   "this is a test file 1 in file*",
		"file*/file1.txt":  "this is a test file 2 in file*",
		"file*a/file.txt":  "this is a test file 1 in file*b",
		"file*a/file1.txt": "this is a test file 2 in file*b",
	}

	for filename, content := range filesToContent {
		putFile(t, s3client, srcbucket, filename, content)
	}

	src := fmt.Sprintf(tc.storage+"://%v/file*", srcbucket)
	dst := fmt.Sprintf(tc.storage+"://%v", dstbucket)

	cmd := s5cmd("cp", "--raw", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Expected{ExitCode: 1})

	expected := fmt.Sprintf(`ERROR "cp %v %v/file*": NoSuchKey:`, src, dst)

	assertLines(t, result.Stderr()[:len(expected)], map[int]compareFunc{
		0: equals(expected),
	})
}

// cp --raw s3://bucket/file* s3://destbucket
func TestCopyRawModeAllowDestinationWithoutPrefix(t *testing.T) {
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTestCopyRawModeAllowDestinationWithoutPrefix(t, &tc)
		})
	}
}

func runTestCopyRawModeAllowDestinationWithoutPrefix(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)
	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	filesToContent := map[string]string{
		"test*/file.txt": "this is a test file 1 in file*",
	}

	for filename, content := range filesToContent {
		putFile(t, s3client, bucket, filename, content)
	}

	folderLayout := []fs.PathOp{
		fs.WithFile("testfile.txt", "this is a test file 1"),
		fs.WithFile("readme.md", "this is a readme file"),
		fs.WithDir(
			"a",
			fs.WithFile("another_test_file.txt", "yet another txt file. yatf."),
		),
		fs.WithDir(
			"b",
			fs.WithFile("filename-with-hypen.gz", "file has hypen in its name"),
		),
	}

	workdir := fs.NewDir(t, "somedir", folderLayout...)
	defer workdir.Remove()

	src := fmt.Sprintf("%v/testfile.txt", workdir.Path())
	src = filepath.ToSlash(src)
	dst := fmt.Sprintf("%v://%s/test*/", tc.storage, bucket)

	cmd := s5cmd("cp", "--raw", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals("cp %v %vtestfile.txt", src, dst),
	})

	err := ensureS3Object(s3client, bucket, "test*/testfile.txt", "this is a test file 1")
	if err != nil {
		t.Errorf("testfile*.txt not exist in bucket %v\n", dst)
	}
}

// cp --exclude "*.py" s3://bucket/* .
func TestCopyS3ObjectsWithExcludeFilter(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.storage, func(t *testing.T) {
			t.Parallel()

			s3client, s5cmd := setup(t)

			bucket := s3BucketFromTestName(t)
			createBucket(t, s3client, bucket)

			const (
				excludePattern = "*.py"
				fileContent    = "content"
			)

			files := [...]string{
				"file1.txt",
				"file2.txt",
				"file.py",
				"a.py",
				"src/file.py",
			}

			for _, filename := range files {
				putFile(t, s3client, bucket, filename, fileContent)
			}

			srcpath := fmt.Sprintf("%s://%s", tc.storage, bucket)

			cmd := s5cmd("cp", "--exclude", excludePattern, srcpath+"/*", ".")
			result := icmd.RunCmd(cmd)

			result.Assert(t, icmd.Success)

			assertLines(t, result.Stdout(), map[int]compareFunc{
				0: equals("cp %v/file1.txt %s", srcpath, files[0]),
				1: equals("cp %v/file2.txt %s", srcpath, files[1]),
			}, sortInput(true))

			// assert s3
			for _, f := range files {
				assert.Assert(t, ensureS3Object(s3client, bucket, f, fileContent))
			}

			expectedFileSystem := []fs.PathOp{
				fs.WithFile("file1.txt", fileContent),
				fs.WithFile("file2.txt", fileContent),
			}
			// assert local filesystem
			expected := fs.Expected(t, expectedFileSystem...)
			assert.Assert(t, fs.Equal(cmd.Dir, expected))
		})
	}
}

// cp --exclude "*.py" --exclude "file*" s3://bucket/* .

func TestCopyS3ObjectsWithExcludeFilters(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.storage, func(t *testing.T) {
			t.Parallel()
			runTestCopyS3ObjectsWithExcludeFilters(t, &tc)
		})
	}
}

func runTestCopyS3ObjectsWithExcludeFilters(t *testing.T, tc *testCase) {
	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const fileContent = "content"

	files := [...]string{
		"file1.txt",
		"file2.txt",
		"file.py",
		"a.py",
		"b.c",
	}

	for _, filename := range files {
		putFile(t, s3client, bucket, filename, fileContent)
	}

	srcpath := fmt.Sprintf("%s://%s", tc.storage, bucket)

	cmd := s5cmd("cp", "--exclude", "*.py", "--exclude", "file*", srcpath+"/*", ".")
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals("cp %v/b.c %s", srcpath, files[4]),
	}, sortInput(true))

	// assert s3
	for _, f := range files {
		assert.Assert(t, ensureS3Object(s3client, bucket, f, fileContent))
	}

	expectedFileSystem := []fs.PathOp{
		fs.WithFile("b.c", fileContent),
	}
	// assert local filesystem
	expected := fs.Expected(t, expectedFileSystem...)
	assert.Assert(t, fs.Equal(cmd.Dir, expected))
}

// cp --exclude ".txt" s3://bucket/abc* .

func TestCopyS3ObjectsWithPrefixWithExcludeFilters(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.storage, func(t *testing.T) {
			t.Parallel()
			runTestCopyS3ObjectsWithPrefixWithExcludeFilters(t, &tc)
		})
	}
}

func runTestCopyS3ObjectsWithPrefixWithExcludeFilters(t *testing.T, tc *testCase) {
	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const fileContent = "content"

	files := [...]string{
		"abc.txt",
		"abc.dat",
		"aaa.txt",
		"aaa",
		"abcd",
	}

	for _, filename := range files {
		putFile(t, s3client, bucket, filename, fileContent)
	}

	srcpath := fmt.Sprintf("%s://%s", tc.storage, bucket)

	cmd := s5cmd("cp", "--exclude", ".txt", srcpath+"/abc*", ".")
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals("cp %v/abc.dat %s", srcpath, files[1]),
		1: equals("cp %v/abcd %s", srcpath, files[4]),
	}, sortInput(true))

	// assert s3
	for _, f := range files {
		assert.Assert(t, ensureS3Object(s3client, bucket, f, fileContent))
	}

	expectedFileSystem := []fs.PathOp{
		fs.WithFile("abc.dat", fileContent),
		fs.WithFile("abcd", fileContent),
	}
	// assert local filesystem
	expected := fs.Expected(t, expectedFileSystem...)
	assert.Assert(t, fs.Equal(cmd.Dir, expected))
}

// cp --exclude "*.gz" dir s3://bucket/
// cp --exclude "*.gz" dir/ s3://bucket/
// cp --exclude "*.gz" dir/* s3://bucket/

func TestCopyLocalDirectoryToS3WithExcludeFilters(t *testing.T) {
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.storage, func(t *testing.T) {
			t.Parallel()
			runTestCopyLocalDirectoryToS3WithExcludeFilters(t, &tc)
		})
	}
}

func runTestCopyLocalDirectoryToS3WithExcludeFilters(t *testing.T, tc *testCase) {
	bucket := s3BucketFromTestName(t)

	s3client, s5cmd := setup(t)

	createBucket(t, s3client, bucket)

	folderLayout := []fs.PathOp{
		fs.WithFile("testfile1.txt", "this is a test file 1"),
		fs.WithFile("readme.md", "this is a readme file"),
		fs.WithDir(
			"a",
			fs.WithFile("another_test_file.txt", "yet another txt file. yatf."),
		),
		fs.WithDir(
			"b",
			fs.WithFile("filename-with-hypen.gz", "file has hypen in its name"),
		),
	}

	workdir := fs.NewDir(t, "somedir", folderLayout...)
	defer workdir.Remove()

	const (
		excludePattern1 = "*.gz"
		excludePattern2 = "*.txt"
	)

	src := filepath.ToSlash(workdir.Path()) + "/"
	dst := fmt.Sprintf("%s://%v/prefix/", tc.storage, bucket)

	cmd := s5cmd("cp", "--exclude", excludePattern1, "--exclude", excludePattern2, src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	// assert local filesystem
	expected := fs.Expected(t, folderLayout...)
	assert.Assert(t, fs.Equal(workdir.Path(), expected))

	expectedS3Content := map[string]string{
		"prefix/readme.md": "this is a readme file",
	}

	nonExpectedS3Content := map[string]string{
		"prefix/testfile1.txt":            "this is a test file 1",
		"prefix/a/another_test_file.txt":  "yet another txt file. yatf.",
		"prefix/b/filename-with-hypen.gz": "file has hypen in its name",
	}

	// assert objects should be in S3
	for key, content := range expectedS3Content {
		assert.Assert(t, ensureS3Object(s3client, bucket, key, content))
	}

	//assert objects should not be in S3.
	for key, content := range nonExpectedS3Content {
		err := ensureS3Object(s3client, bucket, key, content)
		assertError(t, err, errS3NoSuchKey)
	}
}

// cp --exclude "main*" 's3://srcbucket/*' s3://dstbucket
func TestCopySingleObjectsIntoAnotherBucketWithExcludeFilters(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.storage, func(t *testing.T) {
			t.Parallel()
			runTestCopySingleS3ObjectsIntoAnotherBucketWithExcludeFilter(t, &tc)
		})
	}
}

func runTestCopySingleS3ObjectsIntoAnotherBucketWithExcludeFilter(t *testing.T, tc *testCase) {
	srcbucket := s3BucketFromTestNameWithPrefix(t, tc.storage+"-src")
	dstbucket := s3BucketFromTestNameWithPrefix(t, tc.storage+"-dst")

	s3client, s5cmd := setup(t)

	createBucket(t, s3client, srcbucket)
	createBucket(t, s3client, dstbucket)

	files := []string{
		"file.txt",
		"file1.txt",
		"main.py",
		"main.js",
		"readme.md",
		"main.pdf",
		"main/file.txt",
	}

	expectedFiles := []string{
		"file.txt",
		"file1.txt",
		"readme.md",
	}

	nonExpectedFiles := []string{
		"main.py",
		"main.js",
		"main.pdf",
		"main/file.txt",
	}

	const (
		content        = "this is a file content"
		excludePattern = "main*"
	)

	for _, filename := range files {
		putFile(t, s3client, srcbucket, filename, content)
	}

	src := fmt.Sprintf("%v://%v/*", tc.storage, srcbucket)
	dst := fmt.Sprintf("%v://%v/", tc.storage, dstbucket)

	cmd := s5cmd("cp", "--exclude", excludePattern, src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %v/file.txt %v/file.txt`, src, dst),
		1: equals(`cp %v/file1.txt %v/file1.txt`, src, dst),
		2: equals(`cp %v/readme.md %v/readme.md`, src, dst),
	}, sortInput(true))

	// assert s3 source objects
	for _, filename := range files {
		assert.Assert(t, ensureS3Object(s3client, srcbucket, filename, content))
	}

	// assert s3 destination objects
	for _, filename := range expectedFiles {
		assert.Assert(t, ensureS3Object(s3client, dstbucket, filename, content))
	}

	// assert s3 destination objects which should not be in bucket.
	for _, filename := range nonExpectedFiles {
		err := ensureS3Object(s3client, dstbucket, filename, content)
		assertError(t, err, errS3NoSuchKey)
	}
}

func TestCopyExpectExitCode1OnUnreachableHost(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.storage, func(t *testing.T) {
			t.Parallel()
			runCopyExpectExitCode1OnUnreachableHost(t, &tc)
		})
	}
}

func runCopyExpectExitCode1OnUnreachableHost(t *testing.T, tc *testCase) {
	_, s5cmd := setup(t)

	bucket := "example"
	filename := "file"

	cmd := s5cmd("cp", tc.storage+"://"+bucket+"/"+filename, ".")
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Expected{ExitCode: 1})

	assertLines(t, result.Stderr(), map[int]compareFunc{
		0: contains(`ERROR "cp %v://%v/%v .": Get "%v://%v/%v": dial tcp: lookup %v`, tc.storage, bucket, filename, tc.storage, bucket, filename, bucket),
	})
}

func TestCopySingleFileToStorageWithNoSuchUploadRetryCount(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.storage, func(t *testing.T) {
			t.Parallel()
			runTestCopySingleFileToStorageWithNoSuchUploadRetryCount(t, &tc)
		})
	}
}

func runTestCopySingleFileToStorageWithNoSuchUploadRetryCount(t *testing.T, tc *testCase) {
	_, s5cmd := setup(t)

	const (
		filename     = "file.txt"
		content      = "this is a test file"
		dstfile      = "file-copy.txt"
		nosuchbucket = "nosuchbucket"
	)

	file := fs.NewFile(t, filename, fs.WithContent(content))
	defer file.Remove()

	// copy without parents and expect to fail
	cmd := s5cmd(
		"cp",
		file.Path(),
		fmt.Sprintf("%v://%v/%v", tc.storage, nosuchbucket, dstfile),
	)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Expected{ExitCode: 1})

	assertLines(t, result.Stderr(), map[int]compareFunc{
		0: contains(`ERROR "cp %v %v://%v/%v": upload %v://%v/%v`, file.Path(), tc.storage, nosuchbucket, dstfile, tc.storage, nosuchbucket, dstfile),
	}, strictLineCheck(false))
}

func TestVersionedDownload(t *testing.T) {
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.storage, func(t *testing.T) {
			t.Parallel()
			runTestVersionedDownload(t, &tc)
		})
	}
}

func runTestVersionedDownload(t *testing.T, tc *testCase) {
	t.Parallel()

	bucket := s3BucketFromTestName(t)
	// versioning is only supported with in memory backend!
	s3client, s5cmd := setup(t, withS3Backend("mem"))

	const filename = "testfile.txt"
	var contents = []string{
		"This is first content",
		"Second content it is, and it is a bit longer",
	}

	workdir := fs.NewDir(t, t.Name(), fs.WithFile(filename+"1", contents[0]), fs.WithFile(filename+"2", contents[1]))
	defer workdir.Remove()

	// create a bucket and Enable versioning
	createBucket(t, s3client, bucket)
	setBucketVersioning(t, s3client, bucket, "Enabled")

	// upload two versions of the file with same key
	putFile(t, s3client, bucket, filename, contents[0])
	putFile(t, s3client, bucket, filename, contents[1])

	// we expect to see 2 versions of objects
	cmd := s5cmd("ls", "--all-versions", tc.storage+"://"+bucket+"/"+filename)
	result := icmd.RunCmd(cmd)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: contains("%v", filename),
		1: contains("%v", filename),
	})

	// now we will parse their version IDs in the order we put them into s3 server.
	// the rest of the tests depends on this assumption
	versionIDs := make([]string, 0)
	for _, row := range strings.Split(result.Stdout(), "\n") {
		if row != "" {
			arr := strings.Split(row, " ")
			versionIDs = append(versionIDs, arr[len(arr)-1])
		}
	}

	// create new dir to download files
	newDir := fs.NewDir(t, t.Name())
	defer newDir.Remove()

	// download both old and new versions of the file to newDir
	for i, version := range versionIDs {
		cmd = s5cmd("cp", "--version-id", version,
			fmt.Sprintf(tc.storage+"://%v/%v", bucket, filename), newDir.Path()+"/"+filename+strconv.Itoa(1+i))
		_ = icmd.RunCmd(cmd)
	}

	assert.Assert(t, fs.Equal(workdir.Path(), fs.ManifestFromDir(t, newDir.Path())))
}

// Before downloading a file from s3 a local target file is created. If download
// fails the created file should be deleted.
func TestDeleteFileWhenDownloadFailed(t *testing.T) {
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.storage, func(t *testing.T) {
			t.Parallel()
			runTestDeleteFileWhenDownloadFailed(t, &tc)
		})
	}
}

func runTestDeleteFileWhenDownloadFailed(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	filename := "testfile1.txt"
	createBucket(t, s3client, bucket)

	// It is going try downloading a nonexistent file from the s3 so it will fail.
	// In this case we don't expect to have a local file with the name `filename`.
	cmd := s5cmd("cp", "s3://"+bucket+"/"+filename, filename)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Expected{ExitCode: 1})

	// assert local filesystem does not have any (such) file
	expected := fs.Expected(t)
	assert.Assert(t, fs.Equal(cmd.Dir, expected))
}

// Target local file should be overriden only if download completed successfully
func TestLocalFileOverridenWhenDownloadFailed(t *testing.T) {
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.storage, func(t *testing.T) {
			t.Parallel()
			runTestLocalFileOverridenWhenDownloadFailed(t, &tc)
		})
	}
}

func runTestLocalFileOverridenWhenDownloadFailed(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)
	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const (
		filename        = "testfile1.txt"
		content         = "preserved content"
		expectedContent = "preserved content"
	)

	workdir := fs.NewDir(t, t.Name(), fs.WithFile(filename, content))
	defer workdir.Remove()

	// It is going try downloading a nonexistent file from the s3 so it will fail.
	// In this case we don't expect to have a local file will be overwritten.
	cmd := s5cmd("cp", tc.storage+"://"+bucket+"/"+filename, filename)
	result := icmd.RunCmd(cmd, withWorkingDir(workdir))

	result.Assert(t, icmd.Expected{ExitCode: 1})

	// assert initial file is untouched
	expected := fs.Expected(t, fs.WithFile(filename, content))
	assert.Assert(t, fs.Equal(workdir.Path(), expected))
}

// Test that counting writer does not corrupt objects during a download process
func TestCountingWriter(t *testing.T) {
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.storage, func(t *testing.T) {
			t.Parallel()
			runTestCountingWriter(t, &tc)
		})
	}
}

func runTestCountingWriter(t *testing.T, tc *testCase) {
	t.Parallel()
	const filename = "log.txt"
	content := randomString(3_000_000)
	s3client, s5cmd := setup(t)
	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)
	putFile(t, s3client, bucket, filename, content)
	cmd := s5cmd("cp", "--show-progress", "--concurrency", "3", "--part-size", "1", tc.storage+"://"+bucket+"/"+filename, ".")
	result := icmd.RunCmd(cmd)
	result.Assert(t, icmd.Success)
	// assert the downloaded file has the same content with the remote object
	expected := fs.Expected(t, fs.WithFile(filename, content, fs.WithMode(0644)))
	assert.Assert(t, fs.Equal(cmd.Dir, expected))
}

// It should skip special files
func TestUploadingSocketFile(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.storage, func(t *testing.T) {
			t.Parallel()
			runUploadingSocketFile(t, &tc)
		})
	}
}

func runUploadingSocketFile(t *testing.T, tc *testCase) {
	_, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)

	dir, err := ioutil.TempDir("", "s5cmd-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	socketFile := filepath.Join(dir, "socket.sock")

	mkfifo := exec.Command("mkfifo", socketFile)
	if err := mkfifo.Run(); err != nil {
		t.Fatalf("unable to create fifo file: %v", err)
	}

	cmd := s5cmd("cp", socketFile, tc.storage+"://"+bucket+"/")
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Expected{ExitCode: 1})

	assertLines(t, result.Stderr(), map[int]compareFunc{
		0: equals(`ERROR "cp %v %v://%v/socket.sock": "%v" is a named pipe`, socketFile, tc.storage, bucket, socketFile),
	})
}

// cp --include "*.py" s3://bucket/* .

func TestCopyS3ObjectsWithIncludeFilter(t *testing.T) {

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.storage, func(t *testing.T) {
			t.Parallel()
			runCopyS3ObjectsWithIncludeFilter(t, &tc)
		})
	}
}

func runCopyS3ObjectsWithIncludeFilter(t *testing.T, tc *testCase) {
	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	filesToContent := map[string]string{
		"testfile1.txt": "this is a test file 1",
		"testfile2.txt": "this is a test file 2",
		"testfile3.py":  "print('hello world!')",
	}

	for filename, content := range filesToContent {
		putFile(t, s3client, bucket, filename, content)
	}

	cmd := s5cmd("cp", tc.storage+"://"+bucket+"/*", ".", "--include", "*.py")
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stderr(), map[int]compareFunc{
		0: equals(""),
	}, strictLineCheck(true))

	// assert local filesystem
	expected := fs.Expected(t, fs.WithFile("testfile3.py", filesToContent["testfile3.py"], fs.WithMode(0644)))
	assert.Assert(t, fs.Equal(cmd.Dir, expected))
}

// cp --include "file*" --exclude "*.py" s3://bucket/* .

func TestCopyS3ObjectsWithIncludeExcludeFilter(t *testing.T) {
	for _, tc := range testCases {
		t.Run(tc.storage, func(t *testing.T) {
			t.Parallel()
			runCopyS3ObjectsWithIncludeExcludeFilter(t, &tc)
		})
	}
}

func runCopyS3ObjectsWithIncludeExcludeFilter(t *testing.T, tc *testCase) {
	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const (
		includePattern = "file*"
		excludePattern = "*.py"
		fileContent    = "content"
	)

	files := [...]string{
		"file1.py",
		"file2.py",
		"file1.go",
		"file2.go",
		"test.py",
		"app.go",
		"vendor/file1.py",
		"vendor/file1.py",
		"vendor/file1.py",
		"vendor/file2.go",
	}

	for _, filename := range files {
		putFile(t, s3client, bucket, filename, fileContent)
	}

	srcpath := fmt.Sprintf("%s://%s", tc.storage, bucket)

	cmd := s5cmd("cp", "--exclude", "file*", "--exclude", "vendor/*", "--include", "*.py", "--include", "*.go", srcpath+"/*", ".")
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)
	assertLines(t, result.Stdout(), map[int]compareFunc{}, sortInput(true))

	// assert s3
	for _, f := range files {
		assert.Assert(t, ensureS3Object(s3client, bucket, f, fileContent))
	}

	expectedFileSystem := []fs.PathOp{}
	// assert local filesystem
	expected := fs.Expected(t, expectedFileSystem...)
	assert.Assert(t, fs.Equal(cmd.Dir, expected))
}

// cp --exclude "file*" --exclude "vendor/*" --include "*.py" --include "*.go" s3://bucket/* .

func TestCopyS3ObjectsWithIncludeExcludeFilter2(t *testing.T) {
	for _, tc := range testCases {
		t.Run(tc.storage, func(t *testing.T) {
			t.Parallel()
			runTestCopyS3ObjectsWithIncludeExcludeFilter2(t, &tc)
		})
	}
}

func runTestCopyS3ObjectsWithIncludeExcludeFilter2(t *testing.T, tc *testCase) {
	s3client, s5cmd := setup(t)
	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const (
		fileContent = "content"
	)

	files := [...]string{
		"file1.py",
		"file2.py",
		"file1.go",
		"file2.go",
		"test.py",
		"app.go",
		"vendor/package.go",
		"docs/readme.md",
		"vendor/file1.py",
		"vendor/file2.go",
	}

	for _, filename := range files {
		putFile(t, s3client, bucket, filename, fileContent)
	}

	srcpath := fmt.Sprintf("%s://%s", tc.storage, bucket)

	cmd := s5cmd("cp", "--exclude", "file*", "--exclude", "vendor/*", "--include", "*.py", "--include", "*.go", srcpath+"/*", ".")
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)
	assertLines(t, result.Stdout(), map[int]compareFunc{}, sortInput(true))

	// assert s3
	for _, f := range files {
		assert.Assert(t, ensureS3Object(s3client, bucket, f, fileContent))
	}

	// assert local filesystem
	expected := fs.Expected(t,
		fs.WithFile("test.py", fileContent),
		fs.WithFile("app.go", fileContent),
	)
	assert.Assert(t, fs.Equal(cmd.Dir, expected))
}

func strPtr(s string) *string {
	return &s
}
