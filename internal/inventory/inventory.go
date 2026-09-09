// Package inventory stores the set of enrolled stacks. The file is machine
// owned and never hand-edited, so it is written atomically and carries a
// version for future migrations.
package inventory

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// CurrentVersion is the on-disk schema version.
const CurrentVersion = 1

// Stack is one enrolled stack. Name is the directory basename.
type Stack struct {
	Name     string    `json:"name"`
	Dir      string    `json:"dir"`
	File     string    `json:"file"`
	Enrolled time.Time `json:"enrolled"`
}

// Path is the absolute path of the stack's compose file.
func (s Stack) Path() string { return filepath.Join(s.Dir, s.File) }

// Inventory is the whole enrollment store.
type Inventory struct {
	Version int     `json:"version"`
	Stacks  []Stack `json:"stacks"`
}

// Find returns the stack with the given name.
func (i Inventory) Find(name string) (Stack, bool) {
	for _, s := range i.Stacks {
		if s.Name == name {
			return s, true
		}
	}
	return Stack{}, false
}

// Add enrolls a stack, rejecting a name that is already taken so that
// basename collisions between roots surface at enroll time.
func (i *Inventory) Add(s Stack) error {
	if existing, ok := i.Find(s.Name); ok {
		return fmt.Errorf("inventory: %q is already enrolled from %s; pass --name to choose a different name", s.Name, existing.Dir)
	}
	i.Stacks = append(i.Stacks, s)
	return nil
}

// Remove unenrolls a stack by name.
func (i *Inventory) Remove(name string) error {
	for idx, s := range i.Stacks {
		if s.Name == name {
			i.Stacks = append(i.Stacks[:idx], i.Stacks[idx+1:]...)
			return nil
		}
	}
	return fmt.Errorf("inventory: %q is not enrolled", name)
}

// DefaultPath is ~/.local/state/stackmon/inventory.json, honouring
// XDG_STATE_HOME. Go has no UserStateDir, so this is derived from the home
// directory directly.
func DefaultPath() (string, error) {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "stackmon", "inventory.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "stackmon", "inventory.json"), nil
}

// Load reads the inventory. A missing file yields an empty inventory.
func Load(path string) (Inventory, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Inventory{Version: CurrentVersion}, nil
	}
	if err != nil {
		return Inventory{}, fmt.Errorf("inventory: reading %s: %w", path, err)
	}

	var inv Inventory
	if err := json.Unmarshal(data, &inv); err != nil {
		return Inventory{}, fmt.Errorf("inventory: parsing %s: %w", path, err)
	}
	if inv.Version > CurrentVersion {
		return Inventory{}, fmt.Errorf("inventory: %s is version %d, but this stackmon understands only %d", path, inv.Version, CurrentVersion)
	}
	return inv, nil
}

// Save writes the inventory atomically: a temp file in the destination
// directory, then a rename, so an interrupted write cannot corrupt it.
func Save(path string, inv Inventory) error {
	inv.Version = CurrentVersion
	if inv.Stacks == nil {
		inv.Stacks = []Stack{}
	}

	data, err := json.Marshal(inv)
	if err != nil {
		return fmt.Errorf("inventory: encoding: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("inventory: creating %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".inventory-*.json")
	if err != nil {
		return fmt.Errorf("inventory: creating temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // No-op once the rename succeeds.

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("inventory: writing temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("inventory: syncing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("inventory: closing temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("inventory: renaming into place: %w", err)
	}
	return nil
}
