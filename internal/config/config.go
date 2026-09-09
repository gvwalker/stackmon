// Package config loads stackmon's hand-written TOML configuration.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// DefaultConcurrency is the number of simultaneous registry probes.
const DefaultConcurrency = 8

// ImageConfig overrides stackmon's inference for one image in one stack.
type ImageConfig struct {
	// Repo is an "owner/name" GitHub repository for release notes, used when
	// the image carries no org.opencontainers.image.source label.
	Repo string `toml:"repo"`
	// Constraint is a glob over tag strings, used when stackmon declines to
	// infer a constraint from the current tag.
	Constraint string `toml:"constraint"`
}

// StackConfig groups overrides for one stack, keyed by image repository.
type StackConfig struct {
	Images map[string]ImageConfig `toml:"images"`
}

// Config is the parsed contents of config.toml.
type Config struct {
	Roots       []string               `toml:"roots"`
	Concurrency int                    `toml:"concurrency"`
	Stacks      map[string]StackConfig `toml:"stacks"`
}

// Image returns the override for one image in one stack, or the zero value if
// none is configured.
func (c Config) Image(stack, repo string) ImageConfig {
	s, ok := c.Stacks[stack]
	if !ok {
		return ImageConfig{}
	}
	return s.Images[repo]
}

// DefaultPath is ~/.config/stackmon/config.toml, honouring XDG_CONFIG_HOME.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "stackmon", "config.toml"), nil
}

// Load reads config.toml. A missing file yields a usable zero config, because
// stacks may be enrolled by path without any roots configured.
func Load(path string) (Config, error) {
	c := Config{Concurrency: DefaultConcurrency}

	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("config: reading %s: %w", path, err)
	}

	// Zero Concurrency distinguishes "absent" from "explicitly set".
	c.Concurrency = 0
	if err := toml.Unmarshal(data, &c); err != nil {
		return Config{}, fmt.Errorf("config: parsing %s: %w", path, err)
	}

	switch {
	case c.Concurrency == 0:
		c.Concurrency = DefaultConcurrency
	case c.Concurrency < 0:
		return Config{}, fmt.Errorf("config: concurrency must be positive, got %d", c.Concurrency)
	}

	return c, nil
}
