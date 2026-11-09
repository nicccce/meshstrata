package localstate

import (
	"fmt"
	"os"
)

// Prepare creates a private component state directory. Refuse symlink roots so
// a misplaced configuration cannot chmod an unrelated directory through a link.
func Prepare(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect state directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("state directory must be a directory, not a symlink: %s", path)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("restrict state directory permissions: %w", err)
	}
	return nil
}
