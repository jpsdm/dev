// clean.go implements gitignore-aware cleanup of a project directory:
// collecting every .gitignore file's patterns (honoring nested
// directory scoping and negation), then removing exactly the paths
// those patterns mark ignored.
package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"

	"github.com/jpsdm/dev/internal/filesystem"
)

// isGitDir reports whether rel (a path relative to a project root, as
// produced by filepath.Rel) names the project's own top-level .git
// directory or something inside it. A nested .git deeper in the tree
// is not protected — out of scope for this project.
func isGitDir(rel string) bool {
	return rel == ".git" || strings.HasPrefix(rel, ".git"+string(filepath.Separator))
}

// gitignoreGroup holds one .gitignore file's parsed patterns together
// with the depth of the directory (relative to the project root) it
// was found in — root's own .gitignore is depth 0, one level down is
// depth 1, and so on. Grouping this way lets collectGitignorePatterns
// order patterns by depth regardless of the (alphabetical, not
// depth-first) order filepath.WalkDir happened to visit them in.
type gitignoreGroup struct {
	depth    int
	patterns []gitignore.Pattern
}

// collectGitignorePatterns walks projectDir looking for .gitignore
// files at every level (skipping the project's own .git directory),
// parsing each into gitignore.Patterns scoped to the directory
// (relative to projectDir) they were found in — this domain scoping is
// what lets the resulting Matcher correctly apply git's real nested-
// .gitignore precedence and negation rules.
//
// gitignore.NewMatcher requires patterns in increasing-priority order
// (most generic first, most specific/deepest last), since Matcher.Match
// scans from the end and returns on the first hit — the last matching
// pattern wins. filepath.WalkDir visits entries in lexical order, not
// depth-first-shallowest, so a subdirectory that sorts alphabetically
// before ".gitignore" (e.g. ".alpha") gets its own nested .gitignore
// collected before the current directory's .gitignore is reached. Left
// uncorrected, that would put a shallower, more-generic pattern after a
// deeper, more-specific one in the slice, letting the generic pattern
// wrongly win. To fix this, patterns are first accumulated per-file into
// depth-tagged groups, then stable-sorted by depth ascending before
// flattening — this preserves each .gitignore file's own line order
// while guaranteeing deeper files' patterns always come after (and thus
// outrank) shallower ones, independent of walk order.
func collectGitignorePatterns(projectDir string) ([]gitignore.Pattern, error) {
	var groups []gitignoreGroup
	err := filepath.WalkDir(projectDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == projectDir {
			return nil
		}
		rel, relErr := filepath.Rel(projectDir, path)
		if relErr != nil {
			return relErr
		}
		if isGitDir(rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() != ".gitignore" {
			// An ordinary directory: nil (not fs.SkipDir) lets WalkDir
			// keep descending into it. An ordinary non-.gitignore file:
			// nothing to do.
			return nil
		}
		// Name matches ".gitignore" — read it as a file even if it's
		// actually a directory, so a misconfigured project (a directory
		// literally named ".gitignore") surfaces as a read error instead
		// of being silently walked into and ignored.
		relDir := filepath.Dir(rel)
		var domain []string
		if relDir != "." {
			domain = strings.Split(relDir, string(filepath.Separator))
		}
		depth := len(domain)
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("reading %s: %w", path, readErr)
		}
		var filePatterns []gitignore.Pattern
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimRight(line, "\r")
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			filePatterns = append(filePatterns, gitignore.ParsePattern(line, domain))
		}
		if len(filePatterns) > 0 {
			groups = append(groups, gitignoreGroup{depth: depth, patterns: filePatterns})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("collecting .gitignore patterns under %s: %w", projectDir, err)
	}

	sort.SliceStable(groups, func(i, j int) bool {
		return groups[i].depth < groups[j].depth
	})

	var patterns []gitignore.Pattern
	for _, g := range groups {
		patterns = append(patterns, g.patterns...)
	}
	return patterns, nil
}

