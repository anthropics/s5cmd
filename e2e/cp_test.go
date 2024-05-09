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
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/aws"
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
  t.Run("SingleS3ObjectToLocalWithDestinationWildcard", func(t *testing.T) {
    for _, tc := range testCases {
      tc := tc
      t.Run(tc.storage, func(t *testing.T) {
        runTestCopySingleS3ObjectToLocalWithDestinationWildcard(t, \&tc)
      })
    }
  }) 
}

func runTestCopySingleS3ObjectToLocalWithDestinationWildcard(t *testing.T, tc *testCase) {
  t.Parallel()

  const (
    filename = "testfile1.txt"
    content = "this is a file content"
  )

  bucket := s3BucketFromTestName(t)

  s3client, s5cmd := setup(t)
  createBucket(t, s3client, bucket)

  putFile(t, s3client, bucket, filename, content)

  cmd := s5cmd("cp", tc.storage+"://"+bucket+"/"+filename, "*")
  result := icmd.RunCmd(cmd)

  result.Assert(t, icmd.Success)

  // assert local filesystem
  expected := fs.Expected(t, fs.WithFile(filename, content, fs.WithMode(0644)))
  assert.Assert(t, fs.Equal(cmd.Dir, expected))

  // assert s3 object
  assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
}
}

// cp s3://bucket/prefix/ dir/

func TestCopyS3PrefixToLocalMustReturnError(t *testing.T) {
	t.Parallel()
	t.Run("CopyS3PrefixToLocalMustReturnError", func(t *testing.T) {
		for _, tc := range testCases {
			tc := tc
			t.Run(tc.storage, func(t *testing.T) {
				runTestCopyS3PrefixToLocalMustReturnError(t, \&tc)
			})
		}
	})
}

func runTestCopyS3PrefixToLocalMustReturnError(t *testing.T, tc *testCase) {
	t.Parallel()

	bucket := s3BucketFromTestName(t)

	s3client, s5cmd := setup(t)
	createBucket(t, s3client, bucket)

	createPrefix(t, s3client, bucket, "prefix/")

	cmd := s5cmd("cp", tc.storage+"://"+bucket+"/prefix/", ".")
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Expected{ExitCode: 127})

	assertLines(t, result.Stderr(), map[int]compareFunc{
		0: equals("ERROR copying multiple objects to a single file is not supported"),
	})
}
}

// cp --flatten s3://bucket/* dir/ (flat source hiearchy)

func TestCopyMultipleFlatS3ObjectsToLocal(t *testing.T) {
	t.Parallel()
	t.Run("CopyMultipleFlatS3ObjectsToLocal", func(t *testing.T) {
		for _, tc := range testCases {
			tc := tc
			t.Run(tc.storage, func(t *testing.T) {
				runTestCopyMultipleFlatS3ObjectsToLocal(t, \&tc)
			})
		}
	})
}

func runTestCopyMultipleFlatS3ObjectsToLocal(t *testing.T, tc *testCase) {
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

	cmd := s5cmd("cp", "--flatten", tc.storage+"://"+bucket+"/*", ".")
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %s://%v/a/filename-with-hypen.gz filename-with-hypen.gz`, tc.storage, bucket),
		1: equals(`cp %s://%v/a/readme.md readme.md`, tc.storage, bucket),
		2: equals(`cp %s://%v/b/another_test_file.txt another_test_file.txt`, tc.storage, bucket),
		3: equals(`cp %s://%v/testfile1.txt testfile1.txt`, tc.storage, bucket),
	}, sortInput(true))

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
}

// cp --flatten s3://bucket/*.txt dir/

func TestCopyMultipleFlatS3ObjectsToLocalWithPartialMatching(t *testing.T) {
	t.Parallel()
	t.Run("CopyMultipleFlatS3ObjectsToLocalWithPartialMatching", func(t *testing.T) {
		for _, tc := range testCases {
			tc := tc
			t.Run(tc.storage, func(t *testing.T) {
				runTestCopyMultipleFlatS3ObjectsToLocalWithPartialMatching(t, \&tc)
			})
		}
	})
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

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %s://%v/b/another_test_file.txt another_test_file.txt`, tc.storage, bucket),
		1: equals(`cp %s://%v/testfile1.txt testfile1.txt`, tc.storage, bucket),
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
}

// cp s3://bucket/*/*.txt dir/

func TestCopyMultipleFlatNestedS3ObjectsToLocalWithPartialMatching(t *testing.T) {
	t.Parallel()
	t.Run("CopyMultipleFlatNestedS3ObjectsToLocalWithPartialMatching", func(t *testing.T) {
		for _, tc := range testCases {
			tc := tc
			t.Run(tc.storage, func(t *testing.T) {
				runTestCopyMultipleFlatNestedS3ObjectsToLocalWithPartialMatching(t, \\&tc)
			})
		}
	})
}

func runTestCopyMultipleFlatNestedS3ObjectsToLocalWithPartialMatching(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	filesToContent := map[string]string{
		"testfile1.txt":                 "this is a test file 1",
		"a/readme.md":                   "this is a readme file",
		"a/b/filename-with-hypen.gz":    "file has hypen in its name",
		"a/another_test_file.txt":       "yet another txt file. yatf.",
		"c/d/e/f/deep_nested_file.txt":  "go deep or go home",
	}

	for filename, content := range filesToContent {
		putFile(t, s3client, bucket, filename, content)
	}

	cmd := s5cmd("cp", tc.storage+"://"+bucket+"/*/*.txt", ".")
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %s://%v/a/another_test_file.txt another_test_file.txt`, tc.storage, bucket),
		1: equals(`cp %s://%v/c/d/e/f/deep_nested_file.txt deep_nested_file.txt`, tc.storage, bucket),
	}, sortInput(true))

	// assert local filesystem
	var expectedFiles = []fs.PathOp{
		fs.WithFile("another_test_file.txt", "yet another txt file. yatf."),
		fs.WithFile("deep_nested_file.txt", "go deep or go home"),
	}
	expected := fs.Expected(t, expectedFiles...)
	assert.Assert(t, fs.Equal(cmd.Dir, expected))

	// assert s3 objects
	for filename, content := range filesToContent {
		assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
	}
}
}

// --json cp --flatten s3://bucket/* .

func TestCopyMultipleFlatS3ObjectsToLocalJSON(t *testing.T) {
	t.Parallel()
	t.Run("CopyMultipleFlatS3ObjectsToLocalJSON", func(t *testing.T) {
		for _, tc := range testCases {
			tc := tc
			t.Run(tc.storage, func(t *testing.T) {
				runTestCopyMultipleFlatS3ObjectsToLocalJSON(t, \&tc)
			})
		}
	})
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
		0: json(`
            {
              "operation": "cp", "success": true, "source": "%v://%v/a/filename-with-hypen.gz",
              "destination": "filename-with-hypen.gz", "object": { "type": "file", "size": 26 }
            }
        `, tc.storage, bucket, nil),
		1: json(`
            {
              "operation": "cp", "success": true, "source": "%v://%v/a/readme.md",
              "destination": "readme.md", "object": { "type": "file", "size": 22 }
            }
        `, tc.storage, bucket, nil),
		2: json(`
            {
              "operation": "cp", "success": true, "source": "%v://%v/b/another_test_file.txt",
              "destination": "another_test_file.txt", "object": { "type": "file", "size": 28 }
            }
        `, tc.storage, bucket, nil),
		3: json(`
            {
              "operation": "cp", "success": true, "source": "%v://%v/testfile1.txt",
              "destination": "testfile1.txt", "object": { "type": "file", "size": 21 }
            }
        `, tc.storage, bucket, nil),
	}, jsonCheck(true), sortInput(true))

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
}

// cp s3://bucket/* dir/ (nested source hierarchy)

func TestCopyMultipleNestedS3ObjectsToLocal(t *testing.T) {
	t.Parallel()
	t.Run("CopyMultipleNestedS3ObjectsToLocal", func(t *testing.T) {
		for _, tc := range testCases {
			tc := tc
			t.Run(tc.storage, func(t *testing.T) {
				runTestCopyMultipleNestedS3ObjectsToLocal(t, \&tc)
			})
		}
	})
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

	// assert s3 objects
	for filename, content := range filesToContent {
		assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
	}
}
}

// cp s3://bucket/*/*.ext dir/

func TestCopyMultipleNestedS3ObjectsToLocalWithPartial(t *testing.T) {
	t.Parallel()
	t.Run("CopyMultipleNestedS3ObjectsToLocalWithPartial", func(t *testing.T) {
		for _, tc := range testCases {
			tc := tc
			t.Run(tc.storage, func(t *testing.T) {
				runTestCopyMultipleNestedS3ObjectsToLocalWithPartial(t, \\&tc)
			})
		}
	})
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
		"c/d/e/f/deep_nested_file.txt":  "go deep or go home",
	}

	for filename, content := range filesToContent {
		putFile(t, s3client, bucket, filename, content)
	}

	cmd := s5cmd("cp", tc.storage+"://"+bucket+"/*/*.txt", ".")
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %s://%v/b/another_test_file.txt b/another_test_file.txt`, tc.storage, bucket),
		1: equals(`cp %s://%v/c/d/e/f/deep_nested_file.txt c/d/e/f/deep_nested_file.txt`, tc.storage, bucket),
	}, sortInput(true))

	// assert local filesystem
	var expectedFiles = []fs.PathOp{
		fs.WithDir("b",
		  fs.WithFile("another_test_file.txt", "yet another txt file. yatf."),
		),
		fs.WithDir("c",
		  fs.WithDir("d",
		    fs.WithDir("e",
		      fs.WithDir("f",
		        fs.WithFile("deep_nested_file.txt", "go deep or go home"),
		      ),
		    ),
		  ),
		),
	}
	expected := fs.Expected(t, expectedFiles...)
	assert.Assert(t, fs.Equal(cmd.Dir, expected))

	// assert s3 objects
	for filename, content := range filesToContent {
		assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
	}
}
}

// cp s3://bucket/* dir/ (dir/ doesn't exist)

func TestCopyMultipleS3ObjectsToGivenLocalDirectory(t *testing.T) {
	t.Parallel()
	t.Run("CopyMultipleS3ObjectsToGivenLocalDirectory", func(t *testing.T) {
		for _, tc := range testCases {
			tc := tc
			t.Run(tc.storage, func(t *testing.T) {
				runTestCopyMultipleS3ObjectsToGivenLocalDirectory(t, \\&tc)
			})
		}
	})
}

func runTestCopyMultipleS3ObjectsToGivenLocalDirectory(t *testing.T, tc *testCase) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	filesToContent := map[string]string{
		"testfile1.txt":          "this is a test file 1",
		"a/readme.md":            "this is a readme file",
		"a/b/file-with-hypen.gz": "file has hypen in its name",
		"b/another_test_file":    "yet another txt file. yatf.",
	}

	for filename, content := range filesToContent {
		putFile(t, s3client, bucket, filename, content)
	}

	// non-existing directory
	destDir := "given-dir"
	cmd := s5cmd("cp", tc.storage+"://"+bucket+"/*", destDir+"/")
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %v://%v/a/b/file-with-hypen.gz %v/a/b/file-with-hypen.gz`, tc.storage, bucket, destDir),
		1: equals(`cp %v://%v/a/readme.md %v/a/readme.md`, tc.storage, bucket, destDir),
		2: equals(`cp %v://%v/b/another_test_file %v/b/another_test_file`, tc.storage, bucket, destDir),
		3: equals(`cp %v://%v/testfile1.txt %v/testfile1.txt`, tc.storage, bucket, destDir),
	}, sortInput(true))

	// assert local filesystem
	var expectedFiles = []fs.PathOp{
		fs.WithDir(
			destDir,
			fs.WithFile("testfile1.txt", "this is a test file 1"),
			fs.WithDir(
				"a",
				fs.WithFile("readme.md", "this is a readme file"),
				fs.WithDir(
					"b",
					fs.WithFile("file-with-hypen.gz", "file has hypen in its name"),
				),
			),
			fs.WithDir(
				"b",
				fs.WithFile("another_test_file", "yet another txt file. yatf."),
			),
		),
	}
	expected := fs.Expected(t, expectedFiles...)
	assert.Assert(t, fs.Equal(cmd.Dir, expected))

	// assert s3 objects
	for filename, content := range filesToContent {
		assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
	}
}
}

// cp dir/file s3://bucket/

