package main

import (
	"errors"
	"fmt"
	"os"
)

// errUpdatesFound signals exit code 2 without printing an error.
var errUpdatesFound = errors.New("updates available")

func main() {
	if err := newRootCmd().Execute(); err != nil {
		if errors.Is(err, errUpdatesFound) {
			os.Exit(exitUpdates)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(exitError)
	}
	os.Exit(exitOK)
}
