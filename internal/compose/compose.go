// Package compose parses a stack's compose file into services and image
// references, recording the byte range of each image value so that bump can
// rewrite it without reserialising the document. An image a service inherits
// -- from an anchor, or from another service -- is reported like any other,
// but has no bytes of its own to rewrite.
package compose

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
	"gopkg.in/yaml.v3"

	"github.com/gvwalker/stackmon/internal/imageref"
	"github.com/gvwalker/stackmon/internal/inventory"
)

// projectName is Compose's own rule for a project name: lowercase
// alphanumerics, dashes and underscores, starting with a letter or digit.
// Compose rejects anything else, so a binding stackmon accepts but Compose
// would refuse would only ever fail later, at `docker compose up`.
var projectName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// ValidProjectName rejects a Docker Compose project name Compose itself would
// not accept. Validating at the command boundary means a typo surfaces where
// it was typed, rather than as a project that never matches anything.
func ValidProjectName(name string) error {
	if !projectName.MatchString(name) {
		return fmt.Errorf("compose: %q is not a valid Compose project name; use lowercase letters, digits, dashes and underscores, starting with a letter or digit", name)
	}
	return nil
}

// Origin says where a service's image value is written, which is what
// decides whether bump may rewrite it: only a value the service owns, and
// that no other service depends on, is a Pin.
type Origin string

const (
	// OriginOwn is a literal written in the service's own mapping and used
	// by no other service: the exact bytes bump would rewrite.
	OriginOwn Origin = "own"
	// OriginShared is a literal written in the service's own mapping but
	// used by another service -- through an alias, a merge key, or
	// Compose's extends. Rewriting it would change that service too.
	OriginShared Origin = "shared"
	// OriginMerge is a value the service inherits through a YAML merge key:
	// its text lives with an anchor.
	OriginMerge Origin = "merge"
	// OriginAlias is a value that is an alias of an anchor: its text lives
	// with the anchor.
	OriginAlias Origin = "alias"
	// OriginExtends is a value the service inherits from another service
	// through Compose's extends: its text lives in that service.
	OriginExtends Origin = "extends"
	// OriginExternal is a value declared outside this file, in a file
	// Compose includes: its text lives there.
	OriginExternal Origin = "external"
)

// Rewritable reports whether the value is the service's own literal and
// nothing else's, so bump may rewrite exactly those bytes.
func (o Origin) Rewritable() bool { return o == OriginOwn }

// Service is one service's image and where it lives in the file.
type Service struct {
	Name string
	Ref  imageref.Ref
	// Platform is the target platform Compose declares for the service,
	// empty when it declares none. It selects which child of a
	// multi-platform image carries the metadata stackmon reports, so it is
	// recorded exactly as written and resolved once, against the registry's
	// default, further out.
	Platform string
	// Offset and Length delimit Ref.Raw within the compose file. They
	// address the image value only when Origin is OriginOwn: every other
	// origin keeps its text somewhere else -- in an anchor, or in another
	// service -- and is left with a range that cannot be rewritten.
	Offset int
	Length int
	// Origin says where the image value is written.
	Origin Origin
}

// Stack is a parsed compose file.
type Stack struct {
	// Name is the enrolled display name. It is not a Docker Compose project
	// name: the two can differ, and matching one against the other is how a
	// running service used to be reported as stopped.
	Name string
	Dir  string
	File string
	// Project is an explicit binding to a Docker Compose project, empty when
	// the project is to be matched from the paths Docker recorded.
	Project  string
	Services []Service
	// ServiceNames includes every Compose declaration, including build-only
	// services and services whose image could not be safely inspected.
	// Services contains only the inspectable image declarations.
	ServiceNames []string
	// Warnings records per-service issues that were skipped rather than
	// failing the whole stack: one unreadable image value must not erase
	// its siblings.
	Warnings []error
}

// Path is the absolute path of the compose file.
func (s Stack) Path() string { return filepath.Join(s.Dir, s.File) }

