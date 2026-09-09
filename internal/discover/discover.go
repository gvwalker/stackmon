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

// Scan walks each root looking for compose files, marking those already in
// inv as enrolled. Roots that do not exist are skipped, since a drive may
// simply not be mounted. Results are sorted by name.
func Scan(roots []string, inv inventory.Inventory) ([]Candidate, error) {
	enrolled := make(map[string]bool, len(inv.Stacks))
	for _, s := range inv.Stacks {
		enrolled[filepath.Clean(s.Dir)] = true
	}

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
			if !d.IsDir() || path == root {
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

			if file, ok := FindComposeFile(path); ok && !seen[path] {
				seen[path] = true
				out = append(out, Candidate{
					Name:     filepath.Base(path),
					Dir:      path,
					File:     file,
					Enrolled: enrolled[path],
				})
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Dir < out[j].Dir
	})
	return out, nil
}
