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
	"bufio"
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
	"golang.org/x/sys/unix"
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
	// Isolated harness probes reuse this test executable without rebuilding CLIs.
	if os.Getenv("STACKMON_E2E_RACE_PROBE") != "" {
		os.Exit(m.Run())
	}
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
	// stackmon_e2e compiles the deterministic after-validation barrier into the
	// test binary only; a shipped binary contains no test hook.
	args := []string{"build", "-tags", "stackmon_e2e", "-o", out}
	// The race build tag describes the harness; subprocesses need their own flag.
	if raceEnabled {
		args = append(args, "-race")
	}
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
	// Registered after srv.Close, so it runs before it: stalled handlers
	// must be released before the server waits on them to finish.
	t.Cleanup(e.hits.releaseStalls)
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
// for a tag stackmon cannot reason about. It also carries the two ways a
// scenario can make the registry misbehave on purpose: blocked paths (a
// failure answer) and stalls (no answer at all).
type requestLog struct {
	faults httpFaults
	next   http.Handler

	mu      sync.Mutex
	paths   []string
	blocked []string
	held    []*gate
	// stalls maps a path prefix to the way requests under it are stranded.
	stalls map[string]stallMode
	// unstall, when the first stall is registered, is closed by cleanup so
	// parked handlers return and the test server can close.
	unstall chan struct{}
}

func (l *requestLog) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if l.faults.serve(w, r) {
		return
	}
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

	if mode, release, stalled := l.stallFor(r.URL.Path); stalled {
		serveStall(w, mode, release)
		return
	}
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

// ---------------------------------------------------------------------------
// Stalls
// ---------------------------------------------------------------------------

// stallMode is one way a server can strand a client mid-exchange: never
// answer at all, or promise a body and never finish it.
type stallMode int

const (
	// stallHeaders accepts the request and never answers it.
	stallHeaders stallMode = iota
	// stallBody answers with a length and the first byte of the promised
	// body, then nothing more.
	stallBody
)

// serveStall writes exactly as far as the mode gets, then parks the handler
// until release. Parking -- rather than returning an error -- is what leaves
// the client precisely where a request deadline has to rescue it; the
// release channel exists so a stalled server still lets the test close.
func serveStall(w http.ResponseWriter, mode stallMode, release <-chan struct{}) {
	switch mode {
	case stallHeaders:
		<-release
	case stallBody:
		w.Header().Set("Content-Length", "4096")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{"))
		// Flush, or the server buffers the partial write and the stall is
		// merely a slower header stall: the promised length and the first
		// byte have to reach the client for the body read to be the thing
		// that stalls.
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-release
	}
}

// stall strands every request under prefix in mode.
func (l *requestLog) stall(prefix string, mode stallMode) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stalls == nil {
		l.stalls = map[string]stallMode{}
		l.unstall = make(chan struct{})
	}
	l.stalls[prefix] = mode
}

// stallFor reports the stall a path is stranded in, and the channel that
// releases its handlers.
func (l *requestLog) stallFor(path string) (stallMode, <-chan struct{}, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for prefix, mode := range l.stalls {
		if strings.HasPrefix(path, prefix) {
			return mode, l.unstall, true
		}
	}
	return 0, nil, false
}

// releaseStalls lets every parked handler return. Registered by newEnv
// before the server's own cleanup, so closing the server never waits on a
// handler the scenario stranded on purpose.
func (l *requestLog) releaseStalls() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.unstall != nil {
		select {
		case <-l.unstall:
		default:
			close(l.unstall)
		}
	}
}

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
	// tempRe is the name of the temp file an inventory is written through.
	// Its random suffix is not the same on every run.
	tempRe = regexp.MustCompile(`\.inventory-\d+\.json`)
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
	s = tempRe.ReplaceAllString(s, ".inventory-TEMP.json")
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
	cmd  *exec.Cmd
	done chan result
	// err is the failure that stopped the run, when it was not an exit code.
	err error
}

// start runs the binary in the background. The caller must wait on it.
func (e *env) start(args ...string) *pending { return e.startWith(nil, args...) }

