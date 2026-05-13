//go:build windows

package storage

import "os"

// The disk credential cache is not implemented on Windows (no O_NOFOLLOW,
// different permission model). All callers degrade to the original
// per-process WIF resolve.

func openPrivateForRead(path string) (*os.File, os.FileInfo, error) {
	return nil, nil, os.ErrNotExist
}

func openPrivateForWrite(path string) (*os.File, error) {
	return nil, os.ErrNotExist
}

func isOwnedAndPrivate(info os.FileInfo) bool { return false }
func isOwnedPrivateDir(info os.FileInfo) bool { return false }
