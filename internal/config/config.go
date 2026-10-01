// Package config loads and saves dev's config.json.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jpsdm/dev/internal/filesystem"
	"github.com/jpsdm/dev/internal/platform"
)

// ErrNotFound indicates the config file does not exist yet. Callers
// may treat this as "use defaults" rather than a fatal error.
var ErrNotFound = errors.New("config file not found")

// WorkspaceConfig holds workspace-related settings.
type WorkspaceConfig struct {
	Path string `json:"path"`
}

// UpdateCheck records the last time dev checked GitHub for a newer
// release, and what it found — read and written by internal/update's
// CachedNotice so passive checks (behind `dev version`'s notice and
// the notice shown on every other command) don't hit GitHub's API on
// every single invocation.
type UpdateCheck struct {
	LastChecked   time.Time `json:"last_checked"`
	LatestVersion string    `json:"latest_version"`
}

// Config is the full contents of DEV_HOME/config/config.json.
type Config struct {
	Workspace   WorkspaceConfig `json:"workspace"`
	UpdateCheck UpdateCheck     `json:"update_check"`
}

// DefaultPath returns the default location of the config file.
func DefaultPath() (string, error) {
	dir, err := platform.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load reads and parses the config file at path. If the file does not
// exist, it returns a zero-value *Config together with an error that
// wraps ErrNotFound (checkable via errors.Is), so callers can choose
// to treat a missing file as "use defaults" instead of failing.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return nil, fmt.Errorf("reading config file %s: %w", path, err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file %s: %w", path, err)
	}
	return &cfg, nil
}

// Save writes cfg to path as pretty-printed JSON, creating parent
// directories as needed and writing atomically.
func Save(path string, cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	if err := filesystem.WriteFileAtomic(path, data, 0o644); err != nil {
		return fmt.Errorf("writing config file %s: %w", path, err)
	}
	return nil
}