func TestCopySingleFileToS3(t *testing.T) {
	t.Parallel()
	t.Run("CopySingleFileToS3", func(t *testing.T) {
		for _, tc := range testCases {
			tc := tc
			t.Run(tc.storage, func(t *testing.T) {
				t.Parallel()

				const (
					filename = "testfile1.txt"
					content  = "this is a test file"
				)

				bucket := s3BucketFromTestName(t)

				s3client, s5cmd := setup(t)

				createBucket(t, s3client, bucket)

				workdir := fs.NewDir(t, bucket, fs.WithFile(filename, content))
				defer workdir.Remove()

				srcpath := workdir.Join(filename)
				dstpath := fmt.Sprintf("%v://%v/", tc.storage, bucket)

				srcpath = filepath.ToSlash(srcpath)
				cmd := s5cmd("cp", srcpath, dstpath)
				result := icmd.RunCmd(cmd)

				result.Assert(t, icmd.Success)

				assertLines(t, result.Stdout(), map[int]compareFunc{
					0: suffix(`cp %v %v%v`, srcpath, dstpath, filename),
				})

				// assert local filesystem
				expected := fs.Expected(t, fs.WithFile(filename, content))
				assert.Assert(t, fs.Equal(workdir.Path(), expected))

				// assert s3
				assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
			})
		}
	})
}
}


func TestCopySingleFileToS3WithAllMetadataFlags(t *testing.T) {
	t.Parallel()
	t.Run("CopySingleFileToS3WithAllMetadataFlags", func(t *testing.T) {
		for _, tc := range testCases {
			tc := tc
			t.Run(tc.storage, func(t *testing.T) {
				t.Parallel()

				const (
					filename        = "testfile1.txt"
					content         = "this is a test file"
					contentType     = "text/plain"
					contentEncoding = "gzip"
					cacheControl    = "no-cache"
				)

				metadata := map[string]string{
					"content-type":     contentType,
					"content-encoding": contentEncoding,
					"cache-control":    cacheControl,
				}

				bucket := s3BucketFromTestName(t)

				s3client, s5cmd := setup(t)

				createBucket(t, s3client, bucket)

				workdir := fs.NewDir(t, bucket, fs.WithFile(filename, content))
				defer workdir.Remove()

				srcpath := workdir.Join(filename)
				dstpath := fmt.Sprintf("%v://%v/", tc.storage, bucket)

				srcpath = filepath.ToSlash(srcpath)

				var metaFlags []string
				for name, value := range metadata {
					metaFlags = append(metaFlags, "--"+name, value)
				}

				cmd := s5cmd(append([]string{"cp", srcpath, dstpath}, metaFlags...)...)
				result := icmd.RunCmd(cmd)

				result.Assert(t, icmd.Success)

				assertLines(t, result.Stdout(), map[int]compareFunc{
					0: suffix(`cp %v %v%v`, srcpath, dstpath, filename),
				}, jsonCheck(true))

				// assert local filesystem
				expected := fs.Expected(t, fs.WithFile(filename, content))
				assert.Assert(t, fs.Equal(workdir.Path(), expected))

				obj := getS3Object(t, s3client, bucket, filename)

				// assert s3 object metadata
				assert.Equal(t, obj.ContentType, contentType)
				assert.Equal(t, obj.ContentEncoding, contentEncoding)
				assert.Equal(t, obj.CacheControl, cacheControl)
			})
		}
	})
}
}

// cp dir/file s3://bucket/ --metadata key1=val1 --metadata key2=val2 ...

func TestCopySingleFileToS3WithArbitraryMetadata(t *testing.T) {
	t.Parallel()
	t.Run("CopySingleFileToS3WithArbitraryMetadata", func(t *testing.T) {
		for _, tc := range testCases {
			tc := tc
			t.Run(tc.storage, func(t *testing.T) {
				t.Parallel()

				const (
					filename = "testfile1.txt"  
					content  = "this is a test file"
				)

				metadata := map[string]string{
					"key1": "val1",
					"key2": "val2", 
					"key3": "val3",
				}

				bucket := s3BucketFromTestName(t)

				s3client, s5cmd := setup(t)

				createBucket(t, s3client, bucket)

				workdir := fs.NewDir(t, bucket, fs.WithFile(filename, content))
				defer workdir.Remove()

				srcpath := workdir.Join(filename)
				dstpath := fmt.Sprintf("%v://%v/", tc.storage, bucket)

				srcpath = filepath.ToSlash(srcpath)

				var metaFlags []string
				for name, value := range metadata {
					metaFlags = append(metaFlags, "--metadata", name+"="+value)
				}

				cmd := s5cmd(append([]string{"cp", srcpath, dstpath}, metaFlags...)...)
				result := icmd.RunCmd(cmd)

				result.Assert(t, icmd.Success)

				assertLines(t, result.Stdout(), map[int]compareFunc{
					0: suffix(`cp %v %v%v`, srcpath, dstpath, filename),
				}, jsonCheck(true))

				// assert local filesystem
				expected := fs.Expected(t, fs.WithFile(filename, content))
				assert.Assert(t, fs.Equal(workdir.Path(), expected))

				obj := getObject(t, s3client, bucket, filename)

				// assert s3 object metadata
				if tc.storage == "s3" {
					for k, v := range metadata {
						assert.Equal(t, obj.Metadata.Get(k), v)
					}
				} else {
					assert.DeepEqual(t, obj.Metadata, metadata)
				}
			})
		}
	})
}
}

// cp s3://bucket2/obj2 s3://bucket1/obj1 --metadata key1=val1 --metadata key2=val2 ...

func TestCopyS3ToS3WithArbitraryMetadata(t *testing.T) {
	t.Parallel()
	t.Run("CopyS3ToS3WithArbitraryMetadata", func(t *testing.T) {
		for _, tc := range testCases {
			tc := tc
			t.Run(tc.storage, func(t *testing.T) {
				t.Parallel()

				const (
					filename = "testfile1.txt"
					content  = "this is a test file"
				)

				metadata := map[string]string{
					"key1": "val1",
					"key2": "val2",
					"key3": "val3",
				}

				bucket1 := s3BucketFromTestName(t)
				bucket2 := s3BucketFromTestName(t) + "-copy"

				s3client, s5cmd := setup(t)

				createBucket(t, s3client, bucket1)
				createBucket(t, s3client, bucket2)

				putFile(t, s3client, bucket2, filename, content)

				srcpath := fmt.Sprintf("%v://%v/%v", tc.storage, bucket2, filename)
				dstpath := fmt.Sprintf("%v://%v/", tc.storage, bucket1)

				var metaFlags []string
				for name, value := range metadata {
					metaFlags = append(metaFlags, "--metadata", name+"="+value)
				}

				cmd := s5cmd(append([]string{"cp", srcpath, dstpath}, metaFlags...)...)
				result := icmd.RunCmd(cmd)

				result.Assert(t, icmd.Success)

				assertLines(t, result.Stdout(), map[int]compareFunc{
					0: suffix(`cp %v %v%v`, srcpath, dstpath, filename),
				}, jsonCheck(true))

				// assert src object
				assert.Assert(t, ensureS3Object(s3client, bucket2, filename, content))

				obj := getObject(t, s3client, bucket1, filename)

				// assert copied object
				assert.Assert(t, ensureS3Object(s3client, bucket1, filename, content))

				// assert copied object metadata 
				switch tc.storage {
				case "s3":
					for k, v := range metadata {
						assert.Equal(t, obj.Metadata.Get(k), v)
					}
				case "gs":
					assert.DeepEqual(t, obj.Metadata, metadata)
				}
			})
		}
	})
}
}


func TestCopySingleFileToS3WithAdjacentSlashes(t *testing.T) {
	t.Parallel()
	t.Run("CopySingleFileToS3WithAdjacentSlashes", func(t *testing.T) {
		for _, tc := range testCases {
			tc := tc
			t.Run(tc.storage, func(t *testing.T) {
				t.Parallel()

				const (
					filename = "testfile1.txt"
					content  = "this is a test file"
				)

				bucket := s3BucketFromTestName(t)

				s3client, s5cmd := setup(t)

				createBucket(t, s3client, bucket)

				workdir := fs.NewDir(t, bucket, fs.WithFile(filename, content))
				defer workdir.Remove()

				srcpath := workdir.Join(filename)
				dstpath := fmt.Sprintf("%v:///%v//", tc.storage, bucket)

				srcpath = filepath.ToSlash(srcpath)
				cmd := s5cmd("cp", srcpath, dstpath)
				result := icmd.RunCmd(cmd)

				result.Assert(t, icmd.Success)

				assertLines(t, result.Stdout(), map[int]compareFunc{
					0: suffix(`cp %v %v%v`, srcpath, strings.TrimRight(dstpath, "/"), filename),
				})

				// assert local filesystem
				expected := fs.Expected(t, fs.WithFile(filename, content))
				assert.Assert(t, fs.Equal(workdir.Path(), expected))

				// assert s3
				assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
			})
		}
	})
}
}

// --json cp dir/file s3://bucket

func TestCopySingleFileToS3JSON(t *testing.T) {
	t.Parallel()
	t.Run("CopySingleFileToS3JSON", func(t *testing.T) {
		for _, tc := range testCases {
			tc := tc
			t.Run(tc.storage, func(t *testing.T) {
				t.Parallel()

				const (
					filename = "testfile1.txt"
					content  = "this is a test file"
				)

				bucket := s3BucketFromTestName(t)

				s3client, s5cmd := setup(t)

				createBucket(t, s3client, bucket)

				workdir := fs.NewDir(t, bucket, fs.WithFile(filename, content))
				defer workdir.Remove()

				srcpath := workdir.Join(filename)
				dstpath := fmt.Sprintf("%v://%v/", tc.storage, bucket)

				srcpath = filepath.ToSlash(srcpath)
				cmd := s5cmd("--json", "cp", srcpath, dstpath)
				result := icmd.RunCmd(cmd)

				result.Assert(t, icmd.Success)

				assertLines(t, result.Stdout(), map[int]compareFunc{
					0: suffix(`cp %v %v%v`, srcpath, dstpath, filename),
				}, jsonCheck(true))

				// assert local filesystem
				expected := fs.Expected(t, fs.WithFile(filename, content))
				assert.Assert(t, fs.Equal(workdir.Path(), expected))

				// assert s3
				assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
			})
		}
	})
}
}

// cp dir/ s3://bucket/

// cp dir/ s3://bucket/
func TestCopyDirToS3(t *testing.T) {
	t.Parallel()

	t.Run("CopyDirToS3", func(t *testing.T) {
		for _, tc := range testCases {
			tc := tc
			t.Run(tc.storage, func(t *testing.T) {
				t.Parallel()

				const (
					filename = "file1.txt"
					content  = "this is a test file"
				)

				bucket := s3BucketFromTestName(t)

				s3client, s5cmd := setup(t)

				createBucket(t, s3client, bucket)

				folderLayout := []fs.PathOp{
					fs.WithFile(filename, content),
					fs.WithDir(
						"c",
						fs.WithFile("file2.txt", content),
					),
				}

				workdir := fs.NewDir(t, tc.storage, folderLayout...)
				defer workdir.Remove()

				srcpath := filepath.ToSlash(workdir.Path())
				dstpath := fmt.Sprintf("%v://%v/", tc.storage, bucket)

				cmd := s5cmd("cp", workdir.Path()+"/", dstpath)
				result := icmd.RunCmd(cmd)

				result.Assert(t, icmd.Success)

				assertLines(t, result.Stdout(), map[int]compareFunc{
					0: suffix(`cp %v/c/file2.txt %vc/file2.txt`, srcpath, dstpath),
					1: suffix(`cp %v/file1.txt %vfile1.txt`, srcpath, dstpath),
				}, sortInput(true))

				// assert s3 objects
				assert.Assert(t, ensureS3Object(s3client, bucket, "c/file2.txt", content))
				assert.Assert(t, ensureS3Object(s3client, bucket, "file1.txt", content))

				// assert local filesystem
				expected := fs.Expected(t, folderLayout...)
				assert.Assert(t, fs.Equal(workdir.Path(), expected))
			})
		}
	})
}
}

// cp dir/{file, folderWithBackslash} s3://bucket

func TestCopyDirBackslashedToS3(t *testing.T) {
	t.Parallel()

	t.Run("CopyDirBackslashedToS3", func(t *testing.T) {
		for _, tc := range testCases {
			tc := tc
			t.Run(tc.storage, func(t *testing.T) {
				t.Parallel()

				const (
					filename     = "file1.txt"
					content      = "this is a test file"
					backslashDir = `a`
				)

				bucket := s3BucketFromTestName(t)

				s3client, s5cmd := setup(t)

				createBucket(t, s3client, bucket)

				dirpath := filepath.Join("dir")
				folderLayout := []fs.PathOp{
					fs.WithDir(
						dirpath,
						fs.WithFile(filename, content),
						fs.WithDir(backslashDir),
					),
				}

				workdir := fs.NewDir(t, tc.storage, folderLayout...)
				defer workdir.Remove()

				// here using "/" to ensure that the source folder
				// gets copied when ended with a "/"
				srcpath := filepath.ToSlash(filepath.Join(workdir.Path(), dirpath)) + "/"
				dstpath := fmt.Sprintf("%v://%v/", tc.storage, bucket)

				cmd := s5cmd("cp", srcpath, dstpath)
				result := icmd.RunCmd(cmd)

				result.Assert(t, icmd.Success)

				assertLines(t, result.Stdout(), map[int]compareFunc{
					0: suffix(`cp %v %v%v/`, srcpath, dstpath, "dir"),
				}, sortInput(true))

				// assert local filesystem
				expected := fs.Expected(t, folderLayout...)
				assert.Assert(t, fs.Equal(workdir.Path(), expected))

				// assert s3 objects
				assert.Assert(t, ensureS3Object(s3client, bucket, filepath.Join(dirpath, filename), content))
				assert.Assert(t, ensureS3Object(s3client, bucket, filepath.Join(dirpath, backslashDir)+"/", ""))
			})
		}
	})
}
}

