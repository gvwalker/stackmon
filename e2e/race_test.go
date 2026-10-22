package e2e

import (
	"debug/buildinfo"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const raceArtifact = "out/race-coverage.txt"

func binaryRace(t *testing.T, path string) bool {
	t.Helper()
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range info.Settings {
		if s.Key == "-race" {
			return s.Value == "true"
		}
	}
	return false
}

func TestCLIBuildInstrumentation(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	want := binaryRace(t, self)
	var evidence strings.Builder
	for _, bin := range []string{devBinary, taggedBinary} {
		got := binaryRace(t, bin)
		fmt.Fprintf(&evidence, "%s: -race=%t (harness=%t)\n", filepath.Base(bin), got, want)
		if got != want {
			t.Errorf("%s: -race=%t, want %t", filepath.Base(bin), got, want)
		}
	}
	if err := os.MkdirAll("out", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(raceArtifact, []byte(evidence.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestChildRaceFailures(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, body string
		reject     bool
	}{
		{"report-success", "echo 'WARNING: DATA RACE' >&2\nexit 0", true},
		{"report-expected-error", "echo 'WARNING: DATA RACE' >&2\nexit 1", true},
		{"report-stdout", "echo 'WARNING: DATA RACE'\nexit 0", true},
		{"race-exit", "exit 66", true},
		{"ordinary-error", "exit 1", false},
	}
	var evidence strings.Builder
	for _, tc := range cases {
		for _, mode := range []string{"sync", "async"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				fixture := filepath.Join(t.TempDir(), "child")
				if err := os.WriteFile(fixture, []byte("#!/bin/sh\n"+tc.body+"\n"), 0o755); err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command(self, "-test.run=^TestChildRaceProbe$", "-test.v")
				cmd.Env = append(os.Environ(), "STACKMON_E2E_RACE_PROBE="+mode, "STACKMON_E2E_RACE_FIXTURE="+fixture)
				out, err := cmd.CombinedOutput()
				rejected := err != nil && strings.Contains(string(out), "child race detected")
				fmt.Fprintf(&evidence, "%s/%s: rejected=%t, expected=%t\n", tc.name, mode, rejected, tc.reject)
				if (err != nil) != tc.reject || rejected != tc.reject {
					t.Fatalf("harness rejection=%t, want %t; err=%v\n%s", rejected, tc.reject, err, out)
				}
			})
		}
	}
	f, err := os.OpenFile(raceArtifact, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(evidence.String()); err != nil {
		t.Fatal(err)
	}
}

// Run only in a child harness: rejecting a race must fail its testing.T even
// when the scenario would otherwise accept any nonzero CLI exit code.
func TestChildRaceProbe(t *testing.T) {
	mode := os.Getenv("STACKMON_E2E_RACE_PROBE")
	if mode == "" {
		t.Skip("isolated harness probe")
	}
	e := newEnv(t)
	fixture := os.Getenv("STACKMON_E2E_RACE_FIXTURE")
	if mode == "async" {
		e.startAs(nil, fixture).wait(t)
	} else {
		e.exec(fixture)
	}
}
