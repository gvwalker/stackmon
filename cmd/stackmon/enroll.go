package main

import (
	"context"
	"fmt"
	"path/filepath"
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
		Use:   "enroll [<path>]",
		Short: "Add a stack to the inventory",
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
				bound = project
				if bound != "" {
					if err := compose.ValidProjectName(bound); err != nil {
						return err
					}
				}
			}

			inv, path, err := loadInventory()
			if err != nil {
				return err
			}
			if name == "" {
				name = defaultName
			}
			if err := inv.Add(inventory.Stack{Name: name, Dir: dir, File: file, Project: bound, Enrolled: time.Now().UTC()}); err != nil {
				return err
			}
			if err := inventory.Save(path, inv); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "enrolled %s (%s/%s)\n", name, dir, file)
			if bound != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "bound to Compose project %s\n", bound)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "override the stack name (default: directory basename)")
	cmd.Flags().StringVar(&running, "running", "", "enroll a running Compose project by project name")
	cmd.Flags().StringVar(&project, "project", "", "bind the stack to this Docker Compose project (default: matched from the paths Docker recorded)")
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
			inv, path, err := loadInventory()
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
			if err := inv.SetProject(args[0], project); err != nil {
				return err
			}
			if err := inventory.Save(path, inv); err != nil {
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
