package localstate

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Lock protects state and socket recovery. The file stays on disk; the OS
// releases the lock after a crash. This scaffold targets Linux.
func Lock(dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, "process.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open process lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("state directory is already in use: %w", err)
	}
	return func() { _ = f.Close() }, nil
}
