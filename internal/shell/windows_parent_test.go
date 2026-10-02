//go:build windows

package shell

import (
	"os/exec"
	"testing"
)

func TestParentCommName_Windows_EndToEndWithARealProcess(t *testing.T) {
	// cmd.exe is present on every Windows install; "ping" keeps the
	// child alive briefly without needing a real console (unlike
	// "timeout", which fails under a redirected/closed stdin).
	cmd := exec.Command("cmd.exe", "/c", "ping", "-n", "6", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting cmd.exe subprocess: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	name, ok := parentCommName("windows", cmd.Process.Pid)
	if !ok {
		t.Fatal("parentCommName() ok = false for a real, running process")
	}
	if name != "cmd" {
		t.Errorf("parentCommName() = %q, want %q", name, "cmd")
	}
}

func TestParentCommName_Windows_NonexistentPIDReturnsFalse(t *testing.T) {
	if _, ok := parentCommName("windows", 999999); ok {
		t.Error("parentCommName() ok = true for a PID that almost certainly doesn't exist")
	}
}

func TestParseWindowsExeName_StripsExtensionAndLowercases(t *testing.T) {
	t.Parallel()
	if got := parseWindowsExeName("Bash.EXE"); got != "bash" {
		t.Errorf("parseWindowsExeName(%q) = %q, want %q", "Bash.EXE", got, "bash")
	}
}