// cp --storage-class=GLACIER file s3://bucket/

func TestCopySingleFileToS3WithStorageClassGlacier(t *testing.T) {
	t.Parallel()
	t.Run("CopySingleFileToS3WithStorageClassGlacier", func(t *testing.T) {
		for _, tc := range testCases {
			tc := tc
			t.Run(tc.storage, func(t *testing.T) {
				t.Parallel()

				const (
					filename = "testfile1.txt"
					content  = "this is a test file"
				)

				bucket := s3BucketFromTestName(t)

				s3client, s5cmd := setup(t)

				createBucket(t, s3client, bucket)

				workdir := fs.NewDir(t, tc.storage, fs.WithFile(filename, content))
				defer workdir.Remove()

				srcpath := workdir.Join(filename)
				dstpath := fmt.Sprintf("%v://%v/", tc.storage, bucket)

				cmd := s5cmd("cp", "--storage-class", "GLACIER", srcpath, dstpath)
				result := icmd.RunCmd(cmd)

				result.Assert(t, icmd.Success)

				assertLines(t, result.Stdout(), map[int]compareFunc{
					0: suffix(`cp %v %v%v`, srcpath, dstpath, filename),
				}, jsonCheck(true))

				obj := getObject(t, s3client, bucket, filename)

				// assert copied object storage class
				if tc.storage == "s3" {
					assert.Equal(t, string(obj.StorageClass), "GLACIER")
				}

				assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))

				// assert local filesystem
				expected := fs.Expected(t, fs.WithFile(filename, content))
				assert.Assert(t, fs.Equal(workdir.Path(), expected))
			})
		}
	})
}
}

// cp --flatten dir/ s3://bucket/

func TestFlattenCopyDirToS3(t *testing.T) {
	t.Parallel()

	t.Run("FlattenCopyDirToS3", func(t *testing.T) {
		for _, tc := range testCases {
			tc := tc
			t.Run(tc.storage, func(t *testing.T) {
				t.Parallel()

				const (
					rootFile   = "file1.txt" 
					rootContent = "this is a test file"
					subFile     = "sub/file2.txt"
					subContent  = "this is another test file"
				)

				bucket := s3BucketFromTestName(t)

				s3client, s5cmd := setup(t)

				createBucket(t, s3client, bucket)

				folderLayout := []fs.PathOp{
					fs.WithFile(rootFile, rootContent),
					fs.WithDir("sub",
						fs.WithFile("file2.txt", subContent),
					),
				}

				workdir := fs.NewDir(t, tc.storage, folderLayout...)
				defer workdir.Remove()

				srcpath := filepath.ToSlash(workdir.Path()) + "/"
				dstpath := fmt.Sprintf("%v://%v/", tc.storage, bucket)

				cmd := s5cmd("cp", "--flatten", srcpath, dstpath)
				result := icmd.RunCmd(cmd)

				result.Assert(t, icmd.Success)

				assertLines(t, result.Stdout(), map[int]compareFunc{
					0: suffix(`cp %v %v%v`, filepath.ToSlash(workdir.Join(subFile)), dstpath, filepath.Base(subFile)),
					1: suffix(`cp %v %v%v`, filepath.ToSlash(workdir.Join(rootFile)), dstpath, rootFile),
				}, sortInput(true))

				// assert s3 objects
				assert.Assert(t, ensureS3Object(s3client, bucket, filepath.Base(subFile), subContent))
				assert.Assert(t, ensureS3Object(s3client, bucket, rootFile, rootContent))

				// assert local filesystem
				expected := fs.Expected(t, folderLayout...)
				assert.Assert(t, fs.Equal(workdir.Path(), expected))
			})
		}
	})
}
}

// cp dir/* s3://bucket/

func TestCopyMultipleFilesToS3Bucket(t *testing.T) {
	t.Parallel()
	t.Run("CopyMultipleFilesToS3Bucket", func(t *testing.T) {
		for _, tc := range testCases {
			tc := tc
			t.Run(tc.storage, func(t *testing.T) {
				t.Parallel()

				const (
					file1 = "testfile1.txt"
					file2 = "testfile2.txt"
					file3 = "testfile3.txt"
				)

				bucket := s3BucketFromTestName(t)

				s3client, s5cmd := setup(t)

				createBucket(t, s3client, bucket)

				workdir := fs.NewDir(t, tc.storage,
					fs.WithFile(file1, "content"),
					fs.WithFile(file2, "content"),
					fs.WithFile(file3, "content"),
				)
				defer workdir.Remove()

				srcpath := filepath.ToSlash(workdir.Path()) + "/*"
				dstpath := fmt.Sprintf("%v://%v", tc.storage, bucket)

				cmd := s5cmd("cp", srcpath, dstpath)
				result := icmd.RunCmd(cmd)

				result.Assert(t, icmd.Success)

				assertLines(t, result.Stdout(), map[int]compareFunc{
					0: suffix(`cp %v/testfile1.txt %v/testfile1.txt`, workdir.Path(), dstpath),
					1: suffix(`cp %v/testfile2.txt %v/testfile2.txt`, workdir.Path(), dstpath),
					2: suffix(`cp %v/testfile3.txt %v/testfile3.txt`, workdir.Path(), dstpath),
				})

				// assert local filesystem
				expected := fs.Expected(t,
					fs.WithFile(file1, "content"),
					fs.WithFile(file2, "content"),
					fs.WithFile(file3, "content"),
				)
				assert.Assert(t, fs.Equal(workdir.Path(), expected))

				// assert s3 objects
				assert.Assert(t, ensureS3Object(s3client, bucket, file1, "content"))
				assert.Assert(t, ensureS3Object(s3client, bucket, file2, "content"))
				assert.Assert(t, ensureS3Object(s3client, bucket, file3, "content"))
			})
		}
	})
}
}

// cp parent/*/name.txt s3://bucket/newfolder

func TestCopyMultipleFilesWithWildcardedDirectoryToS3Bucket(t *testing.T) {
	t.Parallel()
	t.Run("CopyMultipleFilesWithWildcardedDirectoryToS3Bucket", func(t *testing.T) {
		for _, tc := range testCases {
			tc := tc
			t.Run(tc.storage, func(t *testing.T) {
				t.Parallel()

				const (
					file1 = "parent1/sub/name.txt"
					file2 = "parent2/sub/name.txt" 
					file3 = "parent3/sub/name.txt"
					content = "this is a test file"
				)

				bucket := s3BucketFromTestName(t)

				s3client, s5cmd := setup(t)

				createBucket(t, s3client, bucket)

				folderLayout := []fs.PathOp{
					fs.WithDir("parent1", 
						fs.WithDir("sub",
							fs.WithFile("name.txt", content),
						),
					),
					fs.WithDir("parent2",
						fs.WithDir("sub", 
							fs.WithFile("name.txt", content),
						),
					),
					fs.WithDir("parent3",
						fs.WithDir("sub",
							fs.WithFile("name.txt", content),
						),
					),
				}

				workdir := fs.NewDir(t, tc.storage, folderLayout...)
				defer workdir.Remove()

				srcpath := filepath.ToSlash(workdir.Path()) + "/parent*/sub/name.txt"
				dstpath := fmt.Sprintf("%v://%v/newfolder/", tc.storage, bucket)

				cmd := s5cmd("cp", srcpath, dstpath)
				result := icmd.RunCmd(cmd)

				result.Assert(t, icmd.Success)

				assertLines(t, result.Stdout(), map[int]compareFunc{
					0: suffix(`cp %v/parent1/sub/name.txt %vnewfolder/parent1/sub/name.txt`, workdir.Path(), dstpath),
					1: suffix(`cp %v/parent2/sub/name.txt %vnewfolder/parent2/sub/name.txt`, workdir.Path(), dstpath),
					2: suffix(`cp %v/parent3/sub/name.txt %vnewfolder/parent3/sub/name.txt`, workdir.Path(), dstpath),
				})

				// assert local filesystem
				expected := fs.Expected(t, folderLayout...)
				assert.Assert(t, fs.Equal(workdir.Path(), expected))

				// assert s3 objects
				assert.Assert(t, ensureS3Object(s3client, bucket, "newfolder/"+file1, content))
				assert.Assert(t, ensureS3Object(s3client, bucket, "newfolder/"+file2, content))
				assert.Assert(t, ensureS3Object(s3client, bucket, "newfolder/"+file3, content))
			})
		}
	})
}
}

// cp parent/c*/name.txt s3://bucket/newfolder

func TestCopyMultipleFilesEndWildcardedToS3Bucket(t *testing.T) {
	t.Parallel()
	t.Run("CopyMultipleFilesEndWildcardedToS3Bucket", func(t *testing.T) {
		for _, tc := range testCases {
			tc := tc
			t.Run(tc.storage, func(t *testing.T) {
				t.Parallel()

				const (
					file1   = "parent/child1/name.txt"
					file2   = "parent/child2/name.txt"
					file3   = "parent/child3/name.txt"
					content = "this is a test file"
				)

				bucket := s3BucketFromTestName(t)

				s3client, s5cmd := setup(t)

				createBucket(t, s3client, bucket)

				folderLayout := []fs.PathOp{
					fs.WithDir("parent",
						fs.WithDir("child1",
							fs.WithFile("name.txt", content),
						),
						fs.WithDir("child2",
							fs.WithFile("name.txt", content),
						),
						fs.WithDir("child3",
							fs.WithFile("name.txt", content),
						),
					),
				}

				workdir := fs.NewDir(t, tc.storage, folderLayout...)
				defer workdir.Remove()

				srcpath := filepath.ToSlash(workdir.Path()) + "/parent/c*/name.txt"
				dstpath := fmt.Sprintf("%v://%v/newfolder/", tc.storage, bucket)

				cmd := s5cmd("cp", srcpath, dstpath)
				result := icmd.RunCmd(cmd)

				result.Assert(t, icmd.Success)

				assertLines(t, result.Stdout(), map[int]compareFunc{
					0: suffix(`cp %v/parent/child1/name.txt %vnewfolder/parent/child1/name.txt`, workdir.Path(), dstpath),
					1: suffix(`cp %v/parent/child2/name.txt %vnewfolder/parent/child2/name.txt`, workdir.Path(), dstpath),
					2: suffix(`cp %v/parent/child3/name.txt %vnewfolder/parent/child3/name.txt`, workdir.Path(), dstpath),
				})

				// assert local filesystem
				expected := fs.Expected(t, folderLayout...)
				assert.Assert(t, fs.Equal(workdir.Path(), expected))

				// assert s3 objects
				assert.Assert(t, ensureObject(s3client, bucket, "newfolder/"+file1, content, tc.storage))
				assert.Assert(t, ensureObject(s3client, bucket, "newfolder/"+file2, content, tc.storage))  
				assert.Assert(t, ensureObject(s3client, bucket, "newfolder/"+file3, content, tc.storage))
			})
		}
	})
}
}

// cp parent/c*1/name.txt s3://bucket/newfolder
func TestCopyMultipleFilesMiddleWildcardedDirectoryToS3Bucket(t *testing.T) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	folderLayout := []fs.PathOp{
		fs.WithDir("parent", fs.WithDir(
			"child1",
			fs.WithFile("name.txt", "A file in parent/child1/"),
		)),
	}

	workdir := fs.NewDir(t, "somedir", folderLayout...)
	dstpath := fmt.Sprintf("s3://%v/newfolder/", bucket)
	srcpath := workdir.Path()
	srcpath = filepath.ToSlash(srcpath)
	defer workdir.Remove()

	cmd := s5cmd("cp", srcpath+"/parent/c*1/name.txt", dstpath)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)
	rs := result.Stdout()
	assertLines(t, rs, map[int]compareFunc{
		0: equals(`cp %v/parent/child1/name.txt %vchild1/name.txt`, srcpath, dstpath),
	}, sortInput(true))

	// assert local filesystem
	expected := fs.Expected(t, folderLayout...)
	assert.Assert(t, fs.Equal(workdir.Path(), expected))
	expectedS3Content := map[string]string{
		"newfolder/child1/name.txt": "A file in parent/child1/",
	}

	// assert s3
	for filename, content := range expectedS3Content {
		assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
	}
}

