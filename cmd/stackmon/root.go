package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/gvwalker/stackmon/internal/config"
	"github.com/gvwalker/stackmon/internal/inventory"
)

// Exit codes. 2 is reserved for --fail-on-update so that cron can alert on
// available updates without treating them as errors.
const (
	exitOK      = 0
	exitError   = 1
	exitUpdates = 2
)

var (
	flagConfig    string
	flagInventory string
)

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "stackmon",
		Short:         "Report outdated container images across enrolled Compose stacks",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.PersistentFlags().StringVar(&flagConfig, "config", "", "path to config.toml (default ~/.config/stackmon/config.toml)")
	cmd.PersistentFlags().StringVar(&flagInventory, "inventory", "", "path to inventory.json (default ~/.local/state/stackmon/inventory.json)")
	_ = cmd.MarkPersistentFlagFilename("config", "toml")
	_ = cmd.MarkPersistentFlagFilename("inventory", "json")

	cmd.AddCommand(newDiscoverCmd(), newEnrollCmd(), newUnenrollCmd(), newInventoryCmd(), newCheckCmd(), newShowCmd(), newBumpCmd())
	return cmd
}

func loadConfig() (config.Config, error) {
	path := flagConfig
	if path == "" {
		p, err := config.DefaultPath()
		if err != nil {
			return config.Config{}, err
		}
		path = p
	}
	return config.Load(path)
}

func inventoryPath() (string, error) {
	if flagInventory != "" {
		return flagInventory, nil
	}
	return inventory.DefaultPath()
}

func loadInventory() (inventory.Inventory, string, error) {
	path, err := inventoryPath()
	if err != nil {
		return inventory.Inventory{}, "", err
	}
	inv, err := inventory.Load(path)
	return inv, path, err
}

// resolve selects enrolled stacks by name, or all of them when names is
// empty. A stack whose path no longer exists (an unmounted drive, a moved
// directory) is reported as a warning alongside compose parse failures
// rather than aborting the run: the exit-code table reserves 1 for stackmon
// itself failing, and one moved directory must not blank the whole report.
// Only an explicitly named stack that isn't enrolled at all is a hard
// error -- that's a user mistake, not a runtime fact to degrade around.
func resolve(inv inventory.Inventory, names []string) ([]inventory.Stack, []error, error) {
	var candidates []inventory.Stack

	if len(names) == 0 {
		candidates = append(candidates, inv.Stacks...)
	} else {
		for _, n := range names {
			s, ok := inv.Find(n)
			if !ok {
				return nil, nil, fmt.Errorf("%q is not enrolled; run 'stackmon discover' to see what is available", n)
			}
			candidates = append(candidates, s)
		}
	}

	var out []inventory.Stack
	var missing []error
	for _, s := range candidates {
		if _, err := os.Stat(filepath.Join(s.Dir, s.File)); err != nil {
			missing = append(missing, fmt.Errorf("enrolled stack %q no longer exists at %s; re-enroll it or run 'stackmon unenroll %s'", s.Name, s.Path(), s.Name))
			continue
		}
		out = append(out, s)
	}
	return out, missing, nil
}
