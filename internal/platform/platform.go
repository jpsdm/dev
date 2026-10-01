// Package platform resolves the on-disk layout dev manages everything
// under, plus the current OS/architecture.
package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Executable resolves the currently-running executable's path. A
// package-level variable (not a direct os.Executable call) so tests can
// override it — a compiled `go test` binary never lives inside a real
// $DEV_HOME, so exercising ordinary commands under test requires a way
// to report a path that does.
var Executable = os.Executable

// DevHome resolves the root directory dev manages everything under.
// It honors the DEV_HOME environment variable when set to a non-empty
// value; otherwise it defaults to $HOME/.dev.
//
// An env-provided value is made absolute: a relative $DEV_HOME could
// never match the absolute directory os.Executable() reports, so
// RunningFromDevHome would refuse every command with a "not installed"
// warning that running `dev setup` again cannot possibly fix. (The
// $HOME/.dev fallback is already absolute.)
func DevHome() (string, error) {
	if v := os.Getenv("DEV_HOME"); v != "" {
		abs, err := filepath.Abs(v)
		if err != nil {
			return "", fmt.Errorf("resolving DEV_HOME %q to an absolute path: %w", v, err)
		}
		return abs, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving user home directory: %w", err)
	}
	return filepath.Join(home, ".dev"), nil
}

// VersionsDir returns DEV_HOME/versions, where installed runtime
// versions live.
func VersionsDir() (string, error) {
	return subdir("versions")
}

// CurrentDir returns DEV_HOME/current, holding the active-version
// links for each runtime.
func CurrentDir() (string, error) {
	return subdir("current")
}

// CacheDir returns DEV_HOME/cache, used for downloaded artifacts.
func CacheDir() (string, error) {
	return subdir("cache")
}

// ConfigDir returns DEV_HOME/config, holding config.json.
func ConfigDir() (string, error) {
	return subdir("config")
}

// DefaultWorkspaceLocation returns a suggested default parent
// directory for the workspace folder: <home>/Documents if a Documents
// directory already exists (the common case on macOS and Windows, and
// increasingly common on Linux desktops), otherwise <home>. This is
// only ever used to pre-fill the first-use location prompt — dev
// never creates or assumes a workspace location without the user
// confirming it.
func DefaultWorkspaceLocation() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving user home directory: %w", err)
	}
	docs := filepath.Join(home, "Documents")
	if info, err := os.Stat(docs); err == nil && info.IsDir() {
		return docs, nil
	}
	return home, nil
}

// DefaultWorkspaceName is the default folder name suggested for the
// workspace on first use — pre-fills the first-use name prompt
// alongside DefaultWorkspaceLocation; the user can rename it there.
const DefaultWorkspaceName = "Workspace"

func subdir(name string) (string, error) {
	home, err := DevHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, name), nil
}

// OS returns the current operating system identifier (e.g. "linux").
func OS() string {
	return runtime.GOOS
}

// Arch returns the current architecture identifier (e.g. "amd64").
func Arch() string {
	return runtime.GOARCH
}

// NotInstalledWarning is shown when dev is invoked from outside
// $DEV_HOME — cmd/root.go's PersistentPreRunE uses this exact text.
const NotInstalledWarning = "dev isn't installed yet — this looks like a copy running from outside $DEV_HOME. " +
	"Run `dev setup` to install it (creates $DEV_HOME and configures your PATH), then run this command again."

// RunningFromDevHome reports whether the currently-executing binary's
// own directory is exactly $DEV_HOME. Also returns the resolved
// devHome path so callers that
// need it (e.g. to build the warning, or to skip a relocation step
// that's already done) don't have to call DevHome() a second time.
func RunningFromDevHome() (ok bool, devHome string, err error) {
	devHome, err = DevHome()
	if err != nil {
		return false, "", err
	}
	exe, err := Executable()
	if err != nil {
		return false, "", fmt.Errorf("finding the running executable: %w", err)
	}
	// Both sides are symlink-resolved before comparing, and only for
	// the comparison. os.Executable() on Linux reads /proc/self/exe,
	// which is already fully symlink-resolved, while DevHome() returns
	// $DEV_HOME exactly as the user spelled it — so any symlink between
	// the two (Fedora Atomic's /home -> var/home, an automounted or
	// bind-mounted home, a macOS $DEV_HOME under /tmp) would otherwise
	// make this comparison fail permanently, with no in-tool recovery:
	// every command but `dev setup` would report "not installed" and
	// point at the very command that just succeeded. The devHome
	// returned to callers stays unresolved so `dev setup`'s rc-file
	// lines keep the user's own spelling of $DEV_HOME.
	exeDir := resolveSymlinks(filepath.Dir(exe))
	return dirMatchesDevHome(exeDir, resolveSymlinks(devHome), runtime.GOOS), devHome, nil
}

// resolveSymlinks returns path with symlinks resolved, or path
// unchanged if it can't be resolved. Resolution is best-effort by
// design: filepath.EvalSymlinks errors on a path that doesn't exist
// yet, which is exactly the state $DEV_HOME is in before its first
// `dev setup` — that must skip resolution for that side, not fail.
func resolveSymlinks(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// dirMatchesDevHome is RunningFromDevHome's pure comparison logic,
// separated so it's testable with arbitrary inputs regardless of
// where the test binary actually lives — the same reason javaOS's
// switch logic lives in a separate mapJavaOS function elsewhere in
// this codebase. goos is a parameter (not read from runtime.GOOS
// directly) for the same testability reason.
func dirMatchesDevHome(exeDir, devHome, goos string) bool {
	if goos == "windows" {
		// filepath.Dir/Join/Clean use the separator of the OS this
		// binary was actually built for, not the goos parameter — on a
		// non-Windows build (e.g. this package's own tests, run on
		// Linux CI) they'd treat a backslash-separated Windows path as
		// one giant path component instead of splitting it. Normalizing
		// to "/" first keeps the comparison correct regardless of which
		// OS this code is running on; it's a no-op in production, where
		// goos always matches the real build target and real Windows
		// paths from os.Executable() already use backslashes natively
		// (which filepath.Clean on Windows accepts and normalizes).
		exeDir = strings.ReplaceAll(exeDir, `\`, "/")
		devHome = strings.ReplaceAll(devHome, `\`, "/")
	}
	exeDir = filepath.Clean(exeDir)
	devHome = filepath.Clean(devHome)
	if goos == "windows" {
		return strings.EqualFold(exeDir, devHome)
	}
	return exeDir == devHome
}
