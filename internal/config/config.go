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

// BumpConfig controls the default behavior of the bump command.
type BumpConfig struct {
	// Digest makes bump add a resolved digest when advancing a tag-only pin.
	// It can be overridden for one invocation with --digest or --digest=false.
	Digest bool `toml:"digest"`
}

// Config is the parsed contents of config.toml.
type Config struct {
	Roots       []string
	Concurrency int
	Stacks      map[string]StackConfig
	Bump        BumpConfig
}

// rawConfig is the direct TOML-unmarshalling target. Concurrency is a
// pointer here so an absent key (nil) can be distinguished from an
// explicitly-set value of 0, which the exported Config.Concurrency (a plain
// int) cannot represent.
type rawConfig struct {
	Roots       []string               `toml:"roots"`
	Concurrency *int                   `toml:"concurrency"`
	Stacks      map[string]StackConfig `toml:"stacks"`
	Bump        BumpConfig             `toml:"bump"`
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
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{Concurrency: DefaultConcurrency}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("config: reading %s: %w", path, err)
	}

	var raw rawConfig
	if err := toml.Unmarshal(data, &raw); err != nil {
		return Config{}, fmt.Errorf("config: parsing %s: %w", path, err)
	}

	concurrency := DefaultConcurrency
	if raw.Concurrency != nil {
		if *raw.Concurrency < 1 {
			return Config{}, fmt.Errorf("config: concurrency must be positive, got %d", *raw.Concurrency)
		}
		concurrency = *raw.Concurrency
	}

	return Config{
		Roots:       raw.Roots,
		Concurrency: concurrency,
		Stacks:      raw.Stacks,
		Bump:        raw.Bump,
	}, nil
}
