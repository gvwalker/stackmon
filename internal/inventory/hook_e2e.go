//go:build stackmon_e2e

package inventory

import (
	"fmt"
	"os"
)

// The e2e build announces the points of an inventory transaction on the FIFO
// named by STACKMON_E2E_INVENTORY_EVENTS -- one line per event, "<event>
// <pid>" -- and parks the transaction after loading when
// STACKMON_E2E_INVENTORY_PARK names a FIFO to wait on.
//
// It lets a scenario hold a transaction open and watch that the next writer
// waits for it rather than racing it. A shipped binary has neither: hook.go
// replaces all of this with no-ops.

// announce writes one event to the event FIFO. A FIFO write below PIPE_BUF
// is atomic, so events from several processes arrive one line at a time and
// in the order they were announced.
func announce(event string) {
	fifo := os.Getenv("STACKMON_E2E_INVENTORY_EVENTS")
	if fifo == "" {
		return
	}
	f, err := os.OpenFile(fifo, os.O_WRONLY, 0)
	if err != nil {
		return
	}
	fmt.Fprintf(f, "%s %d\n", event, os.Getpid())
	f.Close()
}

// park waits for the scenario to let the transaction continue.
func park() {
	fifo := os.Getenv("STACKMON_E2E_INVENTORY_PARK")
	if fifo == "" {
		return
	}
	release, err := os.OpenFile(fifo, os.O_RDONLY, 0)
	if err != nil {
		return
	}
	defer release.Close()
	var b [1]byte
	_, _ = release.Read(b[:])
}

// afterLock announces that the transaction holds the lock.
func afterLock() { announce("lock") }

// afterLoad announces that the inventory has been loaded, and parks the
// transaction there when the scenario asked for it.
func afterLoad() {
	announce("load")
	park()
}

// afterSave announces that the inventory has been written.
func afterSave() { announce("save") }

// onContended announces that another writer holds the lock.
func onContended() { announce("contended") }
