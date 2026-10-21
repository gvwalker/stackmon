// Package config loads stackmon's hand-written TOML configuration.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/BurntSushi/toml"
	"github.com/google/go-containerregistry/pkg/name"
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
// none is configured. repo is a repository name, including its registry when
// it is outside Docker Hub; it follows the same conventions as image refs.
func (c Config) Image(stack, repo string) ImageConfig {
	s, ok := c.Stacks[stack]
	if !ok {
		return ImageConfig{}
	}
	parsed, err := name.NewRepository(repo)
	if err != nil {
		return ImageConfig{}
	}
	return s.Images[parsed.Name()]
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

	// Normalize once at load time, rejecting ambiguous aliases before any command
	// can perform I/O. Sorted traversal also makes errors independent of map order.
	for _, stack := range slices.Sorted(maps.Keys(raw.Stacks)) {
		st := raw.Stacks[stack]
		images := make(map[string]ImageConfig, len(st.Images))
		originals := make(map[string]string, len(st.Images))
		for _, key := range slices.Sorted(maps.Keys(st.Images)) {
			repo, err := name.NewRepository(key)
			if err != nil {
				return Config{}, fmt.Errorf("config: stack %q image key %q: %w", stack, key, err)
			}
			canonical := repo.Name()
			value := st.Images[key]
			if previous, exists := images[canonical]; exists && previous != value {
				return Config{}, fmt.Errorf("config: stack %q image keys %q and %q conflict for %q; use one key or identical repo and constraint values", stack, originals[canonical], key, canonical)
			}
			images[canonical] = value
			originals[canonical] = key
		}
		st.Images = images
		raw.Stacks[stack] = st
	}

	return Config{
		Roots:       raw.Roots,
		Concurrency: concurrency,
		Stacks:      raw.Stacks,
		Bump:        raw.Bump,
	}, nil
}
