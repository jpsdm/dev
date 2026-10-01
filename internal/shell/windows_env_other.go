//go:build !windows

package shell

import "fmt"

// ConfigureWindowsUserEnv's non-Windows stub. Never actually called
// outside a runtime.GOOS == "windows" branch — exists only so the rest
// of the codebase compiles on every OS.
func ConfigureWindowsUserEnv(devHome string) error {
	return fmt.Errorf("ConfigureWindowsUserEnv is only supported on Windows")
}
