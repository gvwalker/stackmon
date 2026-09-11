package main

import (
	"errors"
	"fmt"
	"os"
)

// errUpdatesFound signals exit code 2 without printing an error.
var errUpdatesFound = errors.New("updates available")

// version is set at build time via -ldflags "-X main.version=vX.Y.Z" (see
// .github/workflows/ci.yml). It stays "dev" for plain `go build`/`go run`,
// which also means "stackmon update"/"whats-new" never claim a dev build is
// up to date -- an unparseable version can't be compared, so they say so.
var version = "dev"

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
