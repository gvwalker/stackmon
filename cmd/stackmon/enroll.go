package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/gvwalker/stackmon/internal/compose"
	"github.com/gvwalker/stackmon/internal/discover"
	"github.com/gvwalker/stackmon/internal/inventory"
	"github.com/gvwalker/stackmon/internal/local"
)

func newEnrollCmd() *cobra.Command {
	var name, running, project string

	cmd := &cobra.Command{
		Use:   "enroll [<path>|<discovered-name>]",
		Short: "Add a stack to the inventory",
		Long: "Add one stack by directory, by a unique name shown by discover, or by --running.\n\n" +
			"An existing directory takes precedence over a discovered name, even when it has\n" +
			"no Compose file. Only a bare name falls back to discovery; explicit paths such\n" +
			"as ./name or /srv/name always select a directory. Duplicate names are refused.\n\n" +
			"Name enrollment preserves the discovered Compose file and does not bind a\n" +
			"Compose project. Use --project to bind explicitly, or --running <project> to\n" +
			"enroll and bind a running project. Use --name to override the stack name.",
		Args: func(_ *cobra.Command, args []string) error {
			if running != "" {
				if name != "" {
					return fmt.Errorf("--name cannot be combined with --running")
				}
				// --running already knows which project it enrolled: the one
				// it found and read the compose file from.
				if project != "" {
					return fmt.Errorf("--project cannot be combined with --running")
				}
				if len(args) != 0 {
					return fmt.Errorf("--running cannot be combined with a path or discovered name")
				}
				return nil
			}
			if len(args) != 1 {
				return fmt.Errorf("requires exactly one path or discovered name unless --running is set")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			var dir, file, defaultName, bound string
			if running != "" {
				prober := local.New()
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
				dir, file, defaultName, bound = candidate.Dir, candidate.File, candidate.Name, candidate.Project
			} else {
				candidate, err := resolveEnrollment(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				dir, file, defaultName = candidate.Dir, candidate.File, candidate.Name
				bound = project
				if bound != "" {
					if err := compose.ValidProjectName(bound); err != nil {
						return err
					}
				}
			}

			path, err := inventoryPath()
			if err != nil {
				return err
			}
			if name == "" {
				name = defaultName
			}
			if err := inventory.Update(path, func(inv *inventory.Inventory) error {
				return inv.Add(inventory.Stack{Name: name, Dir: dir, File: file, Project: bound, Enrolled: time.Now().UTC()})
			}); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "enrolled %s (%s/%s)\n", name, dir, file)
			if bound != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "bound to Compose project %s\n", bound)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "override the stack name (default: directory basename or discovered name)")
	cmd.Flags().StringVar(&running, "running", "", "enroll a running Compose project by project name")
	cmd.Flags().StringVar(&project, "project", "", "bind the stack to this Docker Compose project (default: matched from the paths Docker recorded)")
	return cmd
}

func resolveEnrollment(ctx context.Context, arg string) (discover.Candidate, error) {
	dir, err := filepath.Abs(arg)
	if err != nil {
		return discover.Candidate{}, err
	}
	// Test the original spelling: cleaning ./name or name/ would turn an
	// explicit path into a bare name and allow an unintended fallback.
	_, statErr := os.Stat(dir)
	bare := arg != "." && arg != ".." && !strings.ContainsRune(arg, filepath.Separator)
	if !bare || !os.IsNotExist(statErr) {
		file, ok := discover.FindComposeFile(dir)
		if !ok {
			return discover.Candidate{}, fmt.Errorf("no compose file in %s (looked for %v)", dir, discover.ComposeFilenames)
		}
		return discover.Candidate{Name: filepath.Base(dir), Dir: dir, File: file}, nil
	}

	candidates, err := discoveredStacks(ctx)
	if err != nil {
		return discover.Candidate{}, err
	}
	var matches []discover.Candidate
	for _, c := range candidates {
		if c.Name == arg {
			c.Dir, err = filepath.Abs(c.Dir)
			if err != nil {
				return discover.Candidate{}, err
			}
			matches = append(matches, c)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) == 0 {
		return discover.Candidate{}, fmt.Errorf("no discovered stack named %q; run 'stackmon discover', enroll a directory with 'stackmon enroll /path/to/stack', or use 'stackmon enroll --running <project>'", arg)
	}
	var alternatives []string
	for _, c := range matches {
		command := "stackmon enroll '" + strings.ReplaceAll(c.Dir, "'", "'\\''") + "'"
		if c.Project != "" {
			command = fmt.Sprintf("stackmon enroll --running %q", c.Project)
		}
		alternatives = append(alternatives, fmt.Sprintf("  %s: %s", filepath.Join(c.Dir, c.File), command))
	}
	return discover.Candidate{}, fmt.Errorf("discovered stack name %q is ambiguous; choose an explicit directory or running project:\n%s", arg, strings.Join(alternatives, "\n"))
}

func newUnenrollCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unenroll <name>",
		Short: "Remove a stack from the inventory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := inventoryPath()
			if err != nil {
				return err
			}
			if err := inventory.Update(path, func(inv *inventory.Inventory) error {
				return inv.Remove(args[0])
			}); err != nil {
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

// autoProject is what `inventory list` shows for a stack with no explicit
// binding: its Compose project is matched from Docker's labels at check time.
const autoProject = "auto"

func newInventoryCmd() *cobra.Command {
	var clearBinding bool

	list := &cobra.Command{
		Use:   "list",
		Short: "Show enrolled stacks",
		RunE: func(cmd *cobra.Command, _ []string) error {
			inv, _, err := loadInventory()
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tPATH\tPROJECT\tENROLLED")
			for _, s := range inv.Stacks {
				// "auto" is the honest answer for an unbound stack: the
				// project is worked out from the paths Docker recorded, not
				// asserted here.
				project := s.Project
				if project == "" {
					project = autoProject
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", s.Name, s.Path(), project, s.Enrolled.Format("2006-01-02"))
			}
			return tw.Flush()
		},
	}

	setProject := &cobra.Command{
		Use:   "set-project <stack> <project>",
		Short: "Bind a stack to a Docker Compose project, or clear the binding",
		Long: "Bind an enrolled stack to the Docker Compose project its containers run in.\n\n" +
			"Without a binding, stackmon matches the stack to a project using the working\n" +
			"directory and compose file Docker recorded on that project's containers, and\n" +
			"reports an unknown identity rather than a guess when several projects match.\n\n" +
			"Pass --clear to drop the binding and go back to that automatic matching. The\n" +
			"stack's name, compose path, and enrollment date are left untouched.",
		Args: func(_ *cobra.Command, args []string) error {
			if clearBinding {
				if len(args) != 1 {
					return fmt.Errorf("--clear takes only a stack name; drop the project argument")
				}
				return nil
			}
			if len(args) != 2 {
				return fmt.Errorf("requires a stack and a project name (or --clear to remove the binding)")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := inventoryPath()
			if err != nil {
				return err
			}
			project := ""
			if !clearBinding {
				project = args[1]
				if err := compose.ValidProjectName(project); err != nil {
					return err
				}
			}
			if err := inventory.Update(path, func(inv *inventory.Inventory) error {
				return inv.SetProject(args[0], project)
			}); err != nil {
				return err
			}
			if clearBinding {
				fmt.Fprintf(cmd.OutOrStdout(), "cleared the project binding for %s; its project is matched automatically again\n", args[0])
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "bound %s to Compose project %s\n", args[0], project)
			}
			return nil
		},
	}
	setProject.Flags().BoolVar(&clearBinding, "clear", false, "remove the binding instead of setting one")
	setProject.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return completeEnrolledStacks(cmd, args, toComplete)
		}
		if len(args) == 1 && !clearBinding {
			return completeRunningProjects(cmd)
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	cmd := &cobra.Command{Use: "inventory", Short: "Inspect the enrollment store"}
	cmd.AddCommand(list, setProject)
	return cmd
}
