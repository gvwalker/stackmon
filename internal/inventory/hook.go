//go:build !stackmon_e2e

package inventory

// The e2e build replaces these with hooks that let a scenario observe an
// inventory transaction and park it at a known point. In a shipped binary
// they do nothing.
func afterLock()   {}
func afterLoad()   {}
func afterSave()   {}
func onContended() {}
