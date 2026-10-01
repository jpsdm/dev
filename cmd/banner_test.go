package cmd

import (
	"bytes"
	"testing"
)

func TestPrintBanner_NonTerminalPrintsNothing(t *testing.T) {
	var buf bytes.Buffer
	printBanner(&buf)
	if got := buf.String(); got != "" {
		t.Errorf("printBanner() wrote %q under go test, want nothing (no real tty attached)", got)
	}
}
