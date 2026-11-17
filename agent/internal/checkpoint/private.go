package checkpoint

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

var ErrUnsafe = errors.New("UNSAFE_AGENT_LOCAL_PATH")

// PrivateDir pins a directory descriptor. Each component is opened with
// O_NOFOLLOW. Only a root-owned sticky ancestor (e.g. /tmp) may be writable
// by other users. The final directory must belong to this service UID.
type PrivateDir struct {
	file *os.File
	path string
}

func OpenPrivateDir(path string) (*PrivateDir, error) {
	if path == "/" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrUnsafe
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrUnsafe
	}
	parts := strings.Split(path[1:], "/")
	for i, name := range parts {
		next, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if err != nil {
			return nil, ErrUnsafe
		}
		fd = next
		var st unix.Stat_t
		if unix.Fstat(fd, &st) != nil || !safeDirectory(st, i == len(parts)-1) {
			unix.Close(fd)
			return nil, ErrUnsafe
		}
	}
	return &PrivateDir{file: os.NewFile(uintptr(fd), path), path: path}, nil
}

func safeDirectory(st unix.Stat_t, final bool) bool {
	uid := uint32(os.Geteuid())
	if final {
		return st.Uid == uid && st.Mode&07777 == 0700
	}
	return (st.Uid == uid || st.Uid == 0) &&
		(st.Mode&0022 == 0 || (st.Uid == 0 && st.Mode&unix.S_ISVTX != 0))
}

func (d *PrivateDir) Close() error { return d.file.Close() }

func simpleName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name
}

func (d *PrivateDir) OpenFile(name string, flags int) (*os.File, error) {
	if !simpleName(name) {
		return nil, ErrUnsafe
	}
	fd, err := unix.Openat(int(d.file.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0600)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, os.ErrNotExist
		}
		return nil, ErrUnsafe
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(os.Geteuid()) || st.Mode&07777 != 0600 || st.Nlink != 1 {
		unix.Close(fd)
		return nil, ErrUnsafe
	}
	return os.NewFile(uintptr(fd), filepath.Join(d.path, name)), nil
}

func (d *PrivateDir) Read(name string, max int64) ([]byte, error) {
	f, err := d.OpenFile(name, unix.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || int64(len(b)) > max {
		return nil, errors.New("AGENT_LOCAL_READ_FAILED")
	}
	return b, nil
}

// Replace commits one file using a same-directory atomic rename. The returned
// bool indicates a rename took effect even if the final directory sync failed.
func (d *PrivateDir) Replace(name string, data []byte) (bool, error) {
	if !simpleName(name) {
		return false, ErrUnsafe
	}
	if old, err := d.OpenFile(name, unix.O_RDONLY); err == nil {
		old.Close()
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return false, err
	}
	tmp := ".write-" + hex.EncodeToString(suffix[:])
	f, err := d.OpenFile(tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL)
	if err != nil {
		return false, err
	}
	fd := int(d.file.Fd())
	defer unix.Unlinkat(fd, tmp, 0)
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return false, err
	}
	if err = unix.Renameat(fd, tmp, fd, name); err != nil {
		return false, err
	}
	return true, d.file.Sync()
}
