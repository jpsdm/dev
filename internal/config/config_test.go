package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jpsdm/dev/internal/platform"
)

func TestLoad_MissingFileReturnsErrNotFound(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg, err := Load(path)
	if cfg == nil {
		t.Fatal("Load() returned nil *Config, want zero-value *Config")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Load() error = %v, want it to wrap ErrNotFound", err)
	}
}

func TestLoad_InvalidJSONReturnsClearError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() returned nil error for invalid JSON")
	}
	if errors.Is(err, ErrNotFound) {
		t.Error("Load() on invalid JSON should not report ErrNotFound")
	}
}

func TestSaveThenLoad_RoundTrips(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "config.json")

	want := &Config{Workspace: WorkspaceConfig{Path: "/home/user/workspace"}}
	if err := Save(path, want); err != nil {
		t.Fatalf("Save() returned error: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if got.Workspace.Path != want.Workspace.Path {
		t.Errorf("Workspace.Path = %q, want %q", got.Workspace.Path, want.Workspace.Path)
	}
}

func TestDefaultPath_UsesConfigDir(t *testing.T) {
	t.Setenv("DEV_HOME", "/custom/dev/home")

	// Computed via platform.DevHome() itself, not a hardcoded POSIX
	// literal — DevHome() now resolves DEV_HOME through filepath.Abs
	// (added so a relative DEV_HOME can still match the install-check),
	// which rewrites this Windows-style-driveless path to a real
	// absolute one (e.g. a C:\... form) on Windows. Asking DevHome()
	// directly for the expected value keeps this test correct on every
	// platform without guessing its exact resolution rules.
	devHome, err := platform.DevHome()
	if err != nil {
		t.Fatalf("platform.DevHome() returned error: %v", err)
	}

	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath() returned error: %v", err)
	}
	want := filepath.Join(devHome, "config", "config.json")
	if got != want {
		t.Errorf("DefaultPath() = %q, want %q", got, want)
	}
}

func TestSaveThenLoad_RoundTripsUpdateCheck(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	when := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	want := &Config{UpdateCheck: UpdateCheck{LastChecked: when, LatestVersion: "v0.2.1"}}
	if err := Save(path, want); err != nil {
		t.Fatalf("Save() returned error: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if !got.UpdateCheck.LastChecked.Equal(when) {
		t.Errorf("UpdateCheck.LastChecked = %v, want %v", got.UpdateCheck.LastChecked, when)
	}
	if got.UpdateCheck.LatestVersion != "v0.2.1" {
		t.Errorf("UpdateCheck.LatestVersion = %q, want %q", got.UpdateCheck.LatestVersion, "v0.2.1")
	}
}
