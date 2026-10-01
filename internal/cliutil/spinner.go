package cliutil

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Colors match brand.svg's own palette, so the animated output reads
// as the same brand as the logo it accompanies.
var (
	spinnerStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("#00ADD8"))
	progressFilledStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#00ADD8"))
	progressEmptyStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#3B4261"))
)

const progressBarWidth = 24

// WithSpinner runs fn while showing an animated progress indicator on
// Stdout in a real terminal. See FWithSpinner for the full contract.
func WithSpinner(message string, fn func(update func(done, total int64)) error) error {
	return FWithSpinner(Stdout, message, fn)
}

// FWithSpinner runs fn while showing an animated progress indicator in
// a real terminal — fn may call the provided update(done, total)
// callback any number of times to report measurable progress; total
// <= 0 means indeterminate, rendered as a spinner instead of a bar.
//
// In a non-terminal environment (piped output, CI, every automated
// test — go test never attaches a real tty) this is exactly
// Fstep(w, message) followed by fn(no-op): no animation, no extra
// output beyond that one line, byte-for-byte identical to every call
// site's own Step/Fstep line before WithSpinner existed.
//
// The animation itself (when shown) always writes to ttyWriter, never
// w — terminal control sequences like carriage returns only make
// sense on the real terminal device, and this path is only reached
// when isTerminal() has confirmed the real stdout is one. w still
// receives nothing from the animated path either way, by design: the
// caller's own Success/Error call after FWithSpinner returns is what
// prints the final result line, through w, exactly as before this
// existed.
func FWithSpinner(w io.Writer, message string, fn func(update func(done, total int64)) error) error {
	if !isTerminal() {
		Fstep(w, "%s", message)
		return fn(func(int64, int64) {})
	}

	var mu sync.Mutex
	doneN, totalN := int64(0), int64(0)
	stop := make(chan struct{})
	stopped := make(chan struct{})

	go func() {
		defer close(stopped)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		frame := 0
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				mu.Lock()
				d, t := doneN, totalN
				mu.Unlock()
				fmt.Fprint(ttyWriter, "\r\033[K"+renderFrame(frame, message, d, t))
				frame++
			}
		}
	}()

	err := fn(func(d, t int64) {
		mu.Lock()
		doneN, totalN = d, t
		mu.Unlock()
	})

	close(stop)
	<-stopped
	fmt.Fprint(ttyWriter, "\r\033[K")
	return err
}

// renderFrame renders one animation frame: a spinner alone when total
// is unknown/indeterminate, or a spinner plus a percentage bar once a
// real total is known.
func renderFrame(frame int, message string, done, total int64) string {
	spin := spinnerStyle.Render(spinnerFrames[frame%len(spinnerFrames)])
	if total <= 0 {
		return fmt.Sprintf("%s %s", spin, message)
	}

	filled := int(float64(progressBarWidth) * float64(done) / float64(total))
	if filled > progressBarWidth {
		filled = progressBarWidth
	}
	if filled < 0 {
		filled = 0
	}
	bar := progressFilledStyle.Render(strings.Repeat("█", filled)) +
		progressEmptyStyle.Render(strings.Repeat("░", progressBarWidth-filled))
	pct := int(100 * float64(done) / float64(total))
	return fmt.Sprintf("%s %s %3d%% %s", spin, bar, pct, message)
}
