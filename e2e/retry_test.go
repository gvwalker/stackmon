package e2e

// These scenarios precede the retry implementation. They exercise real CLI
// reads, counting HTTP attempts independently of registry authentication and
// fixture setup. A fault sequence is consumed once, then the real handler runs.
import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type httpFaults struct {
	mu         sync.Mutex
	prefix     string
	codes      []int
	retryAfter string
	times      []time.Time
}

func (f *httpFaults) set(prefix, retryAfter string, codes ...int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prefix, f.retryAfter, f.codes, f.times = prefix, retryAfter, codes, nil
}

func (f *httpFaults) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.times)
}

func (f *httpFaults) serve(w http.ResponseWriter, r *http.Request) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.prefix == "" || !strings.HasPrefix(r.URL.Path, f.prefix) || r.Method != "GET" {
		return false
	}
	f.times = append(f.times, time.Now())
	if len(f.codes) == 0 {
		return false
	}
	code := f.codes[0]
	f.codes = f.codes[1:]
	if code == 0 { // A dropped connection, before any response headers.
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
		return true
	}
	w.Header().Set("Retry-After", f.retryAfter)
	w.WriteHeader(code)
	_, _ = w.Write([]byte(`{"message":"fixture failure"}`))
	return true
}

func TestRetryRegistryReads(t *testing.T) {
	for _, endpoint := range []string{"manifests/1.0.0", "tags/list"} {
		for _, code := range []int{429, 503, 404} {
			t.Run(fmt.Sprintf("%s/%d", endpoint, code), func(t *testing.T) {
				e := newEnv(t)
				e.push("app", "1.0.0", image{})
				e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("app", "1.0.0")))
				e.hits.faults.set("/v2/app/"+endpoint, "", code)
				r := e.run("check", "--compact")
				assertCode(t, "check", r.code, 0, r)
				var rep compactReport
				if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil {
					t.Fatal(err)
				}
				row := rep.image(t, "demo", "api")
				if code == 404 {
					assertEq(t, "permanent error status", row.Status, "unknown")
					assertEq(t, "HTTP attempts", e.hits.faults.count(), 1)
				} else {
					assertEq(t, "recovered row status", row.Status, "not-running")
					assertEq(t, "recovered row error", row.Err, "")
					assertEq(t, "HTTP attempts", e.hits.faults.count(), 2)
				}
				e.note("%s initial HTTP %d: attempts=%d; report status=%s", endpoint, code, e.hits.faults.count(), row.Status)
			})
		}
	}
}

func TestRetryGitHubReads(t *testing.T) {
	for _, code := range []int{429, 500, 502, 503, 504, 0, 401, 403, 404} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			e := newEnv(t)
			e.gh.publish("gvwalker/stackmon", ghRelease{Tag: "v9.9.9", Body: "Recovered release."})
			e.gh.faults.set("/repos/gvwalker/stackmon/releases/latest", "", code)
			r := e.runBinary(taggedBinary, "whats-new")
			if code == 401 || code == 403 || code == 404 {
				assertCode(t, "permanent failure", r.code, 1, r)
				assertEq(t, "HTTP attempts", e.gh.faults.count(), 1)
			} else {
				assertCode(t, "recovered whats-new", r.code, 0, r)
				assertContains(t, "release output", r.stdout, "update available", "v9.9.9")
				assertEq(t, "HTTP attempts", e.gh.faults.count(), 2)
			}
			e.note("initial HTTP %d (0=dropped connection): attempts=%d", code, e.gh.faults.count())
		})
	}
}

func TestRetryReleaseNotesAndAsset(t *testing.T) {
	e := newEnv(t)
	src := "https://github.com/e2e/app"
	e.push("app", "1.0.0", image{Source: src})
	e.push("app", "1.1.0", image{Source: src})
	e.gh.publish("e2e/app", ghRelease{Tag: "1.1.0", Body: "Recovered application notes."})
	e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("app", "1.0.0")))
	e.gh.faults.set("/repos/e2e/app/releases", "", 503)
	r := e.run("show", "demo")
	assertCode(t, "show", r.code, 0, r)
	assertContains(t, "notes", r.stdout, "Recovered application notes.")
	assertEq(t, "notes HTTP attempts", e.gh.faults.count(), 2)
	e.note("release notes recovered after 503; attempts=%d", e.gh.faults.count())

	payload := []byte("#!/bin/sh\necho recovered asset\n")
	e.publishStackmonRelease("v9.9.9", payload, "")
	e.gh.faults.set("/dl/", "", 503)
	bin := e.installCopy()
	r = e.runBinary(bin, "update")
	assertCode(t, "update", r.code, 0, r)
	assertEq(t, "installed asset SHA-256", sum([]byte(e.read(bin))), sum(payload))
	assertEq(t, "asset HTTP attempts", e.gh.faults.count(), 2)
	assertNoUpdateTemps(t, bin)
	e.note("asset recovered after 503; attempts=%d; installed bytes verified; no temporary files remain", e.gh.faults.count())
}

