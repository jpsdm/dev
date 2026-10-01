package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jpsdm/dev/internal/filesystem"
	"github.com/jpsdm/dev/internal/runtime/node"
)

// pathEntriesFromEnvOutput parses `dev env`'s PATH line back into its
// individual entries, in the syntax of whichever shell shell.Detect()
// picked for the OS the test is actually running on (POSIX
// export PATH='a:b' here, PowerShell $env:PATH = "a;b" on Windows —
// see shell.ExportLines).
func pathEntriesFromEnvOutput(t *testing.T, out string) []string {
	t.Helper()
	line := strings.TrimSpace(strings.Split(out, "\n")[1])
	if runtime.GOOS == "windows" {
		value := strings.TrimSuffix(strings.TrimPrefix(line, `$env:PATH = "`), `"`)
		return strings.Split(value, ";")
	}
	value := strings.Trim(strings.TrimPrefix(line, "export PATH="), "'")
	return strings.Split(value, ":")
}

func TestEnvCmd_NoActiveVersionsPrintsBareDevHome(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	t.Setenv("PATH", "/usr/bin:/bin")

	buf := new(bytes.Buffer)
	envCmd.SetOut(buf)
	if err := envCmd.RunE(envCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, devHome) {
		t.Fatalf("expected output to contain devHome %q, got %q", devHome, out)
	}
	// The PATH value's closing quote differs by shell: POSIX
	// single-quotes it (these lines are eval'd by the installed shell
	// function, and POSIX double quotes don't suppress command
	// substitution — see shell.posixExportLines), PowerShell
	// double-quotes it (see shell.ExportLines).
	wantSuffix := "/usr/bin:/bin'"
	if runtime.GOOS == "windows" {
		wantSuffix = `/usr/bin:/bin"`
	}
	if !strings.Contains(out, "/usr/bin:/bin") || !strings.HasSuffix(strings.TrimSpace(out), wantSuffix) {
		t.Fatalf("expected the inherited PATH entries preserved at the end, got %q", out)
	}
}

func TestEnvCmd_IncludesActiveVersionBinDir(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	t.Setenv("PATH", "/usr/bin")

	versionDir := filepath.Join(devHome, "versions", "node", "22")
	if err := os.MkdirAll(filepath.Join(versionDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	currentDir := filepath.Join(devHome, "current")
	if err := filesystem.WriteFileAtomic(filepath.Join(currentDir, "node"), []byte("22"), 0o644); err != nil {
		t.Fatal(err)
	}

	buf := new(bytes.Buffer)
	envCmd.SetOut(buf)
	if err := envCmd.RunE(envCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	// Node's bin layout is platform-specific (node.exe sits directly in
	// versionDir on Windows, not under a "bin" subdirectory like the
	// Unix tarball layout) — see node.binDirForOS.
	wantBinDir, err := node.New().BinDir(versionDir)
	if err != nil {
		t.Fatalf("node.BinDir: %v", err)
	}
	if !strings.Contains(buf.String(), wantBinDir) {
		t.Fatalf("expected output to contain %q, got %q", wantBinDir, buf.String())
	}
}

// TestEnvCmd_SkipsAMarkerWhoseContentEscapesTheVersionsDirectory pins
// the path-safety gate the deleted internal/shim package used to apply
// to current/<lang>'s contents and that activeBinDirs initially lost:
// the marker file is dev's own, but a corrupted or hand-edited one is
// externally-sourced input that gets joined into a filesystem path, and
// the resulting directory goes straight onto the user's PATH. A
// traversal payload must be skipped outright, not followed.
func TestEnvCmd_SkipsAMarkerWhoseContentEscapesTheVersionsDirectory(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	t.Setenv("PATH", "/usr/bin")

	if err := filesystem.WriteFileAtomic(filepath.Join(devHome, "current", "node"), []byte("../../../../tmp"), 0o644); err != nil {
		t.Fatal(err)
	}

	buf := new(bytes.Buffer)
	envCmd.SetOut(buf)
	if err := envCmd.RunE(envCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	out := buf.String()
	// Every PATH entry must be devHome itself, something under
	// devHome/versions/, or an entry inherited from the original PATH —
	// never a path the traversal payload resolved to outside devHome.
	versionsDir := filepath.Join(devHome, "versions")
	for _, entry := range pathEntriesFromEnvOutput(t, out) {
		switch {
		case entry == devHome, entry == "/usr/bin":
			continue
		case strings.HasPrefix(entry, versionsDir+string(filepath.Separator)):
			t.Errorf("a versions entry was produced from an invalid marker: %q", entry)
		default:
			t.Errorf("dev env put an entry outside $DEV_HOME/versions on PATH from a traversal marker: %q (full output %q)", entry, out)
		}
	}
	if strings.Contains(out, filepath.Join(devHome, "versions", "node", "..")) {
		t.Errorf("the traversal marker's path leaked into the output: %q", out)
	}
}

func TestEnvCmd_StripsStaleVersionsPathEntry(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	staleEntry := filepath.Join(devHome, "versions", "node", "20", "bin")
	t.Setenv("PATH", staleEntry+":/usr/bin")

	buf := new(bytes.Buffer)
	envCmd.SetOut(buf)
	if err := envCmd.RunE(envCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	if strings.Contains(buf.String(), staleEntry) {
		t.Fatalf("expected stale entry %q to be stripped, got %q", staleEntry, buf.String())
	}
}
