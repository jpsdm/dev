package cliutil

import "testing"

func TestIsTerminal_FalseWhenNoColorSet(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("DEV_NO_ANIMATION", "")
	if isTerminal() {
		t.Error("isTerminal() = true with NO_COLOR set, want false")
	}
}

func TestIsTerminal_FalseWhenAnimationDisabled(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("DEV_NO_ANIMATION", "1")
	if isTerminal() {
		t.Error("isTerminal() = true with DEV_NO_ANIMATION set, want false")
	}
}

func TestIsTerminal_ExportedWrapperMatchesInternal(t *testing.T) {
	if IsTerminal() != isTerminal() {
		t.Error("IsTerminal() disagrees with isTerminal()")
	}
}

func TestIsTerminal_FalseUnderGoTest(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("DEV_NO_ANIMATION", "")
	// go test never attaches a real terminal to stdout regardless of
	// these env vars being cleared — this is the behavior nearly every
	// other test in this codebase implicitly relies on already.
	if isTerminal() {
		t.Error("isTerminal() = true under go test, want false (no real tty attached)")
	}
}
