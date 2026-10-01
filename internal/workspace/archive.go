// archive.go moves a cleaned project from src/ into archive/.
package workspace

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jpsdm/dev/internal/filesystem"
)

// Archive moves root/src/<name> to root/archive/<name>. It first
// errors, touching nothing, if root/archive/<name> already exists —
// before any cleaning happens, so a project that can't be archived is
// never destructively cleaned first. It then runs Clean(root, name,
// dryRun) for real; if that fails, the move never happens and the
// error is returned as-is (Clean's own partial-removal list is still
// returned alongside it, so a caller can report what did happen). If
// dryRun is true, Archive stops after Clean reports what it would
// remove and never moves anything. If dryRun is false and the clean
// succeeds, the project directory is moved into place with a single
// os.Rename — src/<name> and archive/<name> are siblings under the
// same workspace root, so no temp-copy dance like New/Scratch's
// cross-content template copy is needed.
func Archive(root, name string, dryRun bool) ([]string, error) {
	if err := ValidProjectName(name); err != nil {
		return nil, err
	}
	archived := filepath.Join(root, archiveDir, name)
	if filesystem.Exists(archived) {
		return nil, fmt.Errorf("project %q is already archived at %s", name, archived)
	}

	removed, err := Clean(root, name, dryRun)
	if err != nil {
		return removed, err
	}
	if dryRun {
		return removed, nil
	}

	src := filepath.Join(root, srcDir, name)
	if err := os.Rename(src, archived); err != nil {
		return removed, fmt.Errorf("moving %s to %s: %w", src, archived, err)
	}
	return removed, nil
}
