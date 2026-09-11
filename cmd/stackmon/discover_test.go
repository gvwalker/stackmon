package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gvwalker/stackmon/internal/local"
)

type fakeProber struct {
	available  bool
	containers []local.Container
	err        error
}

func (f fakeProber) Available() bool { return f.available }

func (f fakeProber) Containers(context.Context) ([]local.Container, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.containers, nil
}

func withCLIPaths(t *testing.T, config, inventoryPath string) {
	t.Helper()
	oldConfig, oldInventory := flagConfig, flagInventory
	flagConfig, flagInventory = config, inventoryPath
	t.Cleanup(func() {
		flagConfig, flagInventory = oldConfig, oldInventory
	})
}

func TestDiscoverFindsRunningStackWithoutConfiguredRoots(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "missing.toml")
	invPath := filepath.Join(t.TempDir(), "inventory.json")
	withCLIPaths(t, configPath, invPath)

	cmd := newDiscoverCmdWithProber(fakeProber{
		available: true,
		containers: []local.Container{{
			Project:            "media",
			ProjectWorkingDir:  dir,
			ProjectConfigFiles: filepath.Join(dir, "compose.yaml"),
			Service:            "web",
		}},
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("discover error: %v", err)
	}
	if !strings.Contains(out.String(), "media") || !strings.Contains(out.String(), filepath.Join(dir, "compose.yaml")) {
		t.Errorf("discover output = %q, want running stack", out.String())
	}
}

func TestDiscoverKeepsRootCandidatesWhenDockerUnavailable(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "media")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("roots = [\""+root+"\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withCLIPaths(t, configPath, filepath.Join(t.TempDir(), "inventory.json"))

	cmd := newDiscoverCmdWithProber(fakeProber{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("discover error: %v", err)
	}
	if !strings.Contains(out.String(), "media") {
		t.Errorf("discover output = %q, want root candidate", out.String())
	}
}

func TestDiscoverReturnsDockerErrors(t *testing.T) {
	withCLIPaths(t, filepath.Join(t.TempDir(), "missing.toml"), filepath.Join(t.TempDir(), "inventory.json"))
	cmd := newDiscoverCmdWithProber(fakeProber{available: true, err: errors.New("daemon unavailable")})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "daemon unavailable") {
		t.Fatalf("discover error = %v, want daemon error", err)
	}
}
