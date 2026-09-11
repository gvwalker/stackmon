package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/gvwalker/stackmon/internal/selfupdate"
)

func newWhatsNewCmd() *cobra.Command {
	var token string

	cmd := &cobra.Command{
		Use:   "whats-new",
		Short: "Show release notes for the running version and check for updates",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "stackmon %s\n\n", version)

			c := selfupdate.New(token)

			if version != "dev" {
				if rel, err := c.ByTag(cmd.Context(), version); err == nil && rel.Body != "" {
					fmt.Fprintln(out, rel.Body)
					fmt.Fprintln(out)
				}
			}

			latest, newer, err := c.Latest(cmd.Context(), version)
			if err != nil {
				return err
			}
			switch {
			case version == "dev":
				fmt.Fprintf(out, "dev build; latest release is %s (run 'stackmon update' to install it)\n", latest.Tag)
			case newer:
				fmt.Fprintf(out, "update available: %s -> %s (run 'stackmon update')\n", version, latest.Tag)
			default:
				fmt.Fprintln(out, "you are running the latest version")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&token, "github-token", "", "GitHub token (raises the rate limit)")
	return cmd
}
