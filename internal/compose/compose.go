// Package compose parses a stack's compose file into services and image
// references, recording the byte range of each image value so that bump can
// rewrite it without reserialising the document.
package compose

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
	"gopkg.in/yaml.v3"

	"github.com/gvwalker/stackmon/internal/imageref"
)

// Service is one service's image and where it lives in the file.
type Service struct {
	Name string
	Ref  imageref.Ref
	// Offset and Length delimit Ref.Raw within the compose file.
	Offset int
	Length int
}

// Stack is a parsed compose file.
type Stack struct {
	Name     string
	Dir      string
	File     string
	Services []Service
	// Warnings records per-service issues that were skipped rather than
	// failing the whole stack: one unreadable image value must not erase
	// its siblings.
	Warnings []error
}

// Path is the absolute path of the compose file.
func (s Stack) Path() string { return filepath.Join(s.Dir, s.File) }

// Load parses the stack at dir/file. Interpolation uses the process
// environment plus the stack's .env file, exactly as Compose does.
func Load(ctx context.Context, name, dir, file string) (Stack, error) {
	path := filepath.Join(dir, file)

	data, err := os.ReadFile(path)
	if err != nil {
		return Stack{}, fmt.Errorf("compose: reading %s: %w", path, err)
	}

	raw, err := rawImages(data)
	if err != nil {
		return Stack{}, fmt.Errorf("compose: scanning %s: %w", path, err)
	}

	project, err := loader.LoadWithContext(ctx, types.ConfigDetails{
		WorkingDir:  dir,
		ConfigFiles: []types.ConfigFile{{Filename: path, Content: data}},
		Environment: environment(dir),
	}, func(o *loader.Options) {
		// compose-go rejects any non-normalised name when a project name is
		// imperatively set. The inventory's display name is kept as
		// enrolled -- normalisation is a compose-loading detail, not a
		// rename of the user's stack -- so normalise only the name handed
		// to compose-go here, matching what Docker Compose itself does.
		o.SetProjectName(loader.NormalizeProjectName(name), true)
		// Resolving paths would fail on bind mounts pointing at absent
		// directories, and stackmon only needs image fields.
		o.ResolvePaths = false
		o.SkipValidation = true
		// env_file entries keep relative paths when ResolvePaths is off, and
		// compose-go stats them against the process's cwd rather than
		// WorkingDir, so a real env_file that exists would otherwise fail to
		// load. stackmon never reads env_file contents, only svc.Image, so
		// skip resolving them entirely.
		o.SkipResolveEnvironment = true
	})
	if err != nil {
		return Stack{}, fmt.Errorf("compose: loading %s: %w", path, err)
	}

	st := Stack{Name: name, Dir: dir, File: file}
	for svcName, svc := range project.Services {
		if strings.TrimSpace(svc.Image) == "" {
			// A build-only service has nothing to check.
			continue
		}
		pos, ok := raw[svcName]
		if !ok {
			return Stack{}, fmt.Errorf("compose: no image line found for service %q in %s", svcName, path)
		}
		ref, err := imageref.Parse(pos.text, svc.Image)
		if err != nil {
			return Stack{}, fmt.Errorf("compose: service %q: %w", svcName, err)
		}

		// A quoted scalar's reported Column points at the opening quote,
		// not the first content character, so the span verified against
		// the file's bytes must include the quote characters too. The
		// stored Offset/Length brackets only the inner text -- aligned
		// with Ref.Raw, which is the decoded value -- so bump can rewrite
		// it while leaving the quotes in place; image references never
		// contain characters that need YAML escaping, so the decoded
		// value's bytes always equal the inner literal bytes exactly.
		quote := ""
		switch {
		case pos.style&yaml.DoubleQuotedStyle != 0:
			quote = `"`
		case pos.style&yaml.SingleQuotedStyle != 0:
			quote = `'`
		}
		length := len(pos.text)
		fullLength := length + 2*len(quote)
		want := quote + pos.text + quote

		// bump rewrites exactly the stored [Offset, Offset+Length) in the
		// real file, so a wrong position must be caught here rather than
		// silently corrupt a user's compose file later. One unreadable
		// service is skipped with a recorded warning rather than failing
		// the whole stack: its siblings still parsed correctly.
		got := ""
		if pos.offset >= 0 && pos.offset+fullLength <= len(data) {
			got = string(data[pos.offset : pos.offset+fullLength])
		}
		if got != want {
			st.Warnings = append(st.Warnings, fmt.Errorf("compose: service %q in %s: offset [%d:%d] holds %q, want image text %q", svcName, path, pos.offset, pos.offset+fullLength, got, want))
			continue
		}
		st.Services = append(st.Services, Service{
			Name:   svcName,
			Ref:    ref,
			Offset: pos.offset + len(quote),
			Length: length,
		})
	}

	sort.Slice(st.Services, func(i, j int) bool { return st.Services[i].Name < st.Services[j].Name })
	return st, nil
}

