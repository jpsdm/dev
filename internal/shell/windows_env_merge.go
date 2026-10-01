package shell

import "strings"

// mergeUserPath returns existing's ";"-separated PATH entries with
// any of wanted not already present (case-insensitively — Windows
// paths are case-insensitive) prepended, in wanted's own order,
// ahead of existing's entries in their original order. Empty segments
// in existing (e.g. from a stray leading/trailing/double ";") are
// dropped. Pure logic, with no registry I/O, so it's testable on any
// OS — the Windows-only registry read/write lives in windows_env.go.
func mergeUserPath(existing string, wanted []string) string {
	var existingEntries []string
	for _, e := range strings.Split(existing, ";") {
		if e != "" {
			existingEntries = append(existingEntries, e)
		}
	}

	present := make(map[string]bool, len(existingEntries))
	for _, e := range existingEntries {
		present[strings.ToLower(e)] = true
	}

	var missing []string
	for _, w := range wanted {
		if !present[strings.ToLower(w)] {
			missing = append(missing, w)
		}
	}

	all := append(missing, existingEntries...)
	return strings.Join(all, ";")
}

// pathEntriesFor returns the PATH entries ConfigureWindowsUserEnv wants
// present for devHome: just $DEV_HOME itself, where the real dev binary
// lives. That is the one static entry every shell's design keeps — the
// active language versions are no longer static PATH entries at all,
// they're computed fresh by `dev env` and applied by the installed
// shell function. $DEV_HOME\bin is deliberately absent: the
// shell-function PATH redesign removed the whole shim mechanism that
// directory existed for, so nothing creates or references it any more
// (see the "What's removed" table in
// docs/superpowers/specs/2026-10-01-shell-function-path-management-design.md).
//
// When the Path value being written is (or will be) REG_EXPAND_SZ, it
// returns a %DEV_HOME%-relative reference rather than the expanded
// literal — the same principle every other shell dev supports already
// follows ($DEV_HOME, not a hardcoded path), and it means PATH keeps
// working automatically if DEV_HOME is ever changed by hand. When
// useExpand is false (the existing Path is genuinely REG_SZ, so %VAR%
// references are never expanded), it falls back to the literal,
// already-expanded path — writing an unexpandable %DEV_HOME% there
// would silently break PATH resolution.
func pathEntriesFor(devHome string, useExpand bool) []string {
	if useExpand {
		return []string{`%DEV_HOME%`}
	}
	return []string{devHome}
}
