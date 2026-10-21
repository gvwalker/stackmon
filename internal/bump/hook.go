//go:build !stackmon_e2e

package bump

// afterValidate is a no-op in shipped binaries. The e2e build replaces it with
// a deterministic barrier so a scenario can edit the source in the window
// between validation and replacement.
func afterValidate(string) {}
