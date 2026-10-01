package cliutil

import (
	"io"
	"os"

	"github.com/mattn/go-isatty"
)

// ttyWriter is where the animated path in WithSpinner/FWithSpinner
// writes its frames. Defaults to the real os.Stdout; tests swap it to
// a buffer (the same way Stdout/Stderr are swapped) so forcing
// isTerminalFunc true to exercise the animated code path doesn't leak
// raw ANSI control sequences into `go test`'s own captured output.
var ttyWriter io.Writer = os.Stdout

// isTerminalFunc backs isTerminal; a package-level var so a test can
// force either branch without needing a real terminal attached — the
// same pattern platform.Executable and update's now use for an
// OS/hardware boundary `go test` can't otherwise exercise (it never
// attaches a real tty to stdout, regardless of what this package's
// own Stdout is swapped to).
var isTerminalFunc = func() bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("DEV_NO_ANIMATION") != "" {
		return false
	}
	return isatty.IsTerminal(os.Stdout.Fd())
}

// isTerminal reports whether dev's real stdout is attached to an
// interactive terminal and animation hasn't been explicitly disabled
// via NO_COLOR or DEV_NO_ANIMATION.
func isTerminal() bool { return isTerminalFunc() }

// IsTerminal is isTerminal, exported for callers outside this package
// that need to gate their own output on the same check WithSpinner
// uses internally (e.g. cmd's startup banner, which shouldn't appear
// in piped or test output any more than an animated spinner should).
func IsTerminal() bool { return isTerminal() }
