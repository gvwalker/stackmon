// Package e2e drives the built stackmon binary end to end: a real CLI
// process, real compose files on disk, a real in-memory OCI registry, a real
// Docker Engine API on a unix socket, and a real GitHub API.
//
// Nothing here imports stackmon's internal packages, so a scenario passes only
// when the binary a user would install actually works. Every run appends its
// output to a transcript, which TestMain writes to out/transcript.txt and
// compares against testdata/transcript.golden -- the run's verifiable
// artifact. Regenerate the golden with UPDATE_GOLDEN=1.
package e2e

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	ggcrr "github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

const (
	goldenPath   = "testdata/transcript.golden"
	artifactPath = "out/transcript.txt"
	// taggedVersion is baked into the second binary so the version-comparing
	// paths (whats-new, update) run against something newer than itself.
	taggedVersion = "v0.0.1"
)

var (
	// devBinary is the build a plain `go build` produces: version "dev".
	devBinary string
	// taggedBinary reports a real version, so the release-comparison branches
	// are reachable without a published release.
	taggedBinary string
	transcript   strings.Builder
)

// TestMain builds the binaries once, runs the scenarios, then writes and
// verifies the transcript artifact. Comparing the artifact here (rather than
// in a test) keeps the check independent of the order tests happen to run in.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "stackmon-e2e-bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	devBinary = filepath.Join(dir, "stackmon")
	taggedBinary = filepath.Join(dir, "stackmon-"+taggedVersion)
	build(devBinary, "")
	build(taggedBinary, taggedVersion)

	code := m.Run()
	os.Exit(finish(code, dir))
}

func build(out, version string) {
	args := []string{"build", "-o", out}
	if version != "" {
		args = append(args, "-ldflags", "-X main.version="+version)
	}
	args = append(args, "./cmd/stackmon")

	cmd := exec.Command("go", args...)
	cmd.Dir = ".."
	if b, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building stackmon: %v\n%s", err, b)
		os.Exit(1)
	}
}

// finish writes the artifact and, when the scenarios passed, holds the whole
// transcript against the golden.
func finish(code int, tmpDir string) int {
	defer os.RemoveAll(tmpDir)

	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	got := transcript.String()
	if err := os.WriteFile(artifactPath, []byte(got), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if code != 0 {
		fmt.Fprintf(os.Stderr, "\nartifact: %s (scenarios failed; golden not compared)\n", artifactPath)
		return code
	}

	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "\ngolden updated: %s\nartifact: %s\n", goldenPath, artifactPath)
		return 0
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nartifact: %s\nno golden: %v (regenerate with UPDATE_GOLDEN=1)\n", artifactPath, err)
		return 1
	}
	if string(want) != got {
		fmt.Fprintf(os.Stderr, "\nartifact: %s\ntranscript differs from %s:\n%s", artifactPath, goldenPath, firstDiff(string(want), got))
		return 1
	}
	fmt.Fprintf(os.Stderr, "\nartifact: %s (%d lines, matches %s)\n", artifactPath, strings.Count(got, "\n"), goldenPath)
	return 0
}

// firstDiff reports the first differing line with a little context, so a
// golden mismatch is readable without opening both files.
func firstDiff(want, got string) string {
	wl, gl := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < max(len(wl), len(gl)); i++ {
		var w, g string
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w == g {
			continue
		}
		from := max(i-3, 0)
		var b strings.Builder
		fmt.Fprintf(&b, "  first difference at line %d\n", i+1)
		for j := from; j < min(i+4, max(len(wl), len(gl))); j++ {
			lw, lg := "-", "-"
			if j < len(wl) {
				lw = wl[j]
			}
			if j < len(gl) {
				lg = gl[j]
			}
			marker := " "
			if j == i {
				marker = ">"
			}
			fmt.Fprintf(&b, "  %s golden: %s\n    actual: %s\n", marker, strings.TrimRight(lw, " "), strings.TrimRight(lg, " "))
		}
		return b.String()
	}
	return "  (files differ only in trailing bytes)\n"
}

// ---------------------------------------------------------------------------
// Sandbox
// ---------------------------------------------------------------------------