// startWith runs the dev binary in the background with env overrides that
// apply to this process alone, so two concurrent runs can sit at different
// points.
func (e *env) startWith(over map[string]string, args ...string) *pending {
	return e.startAs(over, devBinary, args...)
}

// startAs runs a chosen build of the binary in the background: a deadline
// scenario waits on a binary other than the dev one.
func (e *env) startAs(over map[string]string, bin string, args ...string) *pending {
	e.t.Helper()
	cmd := e.command(over, bin, args...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Start(); err != nil {
		e.t.Fatalf("starting stackmon %v: %v", args, err)
	}
	p := &pending{e: e, args: args, cmd: cmd, done: make(chan result, 1)}
	go func() {
		res, err := outcome(args, cmd.Wait(), out.String(), errOut.String())
		p.err = err
		p.done <- res
	}()
	return p
}

// pid is the process id of the running command, so a scenario can tell one
// command's announcements from another's.
func (p *pending) pid() int { return p.cmd.Process.Pid }

// kill ends the command the way a crash does: no cleanup, and nothing it was
// holding is released by it.
func (p *pending) kill(t *testing.T) {
	t.Helper()
	if err := p.cmd.Process.Kill(); err != nil {
		t.Fatalf("killing stackmon %v: %v", p.args, err)
	}
}

func (p *pending) wait(t *testing.T) result {
	t.Helper()
	select {
	case r := <-p.done:
		if p.err != nil {
			t.Fatal(p.err)
		}
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
func (e *env) exec(bin string, args ...string) result { return e.execWith(nil, bin, args...) }

func (e *env) execWith(over map[string]string, bin string, args ...string) result {
	e.t.Helper()
	cmd := e.command(over, bin, args...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut

	res, err := outcome(args, cmd.Run(), out.String(), errOut.String())
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

// command prepares a run of the binary in the sandbox's environment.
func (e *env) command(over map[string]string, bin string, args ...string) *exec.Cmd {
	e.t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = e.environ(over)
	return cmd
}

// outcome turns a finished run into a result. A run that died of a signal
// reports the signal as its exit code; anything else that went wrong is a
// failure to run at all.
func outcome(args []string, err error, stdout, stderr string) (result, error) {
	res := result{stdout: stdout, stderr: stderr}
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		res.code = exit.ExitCode()
	default:
		return res, fmt.Errorf("running stackmon %v: %w", args, err)
	}
	// Race failures are harness failures even when a scenario expects a CLI
	// error. Check raw output before normalization or golden handling.
	if res.code == 66 || strings.Contains(stdout, "WARNING: DATA RACE") || strings.Contains(stderr, "WARNING: DATA RACE") {
		return res, fmt.Errorf("child race detected running stackmon %v (exit %d):\n%s%s", args, res.code, stdout, stderr)
	}
	return res, nil
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

// waitFor polls cond until it is true. It coordinates on a fact the server has
// already observed -- a request count -- not on elapsed time.
func (e *env) waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// validateBarrier is the pair of FIFOs behind STACKMON_E2E_AFTER_VALIDATE. The
// test binary writes the compose path once it has read and validated the
// source, then blocks until the scenario writes a release byte. That parks a
// bump exactly in the validation-to-replacement window, deterministically.
// Announce and release are separate pipes so the binary cannot consume its own
// announcement while waiting.
type validateBarrier struct {
	announce *os.File
	releaseF *os.File
	env      map[string]string
}

func (e *env) afterValidateBarrier() *validateBarrier {
	e.t.Helper()
	path := filepath.Join(e.root, "after-validate.fifo")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		e.t.Fatal(err)
	}
	if err := unix.Mkfifo(path+".release", 0o600); err != nil {
		e.t.Fatal(err)
	}
	announce, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	releaseF, err := os.OpenFile(path+".release", os.O_RDWR, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	// A failed scenario must not leave a parked binary behind.
	e.t.Cleanup(func() {
		releaseF.Write(make([]byte, 8))
		releaseF.Close()
		announce.Close()
	})
	return &validateBarrier{
		announce: announce,
		releaseF: releaseF,
		env:      map[string]string{"STACKMON_E2E_AFTER_VALIDATE": path},
	}
}

// wait blocks until a binary announces it has validated the source, and fails
// the scenario rather than hanging if it never does.
func (b *validateBarrier) wait(t *testing.T) {
	t.Helper()
	got := make(chan string, 1)
	go func() {
		var line []byte
		one := make([]byte, 1)
		for {
			n, err := b.announce.Read(one)
			if n == 1 {
				if one[0] == '\n' {
					got <- string(line)
					return
				}
				line = append(line, one[0])
			}
			if err != nil {
				got <- ""
				return
			}
		}
	}()
	select {
	case line := <-got:
		if line == "" {
			t.Fatal("after-validate barrier closed before announcing a compose file")
		}
	case <-time.After(60 * time.Second):
		t.Fatal("stackmon never reached the after-validate barrier")
	}
}

// release lets every parked binary continue.
func (b *validateBarrier) release(t *testing.T) {
	t.Helper()
	if _, err := b.releaseF.Write(make([]byte, 8)); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// Inventory transaction barrier
// ---------------------------------------------------------------------------

// inventoryBarrier is the pair of FIFOs behind STACKMON_E2E_INVENTORY_EVENTS
// and STACKMON_E2E_INVENTORY_PARK. A stackmon process announces each point of
// its inventory transaction on the first -- "lock", "load", "save", and
// "contended" when another writer holds the lock -- and, when the park FIFO
// is named, waits after loading until the scenario writes to the second.
//
// It lets a scenario hold a transaction open and watch that the next writer
// waits for it instead of racing it.
type inventoryBarrier struct {
	events   *os.File
	releaseF *os.File

	mu   sync.Mutex
	seen []string
}

// inventoryBarrier creates the FIFOs a scenario steers an inventory
// transaction with.
func (e *env) inventoryBarrier() *inventoryBarrier {
	e.t.Helper()
	events := filepath.Join(e.root, "inventory-events.fifo")
	release := filepath.Join(e.root, "inventory-release.fifo")
	for _, p := range []string{events, release} {
		if err := unix.Mkfifo(p, 0o600); err != nil {
			e.t.Fatal(err)
		}
	}
	eventFile, err := os.OpenFile(events, os.O_RDWR, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	releaseFile, err := os.OpenFile(release, os.O_RDWR, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	b := &inventoryBarrier{events: eventFile, releaseF: releaseFile}
	go b.read()
	// A failed scenario must not leave a parked binary behind.
	e.t.Cleanup(func() {
		releaseFile.Write(make([]byte, 8))
		releaseFile.Close()
		eventFile.Close()
	})
	return b
}

// eventsEnv is the environment for a command that only announces where its
// transaction is.
func (b *inventoryBarrier) eventsEnv() map[string]string {
	return map[string]string{"STACKMON_E2E_INVENTORY_EVENTS": b.events.Name()}
}

// parkEnv is the environment for a command that announces and then waits
// inside its transaction.
func (b *inventoryBarrier) parkEnv() map[string]string {
	return map[string]string{
		"STACKMON_E2E_INVENTORY_EVENTS": b.events.Name(),
		"STACKMON_E2E_INVENTORY_PARK":   b.releaseF.Name(),
	}
}

// read collects the announced events. Every process writes its own line, and
// a FIFO write is atomic, so the events arrive one at a time and in the order
// they were announced.
func (b *inventoryBarrier) read() {
	sc := bufio.NewScanner(b.events)
	for sc.Scan() {
		b.mu.Lock()
		b.seen = append(b.seen, sc.Text())
		b.mu.Unlock()
	}
}

// waitFor blocks until an event matching pred has been announced, and fails
// the scenario rather than hanging if it never is.
func (b *inventoryBarrier) waitFor(t *testing.T, what string, pred func(string) bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		b.mu.Lock()
		for _, ev := range b.seen {
			if pred(ev) {
				b.mu.Unlock()
				return
			}
		}
		b.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; announced so far: %v", what, b.announced())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// announced is every event announced so far.
func (b *inventoryBarrier) announced() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.seen...)
}

// order asserts that one event was announced before another.
func (b *inventoryBarrier) order(t *testing.T, first, second string) {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	firstAt, secondAt := -1, -1
	for i, ev := range b.seen {
		if ev == first && firstAt < 0 {
			firstAt = i
		}
		if ev == second && secondAt < 0 {
			secondAt = i
		}
	}
	if firstAt < 0 || secondAt < 0 {
		t.Fatalf("expected the events %q and %q; announced: %v", first, second, b.seen)
	}
	if firstAt > secondAt {
		t.Errorf("%q was announced after %q; announced: %v", first, second, b.seen)
	}
}

// release lets the parked transaction continue.
func (b *inventoryBarrier) release(t *testing.T) {
	t.Helper()
	if _, err := b.releaseF.Write(make([]byte, 8)); err != nil {
		t.Fatal(err)
	}
}

// environ builds the child's environment, replacing rather than appending:
// exec passes duplicate keys through, and which one the child reads is not
// something to depend on.
func (e *env) environ(over map[string]string) []string {
	set := map[string]string{
		"HOME":                   e.home(),
		"XDG_CONFIG_HOME":        filepath.Join(e.home(), ".config"),
		"XDG_STATE_HOME":         filepath.Join(e.home(), ".local", "state"),
		"STACKMON_DOCKER_SOCKET": e.docker.sock,
		"STACKMON_GITHUB_API":    e.gh.server.URL,
		"NO_PROXY":               "127.0.0.1,localhost",
		// Keep reports capturable and the failure code reserved. Avoid the
		// runtime's one-second exit delay on every instrumented CLI invocation.
		"GORACE": "log_path=stderr exitcode=66 atexit_sleep_ms=0",
	}
	for k, v := range e.over {
		set[k] = v
	}
	for k, v := range over {
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
	Platform         string      `json:"platform"`
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
	// Origin says where the service's image value is written: a literal of
	// its own ("own"), a literal another service also uses ("shared"), or a
	// value it inherits ("merge", "alias", "extends", "external").
	Origin string `json:"origin"`
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
		// Where an image value is written is worth recording when it is not
		// the service's own literal: that row cannot be bumped.
		if i.Origin != "" && i.Origin != "own" {
			extra += " [origin " + i.Origin + "]"
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

// stateDir is the directory stackmon keeps the inventory in.
func (e *env) stateDir() string {
	return filepath.Join(e.home(), ".local", "state", "stackmon")
}

// inventoryPath is the inventory file in the sandbox.
func (e *env) inventoryPath() string { return filepath.Join(e.stateDir(), "inventory.json") }

// inventoryJSON is the inventory file's bytes, or "" when there is no
// inventory yet.
func (e *env) inventoryJSON() string {
	e.t.Helper()
	b, err := os.ReadFile(e.inventoryPath())
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return string(b)
}

// inventoryStack is one entry in the inventory file.
type inventoryStack struct {
	Name    string `json:"name"`
	Project string `json:"project"`
}

// inventoryStacks are the stacks in the inventory, in the order the file
// holds them.
func (e *env) inventoryStacks() []inventoryStack {
	e.t.Helper()
	raw := e.inventoryJSON()
	if raw == "" {
		return nil
	}
	var doc struct {
		Stacks []inventoryStack `json:"stacks"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		e.t.Fatalf("reading the inventory: %v\n%s", err, raw)
	}
	return doc.Stacks
}

// enrolledNames are the names of the enrolled stacks, sorted.
func (e *env) enrolledNames() []string {
	e.t.Helper()
	names := []string{}
	for _, s := range e.inventoryStacks() {
		names = append(names, s.Name)
	}
	sort.Strings(names)
	return names
}

// boundProject is the Compose project a stack is bound to, or "" when it has
// no binding.
func (e *env) boundProject(name string) string {
	e.t.Helper()
	for _, s := range e.inventoryStacks() {
		if s.Name == name {
			return s.Project
		}
	}
	return ""
}

// stateDirEntries are the names of the files in the state directory.
func (e *env) stateDirEntries() []string {
	e.t.Helper()
	entries, err := os.ReadDir(e.stateDir())
	if err != nil {
		e.t.Fatal(err)
	}
	names := []string{}
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
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

// archImage is one child of a multi-platform index: the platform it is
// published for, and the labels its config blob carries. Distinct labels per
// child are how a scenario proves which child's metadata stackmon read.
type archImage struct {
	Platform string
	Version  string
	Revision string
}

// pushIndex publishes a multi-platform index under ref and returns the index
// digest. stackmon must report that digest, not the one platform child's,
// or every digest-pinned multi-arch image looks permanently drifted. With no
// children it publishes the usual amd64/arm64 pair.
func (e *env) pushIndex(repo, tag string, children ...archImage) string {
	e.t.Helper()
	if len(children) == 0 {
		children = []archImage{{Platform: "linux/amd64", Version: tag}, {Platform: "linux/arm64", Version: tag}}
	}
	var idx v1.ImageIndex = empty.Index
	for _, c := range children {
		labels := map[string]string{"org.opencontainers.image.version": c.Version}
		if c.Revision != "" {
			labels["org.opencontainers.image.revision"] = c.Revision
		}
		img, err := mutate.ConfigFile(empty.Image, configWithLabels(labels, c.Platform))
		if err != nil {
			e.t.Fatal(err)
		}
		platform, err := v1.ParsePlatform(c.Platform)
		if err != nil {
			e.t.Fatal(err)
		}
		idx = mutate.AppendManifests(idx, mutate.IndexAddendum{Add: img, Descriptor: v1.Descriptor{Platform: platform}})
	}
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

// sum returns the hex sha256 of b, in the form an asset digest reports it.
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
	// Digest is the "sha256:<hex>" GitHub reports for the asset's content,
	// and the only thing stackmon verifies a download against.
	Digest string `json:"digest"`
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
	faults httpFaults
	server *httptest.Server
	mu     sync.Mutex
	// releases maps "owner/name" to that repository's releases, newest
	// first, the order GitHub returns them in.
	releases map[string][]ghRelease
	// files serves anything else the release points at (asset downloads)
	// by path.
	files map[string][]byte
	// stalls maps a path prefix to the way requests under it are stranded.
	stalls map[string]stallMode
	// unstall, when the first stall is registered, is closed by cleanup so
	// parked handlers return and the test server can close.
	unstall chan struct{}
	// hits counts requests by path, so a scenario can prove which GitHub
	// endpoints a run actually reached.
	hits map[string]int
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{releases: map[string][]ghRelease{}, files: map[string][]byte{}, hits: map[string]int{}}
	g.server = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.server.Close)
	// Registered after g.server.Close, so it runs before it: stalled
	// handlers must be released before the server waits on them to finish.
	t.Cleanup(g.releaseStalls)
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

// stall strands every request under prefix in mode. The returned URL is the
// server's address for path, for pointing a release asset at a stall.
func (g *fakeGitHub) stall(prefix string, mode stallMode) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stalls == nil {
		g.stalls = map[string]stallMode{}
		g.unstall = make(chan struct{})
	}
	g.stalls[prefix] = mode
}

// stallLocked is the stall lookup for a caller already holding the mutex.
func (g *fakeGitHub) stallLocked(path string) (stallMode, bool) {
	for prefix, mode := range g.stalls {
		if strings.HasPrefix(path, prefix) {
			return mode, true
		}
	}
	return 0, false
}

// releaseStalls lets every parked handler return. Registered by
// newFakeGitHub before the server's own cleanup, so closing the server never
// waits on a handler the scenario stranded on purpose.
func (g *fakeGitHub) releaseStalls() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.unstall != nil {
		select {
		case <-g.unstall:
		default:
			close(g.unstall)
		}
	}
}

// count is how many requests reached paths beginning with prefix.
func (g *fakeGitHub) count(prefix string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for p, c := range g.hits {
		if strings.HasPrefix(p, prefix) {
			n += c
		}
	}
	return n
}

func (g *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	if g.faults.serve(w, r) {
		return
	}
	g.mu.Lock()
	g.hits[r.URL.Path]++
	if mode, stalled := g.stallLocked(r.URL.Path); stalled {
		release := g.unstall
		g.mu.Unlock()
		serveStall(w, mode, release)
		return
	}
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
