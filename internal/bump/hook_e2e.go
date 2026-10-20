//go:build stackmon_e2e

package bump

import (
	"fmt"
	"os"
)

// afterValidate parks a bump that the e2e harness is steering. When
// STACKMON_E2E_AFTER_VALIDATE names a FIFO, the compose path is written to it
// and the process blocks until the harness writes a release byte. This is a
// test-only seam compiled in with the stackmon_e2e build tag; a shipped binary
// uses the no-op in hook.go.
func afterValidate(path string) {
	fifo := os.Getenv("STACKMON_E2E_AFTER_VALIDATE")
	if fifo == "" {
		return
	}
	announce, err := os.OpenFile(fifo, os.O_WRONLY, 0)
	if err != nil {
		return
	}
	fmt.Fprintln(announce, path)
	announce.Close()

	release, err := os.OpenFile(fifo+".release", os.O_RDONLY, 0)
	if err != nil {
		return
	}
	defer release.Close()
	var b [1]byte
	_, _ = release.Read(b[:])
}