// env is one scenario's isolated world: its own HOME (so stackmon's default
// config and inventory paths land inside it), registry, Docker socket and
// GitHub API.
type env struct {
	t      *testing.T
	root   string
	host   string // registry host:port
	hits   *requestLog
	docker *fakeDocker
	gh     *fakeGitHub
	over   map[string]string

	mu sync.Mutex
	// stackPaths records the compose file of every stack written under the
	// discovery root, keyed by its directory name, so the fake daemon can
	// report the labels Docker always sets when a project is started from a
	// directory. A scenario that cares about a project's labels sets them
	// explicitly instead.
	stackPaths map[string]string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, root: fixedLengthTempDir(t), over: map[string]string{}, stackPaths: map[string]string{}}

	e.hits = &requestLog{next: ggcrr.New(ggcrr.Logger(log.New(io.Discard, "", 0)))}
	srv := httptest.NewServer(e.hits)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	e.host = u.Host
	e.docker = newFakeDocker(t, e.composeFileOf)
	e.gh = newFakeGitHub(t)
	return e
}

// composeFileOf is the compose file belonging to a project whose containers
// Docker reports, for projects the scenario named after a stack directory.
func (e *env) composeFileOf(project string) (string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	path, ok := e.stackPaths[project]
	return path, ok
}

// requestLog counts registry requests by method and path, so a scenario can
// prove the run probed a shared reference once, and that it never listed tags
// for a tag stackmon cannot reason about.
type requestLog struct {
	next http.Handler

	mu      sync.Mutex
	paths   []string
	blocked []string
	held    []*gate
}

func (l *requestLog) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	l.mu.Lock()
	l.paths = append(l.paths, r.Method+" "+r.URL.Path)
	gate := l.gateFor(r.URL.Path)
	blocked := false
	for _, p := range l.blocked {
		if strings.HasPrefix(r.URL.Path, p) {
			blocked = true
			break
		}
	}
	l.mu.Unlock()

	if gate != nil {
		// Freeze the run with the file it read but not yet written, so a
		// scenario can change that file first.
		gate.once.Do(func() { close(gate.parked) })
		<-gate.release
	}
	if blocked {
		// A registry that lists the tag but cannot serve it: a rate limit or
		// a partial outage, which stackmon must survive.
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	l.next.ServeHTTP(w, r)
}

func (l *requestLog) gateFor(path string) *gate {
	for _, g := range l.held {
		if strings.HasPrefix(path, g.prefix) {
			return g
		}
	}
	return nil
}

// gate holds matching requests until release is closed.
type gate struct {
	prefix  string
	parked  chan struct{}
	release chan struct{}
	once    sync.Once
}

// hold parks every request under prefix. The returned gate reports when a
// request is parked and lets it through. Holding the same prefix twice
// replaces the earlier gate, so each hold is a fresh freeze.
func (l *requestLog) hold(prefix string) *gate {
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := l.held[:0]
	for _, g := range l.held {
		if g.prefix != prefix {
			kept = append(kept, g)
		}
	}
	l.held = append(kept, &gate{prefix: prefix, parked: make(chan struct{}), release: make(chan struct{})})
	return l.held[len(l.held)-1]
}

// wait blocks until a request is parked, or fails the scenario: a hang hides
// what a timeout would explain.
func (g *gate) wait(t *testing.T) {
	t.Helper()
	select {
	case <-g.parked:
	case <-time.After(60 * time.Second):
		t.Fatalf("nothing hit the registry gate for %s", g.prefix)
	}
}

func (g *gate) open() { close(g.release) }

// block makes the registry fail every request under prefix while leaving its
// tag list intact.
func (l *requestLog) block(prefix string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.blocked = append(l.blocked, prefix)
}

func (l *requestLog) count(prefix string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, p := range l.paths {
		if strings.HasPrefix(p, prefix) {
			n++
		}
	}
	return n
}

// reset forgets the setup traffic (the test's own pushes), so a count covers
// the stackmon process alone.
func (l *requestLog) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.paths = nil
}

func (e *env) home() string { return filepath.Join(e.root, "home") }

