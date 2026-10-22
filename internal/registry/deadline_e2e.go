//go:build stackmon_e2e

package registry

import (
	"os"
	"time"
)

// The e2e build shrinks the probe budget when a scenario asks for it, so a
// stalled-registry scenario proves the deadline fires in seconds instead of
// waiting out the shipped budget. A shipped binary has neither this hook nor
// the environment variable.
func init() {
	if os.Getenv("STACKMON_E2E_SHORT_DEADLINES") != "" {
		probeBudget = 2 * time.Second
	}
}