// cp --flatten dir/* s3://bucket/
func TestFlattenCopyMultipleFilesToS3Bucket(t *testing.T) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
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
	dstpath := fmt.Sprintf("s3://%v/", bucket)
	srcpath := workdir.Path()
	srcpath = filepath.ToSlash(srcpath)
	defer workdir.Remove()

	cmd := s5cmd("cp", "--flatten", srcpath+"/*", dstpath)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %v/a/another_test_file.txt %vanother_test_file.txt`, srcpath, dstpath),
		1: equals(`cp %v/b/filename-with-hypen.gz %vfilename-with-hypen.gz`, srcpath, dstpath),
		2: equals(`cp %v/readme.md %vreadme.md`, srcpath, dstpath),
		3: equals(`cp %v/testfile1.txt %vtestfile1.txt`, srcpath, dstpath),
	}, sortInput(true))

	// assert local filesystem
	expected := fs.Expected(t, folderLayout...)
	assert.Assert(t, fs.Equal(workdir.Path(), expected))

	expectedS3Content := map[string]string{
		"testfile1.txt":          "this is a test file 1",
		"readme.md":              "this is a readme file",
		"filename-with-hypen.gz": "file has hypen in its name",
		"another_test_file.txt":  "yet another txt file. yatf.",
	}

	// assert s3
	for filename, content := range expectedS3Content {
		assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
	}
}

// cp dir/* s3://bucket/prefix (error)
func TestCopyMultipleFilesToS3WithPrefixWithoutSlash(t *testing.T) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
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

	src := fmt.Sprintf("%v/*", workdir.Path())
	src = filepath.ToSlash(src)
	dst := fmt.Sprintf("s3://%v/prefix", bucket)

	cmd := s5cmd("cp", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Expected{ExitCode: 1})

	assertLines(t, result.Stderr(), map[int]compareFunc{
		0: equals(`ERROR "cp %v %v": target %q must be a bucket or a prefix`, src, dst, dst),
	})

	// assert local filesystem
	expected := fs.Expected(t, folderLayout...)
	assert.Assert(t, fs.Equal(workdir.Path(), expected))
}

// cp prefix* s3://bucket/
func TestCopyDirectoryWithGlobCharactersToS3Bucket(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("Files in Windows cannot contain glob(*) characters")
	}

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	folderLayout := []fs.PathOp{
		fs.WithDir(
			"abc*",
			fs.WithFile("file1.txt", "this is the first test file"),
			fs.WithFile("file2.txt", "this is the second test file"),
		),
		fs.WithDir(
			"abcd",
			fs.WithFile("file1.txt", "this is the first test file"),
		),
		fs.WithDir(
			"abcde",
			fs.WithFile("file1.txt", "this is the first test file"),
			fs.WithFile("file2.txt", "this is the second test file"),
		),
	}

	workdir := fs.NewDir(t, "somedir", folderLayout...)
	defer workdir.Remove()

	src := fmt.Sprintf("%v/abc*", workdir.Path())
	dst := fmt.Sprintf("s3://%v", bucket)

	cmd := s5cmd("cp", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %v/abc*/file1.txt %v/abc*/file1.txt`, workdir.Path(), dst),
		1: equals(`cp %v/abc*/file2.txt %v/abc*/file2.txt`, workdir.Path(), dst),
		2: equals(`cp %v/abcd/file1.txt %v/abcd/file1.txt`, workdir.Path(), dst),
		3: equals(`cp %v/abcde/file1.txt %v/abcde/file1.txt`, workdir.Path(), dst),
		4: equals(`cp %v/abcde/file2.txt %v/abcde/file2.txt`, workdir.Path(), dst),
	}, sortInput(true))

	// assert local filesystem
	expected := fs.Expected(t, folderLayout...)
	assert.Assert(t, fs.Equal(workdir.Path(), expected))
}

// cp dir/* s3://bucket/prefix/
func TestCopyMultipleFilesToS3WithPrefixWithSlash(t *testing.T) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
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

	srcpath := workdir.Path()

	srcpath = filepath.ToSlash(srcpath)
	src := fmt.Sprintf("%v/*", srcpath)
	dst := fmt.Sprintf("s3://%v/prefix/", bucket)

	cmd := s5cmd("cp", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %v/a/another_test_file.txt %va/another_test_file.txt`, srcpath, dst),
		1: equals(`cp %v/b/filename-with-hypen.gz %vb/filename-with-hypen.gz`, srcpath, dst),
		2: equals(`cp %v/readme.md %vreadme.md`, srcpath, dst),
		3: equals(`cp %v/testfile1.txt %vtestfile1.txt`, srcpath, dst),
	}, sortInput(true))

	// assert local filesystem
	expected := fs.Expected(t, folderLayout...)
	assert.Assert(t, fs.Equal(workdir.Path(), expected))

	expectedS3Content := map[string]string{
		"prefix/testfile1.txt":            "this is a test file 1",
		"prefix/readme.md":                "this is a readme file",
		"prefix/b/filename-with-hypen.gz": "file has hypen in its name",
		"prefix/a/another_test_file.txt":  "yet another txt file. yatf.",
	}

	// assert s3
	for key, content := range expectedS3Content {
		assert.Assert(t, ensureS3Object(s3client, bucket, key, content))
	}
}

// cp --flatten dir/* s3://bucket/prefix/
func TestFlattenCopyMultipleFilesToS3WithPrefixWithSlash(t *testing.T) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
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

	srcpath := workdir.Path()

	srcpath = filepath.ToSlash(srcpath)
	src := fmt.Sprintf("%v/*", srcpath)
	dst := fmt.Sprintf("s3://%v/prefix/", bucket)

	cmd := s5cmd("cp", "--flatten", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %v/a/another_test_file.txt %vanother_test_file.txt`, srcpath, dst),
		1: equals(`cp %v/b/filename-with-hypen.gz %vfilename-with-hypen.gz`, srcpath, dst),
		2: equals(`cp %v/readme.md %vreadme.md`, srcpath, dst),
		3: equals(`cp %v/testfile1.txt %vtestfile1.txt`, srcpath, dst),
	}, sortInput(true))

	// assert local filesystem
	expected := fs.Expected(t, folderLayout...)
	assert.Assert(t, fs.Equal(workdir.Path(), expected))

	expectedS3Content := map[string]string{
		"prefix/testfile1.txt":          "this is a test file 1",
		"prefix/readme.md":              "this is a readme file",
		"prefix/filename-with-hypen.gz": "file has hypen in its name",
		"prefix/another_test_file.txt":  "yet another txt file. yatf.",
	}

	// assert s3
	for key, content := range expectedS3Content {
		assert.Assert(t, ensureS3Object(s3client, bucket, key, content))
	}
}

// cp dir/ s3://bucket/prefix/
func TestCopyLocalDirectoryToS3WithPrefixWithSlash(t *testing.T) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
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

	src := fmt.Sprintf("%v/", workdir.Path())
	dst := fmt.Sprintf("s3://%v/prefix/", bucket)

	src = filepath.ToSlash(src)
	cmd := s5cmd("cp", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %va/another_test_file.txt %va/another_test_file.txt`, src, dst),
		1: equals(`cp %vb/filename-with-hypen.gz %vb/filename-with-hypen.gz`, src, dst),
		2: equals(`cp %vreadme.md %vreadme.md`, src, dst),
		3: equals(`cp %vtestfile1.txt %vtestfile1.txt`, src, dst),
	}, sortInput(true))

	// assert local filesystem
	expected := fs.Expected(t, folderLayout...)
	assert.Assert(t, fs.Equal(workdir.Path(), expected))

	expectedS3Content := map[string]string{
		"prefix/testfile1.txt":            "this is a test file 1",
		"prefix/readme.md":                "this is a readme file",
		"prefix/b/filename-with-hypen.gz": "file has hypen in its name",
		"prefix/a/another_test_file.txt":  "yet another txt file. yatf.",
	}

	// assert s3
	for key, content := range expectedS3Content {
		assert.Assert(t, ensureS3Object(s3client, bucket, key, content))
	}
}

// cp --flatten dir/ s3://bucket/prefix/
func TestFlattenCopyLocalDirectoryToS3WithPrefixWithSlash(t *testing.T) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
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
			fs.WithDir(
				"c",
				fs.WithFile("filename-with-hypen.gz", "file has hypen in its name"),
			),
		),
	}

	workdir := fs.NewDir(t, "somedir", folderLayout...)
	defer workdir.Remove()

	src := fmt.Sprintf("%v/", workdir.Path())
	dst := fmt.Sprintf("s3://%v/prefix/", bucket)

	src = filepath.ToSlash(src)
	cmd := s5cmd("cp", "--flatten", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %va/another_test_file.txt %vanother_test_file.txt`, src, dst),
		1: equals(`cp %vb/c/filename-with-hypen.gz %vfilename-with-hypen.gz`, src, dst),
		2: equals(`cp %vreadme.md %vreadme.md`, src, dst),
		3: equals(`cp %vtestfile1.txt %vtestfile1.txt`, src, dst),
	}, sortInput(true))

	// assert local filesystem
	expected := fs.Expected(t, folderLayout...)
	assert.Assert(t, fs.Equal(workdir.Path(), expected))

	expectedS3Content := map[string]string{
		"prefix/testfile1.txt":          "this is a test file 1",
		"prefix/readme.md":              "this is a readme file",
		"prefix/filename-with-hypen.gz": "file has hypen in its name",
		"prefix/another_test_file.txt":  "yet another txt file. yatf.",
	}

	// assert s3
	for key, content := range expectedS3Content {
		assert.Assert(t, ensureS3Object(s3client, bucket, key, content))
	}
}

// cp dir/ s3://bucket/prefix
func TestCopyLocalDirectoryToS3WithPrefixWithoutSlash(t *testing.T) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	filesToContent := map[string]string{
		"testfile1.txt":          "this is a test file 1",
		"readme.md":              "this is a readme file",
		"filename-with-hypen.gz": "file has hypen in its name",
		"another_test_file.txt":  "yet another txt file. yatf.",
	}

	var files []fs.PathOp
	for filename, content := range filesToContent {
		op := fs.WithFile(filename, content)
		files = append(files, op)
	}

	workdir := fs.NewDir(t, "somedir", files...)
	defer workdir.Remove()

	src := fmt.Sprintf("%v/", workdir.Path())
	dst := fmt.Sprintf("s3://%v/prefix", bucket)

	src = filepath.ToSlash(src)
	cmd := s5cmd("cp", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Expected{ExitCode: 1})

	assertLines(t, result.Stderr(), map[int]compareFunc{
		0: equals(`ERROR "cp %v %v": target %q must be a bucket or a prefix`, src, dst, dst),
	})

	// assert local filesystem
	expected := fs.Expected(t, files...)
	assert.Assert(t, fs.Equal(workdir.Path(), expected))
}

// cp s3://bucket/object s3://bucket/object2
func TestCopySingleS3ObjectToS3(t *testing.T) {
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

	src := fmt.Sprintf("s3://%v/%v", bucket, filename)
	dst := fmt.Sprintf("s3://%v/%v", bucket, dstfilename)

	cmd := s5cmd("cp", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %v %v`, src, dst),
	})

	// assert s3 source object
	assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))

	// assert s3 destination object
	assert.Assert(t, ensureS3Object(s3client, bucket, dstfilename, content))
}

// --json cp s3://bucket/object s3://bucket2/object
func TestCopySingleS3ObjectToS3JSON(t *testing.T) {
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

	src := fmt.Sprintf("s3://%v/%v", bucket, filename)
	dst := fmt.Sprintf("s3://%v/%v", bucket, dstfilename)

	cmd := s5cmd("--json", "cp", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	jsonText := fmt.Sprintf(`
		{
			"operation":"cp",
			"success":true,
			"source":"%v",
			"destination":"%v",
			"object": {
				"key": "%v",
				"type":"file"
			}
		}
	`, src, dst, dst)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: json(jsonText),
	}, jsonCheck(true))

	// assert s3 source object
	assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))

	// assert s3 destination object
	assert.Assert(t, ensureS3Object(s3client, bucket, dstfilename, content))
}

// cp s3://bucket/object s3://bucket2/
func TestCopySingleS3ObjectIntoAnotherBucket(t *testing.T) {
	t.Parallel()

	srcbucket := s3BucketFromTestName(t)
	dstbucket := s3BucketFromTestNameWithPrefix(t, "copy")

	s3client, s5cmd := setup(t)

	createBucket(t, s3client, srcbucket)
	createBucket(t, s3client, dstbucket)

	const (
		filename = "testfile1.txt"
		content  = "this is a file content"
	)

	putFile(t, s3client, srcbucket, filename, content)

	src := fmt.Sprintf("s3://%v/%v", srcbucket, filename)
	dst := fmt.Sprintf("s3://%v/", dstbucket)

	cmd := s5cmd("cp", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %v %v%v`, src, dst, filename),
	})

	// assert s3 source object
	assert.Assert(t, ensureS3Object(s3client, srcbucket, filename, content))

	// assert s3 destination object
	assert.Assert(t, ensureS3Object(s3client, dstbucket, filename, content))
}