func TestRetryBoundsAndRetryAfter(t *testing.T) {
	for _, tc := range []struct {
		name, after string
		codes       []int
		attempts    int
		deadline    bool
	}{
		{"exhausted", "", []int{503, 503, 503, 503}, 3, false},
		{"seconds", "1", []int{429}, 2, false},
		{"deadline", "4", []int{429}, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.set("STACKMON_E2E_SHORT_DEADLINES", "1")
			e.gh.publish("gvwalker/stackmon", ghRelease{Tag: "v9.9.9"})
			e.gh.faults.set("/repos/gvwalker/stackmon/releases/latest", tc.after, tc.codes...)
			start := time.Now()
			r := e.runBounded(taggedBinary, "whats-new")
			elapsed := time.Since(start)
			assertBounded(t, "retry operation", elapsed, 10*time.Second)
			assertEq(t, "HTTP attempts", e.gh.faults.count(), tc.attempts)
			if tc.name == "seconds" {
				assertCode(t, "recovery", r.code, 0, r)
				if e.gh.faults.times[1].Sub(e.gh.faults.times[0]) < time.Second {
					t.Error("Retry-After was ignored")
				}
			} else {
				assertCode(t, "bounded failure", r.code, 1, r)
				if tc.deadline {
					assertContains(t, "deadline", r.stderr, "deadline exceeded")
				}
			}
			e.note("%s: attempts=%d; bounded completion; delayed recovery=%t", tc.name, e.gh.faults.count(), tc.name == "seconds")
		})
	}
}

func TestRetryRegistryHonorsDelayAndAttemptLimit(t *testing.T) {
	for _, exhausted := range []bool{false, true} {
		t.Run(fmt.Sprint(exhausted), func(t *testing.T) {
			e := newEnv(t)
			e.push("app", "1.0.0", image{})
			e.enrollStack("demo", fmt.Sprintf("services:\n  api:\n    image: %s\n", e.ref("app", "1.0.0")))
			codes := []int{429}
			after := "2"
			if exhausted {
				codes, after = []int{503, 503, 503, 503}, ""
			}
			e.hits.faults.set("/v2/app/manifests/1.0.0", after, codes...)
			r := e.run("check", "--compact")
			assertCode(t, "check", r.code, 0, r)
			var rep compactReport
			if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil {
				t.Fatal(err)
			}
			row := rep.image(t, "demo", "api")
			if exhausted {
				assertEq(t, "exhausted attempts", e.hits.faults.count(), 3)
				assertEq(t, "exhausted row", row.Status, "unknown")
			} else {
				assertEq(t, "recovered attempts", e.hits.faults.count(), 2)
				assertEq(t, "recovered row", row.Status, "not-running")
				if e.hits.faults.times[1].Sub(e.hits.faults.times[0]) < 2*time.Second {
					t.Error("registry ignored Retry-After")
				}
			}
			e.note("registry exhausted=%t: attempts=%d, status=%s; Retry-After checked on recovery", exhausted, e.hits.faults.count(), row.Status)
		})
	}
}

// A successful response with invalid content is not a transient HTTP failure.
// Retrying must never turn parse/checksum refusals into repeated reads/writes.
func TestRetryDoesNotRepeatInvalidContent(t *testing.T) {
	e := newEnv(t)
	path := "/repos/gvwalker/stackmon/releases/latest"
	e.gh.file(path, []byte("invalid JSON"))
	e.gh.faults.set(path, "")
	r := e.runBinary(taggedBinary, "whats-new")
	assertCode(t, "invalid release", r.code, 1, r)
	assertEq(t, "parse failure attempts", e.gh.faults.count(), 1)
	e.note("invalid release JSON: attempts=%d", e.gh.faults.count())
	e.gh.mu.Lock()
	delete(e.gh.files, path)
	e.gh.mu.Unlock()
	e.publishStackmonRelease("v9.9.9", []byte("invalid digest"), "sha256:"+strings.Repeat("0", 64))
	e.gh.faults.set("/dl/", "")
	bin := e.installCopy()
	before := sum([]byte(e.read(bin)))
	r = e.runBinary(bin, "update")
	assertCode(t, "checksum refusal", r.code, 1, r)
	assertContains(t, "checksum refusal", r.stderr, "checksum mismatch")
	assertEq(t, "checksum failure attempts", e.gh.faults.count(), 1)
	assertEq(t, "original executable SHA-256", sum([]byte(e.read(bin))), before)
	assertNoUpdateTemps(t, bin)
	e.note("checksum failure: attempts=%d; original executable SHA-256 unchanged; no temporary files remain", e.gh.faults.count())
}