// Load parses the enrolled stack. Interpolation uses the process environment
// plus the stack's .env file, exactly as Compose does: a .env that exists but
// cannot be read or parsed fails the load, because silently interpolating
// without it would report a reference the user never wrote.
func Load(ctx context.Context, s inventory.Stack) (Stack, error) {
	name, dir, file := s.Name, s.Dir, s.File
	path := filepath.Join(dir, file)

	data, err := os.ReadFile(path)
	if err != nil {
		return Stack{}, fmt.Errorf("compose: reading %s: %w", path, err)
	}

	raw, err := rawImages(data)
	if err != nil {
		return Stack{}, fmt.Errorf("compose: scanning %s: %w", path, err)
	}

	// Read before loading: the .env decides what the compose file means, so
	// a .env stackmon could not parse has to fail the load rather than
	// silently produce a declaration resolved against the wrong values.
	env, err := environment(dir)
	if err != nil {
		return Stack{}, err
	}

	project, err := loader.LoadWithContext(ctx, types.ConfigDetails{
		WorkingDir:  dir,
		ConfigFiles: []types.ConfigFile{{Filename: path, Content: data}},
		Environment: env,
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

	st := Stack{Name: name, Dir: dir, File: file, Project: s.Project, ServiceNames: project.ServiceNames()}
	st.ServiceNames = append(st.ServiceNames, project.DisabledServiceNames()...)
	sort.Strings(st.ServiceNames)
	for svcName, svc := range project.Services {
		if strings.TrimSpace(svc.Image) == "" {
			// A build-only service has nothing to check.
			continue
		}
		decl, ok := raw[svcName]
		if !ok {
			// Compose resolved an image for a service this file does not
			// declare one for: the value came from a file stackmon cannot
			// see. It is still a declaration worth reporting, and one
			// stackmon must not rewrite.
			decl = declaration{origin: OriginExternal}
		}
		// The text as written is the raw reference; Compose's resolved
		// value is the same text after interpolation. A declaration whose
		// text stackmon cannot see is reported as the resolved value, which
		// is the reference the stack really runs.
		text := decl.text
		if text == "" {
			text = svc.Image
		}
		ref, err := imageref.Parse(text, svc.Image)
		if err != nil {
			return Stack{}, fmt.Errorf("compose: service %q: %w", svcName, err)
		}

		svcOut := Service{
			Name:     svcName,
			Ref:      ref,
			Platform: svc.Platform,
			Origin:   decl.origin,
			// A range that addresses nothing: only a declaration of the
			// service's own carries bytes to rewrite.
			Offset: -1,
			Length: 0,
		}

		if decl.origin.Rewritable() {
			// A quoted scalar's reported Column points at the opening
			// quote, not the first content character, so the span verified
			// against the file's bytes must include the quote characters
			// too. The stored Offset/Length brackets only the inner text --
			// aligned with Ref.Raw, which is the decoded value -- so bump
			// can rewrite it while leaving the quotes in place; image
			// references never contain characters that need YAML escaping,
			// so the decoded value's bytes always equal the inner literal
			// bytes exactly.
			quote := ""
			switch {
			case decl.style&yaml.DoubleQuotedStyle != 0:
				quote = `"`
			case decl.style&yaml.SingleQuotedStyle != 0:
				quote = `'`
			}
			length := len(decl.text)
			fullLength := length + 2*len(quote)
			want := quote + decl.text + quote

			// bump rewrites exactly the stored [Offset, Offset+Length) in
			// the real file, so a wrong position must be caught here rather
			// than silently corrupt a user's compose file later. One
			// unreadable service is skipped with a recorded warning rather
			// than failing the whole stack: its siblings still parsed
			// correctly.
			got := ""
			if decl.offset >= 0 && decl.offset+fullLength <= len(data) {
				got = string(data[decl.offset : decl.offset+fullLength])
			}
			if got != want {
				st.Warnings = append(st.Warnings, fmt.Errorf("compose: service %q in %s: offset [%d:%d] holds %q, want image text %q", svcName, path, decl.offset, decl.offset+fullLength, got, want))
				continue
			}
			svcOut.Offset = decl.offset + len(quote)
			svcOut.Length = length
		}
		st.Services = append(st.Services, svcOut)
	}

	sort.Slice(st.Services, func(i, j int) bool { return st.Services[i].Name < st.Services[j].Name })
	return st, nil
}

// environment returns the process environment overlaid with the stack's .env
// file, matching Compose's precedence where the process environment wins. A
// .env that cannot be read or parsed is an error rather than a silent absence:
// a value stackmon failed to see would otherwise reach the user as a wrong
// image reference with nothing pointing at the file that caused it.
func environment(dir string) (types.Mapping, error) {
	file, err := readDotenv(filepath.Join(dir, ".env"))
	if err != nil {
		return nil, err
	}

	env := types.Mapping{}
	for k, v := range file {
		env[k] = v
	}
	// The process environment is applied over the file rather than
	// substituted into it: an explicitly empty exported variable is an
	// override, which a substitution would treat as unset.
	for k, v := range processEnv() {
		env[k] = v
	}
	return env, nil
}

// readDotenv parses one .env file with compose-go's dotenv parser -- the same
// parser Docker Compose itself uses -- so quoting, escapes, comments,
// ${VAR} references and ${VAR:-default} behave here exactly as they would
// there. Returns nil for a missing file, which is the normal case for a stack
// whose variables live in the shell or in the compose file itself.
func readDotenv(path string) (map[string]string, error) {
	// Read the whole file first so a failure to read it -- a directory where
	// a file belongs, a permission the user cannot grant stackmon -- is
	// reported as a read failure rather than as malformed input, which the
	// parser could not tell apart from a syntax error.
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("compose: reading %s: %w", path, err)
	}

	// Values already exported are what Compose would expand a ${VAR}
	// reference to, so they are handed to the parser as a lookup rather
	// than applied afterwards: BASE=1.0.0 followed by TAG=${BASE} must see
	// an exported BASE=2.0, the same way docker compose does.
	proc := processEnv()
	vars, err := dotenv.UnmarshalBytesWithLookup(data, func(k string) (string, bool) {
		v, ok := proc[k]
		return v, ok
	})
	if err != nil {
		// The parser's messages quote the offending line and variable
		// names verbatim, and a .env is where credentials live, so the
		// failure is reported by path and category alone. The redacted
		// secret never reaches stackmon's output.
		return nil, fmt.Errorf("compose: %s: invalid .env syntax", path)
	}
	return vars, nil
}

// processEnv is the process environment as a map. Reading it is all the
// resolution does: stackmon never exports a .env value back into its own
// environment, so one stack's file cannot leak into another's interpolation.
func processEnv() map[string]string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, found := strings.Cut(kv, "="); found {
			env[k] = v
		}
	}
	return env
}