// cp --flatten s3://bucket/object s3://bucket2/
func TestFlattenCopySingleS3ObjectIntoAnotherBucket(t *testing.T) {
	t.Parallel()

	srcbucket := s3BucketFromTestName(t)
	dstbucket := s3BucketFromTestNameWithPrefix(t, "copy")

	s3client, s5cmd := setup(t)

	createBucket(t, s3client, srcbucket)
	createBucket(t, s3client, dstbucket)

	const (
		filename = "testfile1.txt"
		content  = "this is a file content"
	)

	putFile(t, s3client, srcbucket, filename, content)

	src := fmt.Sprintf("s3://%v/%v", srcbucket, filename)
	dst := fmt.Sprintf("s3://%v/", dstbucket)

	cmd := s5cmd("cp", "--flatten", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %v %v%v`, src, dst, filename),
	})

	// assert s3 source object
	assert.Assert(t, ensureS3Object(s3client, srcbucket, filename, content))

	// assert s3 destination object
	assert.Assert(t, ensureS3Object(s3client, dstbucket, filename, content))
}

// cp s3://bucket/object s3://bucket2/object
func TestCopySingleS3ObjectIntoAnotherBucketWithObjName(t *testing.T) {
	t.Parallel()

	srcbucket := s3BucketFromTestNameWithPrefix(t, "src")
	dstbucket := s3BucketFromTestNameWithPrefix(t, "dst")

	s3client, s5cmd := setup(t)

	createBucket(t, s3client, srcbucket)
	createBucket(t, s3client, dstbucket)

	const (
		filename = "testfile1.txt"
		content  = "this is a file content"
	)

	putFile(t, s3client, srcbucket, filename, content)

	src := fmt.Sprintf("s3://%v/%v", srcbucket, filename)
	dst := fmt.Sprintf("s3://%v/%v", dstbucket, filename)

	cmd := s5cmd("cp", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %v %v`, src, dst),
	})

	// assert s3 source object
	assert.Assert(t, ensureS3Object(s3client, srcbucket, filename, content))

	// assert s3 destination object
	assert.Assert(t, ensureS3Object(s3client, dstbucket, filename, content))
}

// cp s3://bucket/object s3://bucket2/prefix/
func TestCopySingleS3ObjectIntoAnotherBucketWithPrefix(t *testing.T) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const (
		filename = "testfile1.txt"
		content  = "this is a file content"
	)

	putFile(t, s3client, bucket, filename, content)

	src := fmt.Sprintf("s3://%v/%v", bucket, filename)
	dst := fmt.Sprintf("s3://%v/prefix/%v", bucket, filename)

	cmd := s5cmd("cp", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %v %v`, src, dst),
	})

	// assert s3 source object
	assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))

	// assert s3 destination object
	assert.Assert(t, ensureS3Object(s3client, bucket, "prefix/"+filename, content))
}

// cp s3://bucket/* s3://dstbucket/
func TestCopyAllObjectsIntoAnotherBucketIncludingSpecialCharacter(t *testing.T) {
	t.Parallel()

	// This test fails with GCS as it accepts percent encoding
	// for `X-Amz-Copy-Source` and does not respect `+` as `space character`.
	// skip it as it is not planned to write a workaround for GCS.
	skipTestIfGCS(t, "GCS does not respect `+` as `space character` in `X-Amz-Copy-Source`")

	srcbucket := s3BucketFromTestNameWithPrefix(t, "src")
	dstbucket := s3BucketFromTestNameWithPrefix(t, "dst")

	s3client, s5cmd := setup(t)

	createBucket(t, s3client, srcbucket)
	createBucket(t, s3client, dstbucket)

	filesToContent := map[string]string{
		"sub&@$/test+1.txt":           "this is a test file 1",
		"sub:,?/test; =2.txt":         "this is a test file 2",
		"test&@$:,?;= 3.txt":          "this is a test file 3",
		"sub///test&@$:,?;= 4.txt":    "this is a test file with adjacent slashes",
		"sub/this-is-normal-file.txt": "this is a normal file",
	}

	for filename, content := range filesToContent {
		putFile(t, s3client, srcbucket, filename, content)
	}

	src := fmt.Sprintf("s3://%v/*", srcbucket)
	dst := fmt.Sprintf("s3://%v/", dstbucket)

	cmd := s5cmd("cp", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)
	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp s3://%v/sub&@$/test+1.txt s3://%v/sub&@$/test+1.txt`, srcbucket, dstbucket),
		1: equals(`cp s3://%v/sub///test&@$:,?;= 4.txt s3://%v/sub///test&@$:,?;= 4.txt`, srcbucket, dstbucket),
		2: equals(`cp s3://%v/sub/this-is-normal-file.txt s3://%v/sub/this-is-normal-file.txt`, srcbucket, dstbucket),
		3: equals(`cp s3://%v/sub:,?/test; =2.txt s3://%v/sub:,?/test; =2.txt`, srcbucket, dstbucket),
		4: equals(`cp s3://%v/test&@$:,?;= 3.txt s3://%v/test&@$:,?;= 3.txt`, srcbucket, dstbucket),
	}, sortInput(true))
}

// cp s3://bucket/* s3://bucket/prefix/
func TestCopyMultipleS3ObjectsToS3WithPrefix(t *testing.T) {
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

	src := fmt.Sprintf("s3://%v/*", bucket)
	dst := fmt.Sprintf("s3://%v/dst/", bucket)

	cmd := s5cmd("cp", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp s3://%v/a/another_test_file.txt %va/another_test_file.txt`, bucket, dst),
		1: equals(`cp s3://%v/b/filename-with-hypen.gz %vb/filename-with-hypen.gz`, bucket, dst),
		2: equals(`cp s3://%v/readme.md %vreadme.md`, bucket, dst),
		3: equals(`cp s3://%v/testfile1.txt %vtestfile1.txt`, bucket, dst),
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

	src := fmt.Sprintf("s3://%v/*", bucket)
	dst := fmt.Sprintf("s3://%v/dst/", bucket)

	cmd := s5cmd("cp", "--flatten", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp s3://%v/a/another_test_file.txt %vanother_test_file.txt`, bucket, dst),
		1: equals(`cp s3://%v/b/filename-with-hypen.gz %vfilename-with-hypen.gz`, bucket, dst),
		2: equals(`cp s3://%v/readme.md %vreadme.md`, bucket, dst),
		3: equals(`cp s3://%v/testfile1.txt %vtestfile1.txt`, bucket, dst),
	}, sortInput(true))

	// assert s3 source objects
	for filename, content := range filesToContent {
		assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
	}

	dstContent := map[string]string{
		"dst/testfile1.txt":          "this is a test file 1",
		"dst/readme.md":              "this is a readme file",
		"dst/filename-with-hypen.gz": "file has hypen in its name",
		"dst/another_test_file.txt":  "yet another txt file. yatf.",
	}

	// assert s3 destination objects
	for key, content := range dstContent {
		assert.Assert(t, ensureS3Object(s3client, bucket, key, content))
	}
}

// cp s3://bucket/* s3://bucket/prefix
func TestCopyMultipleS3ObjectsToS3WithPrefixWithoutSlash(t *testing.T) {
	t.Parallel()

	bucket := s3BucketFromTestName(t)
	s3client, s5cmd := setup(t)

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

	src := fmt.Sprintf("s3://%v/*", bucket)
	dst := fmt.Sprintf("s3://%v/dst", bucket)

	cmd := s5cmd("cp", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Expected{ExitCode: 1})

	assertLines(t, result.Stderr(), map[int]compareFunc{
		0: equals(`ERROR "cp %v %v": target %q must be a bucket or a prefix`, src, dst, dst),
	})

	// assert s3 source objects
	for filename, content := range filesToContent {
		assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
	}

}

// --json cp s3://bucket/* s3://bucket/prefix/
func TestCopyMultipleS3ObjectsToS3JSON(t *testing.T) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	filesToContent := map[string]string{
		"testfile1.txt": "this is a test file 1",
		"readme.md":     "this is a readme file",
	}

	for filename, content := range filesToContent {
		putFile(t, s3client, bucket, filename, content)
	}

	src := fmt.Sprintf("s3://%v/*", bucket)
	dst := fmt.Sprintf("s3://%v/dst/", bucket)

	cmd := s5cmd("--json", "cp", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: json(`
			{
				"operation": "cp",
				"success": true,
				"source": "s3://%v/readme.md",
				"destination": "s3://%v/dst/readme.md",
				"object": {
					"key": "s3://%v/dst/readme.md",
					"type": "file"
				}
			}
		`, bucket, bucket, bucket),
		1: json(`
			{
				"operation": "cp",
				"success": true,
				"source": "s3://%v/testfile1.txt",
				"destination": "s3://%v/dst/testfile1.txt",
				"object": {
					"key": "s3://%v/dst/testfile1.txt",
					"type": "file"
				}
			}
		`, bucket, bucket, bucket),
	}, sortInput(true), jsonCheck(true))

	// assert s3 source objects
	for filename, content := range filesToContent {
		assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
	}

	// assert s3 destination objects
	for filename, content := range filesToContent {
		assert.Assert(t, ensureS3Object(s3client, bucket, "dst/"+filename, content))
	}
}

// cp -u -s s3://bucket/prefix/* s3://bucket/prefix2/
func TestCopyMultipleS3ObjectsToS3_Issue70(t *testing.T) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	filesToContent := map[string]string{
		"config/.local/folder1/file1.txt": "this is a test file 1",
		"config/.local/folder2/file2.txt": "this is a test file 2",
	}

	for filename, content := range filesToContent {
		putFile(t, s3client, bucket, filename, content)
	}

	src := fmt.Sprintf("s3://%v/config/.local/*", bucket)
	dst := fmt.Sprintf("s3://%v/.local/", bucket)

	cmd := s5cmd("cp", "-u", "-s", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp s3://%v/config/.local/folder1/file1.txt %vfolder1/file1.txt`, bucket, dst),
		1: equals(`cp s3://%v/config/.local/folder2/file2.txt %vfolder2/file2.txt`, bucket, dst),
	}, sortInput(true))

	// assert s3 source objects
	for filename, content := range filesToContent {
		assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
	}

	// assert s3 destination objects
	assert.Assert(t, ensureS3Object(s3client, bucket, ".local/folder1/file1.txt", "this is a test file 1"))
	assert.Assert(t, ensureS3Object(s3client, bucket, ".local/folder2/file2.txt", "this is a test file 2"))
}

// cp s3://bucket/object dir/ (dirobject exists)
func TestCopyS3ObjectToLocalWithTheSameFilename(t *testing.T) {
	t.Parallel()

	bucket := s3BucketFromTestName(t)

	s3client, s5cmd := setup(t)

	const (
		filename        = "testfile1.txt"
		content         = "this is the content"
		expectedContent = content + "\n"
	)

	workdir := fs.NewDir(t, t.Name(), fs.WithFile(filename, content))
	defer workdir.Remove()

	createBucket(t, s3client, bucket)
	// upload a modified version of the file
	putFile(t, s3client, bucket, filename, expectedContent)

	cmd := s5cmd("cp", "s3://"+bucket+"/"+filename, ".")
	result := icmd.RunCmd(cmd, withWorkingDir(workdir))

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals("cp s3://%v/%v %v", bucket, filename, filename),
	})

	expected := fs.Expected(t, fs.WithFile(filename, expectedContent))
	assert.Assert(t, fs.Equal(workdir.Path(), expected))
}

// -log=debug cp -n s3://bucket/object .
func TestCopyS3ToLocalWithSameFilenameWithNoClobber(t *testing.T) {
	t.Parallel()

	bucket := s3BucketFromTestName(t)

	s3client, s5cmd := setup(t)

	const (
		filename = "testfile1.txt"
		content  = "this is the content"
	)

	workdir := fs.NewDir(t, t.Name(), fs.WithFile(filename, content))
	defer workdir.Remove()

	createBucket(t, s3client, bucket)
	// upload a modified version of the file
	putFile(t, s3client, bucket, filename, content+"\n")

	cmd := s5cmd("--log=debug", "cp", "-n", "s3://"+bucket+"/"+filename, ".")
	result := icmd.RunCmd(cmd, withWorkingDir(workdir))

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`DEBUG "cp s3://%v/%v %v": object already exists`, bucket, filename, filename),
	})

	assertLines(t, result.Stderr(), map[int]compareFunc{})

	expected := fs.Expected(t, fs.WithFile(filename, content))
	assert.Assert(t, fs.Equal(workdir.Path(), expected))
}

