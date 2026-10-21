//go:build !unix

package inventory

import (
	"fmt"
	"runtime"
)

// lock is unavailable on this platform. The inventory transaction needs a
// lock that every writer contends on, and there is no portable one to take
// here, so the command refuses rather than running a transaction that two
// writers could interleave.
func lock(path string) (func(), error) {
	return nil, fmt.Errorf("inventory: locking %s is not supported on %s", lockPath(path), runtime.GOOS)
}
