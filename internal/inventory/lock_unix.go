//go:build unix

package inventory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// lock takes the writer lock for the inventory at path and returns its
// release.
//
// The lock lives in its own file, named after the inventory, and never in the
// inventory itself: the inventory is replaced by rename, so a lock held on
// it would neither survive the replacement nor exclude a writer that opened
// the new file. A separate file is what every writer contends on.
//
// The lock is taken for the whole load-modify-save transaction. A writer
// that finds it held waits: it is about to change the inventory, and it must
// not read a copy that another writer is about to replace.
func lock(path string) (func(), error) {
	lp := lockPath(path)
	dir := filepath.Dir(lp)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("inventory: creating %s: %w", dir, err)
	}
	f, err := os.OpenFile(lp, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("inventory: opening %s: %w", lp, err)
	}
	if err := flock(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("inventory: locking %s: %w", lp, err)
	}
	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, nil
}

// flock takes an exclusive flock on f. It asks without blocking first, so a
// lock that is already held is visible as contention rather than as a wait,
// and then waits for it.
func flock(f *os.File) error {
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, unix.EWOULDBLOCK):
			// Another writer holds the lock. It will be released when that
			// writer finishes its transaction, or when it dies: the kernel
			// drops a flock whose process is gone.
			onContended()
			return wait(f)
		case errors.Is(err, unix.EINTR):
			continue
		default:
			return err
		}
	}
}

// wait blocks until the exclusive flock on f is held.
func wait(f *os.File) error {
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		return err
	}
}
