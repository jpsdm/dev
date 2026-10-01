package cmd

import (
	"fmt"
	"io"

	"github.com/charmbracelet/lipgloss"

	"github.com/jpsdm/dev/internal/cliutil"
)

// Colors match .github/assets/images/brand.svg's own palette, so the
// terminal banner reads as the same brand as the logo used in the
// README and elsewhere.
var (
	bannerPromptStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#00ADD8")).Bold(true)
	bannerWordmarkStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF")).Bold(true)
	bannerSubtitleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#A9B1D6"))
	bannerFrameStyle    = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#00ADD8")).
				Padding(0, 2)
)

// printBanner writes a small character-based rendition of dev's logo,
// framed in a rounded border, to w — but only in a real terminal
// (cliutil.IsTerminal). Piped or non-interactive output, including
// every automated test (go test never attaches a real tty), gets
// nothing here, exactly as if this function were never called at all.
func printBanner(w io.Writer) {
	if !cliutil.IsTerminal() {
		return
	}
	content := bannerPromptStyle.Render(">_") + " " + bannerWordmarkStyle.Render("dev") + "\n" +
		bannerSubtitleStyle.Render("workspace & runtime manager")
	fmt.Fprintln(w, bannerFrameStyle.Render(content))
	fmt.Fprintln(w)
}
