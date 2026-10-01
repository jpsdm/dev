package cliutil

import (
	"bytes"
	"errors"
	"testing"
)

func TestWithSpinner_NonTerminalPrintsStepThenRunsFnSynchronously(t *testing.T) {
	withCapturedOutput(t, func(stdout, _ *bytes.Buffer) {
		var called bool
		err := WithSpinner("Downloading things...", func(update func(int64, int64)) error {
			called = true
			update(50, 100) // must be safe to call even though nothing renders it here
			return nil
		})
		if err != nil {
			t.Fatalf("WithSpinner() returned error: %v", err)
		}
		if !called {
			t.Error("WithSpinner() did not call fn")
		}
		if got := stdout.String(); got != "→ Downloading things...\n" {
			t.Errorf("WithSpinner() output = %q, want the plain Step line", got)
		}
	})
}

func TestWithSpinner_NonTerminalPropagatesFnError(t *testing.T) {
	withCapturedOutput(t, func(_, _ *bytes.Buffer) {
		wantErr := errors.New("boom")
		err := WithSpinner("Doing a thing...", func(func(int64, int64)) error {
			return wantErr
		})
		if !errors.Is(err, wantErr) {
			t.Errorf("WithSpinner() error = %v, want %v", err, wantErr)
		}
	})
}

func TestFWithSpinner_NonTerminalWritesToGivenWriter(t *testing.T) {
	var w bytes.Buffer
	err := FWithSpinner(&w, "Working...", func(func(int64, int64)) error { return nil })
	if err != nil {
		t.Fatalf("FWithSpinner() returned error: %v", err)
	}
	if got := w.String(); got != "→ Working...\n" {
		t.Errorf("FWithSpinner() output = %q, want the plain Step line written to w", got)
	}
}

func TestWithSpinner_TerminalModeRunsFnExactlyOnceAndReturnsItsResult(t *testing.T) {
	origIsTerminal, origTTYWriter := isTerminalFunc, ttyWriter
	isTerminalFunc = func() bool { return true }
	var animBuf bytes.Buffer
	ttyWriter = &animBuf
	t.Cleanup(func() {
		isTerminalFunc, ttyWriter = origIsTerminal, origTTYWriter
	})

	var calls int
	err := WithSpinner("Animated...", func(update func(int64, int64)) error {
		update(1, 10)
		update(10, 10)
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("WithSpinner() returned error: %v", err)
	}
	if calls != 1 {
		t.Errorf("fn called %d times, want exactly 1", calls)
	}
}

func TestWithSpinner_TerminalModePropagatesFnError(t *testing.T) {
	origIsTerminal, origTTYWriter := isTerminalFunc, ttyWriter
	isTerminalFunc = func() bool { return true }
	var animBuf bytes.Buffer
	ttyWriter = &animBuf
	t.Cleanup(func() {
		isTerminalFunc, ttyWriter = origIsTerminal, origTTYWriter
	})

	wantErr := errors.New("boom")
	err := WithSpinner("Animated...", func(func(int64, int64)) error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Errorf("WithSpinner() error = %v, want %v", err, wantErr)
	}
}
