package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gvwalker/stackmon/internal/inventory"
	"github.com/gvwalker/stackmon/internal/registry"
	"github.com/spf13/cobra"
)

const testDigestA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const testDigestB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

type bumpRegistry struct{ images map[string]registry.Image }

func (r bumpRegistry) Inspect(_ context.Context, ref string) (registry.Image, error) {
	return r.images[ref], nil
}
func (bumpRegistry) Tags(context.Context, string) ([]string, error) {
	return []string{"1.0.0", "1.0.1"}, nil
}

func bumpCommandFixture(t *testing.T, compose string) (string, *bytes.Buffer, *bytes.Buffer, *cobra.Command) {
	return bumpCommandFixtureWithConfig(t, compose, "")
}

func bumpCommandFixtureWithConfig(t *testing.T, compose, config string) (string, *bytes.Buffer, *bytes.Buffer, *cobra.Command) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(path, []byte(compose), 0o640); err != nil {
		t.Fatal(err)
	}
	invPath := filepath.Join(t.TempDir(), "inventory.json")
	if err := inventory.Save(invPath, inventory.Inventory{Stacks: []inventory.Stack{{Name: "demo", Dir: dir, File: "compose.yaml"}}}); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfgPath, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	oldInv, oldCfg := flagInventory, flagConfig
	flagInventory, flagConfig = invPath, cfgPath
	t.Cleanup(func() { flagInventory, flagConfig = oldInv, oldCfg })
	reg := bumpRegistry{images: map[string]registry.Image{
		"index.docker.io/library/example:1.0.0": {Digest: testDigestA},
		"index.docker.io/library/example:1.0.1": {Digest: testDigestB},
	}}
	cmd := newBumpCmdWithClients(reg, nil)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	return path, &out, &errOut, cmd
}

func TestBumpDigestConfigurationAndFlagPrecedence(t *testing.T) {
	tests := []struct {
		name       string
		config     string
		args       []string
		wantDigest bool
	}{
		{name: "absent config and omitted flag", wantDigest: false},
		{name: "false config and omitted flag", config: "[bump]\ndigest = false\n", wantDigest: false},
		{name: "true config and omitted flag", config: "[bump]\ndigest = true\n", wantDigest: true},
		{name: "true flag overrides false config", config: "[bump]\ndigest = false\n", args: []string{"--digest"}, wantDigest: true},
		{name: "false flag overrides true config", config: "[bump]\ndigest = true\n", args: []string{"--digest=false"}, wantDigest: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, out, errOut, cmd := bumpCommandFixtureWithConfig(t, "services:\n  api:\n    image: example:1.0.0\n", tt.config)
			cmd.SetArgs(append([]string{"demo"}, tt.args...))
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want := "image: example:1.0.1"
			if tt.wantDigest {
				want += "@" + testDigestB
			}
			if !strings.Contains(string(got), want+"\n") {
				t.Errorf("compose = %q, want %q", got, want)
			}
			if !strings.Contains(out.String(), "bumped demo/api") || errOut.Len() != 0 {
				t.Errorf("stdout=%q stderr=%q", out.String(), errOut.String())
			}
		})
	}
}

func TestBumpDigestPinsCandidateThroughCLI(t *testing.T) {
	path, out, errOut, cmd := bumpCommandFixture(t, "services:\n  api:\n    image: example:1.0.0 # preserve me\n  worker:\n    image: example:1.0.0\n")
	cmd.SetArgs([]string{"demo", "--digest"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "services:\n  api:\n    image: example:1.0.1@" + testDigestB + " # preserve me\n  worker:\n    image: example:1.0.1@" + testDigestB + "\n"
	if string(got) != want {
		t.Errorf("compose = %q, want %q", got, want)
	}
	if !strings.Contains(out.String(), "bumped demo/api") || errOut.Len() != 0 {
		t.Errorf("stdout=%q stderr=%q", out.String(), errOut.String())
	}
}

func TestBumpDigestFalseKeepsTaggedPinFloating(t *testing.T) {
	path, _, _, cmd := bumpCommandFixture(t, "services:\n  api:\n    image: example:1.0.0\n")
	cmd.SetArgs([]string{"demo", "--digest=false"})
	// A stack whose compose file fails to parse must not turn bump into a
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "image: example:1.0.1\n") {
		t.Errorf("tag-only pin was not preserved: %s", got)
	}
}

// stackmon failure: the same condition is a non-fatal warning in check and
// show, and bump must read the same way so a script cannot see a different
// exit code depending on which subcommand it ran.
func TestBumpReportsUnparseableStackWithoutError(t *testing.T) {
	dir := t.TempDir()
	// A required interpolation variable with no default and nothing to
	// supply it: the same failure class as the real sandbox stack
	// ("required variable ... is missing a value").
	compose := "services:\n  api:\n    image: ${REQUIRED_IMAGE:?must be set}\n"
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}

	invPath := filepath.Join(t.TempDir(), "inventory.json")
	inv := inventory.Inventory{Stacks: []inventory.Stack{
		{Name: "broken", Dir: dir, File: "compose.yaml"},
	}}
	if err := inventory.Save(invPath, inv); err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfgPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	oldInv, oldCfg := flagInventory, flagConfig
	flagInventory, flagConfig = invPath, cfgPath
	t.Cleanup(func() { flagInventory, flagConfig = oldInv, oldCfg })

	cmd := newBumpCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"broken", "--dry-run"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("bump returned an error for an unparseable stack, want nil: %v", err)
	}
	if !strings.Contains(stderr.String(), "broken") {
		t.Errorf("stderr should name the stack, got: %q", stderr.String())
	}
}
