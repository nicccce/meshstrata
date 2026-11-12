// Package privatefile reads local sensitive material without following symlinks.
package privatefile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// Only pre-existing files are read. The immediate key directory must be 0700,
// file 0600, both owned by the effective service uid. No symlink component is
// accepted; OpenRoot scopes the final lookup, and SameFile detects replacement.
// Ancestors must not be writable by other users, except sticky shared directories
// such as /tmp (the private immediate directory remains required).
func Read(path string, maximum int64) ([]byte, error) {
	bad := errors.New("private file unavailable")
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, bad
	}
	for p := filepath.Dir(path); ; p = filepath.Dir(p) {
		info, e := os.Lstat(p)
		if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, bad
		}
		if info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0 {
			return nil, bad
		}
		if p == filepath.Dir(path) && (info.Mode().Perm() != 0700 || !owned(info)) {
			return nil, bad
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	before, e := os.Lstat(path)
	if e != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || !owned(before) {
		return nil, bad
	}
	root, e := os.OpenRoot(filepath.Dir(path))
	if e != nil {
		return nil, bad
	}
	defer root.Close()
	file, e := root.Open(filepath.Base(path))
	if e != nil {
		return nil, bad
	}
	defer file.Close()
	after, e := file.Stat()
	if e != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() || after.Mode().Perm() != 0600 || !owned(after) || after.Size() > maximum {
		return nil, bad
	}
	key, e := io.ReadAll(io.LimitReader(file, maximum+1))
	if e != nil || int64(len(key)) > maximum {
		clear(key)
		return nil, bad
	}
	return key, nil
}
func owned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}
