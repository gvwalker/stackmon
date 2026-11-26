package discover

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gvwalker/stackmon/internal/inventory"
	"github.com/gvwalker/stackmon/internal/local"
)

// Project is a running Compose Project, independently of whether its files
// still exist. ComposePaths contains only locally discoverable files.
type Project struct {
	Project       string   `json:"project"`
	Containers    []string `json:"containers"`
	EnrolledStack string   `json:"enrolled_stack"`
	ComposePaths  []string `json:"compose_paths"`
	Reason        string   `json:"reason,omitempty"`
}

// Coverage keeps confirmed unmanaged Projects separate from uncertain matches.
// Complete describes Docker enumeration, not the certainty of every identity.
type Coverage struct {
	Unmanaged []Project `json:"unmanaged"`
	Unknown   []Project `json:"unmanaged_unknown"`
	Complete  bool      `json:"unmanaged_complete"`
	Error     string    `json:"unmanaged_error,omitempty"`
}

// Unmanaged compares running Projects with explicit bindings and the same path
// evidence used for enrolled reports. No image or enrollment operations occur.
func Unmanaged(cs []local.Container, inv inventory.Inventory) *Coverage {
	result := &Coverage{Unmanaged: []Project{}, Unknown: []Project{}, Complete: true}
	groups := map[string][]local.Container{}
	for _, c := range cs {
		if c.Project != "" && !c.OneOff {
			groups[c.Project] = append(groups[c.Project], c)
		}
	}
	covered := map[string]bool{}
	uncertain := map[string][]string{}
	for _, s := range inv.Stacks {
		id := local.ResolveProject(cs, s.Project, s.Dir, s.Path())
		if id.Resolved {
			covered[id.Project] = true
		}
		if s.Project != "" {
			continue
		}
		// Examine each Project as well: missing labels in one Project must not
		// become proof of absence merely because another has usable labels.
		for project, containers := range groups {
			single := local.ResolveProject(containers, "", s.Dir, s.Path())
			if single.Reason != "" || single.Resolved && (!id.Resolved || id.Project != project) {
				reason := single.Reason
				if reason == "" {
					reason = id.Reason
				}
				uncertain[project] = append(uncertain[project], fmt.Sprintf("%s: %s", s.Name, reason))
			}
		}
	}
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if covered[name] {
			continue
		}
		row := Project{Project: name, Containers: []string{}, ComposePaths: []string{}}
		paths, containers := map[string]bool{}, map[string]bool{}
		for _, c := range groups[name] {
			containers[c.Name] = true
			dir := c.ProjectWorkingDir
			for _, f := range strings.Split(c.ProjectConfigFiles, ",") {
				f = strings.TrimSpace(f)
				if f == "" {
					continue
				}
				if !filepath.IsAbs(f) {
					if !filepath.IsAbs(dir) {
						continue
					}
					f = filepath.Join(dir, f)
				}
				if fileExists(f) {
					paths[filepath.Clean(f)] = true
				}
			}
			if filepath.IsAbs(dir) {
				if file, ok := FindComposeFile(dir); ok {
					paths[filepath.Join(dir, file)] = true
				}
			}
		}
		for c := range containers {
			row.Containers = append(row.Containers, c)
		}
		sort.Strings(row.Containers)
		for p := range paths {
			row.ComposePaths = append(row.ComposePaths, p)
		}
		sort.Strings(row.ComposePaths)
		if reasons := uncertain[name]; len(reasons) > 0 {
			sort.Strings(reasons)
			row.Reason = strings.Join(reasons, "; ")
			result.Unknown = append(result.Unknown, row)
		} else {
			result.Unmanaged = append(result.Unmanaged, row)
		}
	}
	return result
}

func fileExists(path string) bool { st, err := os.Stat(path); return err == nil && !st.IsDir() }