// cp -n -s s3://bucket/object dir/
func TestCopyS3ToLocalWithSameFilenameOverrideIfSizeDiffers(t *testing.T) {
	t.Parallel()

	bucket := s3BucketFromTestName(t)

	s3client, s5cmd := setup(t)

	const (
		filename        = "testfile1.txt"
		content         = "this is the content"
		expectedContent = content + "\n"
	)

	workdir := fs.NewDir(t, t.Name(), fs.WithFile(filename, content))
	defer workdir.Remove()

	createBucket(t, s3client, bucket)
	// upload a modified version of the file
	putFile(t, s3client, bucket, filename, expectedContent)

	cmd := s5cmd("cp", "-n", "-s", "s3://"+bucket+"/"+filename, ".")
	result := icmd.RunCmd(cmd, withWorkingDir(workdir))

	// '-n' prevents overriding the file, but '-s' overrides '-n' if the file
	// size differs.
	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp s3://%v/%v %v`, bucket, filename, filename),
	})

	expected := fs.Expected(t, fs.WithFile(filename, expectedContent))
	assert.Assert(t, fs.Equal(workdir.Path(), expected))
}

// cp -n -u s3://bucket/object dir/ (source is newer)
func TestCopyS3ToLocalWithSameFilenameOverrideIfSourceIsNewer(t *testing.T) {
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

	cmd := s5cmd("cp", "-n", "-u", "s3://"+bucket+"/"+filename, ".")
	result := icmd.RunCmd(cmd, withWorkingDir(workdir))

	// '-n' prevents overriding the file, but '-s' overrides '-n' if the file
	// size differs.
	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp s3://%v/%v %v`, bucket, filename, filename),
	})

	expected := fs.Expected(t, fs.WithFile(filename, expectedContent))
	assert.Assert(t, fs.Equal(workdir.Path(), expected))
}

// cp -n -u s3://bucket/object dir/ (source is older)
func TestCopyS3ToLocalWithSameFilenameDontOverrideIfS3ObjectIsOlder(t *testing.T) {
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

	cmd := s5cmd("--log=debug", "cp", "-n", "-u", "s3://"+bucket+"/"+filename, ".")
	result := icmd.RunCmd(cmd, withWorkingDir(workdir))

	// '-n' prevents overriding the file, but '-s' overrides '-n' if the file
	// size differs.
	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`DEBUG "cp s3://%v/%v %v": object is newer or same age`, bucket, filename, filename),
	})

	assertLines(t, result.Stderr(), map[int]compareFunc{})

	expected := fs.Expected(t, fs.WithFile(filename, content))
	assert.Assert(t, fs.Equal(workdir.Path(), expected))
}

// cp -u -s s3://bucket/prefix/* dir/
func TestCopyS3ToLocal_Issue70(t *testing.T) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	filesToContent := map[string]string{
		"config/.local/folder1/file1.txt": "this is a test file 1",
		"config/.local/folder2/file2.txt": "this is a test file 2",
	}

	for filename, content := range filesToContent {
		putFile(t, s3client, bucket, filename, content)
	}

	workdir := fs.NewDir(t, t.Name())
	defer workdir.Remove()

	srcpath := fmt.Sprintf("s3://%v/config/.local/*", bucket)
	dstpath := filepath.Join(workdir.Path(), ".local")

	dstpath = filepath.ToSlash(dstpath)
	cmd := s5cmd("cp", "-u", "-s", srcpath, dstpath)

	result := icmd.RunCmd(cmd, withWorkingDir(workdir))

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp s3://%v/config/.local/folder1/file1.txt %v/folder1/file1.txt`, bucket, dstpath),
		1: equals(`cp s3://%v/config/.local/folder2/file2.txt %v/folder2/file2.txt`, bucket, dstpath),
	}, sortInput(true))

	// assert local filesystem
	expectedFiles := []fs.PathOp{
		fs.WithDir(
			".local",
			fs.WithMode(0755),
			fs.WithDir("folder1", fs.WithMode(0755), fs.WithFile("file1.txt", "this is a test file 1")),
			fs.WithDir("folder2", fs.WithMode(0755), fs.WithFile("file2.txt", "this is a test file 2")),
		),
	}

	expectedResult := fs.Expected(t, expectedFiles...)
	assert.Assert(t, fs.Equal(workdir.Path(), expectedResult))

	// assert s3 objects
	for filename, content := range filesToContent {
		assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
	}
}

// cp file s3://bucket (bucket/file exists)
func TestCopyLocalFileToS3WithTheSameFilename(t *testing.T) {
	t.Parallel()

	bucket := s3BucketFromTestName(t)

	s3client, s5cmd := setup(t)

	const (
		filename   = "testfile1.txt"
		content    = "this is the content"
		newContent = content + "\n"
	)

	createBucket(t, s3client, bucket)
	putFile(t, s3client, bucket, filename, content)

	// the file to be uploaded is modified
	workdir := fs.NewDir(t, t.Name(), fs.WithFile(filename, newContent))
	defer workdir.Remove()

	dst := "s3://" + bucket
	cmd := s5cmd("cp", filename, dst)
	result := icmd.RunCmd(cmd, withWorkingDir(workdir))

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %v %v/%v`, filename, dst, filename),
	})

	// assert local filesystem
	expected := fs.Expected(t, fs.WithFile(filename, newContent))
	assert.Assert(t, fs.Equal(workdir.Path(), expected))

	// expect s3 object to be updated with new content
	assert.Assert(t, ensureS3Object(s3client, bucket, filename, newContent))
}

// -log=debug cp -n file s3://bucket (bucket/file exists)
func TestCopyLocalFileToS3WithSameFilenameWithNoClobber(t *testing.T) {
	t.Parallel()

	bucket := s3BucketFromTestName(t)

	s3client, s5cmd := setup(t)

	const (
		filename   = "testfile1.txt"
		content    = "this is the content"
		newContent = content + "\n"
	)

	createBucket(t, s3client, bucket)
	putFile(t, s3client, bucket, filename, content)

	// the file to be uploaded is modified
	workdir := fs.NewDir(t, t.Name(), fs.WithFile(filename, newContent))
	defer workdir.Remove()

	cmd := s5cmd("--log=debug", "cp", "-n", filename, "s3://"+bucket)
	result := icmd.RunCmd(cmd, withWorkingDir(workdir))

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`DEBUG "cp %v s3://%v/%v": object already exists`, filename, bucket, filename),
	})

	assertLines(t, result.Stderr(), map[int]compareFunc{})

	// assert local filesystem
	expected := fs.Expected(t, fs.WithFile(filename, newContent))
	assert.Assert(t, fs.Equal(workdir.Path(), expected))

	// expect s3 object is not overridden
	assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
}

// cp -n file s3://bucket
func TestCopyLocalFileToS3WithNoClobber(t *testing.T) {
	t.Parallel()

	bucket := s3BucketFromTestName(t)

	s3client, s5cmd := setup(t)

	const (
		filename   = "testfile1.txt"
		content    = "this is the content"
		newContent = content + "\n"
	)

	createBucket(t, s3client, bucket)

	// the file to be uploaded is modified
	workdir := fs.NewDir(t, t.Name(), fs.WithFile(filename, newContent))
	defer workdir.Remove()

	dst := "s3://" + bucket
	cmd := s5cmd("cp", "-n", filename, dst)
	result := icmd.RunCmd(cmd, withWorkingDir(workdir))

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %v %v/%v`, filename, dst, filename),
	})

	assertLines(t, result.Stderr(), map[int]compareFunc{})

	// assert local filesystem
	expected := fs.Expected(t, fs.WithFile(filename, newContent))
	assert.Assert(t, fs.Equal(workdir.Path(), expected))

	// expect s3 object is not overridden
	assert.Assert(t, ensureS3Object(s3client, bucket, filename, newContent))
}

// cp -n -s file s3://bucket (bucket/file exists)
func TestCopyLocalFileToS3WithSameFilenameOverrideIfSizeDiffers(t *testing.T) {
	t.Parallel()

	bucket := s3BucketFromTestName(t)

	s3client, s5cmd := setup(t)

	const (
		filename        = "testfile1.txt"
		content         = "this is the content"
		expectedContent = content + "\n"
	)

	workdir := fs.NewDir(t, t.Name(), fs.WithFile(filename, expectedContent))
	defer workdir.Remove()

	createBucket(t, s3client, bucket)
	// upload a modified version of the file
	putFile(t, s3client, bucket, filename, content)

	dst := "s3://" + bucket
	cmd := s5cmd("cp", "-n", "-s", filename, dst)
	result := icmd.RunCmd(cmd, withWorkingDir(workdir))

	// '-n' prevents overriding the file, but '-s' overrides '-n' if the file
	// size differs.
	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %v %v/%v`, filename, dst, filename),
	})

	assertLines(t, result.Stderr(), map[int]compareFunc{})

	assert.NilError(t, ensureS3Object(s3client, bucket, filename, expectedContent))
}

// cp -n -u file s3://bucket (bucket/file exists, source is newer)
func TestCopyLocalFileToS3WithSameFilenameOverrideIfSourceIsNewer(t *testing.T) {
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
		now.Add(time.Minute), // access time
		now.Add(time.Minute), // mod time
	)
	workdir := fs.NewDir(t, t.Name(), fs.WithFile(filename, expectedContent, timestamp))
	defer workdir.Remove()

	dst := "s3://" + bucket
	cmd := s5cmd("cp", "-n", "-u", filename, dst)
	result := icmd.RunCmd(cmd, withWorkingDir(workdir))

	// '-n' prevents overriding the file, but '-u' overrides '-n' if the file
	// modtime differs.
	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %v %v/%v`, filename, dst, filename),
	})

	assertLines(t, result.Stderr(), map[int]compareFunc{})

	assert.NilError(t, ensureS3Object(s3client, bucket, filename, expectedContent))
}

// cp -n -u file s3://bucket (bucket/file exists, source is older)
func TestCopyLocalFileToS3WithSameFilenameDontOverrideIfS3ObjectIsOlder(t *testing.T) {
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

	cmd := s5cmd("--log=debug", "cp", "-n", "-u", filename, "s3://"+bucket)
	result := icmd.RunCmd(cmd, withWorkingDir(workdir))

	// '-n' prevents overriding the file, but '-u' overrides '-n' if the file
	// modtime differs.
	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`DEBUG "cp %v s3://%v/%v": object is newer or same age`, filename, bucket, filename),
	})

	assertLines(t, result.Stderr(), map[int]compareFunc{})

	assert.NilError(t, ensureS3Object(s3client, bucket, filename, content))
}

// cp file s3://bucket/
func TestCopyLocalFileToS3WithFilePermissions(t *testing.T) {
	t.Parallel()

	bucket := s3BucketFromTestName(t)
	s3client, s5cmd := setup(t)

	createBucket(t, s3client, bucket)

	const (
		filename = "testfile1.txt"
		content  = "this is the content"
	)

	fileModes := []os.FileMode{0400, 0440, 0444, 0600, 0640, 0644, 0700, 0750, 0755}

	for _, fileMode := range fileModes {

		workdir := fs.NewDir(t, t.Name(), fs.WithFile(filename, content, fs.WithMode(fileMode)))
		defer workdir.Remove()

		dstpath := fmt.Sprintf("s3://%v/%v", bucket, filename)

		cmd := s5cmd("cp", filename, dstpath)
		result := icmd.RunCmd(cmd, withWorkingDir(workdir))

		result.Assert(t, icmd.Success)

		assertLines(t, result.Stdout(), map[int]compareFunc{
			0: equals(`cp %v %v`, filename, dstpath),
		})

		// assert local filesystem
		expected := fs.Expected(t, fs.WithFile(filename, content, fs.WithMode(fileMode)))
		assert.Assert(t, fs.Equal(workdir.Path(), expected))

		// assert s3 object
		assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
	}
}

// cp file s3://bucket/object
func TestCopyLocalFileToS3WithCustomName(t *testing.T) {
	t.Parallel()

	bucket := s3BucketFromTestName(t)

	s3client, s5cmd := setup(t)

	const (
		filename = "testfile1.txt"
		content  = "this is the content"
	)

	createBucket(t, s3client, bucket)

	workdir := fs.NewDir(t, t.Name(), fs.WithFile(filename, content))
	defer workdir.Remove()

	dstpath := fmt.Sprintf("s3://%v/%v", bucket, filename)

	cmd := s5cmd("cp", filename, dstpath)
	result := icmd.RunCmd(cmd, withWorkingDir(workdir))

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %v %v`, filename, dstpath),
	})

	// assert local filesystem
	expected := fs.Expected(t, fs.WithFile(filename, content))
	assert.Assert(t, fs.Equal(workdir.Path(), expected))

	// assert s3 object
	assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
}

// cp file s3://bucket/prefix/
func TestCopyLocalFileToS3WithPrefix(t *testing.T) {
	t.Parallel()

	bucket := s3BucketFromTestName(t)

	s3client, s5cmd := setup(t)

	const (
		filename = "testfile1.txt"
		content  = "this is the content"
	)

	createBucket(t, s3client, bucket)

	workdir := fs.NewDir(t, t.Name(), fs.WithFile(filename, content))
	defer workdir.Remove()

	dstpath := fmt.Sprintf("s3://%v/s5cmdtest/", bucket)

	cmd := s5cmd("cp", filename, dstpath)
	result := icmd.RunCmd(cmd, withWorkingDir(workdir))

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %v %v%v`, filename, dstpath, filename),
	})

	// assert local filesystem
	expected := fs.Expected(t, fs.WithFile(filename, content))
	assert.Assert(t, fs.Equal(workdir.Path(), expected))

	// assert s3 object
	assert.Assert(t, ensureS3Object(s3client, bucket, fmt.Sprintf("s5cmdtest/%v", filename), content))
}

