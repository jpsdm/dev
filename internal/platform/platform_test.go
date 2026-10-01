package platform

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// absTestPath builds an absolute path literal that is valid on the
// current OS. A bare "/custom/dev/home" is not absolute on Windows
// (filepath.IsAbs rejects it, and filepath.Abs would prepend the test
// process's current drive), so DEV_HOME literals need a drive letter
// there.
func absTestPath(elem ...string) string {
	root := "/"
	if runtime.GOOS == "windows" {
		root = `C:\`
	}
	return filepath.Join(append([]string{root}, elem...)...)
}

func TestDevHome_UsesEnvVarWhenSet(t *testing.T) {
	want := absTestPath("custom", "dev", "home")
	t.Setenv("DEV_HOME", want)

	got, err := DevHome()
	if err != nil {
		t.Fatalf("DevHome() returned error: %v", err)
	}
	if got != want {
		t.Errorf("DevHome() = %q, want %q", got, want)
	}
}

// TestDevHome_RelativeEnvVarIsMadeAbsolute pins that a relative
// $DEV_HOME is resolved against the current directory. A relative value
// could never match the absolute directory os.Executable() reports, so
// without this every command would refuse with "not installed" and
// point the user at a `dev setup` that can't fix it.
func TestDevHome_RelativeEnvVarIsMadeAbsolute(t *testing.T) {
	t.Setenv("DEV_HOME", ".dev")

	got, err := DevHome()
	if err != nil {
		t.Fatalf("DevHome() returned error: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("DevHome() = %q, want an absolute path", got)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd(): %v", err)
	}
	want := filepath.Join(cwd, ".dev")
	if got != want {
		t.Errorf("DevHome() = %q, want %q", got, want)
	}
}

func TestDevHome_EmptyEnvVarFallsBackToDefault(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.UserHomeDir() reads USERPROFILE on Windows, not HOME")
	}
	t.Setenv("DEV_HOME", "")
	t.Setenv("HOME", "/home/tester")

	got, err := DevHome()
	if err != nil {
		t.Fatalf("DevHome() returned error: %v", err)
	}
	want := filepath.Join("/home/tester", ".dev")
	if got != want {
		t.Errorf("DevHome() = %q, want %q", got, want)
	}
}

func TestSubdirHelpers(t *testing.T) {
	devHome := absTestPath("custom", "dev", "home")
	t.Setenv("DEV_HOME", devHome)

	cases := []struct {
		name string
		fn   func() (string, error)
		sub  string
	}{
		{"VersionsDir", VersionsDir, "versions"},
		{"CurrentDir", CurrentDir, "current"},
		{"CacheDir", CacheDir, "cache"},
		{"ConfigDir", ConfigDir, "config"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tc.fn()
			if err != nil {
				t.Fatalf("%s() returned error: %v", tc.name, err)
			}
			want := filepath.Join(devHome, tc.sub)
			if got != want {
				t.Errorf("%s() = %q, want %q", tc.name, got, want)
			}
		})
	}
}

func TestOSAndArch(t *testing.T) {
	t.Parallel()
	if OS() == "" {
		t.Error("OS() returned empty string")
	}
	if Arch() == "" {
		t.Error("Arch() returned empty string")
	}
}

func TestDefaultWorkspaceLocation_UsesDocumentsWhenItExists(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.UserHomeDir() reads USERPROFILE on Windows, not HOME")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "Documents"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	got, err := DefaultWorkspaceLocation()
	if err != nil {
		t.Fatalf("DefaultWorkspaceLocation() returned error: %v", err)
	}
	want := filepath.Join(home, "Documents")
	if got != want {
		t.Errorf("DefaultWorkspaceLocation() = %q, want %q", got, want)
	}
}

func TestDefaultWorkspaceLocation_FallsBackToHomeWhenNoDocuments(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.UserHomeDir() reads USERPROFILE on Windows, not HOME")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := DefaultWorkspaceLocation()
	if err != nil {
		t.Fatalf("DefaultWorkspaceLocation() returned error: %v", err)
	}
	if got != home {
		t.Errorf("DefaultWorkspaceLocation() = %q, want %q", got, home)
	}
}

func TestDefaultWorkspaceName_IsWorkspace(t *testing.T) {
	t.Parallel()
	if DefaultWorkspaceName != "Workspace" {
		t.Errorf("DefaultWorkspaceName = %q, want %q", DefaultWorkspaceName, "Workspace")
	}
}

func TestDirMatchesDevHome_ExactMatchOnDevHomeItself(t *testing.T) {
	t.Parallel()
	if !dirMatchesDevHome("/home/user/.dev", "/home/user/.dev", "linux") {
		t.Error("dirMatchesDevHome() = false, want true for an exact devHome match")
	}
}

func TestDirMatchesDevHome_RejectsUnrelatedDirectory(t *testing.T) {
	t.Parallel()
	if dirMatchesDevHome("/tmp/extracted", "/home/user/.dev", "linux") {
		t.Error("dirMatchesDevHome() = true, want false for an unrelated directory")
	}
}

func TestDirMatchesDevHome_CaseInsensitiveOnWindows(t *testing.T) {
	t.Parallel()
	if !dirMatchesDevHome(`C:\Users\Foo\.DEV`, `c:\users\foo\.dev`, "windows") {
		t.Error("dirMatchesDevHome() = false, want true — Windows paths are case-insensitive")
	}
}

func TestDirMatchesDevHome_CaseSensitiveOnLinux(t *testing.T) {
	t.Parallel()
	if dirMatchesDevHome("/home/user/.DEV", "/home/user/.dev", "linux") {
		t.Error("dirMatchesDevHome() = true, want false — Linux paths are case-sensitive")
	}
}

func TestRunningFromDevHome_FalseWhenDevHomeIsSomewhereElse(t *testing.T) {
	// The real test binary running this test is never actually inside
	// an arbitrary temp DEV_HOME, so this exercises the true I/O path
	// (os.Executable() + DevHome()) end-to-end and must report false.
	t.Setenv("DEV_HOME", t.TempDir())

	ok, devHome, err := RunningFromDevHome()
	if err != nil {
		t.Fatalf("RunningFromDevHome() returned error: %v", err)
	}
	if ok {
		t.Error("RunningFromDevHome() = true, want false (test binary isn't inside DEV_HOME)")
	}
	if devHome == "" {
		t.Error("RunningFromDevHome() returned empty devHome alongside a nil error")
	}
}

// TestRunningFromDevHome_TrueThroughASymlinkedPath reproduces the real
// bug class the string-literal dirMatchesDevHome tests structurally
// cannot catch: os.Executable() resolves through /proc/self/exe and so
// reports a fully symlink-resolved path, while DevHome() returns
// $DEV_HOME exactly as the user spelled it. On any machine with a
// symlink between the two (Fedora Atomic's /home -> var/home, an
// automounted or bind-mounted home, a macOS $DEV_HOME under /tmp) a
// naive string comparison fails forever — even for a binary `dev setup`
// just correctly relocated.
func TestRunningFromDevHome_TrueThroughASymlinkedPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Symlink requires elevated privileges on Windows")
	}
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// $DEV_HOME is spelled through the symlink (the user's own
	// spelling); the "running executable" lives under the resolved real
	// path, mirroring what os.Executable() actually reports.
	t.Setenv("DEV_HOME", link)
	original := Executable
	Executable = func() (string, error) { return filepath.Join(realDir, "dev"), nil }
	t.Cleanup(func() { Executable = original })

	ok, devHome, err := RunningFromDevHome()
	if err != nil {
		t.Fatalf("RunningFromDevHome() returned error: %v", err)
	}
	if !ok {
		t.Error("RunningFromDevHome() = false, want true — a symlink between $DEV_HOME and the resolved executable path must not break the check")
	}
	if devHome != link {
		t.Errorf("RunningFromDevHome() devHome = %q, want the unresolved %q (callers echo the user's own spelling of $DEV_HOME back into rc files)", devHome, link)
	}
}

// TestRunningFromDevHome_ToleratesUnresolvablePaths pins that symlink
// resolution is best-effort: $DEV_HOME before its first `dev setup`
// doesn't exist yet, so filepath.EvalSymlinks errors on it. That must
// not become a hard error or a false negative — it must just skip
// resolution for that side.
func TestRunningFromDevHome_ToleratesUnresolvablePaths(t *testing.T) {
	devHome := filepath.Join(t.TempDir(), "not-created-yet")
	t.Setenv("DEV_HOME", devHome)
	original := Executable
	Executable = func() (string, error) { return filepath.Join(devHome, "dev"), nil }
	t.Cleanup(func() { Executable = original })

	ok, got, err := RunningFromDevHome()
	if err != nil {
		t.Fatalf("RunningFromDevHome() returned error for a not-yet-created $DEV_HOME: %v", err)
	}
	if !ok {
		t.Error("RunningFromDevHome() = false, want true — an unresolvable path must fall back to the literal path, not fail the comparison")
	}
	if got != devHome {
		t.Errorf("RunningFromDevHome() devHome = %q, want %q", got, devHome)
	}
}

func TestNotInstalledWarning_MentionsSetup(t *testing.T) {
	t.Parallel()
	if !strings.Contains(NotInstalledWarning, "dev setup") {
		t.Errorf("NotInstalledWarning = %q, want it to mention `dev setup`", NotInstalledWarning)
	}
}
