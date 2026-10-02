//go:build !windows

package shell

// parentCommNameWindows's non-Windows stub. Never actually called
// outside parentCommName's "windows" case — exists only so the rest of
// the codebase compiles on every OS.
func parentCommNameWindows(pid int) (string, bool) {
	return "", false
}