// cp file s3://bucket
func TestMultipleLocalFileToS3Bucket(t *testing.T) {
	t.Parallel()

	bucket := s3BucketFromTestName(t)

	s3client, s5cmd := setup(t)

	const (
		filename = "testfile1.txt"
		content  = "this is the content"
	)

	createBucket(t, s3client, bucket)

	workdir := fs.NewDir(t, t.Name(), fs.WithFile(filename, content))
	defer workdir.Remove()

	dstpath := fmt.Sprintf("s3://%v", bucket)

	cmd := s5cmd("cp", filename, dstpath)
	result := icmd.RunCmd(cmd, withWorkingDir(workdir))

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %v %v/%v`, filename, dstpath, filename),
	})

	// assert local filesystem
	expected := fs.Expected(t, fs.WithFile(filename, content))
	assert.Assert(t, fs.Equal(workdir.Path(), expected))

	// assert s3 object
	assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
}

// cp * s3://bucket/prefix/
func TestCopyMultipleLocalNestedFilesToS3(t *testing.T) {
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

	dst := fmt.Sprintf("s3://%v/prefix/", bucket)

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
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	fileContent := "CAFEBABE"
	folderLayout := []fs.PathOp{
		fs.WithDir(
			"a",
			fs.WithFile("f1.txt", fileContent),
		),
		fs.WithDir("b"),
		fs.WithSymlink("b/my_link", "a/f1.txt"),
	}

	workdir := fs.NewDir(t, t.Name(), folderLayout...)
	defer workdir.Remove()

	dst := fmt.Sprintf("s3://%v/prefix/", bucket)

	cmd := s5cmd("cp", "--no-follow-symlinks", "b/my_link", dst)
	result := icmd.RunCmd(cmd, withWorkingDir(workdir))

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{})
}

// cp * s3://bucket/prefix/
func TestCopyWithFollowSymlink(t *testing.T) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	fileContent := "CAFEBABE"
	folderLayout := []fs.PathOp{
		fs.WithDir(
			"a",
			fs.WithFile("f1.txt", fileContent),
		),
		fs.WithDir("b"),
		fs.WithDir("c"),
		fs.WithSymlink("b/link1", "a/f1.txt"),
		fs.WithSymlink("c/link2", "b/link1"),
	}

	workdir := fs.NewDir(t, t.Name(), folderLayout...)
	defer workdir.Remove()

	dst := fmt.Sprintf("s3://%v/prefix/", bucket)

	cmd := s5cmd("cp", "*", dst)
	result := icmd.RunCmd(cmd, withWorkingDir(workdir))

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals("cp a/f1.txt %va/f1.txt", dst),
		1: equals("cp b/link1 %vb/link1", dst),
		2: equals("cp c/link2 %vc/link2", dst),
	}, sortInput(true))

	// assert s3 objects
	assert.Assert(t, ensureS3Object(s3client, bucket, "prefix/a/f1.txt", fileContent))
	assert.Assert(t, ensureS3Object(s3client, bucket, "prefix/b/link1", fileContent))
	assert.Assert(t, ensureS3Object(s3client, bucket, "prefix/c/link2", fileContent))
}

func TestCopyErrorWhenGivenObjectIsNotFoundUsingWildcard(t *testing.T) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	folderLayout := []fs.PathOp{
		// we intentionally did not create a/f1.txt to
		// trigger given object not found error.
		fs.WithDir("b"),
		fs.WithSymlink("b/link1", "a/f1.txt"),
	}

	workdir := fs.NewDir(t, t.Name(), folderLayout...)
	defer workdir.Remove()

	dst := fmt.Sprintf("s3://%v/prefix/", bucket)

	cmd := s5cmd("cp", "*", dst)
	result := icmd.RunCmd(cmd, withWorkingDir(workdir))

	result.Assert(t, icmd.Expected{ExitCode: 1})

	assertLines(t, result.Stderr(), map[int]compareFunc{
		0: equals(`ERROR "cp * %v": given object b/link1 not found`, dst),
	}, sortInput(true))
}

// cp --no-follow-symlinks * s3://bucket/prefix/
func TestCopyWithNoFollowSymlink(t *testing.T) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	fileContent := "CAFEBABE"
	folderLayout := []fs.PathOp{
		fs.WithDir(
			"a",
			fs.WithFile("f1.txt", fileContent),
		),
		fs.WithDir("b"),
		fs.WithDir("c"),
		fs.WithSymlink("b/link1", "a/f1.txt"),
		fs.WithSymlink("c/link2", "b/link1"),
	}

	workdir := fs.NewDir(t, t.Name(), folderLayout...)
	defer workdir.Remove()

	dst := fmt.Sprintf("s3://%v/prefix/", bucket)

	cmd := s5cmd("cp", "--no-follow-symlinks", "*", dst)
	result := icmd.RunCmd(cmd, withWorkingDir(workdir))

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals("cp a/f1.txt %va/f1.txt", dst),
	}, sortInput(true))

	// assert s3 objects
	assert.Assert(t, ensureS3Object(s3client, bucket, "prefix/a/f1.txt", fileContent))
}

// --dry-run cp dir/ s3://bucket/
func TestCopyDirToS3DryRun(t *testing.T) {
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
	dstpath := fmt.Sprintf("s3://%v/", bucket)

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
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	files := [...]string{"c/file2.txt", "file1.txt"}

	putFile(t, s3client, bucket, files[0], "content")
	putFile(t, s3client, bucket, files[1], "content")

	srcpath := fmt.Sprintf("s3://%s", bucket)

	cmd := s5cmd("--dry-run", "cp", srcpath+"/*", "dir/")
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals("cp %v/c/file2.txt dir/%s", srcpath, files[0]),
		1: equals("cp %v/file1.txt dir/%s", srcpath, files[1]),
	}, sortInput(true))

	// not even outermost directory should be created
	_, err := os.Stat(cmd.Dir + "/dir")
	assert.Assert(t, os.IsNotExist(err))

	// assert s3
	for _, f := range files {
		assert.Assert(t, ensureS3Object(s3client, bucket, f, "content"))
	}
}

func TestCopyLocalObjectstoS3WithRawFlag(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}

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
			name: "cp --raw file*.txt s3://bucket/",
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
			name: "cp  file*.txt s3://bucket/",
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
			name: "cp  a*/file*.txt s3://bucket/",
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
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			bucket := s3BucketFromTestName(t)

			s3client, s5cmd := setup(t)

			createBucket(t, s3client, bucket)

			workdir := fs.NewDir(t, "copy-raw-test", tc.src...)
			defer workdir.Remove()

			srcpath := filepath.ToSlash(workdir.Join(tc.wantedFile))
			dst := fmt.Sprintf("s3://%v", bucket)

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
func TestCopyDirToS3WithRawFlag(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}

	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	folderLayout := []fs.PathOp{
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

		fs.WithFile("file*4.txt", "content"),
	}

	workdir := fs.NewDir(t, t.Name(), folderLayout...)
	defer workdir.Remove()

	srcpath := filepath.ToSlash(workdir.Join("a*"))
	dstpath := fmt.Sprintf("s3://%v", bucket)

	cmd := s5cmd("cp", "--raw", srcpath, dstpath)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals("cp %v/file*.txt %v/a*/file*.txt", srcpath, dstpath),
		1: equals("cp %v/file*1.txt %v/a*/file*1.txt", srcpath, dstpath),
	}, sortInput(true))

	expectedObjs := []string{"a*/file*.txt", "a*/file*1.txt"}
	for _, obj := range expectedObjs {
		err := ensureS3Object(s3client, bucket, obj, "content")
		if err != nil {
			t.Fatalf("Object %s is not in S3\n", obj)
		}
	}

	nonExpectedObjs := []string{"a*b/file*2.txt", "a*b/file*3.txt", "file*.txt", "file*1.txt", "file*2.txt", "file*3.txt", "file*4.txt"}
	for _, obj := range nonExpectedObjs {
		err := ensureS3Object(s3client, bucket, obj, "content")
		assertError(t, err, errS3NoSuchKey)
	}

	// assert local filesystem
	expected := fs.Expected(t, folderLayout...)
	assert.Assert(t, fs.Equal(workdir.Path(), expected))
}

func TestCopyS3ObjectstoLocalWithRawFlag(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}

	t.Parallel()
	const (
		fileContent = "this is a file content"
	)

	testcases := []struct {
		name           string
		src            []string
		wantedFile     string
		expectedOutput string
		expectedFiles  []fs.PathOp
		rawFlag        string
	}{
		{
			name:           "cp --raw file*.txt s3://bucket/",
			src:            []string{"file*.txt", "file*1.txt", "file*2.txt"},
			wantedFile:     "file*.txt",
			expectedOutput: "cp s3://bucket/file*.txt file*txt",
			rawFlag:        "--raw",
			expectedFiles: []fs.PathOp{
				fs.WithFile("file*.txt", fileContent),
			},
		},
		{
			name:       "cp  file*.txt s3://bucket/",
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
			name:       "cp  a*/file.txt s3://bucket/",
			src:        []string{"a*/file*.txt", "a*b/file1.txt", "a*c/file2.txt"},
			wantedFile: "a*/file*.txt",
			rawFlag:    "--raw",
			expectedFiles: []fs.PathOp{
				fs.WithFile("file*.txt", fileContent),
			},
		},
		{
			name:       "cp  a*/file.txt s3://bucket/",
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

			cmd := s5cmd("cp", "s3://"+bucket+"/"+tc.wantedFile, ".")
			if tc.rawFlag != "" {
				cmd = s5cmd("cp", "--raw", "s3://"+bucket+"/"+tc.wantedFile, ".")
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

	src := fmt.Sprintf("s3://%v/file*.txt", srcbucket)
	dst := fmt.Sprintf("s3://%v", dstbucket)

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

	src := fmt.Sprintf("s3://%v/file*", srcbucket)
	dst := fmt.Sprintf("s3://%v", dstbucket)

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
	dst := fmt.Sprintf("s3://%s/test*/", bucket)

	cmd := s5cmd("cp", "--raw", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals("cp %v %vtestfile.txt", src, dst),
	})

	err := ensureS3Object(s3client, bucket, "test*/testfile.txt", "this is a test file 1")
	if err != nil {
		t.Errorf("testfile*.txt not exist in S3 bucket %v\n", dst)
	}
}

// cp --exclude "*.py" s3://bucket/* .
func TestCopyS3ObjectsWithExcludeFilter(t *testing.T) {
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

	srcpath := fmt.Sprintf("s3://%s", bucket)

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
}

// cp --exclude "*.py" --exclude "file*" s3://bucket/* .
func TestCopyS3ObjectsWithExcludeFilters(t *testing.T) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const (
		excludePattern1 = "*.py"
		excludePattern2 = "file*"
		fileContent     = "content"
	)

	files := [...]string{
		"file1.txt",
		"file2.txt",
		"file.py",
		"a.py",
		"src/file.py",
		"main.c",
	}

	for _, filename := range files {
		putFile(t, s3client, bucket, filename, fileContent)
	}

	srcpath := fmt.Sprintf("s3://%s", bucket)

	cmd := s5cmd("cp", "--exclude", excludePattern1, "--exclude", excludePattern2, srcpath+"/*", ".")
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals("cp %v/main.c main.c", srcpath),
	})

	// assert s3
	for _, f := range files {
		assert.Assert(t, ensureS3Object(s3client, bucket, f, fileContent))
	}

	expectedFileSystem := []fs.PathOp{
		fs.WithFile("main.c", fileContent),
	}
	// assert local filesystem
	expected := fs.Expected(t, expectedFileSystem...)
	assert.Assert(t, fs.Equal(cmd.Dir, expected))
}

// cp --exclude ".txt" s3://bucket/abc* .
func TestCopyS3ObjectsWithPrefixWithExcludeFilters(t *testing.T) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const (
		excludePattern1 = "*.txt"
		fileContent     = "content"
	)

	files := [...]string{
		"abc/file.txt",
		"abc/file2.txt",
		"abc/abc/file3.txt",
		"abcd/main.py",
		"ab/file.py",
		"a/helper.c",
		"abc.pdf",
	}

	for _, filename := range files {
		putFile(t, s3client, bucket, filename, fileContent)
	}

	srcpath := fmt.Sprintf("s3://%s/abc*", bucket)

	cmd := s5cmd("cp", "--exclude", excludePattern1, srcpath, ".")
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals("cp s3://%s/abc.pdf abc.pdf", bucket),
		1: equals("cp s3://%s/abcd/main.py abcd/main.py", bucket),
	}, sortInput(true))

	// assert s3
	for _, f := range files {
		assert.Assert(t, ensureS3Object(s3client, bucket, f, fileContent))
	}

	expectedFileSystem := []fs.PathOp{
		fs.WithFile("abc.pdf", fileContent),
		fs.WithDir(
			"abcd",
			fs.WithFile("main.py", fileContent),
		),
	}
	// assert local filesystem
	expected := fs.Expected(t, expectedFileSystem...)
	assert.Assert(t, fs.Equal(cmd.Dir, expected))
}

// cp --exclude "*.gz" dir s3://bucket/
// cp --exclude "*.gz" dir/ s3://bucket/
// cp --exclude "*.gz" dir/* s3://bucket/
func TestCopyLocalDirectoryToS3WithExcludeFilter(t *testing.T) {
	t.Parallel()

	testcases := []struct {
		name            string
		directoryPrefix string
	}{
		{
			name:            "folder without /",
			directoryPrefix: "",
		},
		{
			name:            "folder with /",
			directoryPrefix: "/",
		},
		{
			name:            "folder with / and glob *",
			directoryPrefix: "/*",
		},
	}

	for _, tc := range testcases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

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

			const excludePattern = "*.gz"

			src := fmt.Sprintf("%v/", workdir.Path())
			src = src + tc.directoryPrefix
			dst := fmt.Sprintf("s3://%v/prefix/", bucket)

			src = filepath.ToSlash(src)
			cmd := s5cmd("cp", "--exclude", excludePattern, src, dst)
			result := icmd.RunCmd(cmd)

			result.Assert(t, icmd.Success)

			// assert local filesystem
			expected := fs.Expected(t, folderLayout...)
			assert.Assert(t, fs.Equal(workdir.Path(), expected))

			expectedS3Content := map[string]string{
				"prefix/testfile1.txt":           "this is a test file 1",
				"prefix/readme.md":               "this is a readme file",
				"prefix/a/another_test_file.txt": "yet another txt file. yatf.",
			}

			nonExpectedS3Content := map[string]string{
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
		})
	}
}

// cp --exclude "*.gz" --exclude "*.txt" dir/ s3://bucket/
func TestCopyLocalDirectoryToS3WithExcludeFilters(t *testing.T) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
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

	src := fmt.Sprintf("%v/", workdir.Path())
	dst := fmt.Sprintf("s3://%v/prefix/", bucket)

	src = filepath.ToSlash(src)
	cmd := s5cmd("cp", "--exclude", excludePattern1, "--exclude", excludePattern2, src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp %vreadme.md %vreadme.md`, src, dst),
	})

	// assert local filesystem
	expected := fs.Expected(t, folderLayout...)
	assert.Assert(t, fs.Equal(workdir.Path(), expected))

	expectedS3Content := map[string]string{
		"prefix/readme.md": "this is a readme file",
	}

	nonExpectedS3Content := map[string]string{
		"prefix/b/filename-with-hypen.gz": "file has hypen in its name",
		"prefix/a/another_test_file.txt":  "yet another txt file. yatf.",
		"prefix/testfile1.txt":            "this is a test file 1",
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
func TestCopySingleS3ObjectsIntoAnotherBucketWithExcludeFilter(t *testing.T) {
	t.Parallel()

	srcbucket := s3BucketFromTestNameWithPrefix(t, "src")
	dstbucket := s3BucketFromTestNameWithPrefix(t, "dst")

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

	src := fmt.Sprintf("s3://%v/*", srcbucket)
	dst := fmt.Sprintf("s3://%v/", dstbucket)

	cmd := s5cmd("cp", "--exclude", excludePattern, src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp s3://%s/file.txt s3://%s/file.txt`, srcbucket, dstbucket),
		1: equals(`cp s3://%s/file1.txt s3://%s/file1.txt`, srcbucket, dstbucket),
		2: equals(`cp s3://%s/readme.md s3://%s/readme.md`, srcbucket, dstbucket),
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

func TestCopySingleS3ObjectsIntoAnotherBucketWithExcludeFilters(t *testing.T) {
	t.Parallel()

	srcbucket := s3BucketFromTestNameWithPrefix(t, "src")
	dstbucket := s3BucketFromTestNameWithPrefix(t, "dst")

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
	}

	nonExpectedFiles := []string{
		"main.py",
		"main.js",
		"main.pdf",
		"main/file.txt",
		"readme.md",
	}

	const (
		content         = "this is a file content"
		excludePattern1 = "main*"
		excludePattern2 = "*.md"
	)

	for _, filename := range files {
		putFile(t, s3client, srcbucket, filename, content)
	}

	src := fmt.Sprintf("s3://%v/*", srcbucket)
	dst := fmt.Sprintf("s3://%v/", dstbucket)

	cmd := s5cmd("cp", "--exclude", excludePattern1, "--exclude", excludePattern2, src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals(`cp s3://%s/file.txt s3://%s/file.txt`, srcbucket, dstbucket),
		1: equals(`cp s3://%s/file1.txt s3://%s/file1.txt`, srcbucket, dstbucket),
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
	t.Parallel()

	const bucket = "bucket"

	_, s5cmd := setup(t, withEndpointURL("nonExistingEndpointURL"))

	folderLayout := []fs.PathOp{
		fs.WithFile("testfile.txt", "this is a test file 1"),
	}

	workdir := fs.NewDir(t, "somedir", folderLayout...)
	defer workdir.Remove()

	src := fmt.Sprintf("s3://%s/*", bucket)
	src = filepath.ToSlash(src)
	dst := fmt.Sprintf("%v/", workdir.Path())

	cmd := s5cmd("-r", "0", "cp", src, dst)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Expected{ExitCode: 1})
}

func TestCopySingleFileToS3WithNoSuchUploadRetryCount(t *testing.T) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const (
		filename = "example.txt"
		content  = "Some example text"
	)

	workdir := fs.NewDir(t, bucket, fs.WithFile(filename, content))
	defer workdir.Remove()

	srcpath := workdir.Join(filename)
	dstpath := fmt.Sprintf("s3://%v/", bucket)

	srcpath = filepath.ToSlash(srcpath)
	cmd := s5cmd("cp", "--no-such-upload-retry-count", "5", srcpath, dstpath)
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: suffix(`cp %v %v%v`, srcpath, dstpath, filename),
	})

	// assert local filesystem
	expected := fs.Expected(t, fs.WithFile(filename, content))
	assert.Assert(t, fs.Equal(workdir.Path(), expected))

	// assert S3
	assert.Assert(t, ensureS3Object(s3client, bucket, filename, content))
}

