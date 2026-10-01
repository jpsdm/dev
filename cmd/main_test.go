package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jpsdm/dev/internal/platform"
)

// TestMain makes every cmd-package test look like it's running from
// inside $DEV_HOME by default — a compiled `go test` binary never
// actually lives there, but most of this package's tests (lang,
// workspace, env, setup) exercise command logic, not the install-location
// gate itself, and would otherwise be refused by cmd/root.go's
// PersistentPreRunE before their RunE ever runs. The one test that
// deliberately exercises the "outside $DEV_HOME" refusal
// (TestPersistentPreRunE_RefusesOrdinaryCommandOutsideDevHome in
// root_test.go) overrides platform.Executable locally and restores it
// via t.Cleanup.
//
// It also defaults DEV_NO_UPDATE_CHECK=1 process-wide, so cmd/root.go's
// PersistentPostRunE hook never reaches its real config.DefaultPath()
// + config.Load() file read for a test that doesn't isolate DEV_HOME
// itself — otherwise a test combining a clean Version (via the
// withVersion helper) with an un-isolated DEV_HOME would read, and
// potentially write, the real developer's actual ~/.dev/config/config.json
// during `go test`. The handful of tests that deliberately exercise the
// notice-computation path (the TestPersistentPostRunE_* tests in
// root_test.go that assert on the notice itself) clear this var locally
// via t.Setenv, which restores this default afterward.
func TestMain(m *testing.M) {
	platform.Executable = func() (string, error) {
		home, err := platform.DevHome()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "dev"), nil
	}
	os.Setenv("DEV_NO_UPDATE_CHECK", "1")
	os.Exit(m.Run())
}
