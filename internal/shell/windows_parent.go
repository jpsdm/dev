//go:build windows

package shell

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// parentCommNameWindows resolves pid's process executable name via a
// process snapshot (CreateToolhelp32Snapshot) — Windows has no /proc
// filesystem and no portable `ps` equivalent, unlike
// parentCommNameLinux/parentCommNameDarwin. Strips the ".exe" suffix
// and lowercases the result so shellFromName sees the same bare-name
// shape the other two platforms' lookups already produce (e.g.
// "bash.exe" -> "bash").
func parentCommNameWindows(pid int) (string, bool) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return "", false
	}
	defer windows.CloseHandle(snapshot)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return "", false
	}
	for {
		if entry.ProcessID == uint32(pid) {
			return parseWindowsExeName(windows.UTF16ToString(entry.ExeFile[:])), true
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			return "", false
		}
	}
}

// parseWindowsExeName reduces a raw ProcessEntry32.ExeFile value (e.g.
// "bash.exe", sometimes with different casing) to the same bare,
// lowercase, extension-free shape shellFromName expects.
func parseWindowsExeName(raw string) string {
	name := strings.ToLower(strings.TrimSpace(raw))
	return strings.TrimSuffix(name, ".exe")
}