// fixedLengthTempDir returns a fresh sandbox root whose path is the same
// length on every run. t.TempDir() is not: its random suffix varies in length,
// which shows up as a column width in tabwriter output and would make the
// transcript unstable.
func fixedLengthTempDir(t *testing.T) string {
	t.Helper()
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(os.TempDir(), "stackmon-e2e-"+hex.EncodeToString(b[:]))
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// stacks is the configured discovery root. It has a fixed name so that a
// compose file sitting in the root itself is discovered as a named stack
// rather than as a temp directory's basename.
func (e *env) stacks() string { return filepath.Join(e.root, "srv") }

func (e *env) mkdir(dir string) string {
	e.t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.t.Fatal(err)
	}
	return dir
}

func (e *env) write(path, body string, mode os.FileMode) string {
	e.t.Helper()
	e.mkdir(filepath.Dir(path))
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		e.t.Fatal(err)
	}
	return path
}

func (e *env) read(path string) string {
	e.t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		e.t.Fatal(err)
	}
	return string(b)
}

// config writes $XDG_CONFIG_HOME/stackmon/config.toml, i.e. the path stackmon
// resolves on its own, so the default-path lookup is part of every scenario.
func (e *env) config(body string) string {
	return e.write(filepath.Join(e.home(), ".config", "stackmon", "config.toml"), body, 0o644)
}

// stack writes a compose file for a stack under the discovery root and
// returns its path. The path is recorded, so a fake container naming the same
// project gets the working-directory and config-file labels Compose would
// really have set.
func (e *env) stack(name, body string) string {
	e.t.Helper()
	path := e.write(filepath.Join(e.stacks(), name, "compose.yaml"), body, 0o644)
	e.mu.Lock()
	e.stackPaths[name] = path
	e.mu.Unlock()
	return path
}

// enroll runs `enroll <path>`.
func (e *env) enroll(path string) result { return e.run("enroll", path) }

// set overrides one environment variable for subsequent runs.
func (e *env) set(key, value string) { e.over[key] = value }

var (
	timeRe = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}T[\d:.]+Z\b`)
	dateRe = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}\b`)
)

// normalize erases the parts of an output that vary between runs -- the
// random ports, the temp directory, the wall-clock date -- so the transcript
// can be held against a golden. Anything else that changes between runs fails
// the comparison, which is the point.
func (e *env) normalize(s string) string {
	s = strings.ReplaceAll(s, e.host, "REGISTRY")
	s = strings.ReplaceAll(s, e.gh.server.URL, "GITHUB")
	s = strings.ReplaceAll(s, e.home(), "HOME")
	s = strings.ReplaceAll(s, e.root, "ROOT")
	s = timeRe.ReplaceAllString(s, "TIME")
	s = dateRe.ReplaceAllString(s, "DATE")
	return s
}

// result is one CLI invocation.
type result struct {
	stdout, stderr string
	code           int
}

func (e *env) run(args ...string) result { return e.runBinary(devBinary, args...) }

// pending is a CLI run still in flight, so a scenario can change the world
// underneath it.
type pending struct {
	e    *env
	args []string
	done chan result
}

// start runs the binary in the background. The caller must wait on it.
func (e *env) start(args ...string) *pending {
	e.t.Helper()
	p := &pending{e: e, args: args, done: make(chan result, 1)}
	go func() { p.done <- e.exec(devBinary, args...) }()
	return p
}

func (p *pending) wait(t *testing.T) result {
	t.Helper()
	select {
	case r := <-p.done:
		p.e.record(p.args, r)
		return r
	case <-time.After(60 * time.Second):
		t.Fatalf("stackmon %v did not finish", p.args)
		return result{}
	}
}

func (e *env) runBinary(bin string, args ...string) result {
	e.t.Helper()
	r := e.exec(bin, args...)
	e.record(args, r)
	return r
}

// exec runs the binary without recording. Scenarios use it for the verbose
// --json form, whose meaning is captured by the summary they record instead.
func (e *env) exec(bin string, args ...string) result {
	e.t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = e.environ()
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut

	err := cmd.Run()
	res := result{stdout: out.String(), stderr: errOut.String()}
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		res.code = exit.ExitCode()
	default:
		e.t.Fatalf("running stackmon %v: %v", args, err)
	}
	return res
}

