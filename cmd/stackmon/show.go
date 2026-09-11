package main

import (
	"github.com/spf13/cobra"

	"github.com/gvwalker/stackmon/internal/notes"
	"github.com/gvwalker/stackmon/internal/render"
)

func newShowCmd() *cobra.Command {
	var token string

	cmd := &cobra.Command{
		Use:   "show <stack>",
		Short: "Show per-image detail and release notes for one stack",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, _, err := loadStackReport(cmd, args)
			if err != nil {
				return err
			}

			fetcher := notes.New(token)
			for _, img := range r.Images {
				var rels []notes.Release
				if img.Candidate != "" && img.NotesRepo != "" {
					// Notes are best-effort: a rate limit must not hide the
					// rest of the detail view.
					if got, err := fetcher.Between(cmd.Context(), img.NotesRepo, img.Version, img.Candidate); err == nil {
						rels = got
					}
				}
				if err := render.Detail(cmd.OutOrStdout(), img, rels); err != nil {
					return err
				}
				cmd.OutOrStdout().Write([]byte("\n"))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&token, "github-token", "", "GitHub token for release notes (raises the rate limit)")
	cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return completeEnrolledStacks(cmd, args, toComplete)
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return cmd
}
