package main

import (
	"context"
	"fmt"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/gvwalker/stackmon/internal/discover"
	"github.com/gvwalker/stackmon/internal/inventory"
	"github.com/gvwalker/stackmon/internal/local"
)

func newEnrollCmd() *cobra.Command {
	return newEnrollCmdWithProber(local.New())
}

func newEnrollCmdWithProber(prober local.Prober) *cobra.Command {
	var name, running string

	cmd := &cobra.Command{
		Use:   "enroll [<path>]",
		Short: "Add a stack to the inventory",
		Args: func(_ *cobra.Command, args []string) error {
			if running != "" {
				if len(args) != 0 {
					return fmt.Errorf("--running cannot be combined with a path")
				}
				return nil
			}
			if len(args) != 1 {
				return fmt.Errorf("requires exactly one path unless --running is set")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			var dir, file, defaultName string
			if running != "" {
				if !prober.Available() {
					return fmt.Errorf("Docker is unavailable; cannot find running Compose project %q", running)
				}
				containers, err := prober.Containers(context.Background())
				if err != nil {
					return err
				}
				var candidate *discover.Candidate
				for _, c := range discover.DockerCandidates(containers, inventory.Inventory{}) {
					if c.Project == running {
						candidate = &c
						break
					}
				}
				if candidate == nil {
					return fmt.Errorf("running Compose project %q was not found", running)
				}
				dir, file, defaultName = candidate.Dir, candidate.File, candidate.Name
			} else {
				var err error
				dir, err = filepath.Abs(args[0])
				if err != nil {
					return err
				}
				var ok bool
				file, ok = discover.FindComposeFile(dir)
				if !ok {
					return fmt.Errorf("no compose file in %s (looked for %v)", dir, discover.ComposeFilenames)
				}
				defaultName = filepath.Base(dir)
			}

			inv, path, err := loadInventory()
			if err != nil {
				return err
			}
			if name == "" {
				name = defaultName
			}
			if err := inv.Add(inventory.Stack{Name: name, Dir: dir, File: file, Enrolled: time.Now().UTC()}); err != nil {
				return err
			}
			if err := inventory.Save(path, inv); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "enrolled %s (%s/%s)\n", name, dir, file)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "override the stack name (default: directory basename)")
	cmd.Flags().StringVar(&running, "running", "", "enroll a running Compose project by project name")
	return cmd
}

func newUnenrollCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unenroll <name>",
		Short: "Remove a stack from the inventory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			inv, path, err := loadInventory()
			if err != nil {
				return err
			}
			if err := inv.Remove(args[0]); err != nil {
				return err
			}
			if err := inventory.Save(path, inv); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "unenrolled %s\n", args[0])
			return nil
		},
	}
	cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return completeEnrolledStacks(cmd, args, toComplete)
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return cmd
}

func newInventoryCmd() *cobra.Command {
	list := &cobra.Command{
		Use:   "list",
		Short: "Show enrolled stacks",
		RunE: func(cmd *cobra.Command, _ []string) error {
			inv, _, err := loadInventory()
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tPATH\tENROLLED")
			for _, s := range inv.Stacks {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", s.Name, s.Path(), s.Enrolled.Format("2006-01-02"))
			}
			return tw.Flush()
		},
	}

	cmd := &cobra.Command{Use: "inventory", Short: "Inspect the enrollment store"}
	cmd.AddCommand(list)
	return cmd
}