// environment returns the process environment overlaid with the stack's .env
// file, matching Compose's precedence where the process environment wins.
func environment(dir string) types.Mapping {
	env := types.Mapping{}

	if data, err := os.ReadFile(filepath.Join(dir, ".env")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			k, v, found := strings.Cut(line, "=")
			if !found {
				continue
			}
			env[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}

	for _, kv := range os.Environ() {
		if k, v, found := strings.Cut(kv, "="); found {
			env[k] = v
		}
	}
	return env
}

type imagePos struct {
	text   string
	offset int
	// style records whether the YAML node was quoted, so the byte span
	// verified against the file can account for the quote characters.
	style yaml.Style
}

// rawImages finds the uninterpolated image value for each service along with
// its byte offset. compose-go resolves values but discards positions, so the
// document is walked a second time with yaml.v3, which reports line and
// column for every node.
func rawImages(data []byte) (map[string]imagePos, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	out := map[string]imagePos{}
	if len(doc.Content) == 0 {
		return out, nil
	}

	lineStarts := lineOffsets(data)

	root := doc.Content[0]
	services := mappingValue(root, "services")
	if services == nil {
		return out, nil
	}

	// A mapping's Content alternates key, value, key, value.
	for i := 0; i+1 < len(services.Content); i += 2 {
		svcName := services.Content[i].Value
		image := mappingValue(services.Content[i+1], "image")
		if image == nil {
			continue
		}
		off, err := offsetOf(data, lineStarts, image.Line, image.Column)
		if err != nil {
			return nil, fmt.Errorf("service %q: %w", svcName, err)
		}
		out[svcName] = imagePos{text: image.Value, offset: off, style: image.Style}
	}
	return out, nil
}

// mappingValue returns the value node for key in a mapping node.
func mappingValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// lineOffsets returns the byte offset at which each 1-indexed line begins.
func lineOffsets(data []byte) []int {
	offsets := []int{0, 0} // index 0 unused; line 1 starts at byte 0
	for i, b := range data {
		if b == '\n' {
			offsets = append(offsets, i+1)
		}
	}
	return offsets
}

// offsetOf converts a 1-indexed line and 1-indexed column into a byte
// offset. yaml.Node.Column counts runes, not bytes: it is incremented once
// per character scanned regardless of that character's UTF-8 width. A byte
// offset is only correct if it walks the same number of runes rather than
// adding column-1 bytes directly.
func offsetOf(data []byte, lineStarts []int, line, column int) (int, error) {
	if line < 1 || line >= len(lineStarts) {
		return 0, fmt.Errorf("line %d out of range", line)
	}
	lineEnd := len(data)
	if line+1 < len(lineStarts) {
		lineEnd = lineStarts[line+1]
	}
	pos := lineStarts[line]
	for i := 1; i < column; i++ {
		if pos >= lineEnd {
			return 0, fmt.Errorf("column %d past end of line %d", column, line)
		}
		_, size := utf8.DecodeRune(data[pos:lineEnd])
		pos += size
	}
	if pos > lineEnd {
		return 0, fmt.Errorf("column %d past end of line %d", column, line)
	}
	return pos, nil
}
