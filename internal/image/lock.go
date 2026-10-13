package image

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// lockImages takes an exclusive advisory lock that serializes publishing a new
// golden image (rename plus current_image update) against image prune, so prune
// never sees a renamed image before current_image names it. The lock file sits
// next to the config so both commands agree on it. The kernel releases the
// lock if the process dies.
func lockImages(configPath string) (func(), error) {
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "images.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- fixed name beside the user's config.
	if err != nil {
		return nil, fmt.Errorf("open image lock: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, nil
}
