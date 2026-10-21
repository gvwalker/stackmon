//go:build unix

package bump

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// lockTarget takes the cooperating-writer lock for path and returns its
// release. The lock is a flock on the containing directory, not on the compose
// file itself: the file is replaced by rename, so a lock held on the old inode
// would neither survive the replacement nor exclude a writer that opened the
// new one. The directory stays the same for the whole read/validate/replace
// transaction, and locking it leaves no artifact in the user's stack directory.
func lockTarget(path string) (func(), error) {
	dir := filepath.Dir(path)
	f, err := os.Open(dir)
	if err != nil {
		return nil, fmt.Errorf("bump: opening %s to lock it: %w", dir, err)
	}
	for {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("bump: locking %s: %w", dir, err)
	}
	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, nil
}