func TestVersionedDownload(t *testing.T) {
	t.Parallel()

	bucket := s3BucketFromTestName(t)

	// versioninng is only supported with in memory backend!
	s3client, s5cmd := setup(t, withS3Backend("mem"))

	const filename = "testfile.txt"

	var contents = []string{
		"This is first content",
		"Second content it is, and it is a bit longer!!!",
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
	cmd := s5cmd("ls", "--all-versions", "s3://"+bucket+"/"+filename)
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
			fmt.Sprintf("s3://%v/%v", bucket, filename), newDir.Path()+"/"+filename+strconv.Itoa(1+i))
		_ = icmd.RunCmd(cmd)
	}

	assert.Assert(t, fs.Equal(workdir.Path(), fs.ManifestFromDir(t, newDir.Path())))
}

// Before downloading a file from s3 a local target file is created. If download
// fails the created file should be deleted.
func TestDeleteFileWhenDownloadFailed(t *testing.T) {
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
	cmd := s5cmd("cp", "s3://"+bucket+"/"+filename, filename)
	result := icmd.RunCmd(cmd, withWorkingDir(workdir))

	result.Assert(t, icmd.Expected{ExitCode: 1})

	// assert initial file is untouched
	expected := fs.Expected(t, fs.WithFile(filename, content))
	assert.Assert(t, fs.Equal(workdir.Path(), expected))
}

// Test that counting writer does not corrupt objects during a download process
func TestCountingWriter(t *testing.T) {
	t.Parallel()

	const (
		filename = "log.txt"
	)

	content := randomString(3_000_000)

	s3client, s5cmd := setup(t)
	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)
	putFile(t, s3client, bucket, filename, content)

	cmd := s5cmd("cp", "--show-progress", "--concurrency", "3", "--part-size", "1", "s3://"+bucket+"/"+filename, ".")
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	// assert the downloaded file has the same content with the remote object
	expected := fs.Expected(t, fs.WithFile(filename, content, fs.WithMode(0644)))
	assert.Assert(t, fs.Equal(cmd.Dir, expected))
}

// It should skip special files
func TestUploadingSocketFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}

	t.Parallel()

	s3client, s5cmd := setup(t)
	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	workdir := fs.NewDir(t, t.Name())
	defer workdir.Remove()

	sockaddr := workdir.Join("/s5cmd.sock")
	ln, err := net.Listen("unix", sockaddr)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ln.Close()
		os.Remove(sockaddr)
	})

	cmd := s5cmd("cp", sockaddr, "s3://"+bucket+"/")
	result := icmd.RunCmd(cmd, withWorkingDir(workdir))

	// assert error message
	assertLines(t, result.Stderr(), map[int]compareFunc{
		0: contains(`is not a regular file`),
	})

	// assert logs are empty (no copy)
	assertLines(t, result.Stdout(), nil)

	// assert exit code
	result.Assert(t, icmd.Expected{ExitCode: 1})
}

// cp --include "*.py" s3://bucket/* .
func TestCopyS3ObjectsWithIncludeFilter(t *testing.T) {
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const (
		includePattern = "*.py"
		fileContent    = "content"
	)

	files := [...]string{
		"file1.py",
		"file2.py",
		"file.txt",
		"a.txt",
		"src/file.txt",
	}

	for _, filename := range files {
		putFile(t, s3client, bucket, filename, fileContent)
	}

	srcpath := fmt.Sprintf("s3://%s", bucket)

	cmd := s5cmd("cp", "--include", includePattern, srcpath+"/*", ".")
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals("cp %v/file1.py %s", srcpath, files[0]),
		1: equals("cp %v/file2.py %s", srcpath, files[1]),
	}, sortInput(true))

	// assert s3
	for _, f := range files {
		assert.Assert(t, ensureS3Object(s3client, bucket, f, fileContent))
	}

	expectedFileSystem := []fs.PathOp{
		fs.WithFile("file1.py", fileContent),
		fs.WithFile("file2.py", fileContent),
	}
	// assert local filesystem
	expected := fs.Expected(t, expectedFileSystem...)
	assert.Assert(t, fs.Equal(cmd.Dir, expected))
}

// cp --include "file*" --exclude "*.py" s3://bucket/* .
func TestCopyS3ObjectsWithIncludeExcludeFilter(t *testing.T) {
	t.Parallel()

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
		"test.py",
		"app.py",
		"docs/readme.md",
	}

	for _, filename := range files {
		putFile(t, s3client, bucket, filename, fileContent)
	}

	srcpath := fmt.Sprintf("s3://%s", bucket)

	cmd := s5cmd("cp", "--include", includePattern, "--exclude", excludePattern, srcpath+"/*", ".")
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
	t.Parallel()

	s3client, s5cmd := setup(t)

	bucket := s3BucketFromTestName(t)
	createBucket(t, s3client, bucket)

	const (
		includePattern  = "*.py"
		includePattern2 = "*.go"
		excludePattern  = "file*"
		excludePattern2 = "vendor/*"
		fileContent     = "content"
	)

	files := [...]string{
		"file1.py",
		"file2.py",
		"file1.go",
		"file2.go",
		"test.py",
		"app.py",
		"app.go",
		"vendor/package.go",
		"docs/readme.md",
	}

	for _, filename := range files {
		putFile(t, s3client, bucket, filename, fileContent)
	}

	srcpath := fmt.Sprintf("s3://%s", bucket)

	cmd := s5cmd("cp", "--exclude", excludePattern, "--exclude", excludePattern2, "--include", includePattern, "--include", includePattern2, srcpath+"/*", ".")
	result := icmd.RunCmd(cmd)

	result.Assert(t, icmd.Success)

	assertLines(t, result.Stdout(), map[int]compareFunc{
		0: equals("cp %v/app.go %s", srcpath, files[6]),
		1: equals("cp %v/app.py %s", srcpath, files[5]),
		2: equals("cp %v/test.py %s", srcpath, files[4]),
	}, sortInput(true))

	// assert s3
	for _, f := range files {
		assert.Assert(t, ensureS3Object(s3client, bucket, f, fileContent))
	}

	expectedFileSystem := []fs.PathOp{
		fs.WithFile("test.py", fileContent),
		fs.WithFile("app.py", fileContent),
		fs.WithFile("app.go", fileContent),
	}
	// assert local filesystem
	expected := fs.Expected(t, expectedFileSystem...)
	assert.Assert(t, fs.Equal(cmd.Dir, expected))
}