// declaration is one service's image value: the text as written, and where
// it comes from.
type declaration struct {
	// text is the image value as written, before interpolation.
	text string
	// offset is where text begins in the file. It is set only for a value
	// the service owns, because only then are those bytes the ones bump
	// would rewrite.
	offset int
	// style records whether the value was quoted, so the byte span
	// verified against the file can account for the quote characters.
	style yaml.Style
	// origin says where the value comes from.
	origin Origin
}

// rawImages finds the image value each service declares, along with where it
// is written. compose-go resolves values but discards positions, so the
// document is walked a second time with yaml.v3, which reports line and
// column for every node and keeps the anchors and aliases intact.
func rawImages(data []byte) (map[string]declaration, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	out := map[string]declaration{}
	if len(doc.Content) == 0 {
		return out, nil
	}

	lineStarts := lineOffsets(data)

	root := doc.Content[0]
	services := mappingValue(root, "services")
	if services == nil {
		return out, nil
	}

	// A service another service depends on must keep its literal: rewriting
	// it would change a consumer the user did not select.
	consumed := consumedServices(services)

	// A mapping's Content alternates key, value, key, value.
	for i := 0; i+1 < len(services.Content); i += 2 {
		svcName := services.Content[i].Value
		decl, err := imageDeclaration(data, lineStarts, consumed, svcName, services.Content[i+1])
		if err != nil {
			return nil, fmt.Errorf("service %q: %w", svcName, err)
		}
		if decl == nil {
			continue
		}
		out[svcName] = *decl
	}
	return out, nil
}

// imageDeclaration describes the image value one service declares: the text
// as written, and where it comes from.
func imageDeclaration(data []byte, lineStarts []int, consumed map[string]bool, name string, svc *yaml.Node) (*declaration, error) {
	if svc == nil || svc.Kind != yaml.MappingNode {
		return nil, nil
	}

	image := mappingValue(svc, "image")
	switch {
	case image == nil:
		// No image of its own: the value comes from a merge key, or from
		// another service.
		if merged := mergedImage(svc, nil); merged != nil {
			decl := &declaration{origin: OriginMerge}
			decl.text, decl.style = scalarText(merged)
			return decl, nil
		}
		return &declaration{origin: OriginExtends}, nil

	case image.Kind == yaml.AliasNode:
		// The value is an alias: its text lives with the anchor.
		decl := &declaration{origin: OriginAlias}
		if image.Alias != nil {
			decl.text, decl.style = scalarText(image.Alias)
		}
		return decl, nil

	case image.Kind == yaml.ScalarNode:
		origin := OriginOwn
		if consumed[name] {
			origin = OriginShared
		}
		off, err := scalarOffset(data, lineStarts, image)
		if err != nil {
			return nil, err
		}
		return &declaration{text: image.Value, offset: off, style: image.Style, origin: origin}, nil
	}
	return nil, nil
}

// scalarOffset returns the byte offset of a scalar's text. An anchored
// scalar is reported at the anchor's position, not at the value's, so the
// text starts after the anchor and the whitespace that follows it.
func scalarOffset(data []byte, lineStarts []int, n *yaml.Node) (int, error) {
	off, err := offsetOf(data, lineStarts, n.Line, n.Column)
	if err != nil {
		return 0, err
	}
	if n.Anchor == "" {
		return off, nil
	}
	pos := off + len("&"+n.Anchor)
	for pos < len(data) {
		switch data[pos] {
		case ' ', '\t':
			pos++
			continue
		case '\n', '\r':
			// The value may start on the next line, indented under it.
			pos++
			for pos < len(data) && (data[pos] == ' ' || data[pos] == '\t') {
				pos++
			}
			continue
		}
		break
	}
	return pos, nil
}

