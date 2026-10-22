package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// These CLI scenarios use kernel syscall tracing/injection rather than replacing
// the install implementation with mocks. Run explicitly on Linux with strace.
func TestUpdateDurability(t *testing.T) {
	if os.Getenv("STACKMON_TEST_DURABILITY") == "" {
		t.Skip("set STACKMON_TEST_DURABILITY=1; requires Linux strace")
	}
	tracer, err := exec.LookPath("strace")
	if err != nil {
		t.Fatal(err)
	}
	var artifact strings.Builder
	t.Cleanup(func() {
		if err := os.MkdirAll("out", 0o755); err != nil {
			t.Error(err)
			return
		}
		if err := os.WriteFile("out/update-durability.txt", []byte(artifact.String()), 0o644); err != nil {
			t.Error(err)
		}
	})
	for _, tc := range []struct {
		name, inject, message string
		replaced              bool
	}{
		{"success", "", "", true},
		{"rename failure", "renameat:error=EACCES:when=1", "installing new binary", false},
		{"file sync failure", "fsync:error=EIO:when=1", "syncing new binary", false},
		{"directory sync failure", "fsync:error=EIO:when=1", "durability uncertain", true},
		{"required chown failure", "fchown:error=EPERM:when=1", "preserving binary ownership", false},
		{"redundant chown failure", "fchown:error=EPERM:when=1", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			payload := "#!/bin/sh\necho replacement\n"
			e.publishStackmonRelease("v9.9.9", []byte(payload), "")
			bin := e.installCopy()
			before := e.read(bin)
			if tc.name == "required chown failure" {
				groups, err := os.Getgroups()
				if err != nil {
					t.Fatal(err)
				}
				gid := os.Getegid()
				if os.Geteuid() == 0 {
					gid = 65534
				} else {
					for _, g := range groups {
						if g != gid {
							gid = g
							break
						}
					}
				}
				if gid == os.Getegid() {
					t.Skip("requires a distinct permitted group")
				}
				if err := os.Chown(bin, os.Geteuid(), gid); err != nil {
					t.Fatal(err)
				}
			}
			trace := filepath.Join(e.root, "update.trace")
			args := []string{"-f", "-o", trace, "-e", "trace=fsync,rename,renameat,fchown,fchmod,close"}
			if tc.inject != "" {
				args = append(args, "-e", "inject="+tc.inject)
			}
			if tc.name == "directory sync failure" {
				args = append(args, "-P", filepath.Dir(bin))
			}
			args = append(args, bin, "update")
			got := e.exec(tracer, args...)
			code := 0
			if tc.message != "" {
				code = 1
				assertContains(t, "stderr", got.stderr, tc.message)
			}
			assertCode(t, "update", got.code, code, got)
			want := before
			if tc.replaced {
				want = payload
			}
			if e.read(bin) != want {
				t.Error("installed content differs from expected original/replacement")
			}
			assertNoUpdateTemps(t, bin)
			b, err := os.ReadFile(trace)
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "success" {
				s := string(b)
				first := strings.Index(s, "fsync(")
				rename := strings.Index(s, "renameat(")
				last := strings.LastIndex(s, "fsync(")
				if first < 0 || rename <= first || last <= rename {
					t.Fatalf("expected fsync -> rename -> fsync:\n%s", s)
				}
			}
			fmt.Fprintf(&artifact, "%s: exit=%d replacement=%t temporary files=0\n", tc.name, got.code, tc.replaced)
			for _, line := range strings.Split(string(b), "\n") {
				if strings.Contains(line, "fsync(") || strings.Contains(line, "renameat(") || strings.Contains(line, "fchown(") || strings.Contains(line, "fchmod(") {
					fmt.Fprintln(&artifact, e.normalize(line))
				}
			}
		})
	}
}

func TestUpdatePreservesOwnership(t *testing.T) {
	e := newEnv(t)
	e.publishStackmonRelease("v9.9.9", []byte("replacement"), "")
	bin := e.installCopy()
	uid, gid := os.Geteuid(), os.Getegid()
	if uid == 0 {
		uid, gid = 65534, 65534
	} else {
		groups, err := os.Getgroups()
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range groups {
			if g != gid {
				gid = g
				break
			}
		}
	}
	if err := os.Chown(bin, uid, gid); err != nil {
		t.Fatal(err)
	}
	got := e.exec(bin, "update")
	assertCode(t, "update", got.code, 0, got)
	info, err := os.Stat(bin)
	if err != nil {
		t.Fatal(err)
	}
	st := info.Sys().(*syscall.Stat_t)
	assertEq(t, "owner", int(st.Uid), uid)
	assertEq(t, "group", int(st.Gid), gid)
	assertNoUpdateTemps(t, bin)
	t.Logf("replacement uid=%d gid=%d preserved", uid, gid)
}