// Clean removes every path under root/src/<name> that .gitignore rules
// (collected from every .gitignore file in the project, honoring
// nested scoping and negation) mark ignored — the same set `git clean
// -Xdf` would remove. It never requires <name> to be a git repository.
// The project's own top-level .git directory, if present, is never a
// candidate for removal. Returns the list of removed (or, if dryRun,
// would-be-removed) paths relative to the project root, with a
// trailing "/" on directory entries — a matched directory is reported
// and removed as a single unit, never descended into, matching how
// real `git clean` reports removals. Returns an error, with nothing
// removed, if the project doesn't exist.
func Clean(root, name string, dryRun bool) ([]string, error) {
	if err := ValidProjectName(name); err != nil {
		return nil, err
	}
	projectDir := filepath.Join(root, srcDir, name)
	if !filesystem.Exists(projectDir) {
		return nil, fmt.Errorf("project %q does not exist at %s", name, projectDir)
	}

	patterns, err := collectGitignorePatterns(projectDir)
	if err != nil {
		return nil, err
	}
	matcher := gitignore.NewMatcher(patterns)

	var matched []string
	err = filepath.WalkDir(projectDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == projectDir {
			return nil
		}
		rel, relErr := filepath.Rel(projectDir, path)
		if relErr != nil {
			return relErr
		}
		if isGitDir(rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		components := strings.Split(rel, string(filepath.Separator))
		if matcher.Match(components, d.IsDir()) {
			if d.IsDir() {
				// filepath.ToSlash first: rel otherwise uses the
				// OS-native separator, and appending a hardcoded
				// "/" straight to that would mix separators on
				// Windows (e.g. "node_modules\pkg/"). The removal
				// path below still works either way (TrimSuffix +
				// filepath.Join re-normalizes), so this only
				// affects what gets displayed/returned.
				matched = append(matched, filepath.ToSlash(rel)+"/")
				return fs.SkipDir
			}
			// Same reasoning as the directory branch above: rel uses
			// the OS-native separator, so a nested file on Windows
			// (e.g. "logs\debug.log") would otherwise be returned
			// with a backslash — inconsistent with the directory
			// branch's forward-slash convention, and a caller (or
			// test) matching on a literal "logs/debug.log" key would
			// never find it despite the file having been removed
			// correctly. The removal path below re-normalizes via
			// filepath.Join regardless, so this only affects what
			// gets displayed/returned.
			matched = append(matched, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scanning %s: %w", projectDir, err)
	}

	if dryRun {
		return matched, nil
	}

	for _, entry := range matched {
		full := filepath.Join(projectDir, strings.TrimSuffix(entry, "/"))
		if err := os.RemoveAll(full); err != nil {
			return matched, fmt.Errorf("removing %s: %w", full, err)
		}
	}
	return matched, nil
}

// CleanAll runs Clean(root, name, dryRun) for every project currently
// under root/src, independently — one project's clean failure does not
// prevent the others from running. Returns a map of project name to
// its removed-paths list (only for projects that succeeded) and an
// aggregated error (via errors.Join) naming every project that failed,
// or a nil error if all succeeded. An empty (or not-yet-existing) src/
// yields an empty map and a nil error — not an error condition.
func CleanAll(root string, dryRun bool) (map[string][]string, error) {
	srcRoot := filepath.Join(root, srcDir)
	entries, err := os.ReadDir(srcRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string][]string{}, nil
		}
		return nil, fmt.Errorf("reading %s: %w", srcRoot, err)
	}

	results := map[string][]string{}
	var errs []error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Skip leftover temp-creation (".tmp-<name>-*") directories an
		// interrupted `dev workspace new` can leave behind — unlike a
		// non-directory entry, these pass the IsDir() check above, so
		// without this they'd be treated as a real project to clean.
		if strings.HasPrefix(name, ".") {
			continue
		}
		removed, err := Clean(root, name, dryRun)
		if err != nil {
			errs = append(errs, fmt.Errorf("cleaning %s: %w", name, err))
			continue
		}
		results[name] = removed
	}
	return results, errors.Join(errs...)
}
