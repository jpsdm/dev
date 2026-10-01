//go:build windows

package shell

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// ConfigureWindowsUserEnv sets DEV_HOME and merges devHome into the
// current user's PATH (see pathEntriesFor — devHome itself is the only
// static entry; active language versions are computed dynamically by
// `dev env` and applied by the installed PowerShell function, not
// written to the registry) — HKEY_CURRENT_USER\
// Environment, never HKEY_LOCAL_MACHINE (which would need admin
// rights and affect every user on the machine). Idempotent: entries
// already present in PATH are not duplicated (see mergeUserPath).
// Best-effort broadcasts WM_SETTINGCHANGE afterward so already-open
// applications notice without a reboot; a new terminal session picks
// up the registry change on its own regardless, so a broadcast
// failure is not treated as an error.
func ConfigureWindowsUserEnv(devHome string) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("opening HKCU\\Environment: %w", err)
	}
	defer key.Close()

	existing, existingType, err := key.GetStringValue("Path")
	useExpand := true
	if err == nil {
		useExpand = existingType == registry.EXPAND_SZ
	} else if err == registry.ErrNotExist {
		existing = ""
	} else {
		return fmt.Errorf("reading current user PATH: %w", err)
	}

	wanted := pathEntriesFor(devHome, useExpand)
	merged := mergeUserPath(existing, wanted)

	if useExpand {
		err = key.SetExpandStringValue("Path", merged)
	} else {
		err = key.SetStringValue("Path", merged)
	}
	if err != nil {
		return fmt.Errorf("writing user PATH: %w", err)
	}
	if err := key.SetStringValue("DEV_HOME", devHome); err != nil {
		return fmt.Errorf("writing DEV_HOME: %w", err)
	}

	broadcastEnvironmentChange()
	return nil
}

// broadcastEnvironmentChange sends WM_SETTINGCHANGE so already-running
// processes (e.g. File Explorer) notice the environment change without
// a reboot. golang.org/x/sys/windows doesn't wrap SendMessageTimeout,
// so it's declared directly here. Failure is deliberately ignored —
// see ConfigureWindowsUserEnv's doc comment for why this is safe to
// treat as non-fatal. Uses NewLazySystemDLL (not syscall.NewLazyDLL) so
// user32.dll is loaded only from System32, not via the default search
// order that includes the application's own directory.
func broadcastEnvironmentChange() {
	user32 := windows.NewLazySystemDLL("user32.dll")
	sendMessageTimeout := user32.NewProc("SendMessageTimeoutW")
	if err := sendMessageTimeout.Find(); err != nil {
		return
	}

	const (
		hwndBroadcast   = 0xffff
		wmSettingChange = 0x001A
		smtoAbortIfHung = 0x0002
	)
	param, _ := syscall.UTF16PtrFromString("Environment")
	sendMessageTimeout.Call(
		uintptr(hwndBroadcast),
		uintptr(wmSettingChange),
		0,
		uintptr(unsafe.Pointer(param)),
		uintptr(smtoAbortIfHung),
		uintptr(5000),
		0,
	)
}
