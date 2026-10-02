// cliutil's tests mutate package-level state (Stdout, Stderr,
// verbose), so none of them use t.Parallel() against each other.
package cliutil

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func withCapturedOutput(t *testing.T, fn func(stdout, stderr *bytes.Buffer)) {
	t.Helper()
	origStdout, origStderr, origVerbose := Stdout, Stderr, verbose
	t.Cleanup(func() {
		Stdout, Stderr, verbose = origStdout, origStderr, origVerbose
	})

	var stdout, stderr bytes.Buffer
	Stdout, Stderr = &stdout, &stderr
	fn(&stdout, &stderr)
}

func TestSuccess(t *testing.T) {
	withCapturedOutput(t, func(stdout, _ *bytes.Buffer) {
		Success("Node.js %d installed", 22)
		if got := stdout.String(); got != "✓ Node.js 22 installed\n" {
			t.Errorf("Success() output = %q", got)
		}
	})
}

func TestStep(t *testing.T) {
	withCapturedOutput(t, func(stdout, _ *bytes.Buffer) {
		Step("Downloading...")
		if got := stdout.String(); got != "→ Downloading...\n" {
			t.Errorf("Step() output = %q", got)
		}
	})
}

func TestError(t *testing.T) {
	withCapturedOutput(t, func(_, stderr *bytes.Buffer) {
		Error("Node.js 22 could not be installed")
		if got := stderr.String(); got != "✗ Node.js 22 could not be installed\n" {
			t.Errorf("Error() output = %q", got)
		}
	})
}

func TestVerbosef_SilentWhenNotVerbose(t *testing.T) {
	withCapturedOutput(t, func(stdout, _ *bytes.Buffer) {
		SetVerbose(false)
		Verbosef("detail: %s", "value")
		if got := stdout.String(); got != "" {
			t.Errorf("Verbosef() printed %q while not verbose, want nothing", got)
		}
	})
}

func TestVerbosef_PrintsWhenVerbose(t *testing.T) {
	withCapturedOutput(t, func(stdout, _ *bytes.Buffer) {
		SetVerbose(true)
		Verbosef("detail: %s", "value")
		if got := stdout.String(); got != "detail: value\n" {
			t.Errorf("Verbosef() output = %q", got)
		}
	})
}

func TestPrintError_NonVerboseShowsOnlyTopMessage(t *testing.T) {
	withCapturedOutput(t, func(_, stderr *bytes.Buffer) {
		SetVerbose(false)
		inner := errors.New("checksum mismatch")
		outer := fmt.Errorf("installing node 22: %w", inner)

		PrintError(outer)

		got := stderr.String()
		if !strings.Contains(got, "installing node 22: checksum mismatch") {
			t.Errorf("PrintError() output = %q, want it to contain the top-level message", got)
		}
		if strings.Contains(got, "caused by:") {
			t.Errorf("PrintError() output = %q, should not show the error chain when not verbose", got)
		}
	})
}

func TestPrintError_LiteralPercentInMessageIsNotAFormatVerb(t *testing.T) {
	withCapturedOutput(t, func(_, stderr *bytes.Buffer) {
		SetVerbose(false)
		err := errors.New("node.js 100%s-bogus is not installed")

		PrintError(err)

		got := stderr.String()
		if !strings.Contains(got, "100%s-bogus") {
			t.Errorf("PrintError() output = %q, want the literal %%s preserved, not interpreted as a format verb", got)
		}
		if strings.Contains(got, "MISSING") {
			t.Errorf("PrintError() output = %q, want no fmt %%!s(MISSING) artifact", got)
		}
	})
}

func TestFsuccess_WritesToGivenWriter(t *testing.T) {
	var buf bytes.Buffer
	Fsuccess(&buf, "Node.js %d installed", 22)
	if got := buf.String(); got != "✓ Node.js 22 installed\n" {
		t.Errorf("Fsuccess() output = %q", got)
	}
}

func TestFstep_WritesToGivenWriter(t *testing.T) {
	var buf bytes.Buffer
	Fstep(&buf, "Downloading...")
	if got := buf.String(); got != "→ Downloading...\n" {
		t.Errorf("Fstep() output = %q", got)
	}
}

func TestFerror_WritesToGivenWriter(t *testing.T) {
	var buf bytes.Buffer
	Ferror(&buf, "updating %s failed", "/home/u/.bashrc")
	if got := buf.String(); got != "✗ updating /home/u/.bashrc failed\n" {
		t.Errorf("Ferror() output = %q", got)
	}
}

func TestPrintError_VerboseShowsErrorChain(t *testing.T) {
	withCapturedOutput(t, func(_, stderr *bytes.Buffer) {
		SetVerbose(true)
		inner := errors.New("checksum mismatch")
		outer := fmt.Errorf("installing node 22: %w", inner)

		PrintError(outer)

		got := stderr.String()
		if !strings.Contains(got, "caused by: checksum mismatch") {
			t.Errorf("PrintError() output = %q, want it to show the wrapped chain when verbose", got)
		}
	})
}
