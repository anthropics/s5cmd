//go:build !windows

package storage

import (
	"os"
	"syscall"
)

// openPrivateForRead opens path with O_NOFOLLOW so a symlink swap can't
// redirect the read, and O_NONBLOCK so a planted FIFO can't hang the process
// (O_NOFOLLOW only stops symlinks). Returns the open fd's FileInfo so the
// privacy check is TOCTOU-safe (fstat, not a path stat).
func openPrivateForRead(path string) (*os.File, os.FileInfo, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, info, nil
}

func openPrivateForWrite(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
}

// isOwnedAndPrivate is the defence against an accidentally world-readable
// cache and cache poisoning by another local user. aws-iam-authenticator does
// the same on its credential cache.
func isOwnedAndPrivate(info os.FileInfo) bool {
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Geteuid()
}

func isOwnedPrivateDir(info os.FileInfo) bool {
	if !info.IsDir() || info.Mode().Perm()&0o022 != 0 {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Geteuid()
}
