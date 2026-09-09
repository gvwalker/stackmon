package main

import (
	"fmt"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/gvwalker/stackmon/internal/discover"
	"github.com/gvwalker/stackmon/internal/inventory"
)

func newEnrollCmd() *cobra.Command {
	var name string

	cmd := &cobra.Command{
		Use:   "enroll <path>",
		Short: "Add a stack to the inventory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}
			file, ok := discover.FindComposeFile(dir)
			if !ok {
				return fmt.Errorf("no compose file in %s (looked for %v)", dir, discover.ComposeFilenames)
			}

			inv, path, err := loadInventory()
			if err != nil {
				return err
			}
			if name == "" {
				name = filepath.Base(dir)
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
	return cmd
}

func newUnenrollCmd() *cobra.Command {
	return &cobra.Command{
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