func (e *env) record(args []string, r result) {
	fmt.Fprintf(&transcript, "\n### %s\n$ stackmon %s\n", e.t.Name(), e.normalize(strings.Join(args, " ")))
	if r.stdout != "" {
		fmt.Fprintf(&transcript, "--- stdout ---\n%s", e.normalize(r.stdout))
	}
	if r.stderr != "" {
		fmt.Fprintf(&transcript, "--- stderr ---\n%s", e.normalize(r.stderr))
	}
	fmt.Fprintf(&transcript, "--- exit %d ---\n", r.code)
}

// note records a fact about the sandbox that is not CLI output (a fake
// daemon's request count, a file's final bytes) in the artifact too.
func (e *env) note(format string, args ...any) {
	fmt.Fprintf(&transcript, "### %s\n%s\n", e.t.Name(), fmt.Sprintf(format, args...))
}

// environ builds the child's environment, replacing rather than appending:
// exec passes duplicate keys through, and which one the child reads is not
// something to depend on.
func (e *env) environ() []string {
	set := map[string]string{
		"HOME":                   e.home(),
		"XDG_CONFIG_HOME":        filepath.Join(e.home(), ".config"),
		"XDG_STATE_HOME":         filepath.Join(e.home(), ".local", "state"),
		"STACKMON_DOCKER_SOCKET": e.docker.sock,
		"STACKMON_GITHUB_API":    e.gh.server.URL,
		"NO_PROXY":               "127.0.0.1,localhost",
	}
	for k, v := range e.over {
		set[k] = v
	}
	out := make([]string, 0, len(set))
	for k, v := range set {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Decoded `check --json`
// ---------------------------------------------------------------------------

// report and row are the machine-readable contract: the field names here are
// the JSON keys, so a rename shows up as a decode failure.
type report struct {
	DockerAvailable bool        `json:"docker_available"`
	DockerError     string      `json:"docker_error"`
	Images          []row       `json:"images"`
	Diagnostics     diagnostics `json:"diagnostics"`
}

type diagnostics struct {
	MissingStacks   []stackFailure `json:"missing_stacks"`
	ParseFailures   []stackFailure `json:"parse_failures"`
	ServiceWarnings []stackFailure `json:"service_warnings"`
	StacksChecked   int            `json:"stacks_checked"`
	StacksSkipped   int            `json:"stacks_skipped"`
	ServicesChecked int            `json:"services_checked"`
	ServicesSkipped int            `json:"services_skipped"`
}

type stackFailure struct {
	Stack  string
	Reason string
}

type row struct {
	Stack            string
	Service          string
	Ref              refView
	DeclaredDigest   string `json:"declared_digest"`
	RegistryDigest   string `json:"registry_digest"`
	Running          bool
	Project          string
	ProjectBound     bool   `json:"project_bound"`
	IdentityNote     string `json:"identity_note"`
	Replicas         []replicaView
	ReplicaCount     replicaView `json:"replica_comparison"`
	DockerChecked    bool        `json:"docker_checked"`
	Version          string
	Candidate        string
	CandidateDigest  string   `json:"candidate_digest"`
	KindName         string   `json:"kind"`
	Ordered          []string `json:"ordered"`
	NotesRepo        string   `json:"notes_repo"`
	Revision         string
	RegistryRevision string `json:"registry_revision"`
	Status           string
	Statuses         []string `json:"statuses"`
	Err              string   `json:"error"`
	RunningError     string   `json:"running_error"`
}

// replicaView is the same shape whether it describes one running container or
// the summary of how the service's containers compared.
type replicaView struct {
	Image       string   `json:"image"`
	Digests     []string `json:"digests"`
	DigestError string   `json:"digest_error"`
	Compared    string   `json:"compared"`
	Matching    int      `json:"matching"`
	Mismatching int      `json:"mismatching"`
	Unknown     int      `json:"unknown"`
	Incomplete  bool     `json:"incomplete"`
}

type refView struct {
	Raw          string
	Resolved     string
	Registry     string
	Repository   string
	Tag          string
	Digest       string
	Interpolated bool
	Vars         []string
}

// checkJSON runs `check --json`, decodes it, and records a status-per-image
// summary in the artifact rather than the raw document.
func (e *env) checkJSON(args ...string) report {
	e.t.Helper()
	full := append([]string{"check", "--json"}, args...)
	r := e.exec(devBinary, full...)
	if r.code != 0 {
		e.t.Fatalf("check --json: exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	var rep report
	if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil {
		e.t.Fatalf("check --json: %v\n%s", err, r.stdout)
	}

	fmt.Fprintf(&transcript, "\n### %s\n$ stackmon %s\n", e.t.Name(), strings.Join(full, " "))
	for _, i := range rep.Images {
		version := i.Version
		if version == "" {
			version = "-"
		}
		move := ""
		if i.Candidate != "" {
			move = " -> " + i.Candidate + " (" + i.KindName + ")"
		}
		extra := ""
		if i.Err != "" {
			extra = ": " + i.Err
		}
		if i.IdentityNote != "" {
			extra += " [" + i.IdentityNote + "]"
		}
		// The replica counts and the digest they were judged against belong
		// in the artifact: a status of current means something quite
		// different with two agreeing replicas than with one.
		if n := i.ReplicaCount.Matching + i.ReplicaCount.Mismatching + i.ReplicaCount.Unknown; n > 0 {
			extra += fmt.Sprintf(" [replicas %d/%d match, %d differ, %d unknown of %s]",
				i.ReplicaCount.Matching, n, i.ReplicaCount.Mismatching, i.ReplicaCount.Unknown,
				shortDigest(i.ReplicaCount.Compared))
		}
		fmt.Fprint(&transcript, e.normalize(fmt.Sprintf("  %-12s %-10s %-10s %-18s %s%s\n",
			i.Stack, i.Service, version, i.Status, move, extra)))
	}
	fmt.Fprintf(&transcript, "--- docker available: %t ---\n", rep.DockerAvailable)
	return rep
}

// image returns the row for one service.
func (rep report) image(t *testing.T, stack, service string) row {
	t.Helper()
	for _, i := range rep.Images {
		if i.Stack == stack && i.Service == service {
			return i
		}
	}
	t.Fatalf("no row for %s/%s in report: %+v", stack, service, rep.Images)
	return row{}
}

// shortDigest keeps a digest recognisable in a transcript line: twelve hex
// characters identify a digest as well as all sixty-four do.
func shortDigest(digest string) string {
	if i := strings.Index(digest, ":"); i >= 0 {
		digest = digest[i+1:]
	}
	if len(digest) > 12 {
		return digest[:12]
	}
	return digest
}

// ---------------------------------------------------------------------------
// Decoded `check --compact`
// ---------------------------------------------------------------------------

// compactReport and compactRow are the compact form's contract. Decoding
// ignores fields that are not listed here, so keys(t) against the raw
// document is what holds the shape to exactly the promised set.
type compactReport struct {
	DockerAvailable bool         `json:"docker_available"`
	Images          []compactRow `json:"images"`
}

type compactRow struct {
	Stack        string `json:"stack"`
	Service      string `json:"service"`
	Status       string `json:"status"`
	Version      string `json:"version"`
	Candidate    string `json:"candidate"`
	Err          string `json:"error"`
	IdentityNote string `json:"identity_note"`
}

// compactKeys returns the top-level keys of a `check --compact` document.
func compactKeys(t *testing.T, stdout string) []string {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("check --compact: %v\n%s", err, stdout)
	}
	return slices.Sorted(maps.Keys(doc))
}

// compactRowKeys returns the keys of one compact row.
func compactRowKeys(t *testing.T, stdout string) []string {
	t.Helper()
	var doc struct {
		Images []map[string]any `json:"images"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("check --compact: %v\n%s", err, stdout)
	}
	if len(doc.Images) == 0 {
		t.Fatalf("check --compact: no rows in\n%s", stdout)
	}
	return slices.Sorted(maps.Keys(doc.Images[0]))
}

// checkCompact runs `check --compact`, decodes it, and records the compact
// form itself in the artifact: this form exists to be read by someone else, so
// its bytes are the thing worth holding to a golden. The raw document is
// returned alongside the decoded form, for the assertions that are about the
// shape rather than the values.
func (e *env) checkCompact(args ...string) (compactReport, string) {
	e.t.Helper()
	full := append([]string{"check", "--compact"}, args...)
	r := e.exec(devBinary, full...)
	if r.code != 0 {
		e.t.Fatalf("check --compact: exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	var rep compactReport
	if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil {
		e.t.Fatalf("check --compact: %v\n%s", err, r.stdout)
	}
	e.record(full, r)
	return rep, r.stdout
}

// image returns the compact row for one service.
func (rep compactReport) image(t *testing.T, stack, service string) compactRow {
	t.Helper()
	for _, i := range rep.Images {
		if i.Stack == stack && i.Service == service {
			return i
		}
	}
	t.Fatalf("no row for %s/%s in report: %+v", stack, service, rep.Images)
	return compactRow{}
}

// enrollStack writes a stack's compose file, enrolls it, and returns the
// path. An enroll that does not succeed fails the scenario: every later step
// depends on it.
func (e *env) enrollStack(name, body string) string {
	e.t.Helper()
	p := e.stack(name, body)
	if r := e.enroll(filepath.Dir(p)); r.code != 0 {
		e.t.Fatalf("enrolling %s: exit %d\n%s%s", name, r.code, r.stdout, r.stderr)
	}
	return p
}

// ---------------------------------------------------------------------------
// Assertions
// ---------------------------------------------------------------------------

func assertEq[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s:\n got: %v\nwant: %v", what, got, want)
	}
}

func assertContains(t *testing.T, what, haystack string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(haystack, n) {
			t.Errorf("%s: output does not contain %q:\n%s", what, n, haystack)
		}
	}
}

func assertOmits(t *testing.T, what, haystack string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			t.Errorf("%s: output unexpectedly contains %q:\n%s", what, n, haystack)
		}
	}
}

func assertCode(t *testing.T, what string, got, want int, r result) {
	t.Helper()
	if got != want {
		t.Errorf("%s: exit code = %d, want %d\nstdout: %s\nstderr: %s", what, got, want, r.stdout, r.stderr)
	}
}

func assertEqSlice(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s:\n got: %v\nwant: %v", what, got, want)
	}
}

// ---------------------------------------------------------------------------
// In-memory registry
// ---------------------------------------------------------------------------

// image is the metadata a pushed image carries. Its content is a pure
// function of these fields, so a fixture always produces the same digest,
// which is what lets the transcript be held against a golden.
type image struct {
	// Version, Revision and Source are set as OCI labels when non-empty.
	Version  string
	Revision string
	Source   string
	// NoRevision omits the revision label, as Docker Official Images do.
	NoRevision bool
	// Created overrides the build timestamp, which is how two builds of the
	// same source revision get different digests. Zero derives it from the
	// tag, so a fixture only sets it when the build time is the point.
	Created time.Time
}

// on is a fixed build date, for scenarios where the build time is the point.
func on(month int) time.Time {
	return time.Date(2026, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
}

// ref builds a reference into the sandbox registry.
func (e *env) ref(repo, tag string) string { return e.host + "/" + repo + ":" + tag }

// pin is a digest-only reference: no tag, just repo@digest.
func (e *env) pin(repo, digest string) string { return e.host + "/" + repo + "@" + digest }

// push publishes repo:tag and returns its digest. An unset Version defaults
// to the tag and an unset Revision is derived from it, so two different tags
// are always two different digests.
func (e *env) push(repo, tag string, img image) string {
	e.t.Helper()
	labels := map[string]string{}
	if v := img.Version; v != "" {
		labels["org.opencontainers.image.version"] = v
	}
	rev := img.Revision
	if rev == "" {
		rev = "rev-" + shortHash(tag)
	}
	if !img.NoRevision {
		labels["org.opencontainers.image.revision"] = rev
	}
	if img.Source != "" {
		labels["org.opencontainers.image.source"] = img.Source
	}

	cf, err := empty.Image.ConfigFile()
	if err != nil {
		e.t.Fatal(err)
	}
	cf = cf.DeepCopy()
	// Derived from the tag, not the clock: the same fixture must always
	// produce the same bytes.
	created := img.Created
	if created.IsZero() {
		// Derived from the tag, not the clock: the same fixture must always
		// produce the same bytes.
		created = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(hash32(tag)) * time.Hour)
	}
	cf.Created = v1.Time{Time: created}
	cf.Config.Labels = labels
	pushed, err := mutate.ConfigFile(empty.Image, cf)
	if err != nil {
		e.t.Fatal(err)
	}
	parsed, err := name.ParseReference(e.ref(repo, tag))
	if err != nil {
		e.t.Fatal(err)
	}
	if err := remote.Write(parsed, pushed); err != nil {
		e.t.Fatal(err)
	}
	h, err := pushed.Digest()
	if err != nil {
		e.t.Fatal(err)
	}
	return h.String()
}

// pushIndex publishes a multi-platform index under ref and returns the index
// digest. stackmon must report that digest, not the one platform child's,
// or every digest-pinned multi-arch image looks permanently drifted.
func (e *env) pushIndex(repo, tag string) string {
	e.t.Helper()
	amd64, err := mutate.ConfigFile(empty.Image, configWithLabels(map[string]string{
		"org.opencontainers.image.version": tag,
	}, "amd64"))
	if err != nil {
		e.t.Fatal(err)
	}
	arm64, err := mutate.ConfigFile(empty.Image, configWithLabels(map[string]string{
		"org.opencontainers.image.version": tag,
	}, "arm64"))
	if err != nil {
		e.t.Fatal(err)
	}
	idx := mutate.AppendManifests(empty.Index,
		mutate.IndexAddendum{Add: amd64, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "amd64"}}},
		mutate.IndexAddendum{Add: arm64, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "arm64"}}},
	)
	h, err := idx.Digest()
	if err != nil {
		e.t.Fatal(err)
	}
	parsed, err := name.ParseReference(e.ref(repo, tag))
	if err != nil {
		e.t.Fatal(err)
	}
	if err := remote.WriteIndex(parsed, idx); err != nil {
		e.t.Fatal(err)
	}
	return h.String()
}

func configWithLabels(labels map[string]string, salt string) *v1.ConfigFile {
	cf, _ := empty.Image.ConfigFile()
	cf = cf.DeepCopy()
	cf.Config.Labels = labels
	cf.Config.Env = []string{"PLATFORM=" + salt}
	return cf
}

func hash32(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32() % 8000
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

// sum returns the hex sha256 of b, in the form checksums.txt uses.
func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// ---------------------------------------------------------------------------
// Fake Docker Engine API
// ---------------------------------------------------------------------------

// container is one running Compose container the fake daemon reports.
type container struct {
	Project     string
	WorkingDir  string
	ConfigFiles string
	Service     string
	Image       string
	// ImageID is the local config digest, which is deliberately a different
	// hash space from the manifest digests in RepoDigests.
	ImageID     string
	RepoDigests []string
	// OneOff marks a `docker compose run` container.
	OneOff bool
}

type fakeDocker struct {
	sock string
	// composeFile resolves the compose file a project's containers are
	// attributed to when the scenario did not spell out the labels.
	composeFile func(project string) (string, bool)

	mu         sync.Mutex
	containers []container
	broken     bool
	// failImages makes /images/{id}/json answer 500, as when the daemon
	// cannot serve inspect data: a different fact from an image that
	// legitimately has no RepoDigests.
	failImages bool
	// imageLookups counts /images/{id}/json requests, so a scenario can prove
	// N containers sharing an image cost one lookup, not N.
	imageLookups int
	paths        []string
}

func newFakeDocker(t *testing.T, composeFile func(project string) (string, bool)) *fakeDocker {
	t.Helper()
	d := &fakeDocker{composeFile: composeFile}
	// Not t.TempDir(): unix socket paths are length-limited and the
	// scenario names are long.
	dir, err := os.MkdirTemp("", "sm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	d.sock = filepath.Join(dir, "d.sock")

	ln, err := net.Listen("unix", d.sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.44/containers/json", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.paths = append(d.paths, r.Method+" "+r.URL.Path)
		if d.broken {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"daemon is unhappy"}`))
			return
		}
		out := make([]map[string]any, 0, len(d.containers))
		for _, c := range d.containers {
			out = append(out, map[string]any{
				"Image":   c.Image,
				"ImageID": c.ImageID,
				"Labels": map[string]string{
					"com.docker.compose.project":              c.Project,
					"com.docker.compose.project.working_dir":  d.labelledWorkingDir(c),
					"com.docker.compose.project.config_files": d.labelledConfigFiles(c),
					"com.docker.compose.service":              c.Service,
					"com.docker.compose.oneoff":               strconv.FormatBool(c.OneOff),
				},
			})
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("/v1.44/images/", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.imageLookups++
		d.paths = append(d.paths, r.Method+" "+r.URL.Path)
		if d.failImages {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"inspect unavailable"}`))
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1.44/images/"), "/json")
		for _, c := range d.containers {
			if c.ImageID == id {
				// Exactly as Docker reports them, "repo@digest": splitting
				// off the digest is the client's job, not the daemon's.
				writeJSON(w, map[string]any{"RepoDigests": c.RepoDigests})
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]any{"message": "no such image"})
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return d
}

// running sets the containers the fake daemon reports.
func (d *fakeDocker) running(cs ...container) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.containers = cs
}

// labelledWorkingDir and labelledConfigFiles fill in the labels Compose would
// have set for a project named after a stack directory, so a scenario only has
// to spell out the labels it actually cares about. An empty value means
// "Docker recorded nothing here", which is itself a case under test.
func (d *fakeDocker) labelledWorkingDir(c container) string {
	if c.WorkingDir != "" {
		return c.WorkingDir
	}
	if path, ok := d.composeFile(c.Project); ok {
		return filepath.Dir(path)
	}
	return ""
}

func (d *fakeDocker) labelledConfigFiles(c container) string {
	if c.ConfigFiles != "" {
		return c.ConfigFiles
	}
	if path, ok := d.composeFile(c.Project); ok {
		return path
	}
	return ""
}

// fail makes the daemon exist but fail every request, which is a different
// problem from no daemon at all.
func (d *fakeDocker) fail() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.broken = true
}

// failImageLookups makes image inspection fail while the container listing
// keeps working: a partial failure, distinct from a failed listing.
func (d *fakeDocker) failImageLookups() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failImages = true
}

func (d *fakeDocker) lookups() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.imageLookups
}

// reset forgets the setup traffic, so a count covers stackmon alone.
func (d *fakeDocker) reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.imageLookups = 0
	d.paths = nil
}

// ---------------------------------------------------------------------------
// Fake GitHub API
// ---------------------------------------------------------------------------

type ghAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

type ghRelease struct {
	Tag         string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt string    `json:"published_at"`
	Prerelease  bool      `json:"prerelease"`
	Draft       bool      `json:"draft"`
	Assets      []ghAsset `json:"assets"`
}

type fakeGitHub struct {
	server *httptest.Server
	mu     sync.Mutex
	// releases maps "owner/name" to that repository's releases, newest
	// first, the order GitHub returns them in.
	releases map[string][]ghRelease
	// files serves anything else the release points at (assets,
	// checksums.txt) by path.
	files map[string][]byte
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{releases: map[string][]ghRelease{}, files: map[string][]byte{}}
	g.server = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.server.Close)
	return g
}

// publish registers a repository's releases. The first is the latest.
func (g *fakeGitHub) publish(repo string, rels ...ghRelease) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i := range rels {
		rels[i].PublishedAt = "2026-01-01T00:00:00Z"
		if rels[i].Name == "" {
			rels[i].Name = rels[i].Tag
		}
	}
	g.releases[repo] = rels
}

// file registers a downloadable file, returning the URL to put in a release
// asset.
func (g *fakeGitHub) file(path string, body []byte) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.files[path] = body
	return g.server.URL + path
}

func (g *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if body, ok := g.files[r.URL.Path]; ok {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/repos/")
	if !ok {
		notFound(w)
		return
	}
	switch {
	case strings.HasSuffix(rest, "/releases"):
		writeJSON(w, g.releases[strings.TrimSuffix(rest, "/releases")])
	case strings.HasSuffix(rest, "/releases/latest"):
		rels := g.releases[strings.TrimSuffix(rest, "/releases/latest")]
		if len(rels) == 0 {
			notFound(w)
			return
		}
		writeJSON(w, rels[0])
	default:
		repo, tag, found := strings.Cut(rest, "/releases/tags/")
		if !found {
			notFound(w)
			return
		}
		for _, rel := range g.releases[repo] {
			if rel.Tag == tag {
				writeJSON(w, rel)
				return
			}
		}
		notFound(w)
	}
}

func notFound(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	writeJSON(w, map[string]string{"message": "Not Found"})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// assetName is the release asset naming for the platform running the tests.
func assetName() string {
	return fmt.Sprintf("stackmon-%s-%s", runtime.GOOS, runtime.GOARCH)
}