// scalarText returns a node's text and quoting style, empty for a node that
// is not a scalar. An alias is followed to the value it names.
func scalarText(n *yaml.Node) (string, yaml.Style) {
	if n == nil {
		return "", 0
	}
	if n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	if n == nil || n.Kind != yaml.ScalarNode {
		return "", 0
	}
	return n.Value, n.Style
}

// mergedImage finds the image value a service inherits through a YAML merge
// key. The service's own keys win, and among several merged mappings the
// first one that carries the key wins, as YAML merge semantics say.
func mergedImage(mapping *yaml.Node, seen map[*yaml.Node]bool) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	if seen == nil {
		seen = map[*yaml.Node]bool{}
	}
	if seen[mapping] {
		// An anchor that merges itself would otherwise loop forever.
		return nil
	}
	seen[mapping] = true

	if image := mappingValue(mapping, "image"); image != nil {
		return image
	}
	merge := mappingValue(mapping, "<<")
	for _, target := range mergeTargets(merge) {
		if image := mergedImage(target, seen); image != nil {
			return image
		}
	}
	return nil
}

// mergeTargets returns the mappings a merge key pulls in, in the order YAML
// gives them precedence.
func mergeTargets(merge *yaml.Node) []*yaml.Node {
	if merge == nil {
		return nil
	}
	switch merge.Kind {
	case yaml.AliasNode:
		if merge.Alias != nil {
			return []*yaml.Node{merge.Alias}
		}
	case yaml.SequenceNode:
		var out []*yaml.Node
		for _, item := range merge.Content {
			out = append(out, mergeTargets(item)...)
		}
		return out
	}
	return nil
}

// consumedServices returns the services whose image another service depends
// on: through a YAML alias, through a merge key, or through Compose's
// extends. Such a service's literal is not a Pin, because rewriting it
// would change a consumer the user did not select.
func consumedServices(services *yaml.Node) map[string]bool {
	consumed := map[string]bool{}
	if services == nil || services.Kind != yaml.MappingNode {
		return consumed
	}

	// The anchor each service publishes: the one on its own mapping, and
	// the one on its own image value.
	owners := map[string]string{}
	names := map[string]bool{}
	for i := 0; i+1 < len(services.Content); i += 2 {
		name := services.Content[i].Value
		names[name] = true
		svc := services.Content[i+1]
		if svc == nil {
			continue
		}
		if svc.Anchor != "" {
			owners[svc.Anchor] = name
		}
		if image := mappingValue(svc, "image"); image != nil && image.Kind == yaml.ScalarNode && image.Anchor != "" {
			owners[image.Anchor] = name
		}
	}

	for i := 0; i+1 < len(services.Content); i += 2 {
		name := services.Content[i].Value
		svc := services.Content[i+1]
		if svc == nil {
			continue
		}
		// Every anchor this service refers to.
		for anchor := range referencedAnchors(svc, map[string]bool{}) {
			if owner, ok := owners[anchor]; ok && owner != name {
				consumed[owner] = true
			}
		}
		// Compose's own inheritance: a service that extends another one
		// runs that one's image.
		if base := extendsBase(svc); base != "" && names[base] {
			consumed[base] = true
		}
	}
	return consumed
}

// referencedAnchors returns the anchors a node refers to. The aliases
// themselves are not followed: an alias's target is the definition it points
// at, not a reference the definition's owner made.
func referencedAnchors(n *yaml.Node, out map[string]bool) map[string]bool {
	if n == nil {
		return out
	}
	switch n.Kind {
	case yaml.AliasNode:
		if n.Alias != nil && n.Alias.Anchor != "" {
			out[n.Alias.Anchor] = true
		}
	case yaml.MappingNode:
		// A mapping's Content alternates key, value, key, value.
		for i := 0; i+1 < len(n.Content); i += 2 {
			referencedAnchors(n.Content[i+1], out)
		}
	case yaml.SequenceNode:
		for _, item := range n.Content {
			referencedAnchors(item, out)
		}
	}
	return out
}

// extendsBase returns the name of the service this one extends, empty when
// it extends none.
func extendsBase(svc *yaml.Node) string {
	extends := mappingValue(svc, "extends")
	if extends == nil || extends.Kind != yaml.MappingNode {
		return ""
	}
	base := mappingValue(extends, "service")
	if base == nil {
		return ""
	}
	return base.Value
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
