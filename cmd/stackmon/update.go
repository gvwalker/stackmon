package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/gvwalker/stackmon/internal/selfupdate"
)

func newUpdateCmd() *cobra.Command {
	var token string

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Download and install the latest stackmon release",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if version == "dev" {
				fmt.Fprintln(out, "running a dev build; checking for the latest release anyway")
			}

			c := selfupdate.New(token)
			rel, newer, err := c.Latest(cmd.Context(), version)
			if err != nil {
				return err
			}
			if !newer && version != "dev" {
				fmt.Fprintf(out, "stackmon %s is already up to date\n", version)
				return nil
			}

			fmt.Fprintf(out, "updating stackmon %s -> %s...\n", version, rel.Tag)
			if err := c.Install(cmd.Context(), rel.Tag); err != nil {
				return err
			}
			fmt.Fprintf(out, "updated to %s\n", rel.Tag)
			return nil
		},
	}
	cmd.Flags().StringVar(&token, "github-token", "", "GitHub token (raises the rate limit)")
	return cmd
}
