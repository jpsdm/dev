// Package cliutil is the single place dev's commands write
// user-facing output and report errors, so formatting and the
// --verbose contract stay consistent everywhere.
package cliutil

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// Stdout and Stderr are the writers all output functions use. Tests
// may swap them to capture output; production code leaves them at
// their defaults (os.Stdout / os.Stderr).
var (
	Stdout io.Writer = os.Stdout
	Stderr io.Writer = os.Stderr
)

var verbose bool

// SetVerbose sets whether Verbosef output and the extended error
// chain in PrintError are shown. cmd/root.go calls this once, from
// the root command's PersistentPreRun, after flags are parsed.
func SetVerbose(v bool) {
	verbose = v
}

// Success prints a ✓-prefixed success message to Stdout.
func Success(format string, args ...any) {
	fmt.Fprintf(Stdout, "✓ "+format+"\n", args...)
}

// Step prints a →-prefixed in-progress message to Stdout.
func Step(format string, args ...any) {
	fmt.Fprintf(Stdout, "→ "+format+"\n", args...)
}

// Error prints a ✗-prefixed error message to Stderr.
func Error(format string, args ...any) {
	fmt.Fprintf(Stderr, "✗ "+format+"\n", args...)
}

// Fsuccess writes a ✓-prefixed success message to w. For callers that
// need output tied to a specific writer (e.g. a Cobra command's
// OutOrStdout(), which Success/Step cannot reach since they always
// write to the package-level Stdout) rather than this package's
// default writers.
func Fsuccess(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, "✓ "+format+"\n", args...)
}

// Fstep writes a →-prefixed in-progress message to w. See Fsuccess.
func Fstep(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, "→ "+format+"\n", args...)
}

// Verbosef prints diagnostic detail to Stdout, but only when verbose
// mode is enabled; it is a no-op otherwise.
func Verbosef(format string, args ...any) {
	if !verbose {
		return
	}
	fmt.Fprintf(Stdout, format+"\n", args...)
}

// PrintError reports err the way every command's error path should:
// a plain ✗-prefixed top-level message always, and, only when verbose
// mode is enabled, the full wrapped error chain beneath it. It never
// prints a Go stack trace.
func PrintError(err error) {
	Error("%s", err.Error())
	if !verbose {
		return
	}
	for wrapped := errors.Unwrap(err); wrapped != nil; wrapped = errors.Unwrap(wrapped) {
		fmt.Fprintf(Stderr, "  caused by: %v\n", wrapped)
	}
}
