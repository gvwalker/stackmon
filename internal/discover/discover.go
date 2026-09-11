// Package discover finds compose files beneath configured roots and reports
// which of them are already enrolled.
package discover

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gvwalker/stackmon/internal/inventory"
	"github.com/gvwalker/stackmon/internal/local"
)

// MaxDepth is how far below a root a compose file may be found.
const MaxDepth = 3

// ComposeFilenames is in Compose's own precedence order: the first match in
// a directory wins.
var ComposeFilenames = []string{
	"compose.yaml",
	"compose.yml",
	"docker-compose.yaml",
	"docker-compose.yml",
}

// Candidate is a stack that could be, or already is, enrolled.
type Candidate struct {
	Name     string
	Project  string
	Dir      string
	File     string
	Enrolled bool
}

// FindComposeFile returns the compose filename in dir, by precedence.
func FindComposeFile(dir string) (string, bool) {
	for _, name := range ComposeFilenames {
		if st, err := os.Stat(filepath.Join(dir, name)); err == nil && !st.IsDir() {
			return name, true
		}
	}
	return "", false
}

// Scan walks each root looking for compose files. Roots that do not exist are
// skipped, since a drive may simply not be mounted. Results are sorted by name.
func Scan(roots []string, inv inventory.Inventory) ([]Candidate, error) {
	enrolled := enrolledDirs(inv)
	var out []Candidate
	seen := map[string]bool{}

	for _, root := range roots {
		root = filepath.Clean(root)
		if _, err := os.Stat(root); err != nil {
			continue
		}

		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				// An unreadable subtree must not abort the whole scan.
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if !d.IsDir() {
				return nil
			}
			if strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}

			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return nil
			}
			depth := len(strings.Split(rel, string(filepath.Separator)))
			if depth > MaxDepth {
				return fs.SkipDir
			}

			if file, ok := FindComposeFile(path); ok {
				dir := filepath.Clean(path)
				if !seen[dir] {
					seen[dir] = true
					out = append(out, Candidate{
						Name:     filepath.Base(dir),
						Dir:      dir,
						File:     file,
						Enrolled: enrolled[dir],
					})
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sortCandidates(out)
	return out, nil
}

// DockerCandidates returns stacks represented by running Compose containers.
// A candidate is emitted only when Compose labels identify an absolute working
// directory, at least one config file, and a compose file currently exists
// there.
func DockerCandidates(containers []local.Container, inv inventory.Inventory) []Candidate {
	enrolled := enrolledDirs(inv)
	seenProjects := map[string]bool{}
	out := make([]Candidate, 0, len(containers))
	for _, c := range containers {
		if c.Project == "" || seenProjects[c.Project] {
			continue
		}
		if c.ProjectWorkingDir == "" || !filepath.IsAbs(c.ProjectWorkingDir) || strings.TrimSpace(c.ProjectConfigFiles) == "" {
			continue
		}
		seenProjects[c.Project] = true
		dir := filepath.Clean(c.ProjectWorkingDir)
		file, ok := FindComposeFile(dir)
		if !ok {
			continue
		}
		out = append(out, Candidate{
			Name:     c.Project,
			Project:  c.Project,
			Dir:      dir,
			File:     file,
			Enrolled: enrolled[dir],
		})
	}
	sortCandidates(out)
	return out
}

// Merge combines candidate sources, keeping one candidate per directory.
func Merge(inv inventory.Inventory, sources ...[]Candidate) []Candidate {
	enrolled := enrolledDirs(inv)
	byDir := map[string]Candidate{}
	for _, source := range sources {
		for _, candidate := range source {
			dir := filepath.Clean(candidate.Dir)
			if _, exists := byDir[dir]; exists {
				continue
			}
			candidate.Dir = dir
			candidate.Enrolled = enrolled[dir]
			byDir[dir] = candidate
		}
	}
	out := make([]Candidate, 0, len(byDir))
	for _, candidate := range byDir {
		out = append(out, candidate)
	}
	sortCandidates(out)
	return out
}

func enrolledDirs(inv inventory.Inventory) map[string]bool {
	enrolled := make(map[string]bool, len(inv.Stacks))
	for _, s := range inv.Stacks {
		enrolled[filepath.Clean(s.Dir)] = true
	}
	return enrolled
}

func sortCandidates(candidates []Candidate) {
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Name != candidates[j].Name {
			return candidates[i].Name < candidates[j].Name
		}
		return candidates[i].Dir < candidates[j].Dir
	})
}
