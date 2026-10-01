// Package metrics computes a read-only report on a workspace's size and
// shape: per-directory project counts and sizes, totals, and the
// largest project and file. It never mutates the filesystem and never
// prompts — a pure Collect(root) call, mirroring internal/workspace's
// separation of interactive concerns into the cmd layer.
package metrics

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// dirNames lists the four workspace subdirectories, in the order
// they're reported.
var dirNames = []string{"src", "scratch", "archive", "base"}

// isKnownTopDir reports whether name is one of the four scaffolded
// top-level workspace directories in dirNames. Anything else at the
// workspace root (a stray file, a stray directory like a leftover
// notes/ or a VCS directory) is content Collect must treat as
// completely invisible to the report — see the SkipDir handling in
// Collect's WalkDir callback.
func isKnownTopDir(name string) bool {
	for _, n := range dirNames {
		if n == name {
			return true
		}
	}
	return false
}

// projectDirs are the subdirectories whose immediate children are
// "projects" with a meaningful ProjectCount and that participate in
// LargestProject — base/ is a template, not a collection of projects.
var projectDirs = map[string]bool{"src": true, "scratch": true, "archive": true}

// DirStats is one row of the per-directory table. ProjectCount is -1
// for a directory with no "projects" concept (currently only "base") —
// the sentinel a caller renders as "-" rather than a number.
type DirStats struct {
	Name         string
	ProjectCount int
	Size         int64
}

// LargestProject names the single largest top-level project directory
// across src/, scratch/, and archive/ combined. A zero value (empty
// Name) means the workspace has no projects at all.
type LargestProject struct {
	Name string // e.g. "src/api"
	Size int64
}

// LargestFile names the single largest regular file anywhere in the
// workspace (all four directories). A zero value (empty Path) means
// the workspace has no files at all.
type LargestFile struct {
	Path string // relative to the workspace root, e.g. "archive/old/dist/bundle.js"
	Size int64
}

// Report is everything dev workspace metrics prints.
type Report struct {
	Path           string
	Directories    []DirStats
	TotalSize      int64
	TotalFiles     int
	TotalDirs      int
	LargestProject LargestProject
	LargestFile    LargestFile
}

// Collect walks root (a workspace root, already scaffolded) once and
// returns a complete Report. It never follows symlinks:
// filepath.WalkDir does not descend into a symlinked directory
// regardless of its target, and a symlink's own (non-directory) entry
// contributes only its own on-disk size, the same as any other file.
// An empty or not-yet-populated workspace produces a valid, zeroed
// Report rather than an error.
func Collect(root string) (Report, error) {
	dirSizes := map[string]int64{}
	projectSizes := map[string]int64{}
	var totalFiles, totalDirs int
	var largestFile LargestFile

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		parts := strings.Split(rel, string(filepath.Separator))
		topDir := parts[0]

		if !isKnownTopDir(topDir) {
			// Content outside the four scaffolded directories (a
			// stray file or directory sitting directly in the
			// workspace root) must be completely invisible to the
			// report: not counted toward file/dir totals, not
			// counted toward any directory's size or TotalSize, and
			// never eligible for LargestFile. For a directory,
			// SkipDir also avoids walking its (possibly huge)
			// contents entirely.
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			totalDirs++
			return nil
		}

		totalFiles++
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		size := info.Size()
		dirSizes[topDir] += size
		// A leftover ".tmp-<name>-*" directory from an interrupted `dev
		// workspace new`/`scratch` call must never be reported as a
		// project (it would otherwise inflate a project count or even
		// win LargestProject) — the same defensive filter
		// countProjects applies below.
		if projectDirs[topDir] && len(parts) >= 2 && !strings.HasPrefix(parts[1], ".") {
			projectKey := topDir + "/" + parts[1]
			projectSizes[projectKey] += size
		}
		// filepath.ToSlash normalizes the OS-native separator from
		// filepath.Rel to "/", matching LargestProject.Name's
		// existing literal "/" convention so Path is consistent
		// cross-platform.
		relSlash := filepath.ToSlash(rel)
		// The size == case with a path comparison is not redundant
		// with a plain `>`: filepath.WalkDir's depth-first traversal
		// visits a directory's entire subtree before a lexically
		// later sibling file, which does not always match strict
		// alphabetical order of full paths (e.g. a directory "api"
		// is fully visited before a sibling file "api.txt", even
		// though "api.txt" < "api/..." doesn't hold as a bare string
		// comparison here). This clause guarantees the
		// alphabetically-first path wins on a tie, independent of
		// walk order — do not simplify back to a strict `>`.
		if size > largestFile.Size || (size == largestFile.Size && relSlash < largestFile.Path) {
			largestFile = LargestFile{Path: relSlash, Size: size}
		}
		return nil
	})
	if err != nil {
		return Report{}, fmt.Errorf("collecting metrics for %s: %w", root, err)
	}

	directories := make([]DirStats, 0, len(dirNames))
	var totalSize int64
	for _, name := range dirNames {
		count := -1
		if projectDirs[name] {
			count, err = countProjects(filepath.Join(root, name))
			if err != nil {
				return Report{}, err
			}
		}
		size := dirSizes[name]
		totalSize += size
		directories = append(directories, DirStats{Name: name, ProjectCount: count, Size: size})
	}

	projectNames := make([]string, 0, len(projectSizes))
	for key := range projectSizes {
		projectNames = append(projectNames, key)
	}
	sort.Strings(projectNames)
	var largestProject LargestProject
	for i, key := range projectNames {
		size := projectSizes[key]
		if i == 0 || size > largestProject.Size {
			largestProject = LargestProject{Name: key, Size: size}
		}
	}

	return Report{
		Path:           root,
		Directories:    directories,
		TotalSize:      totalSize,
		TotalFiles:     totalFiles,
		TotalDirs:      totalDirs,
		LargestProject: largestProject,
		LargestFile:    largestFile,
	}, nil
}

func countProjects(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("reading %s: %w", dir, err)
	}
	count := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		// Skip leftover temp-creation (".tmp-<name>-*") directories an
		// interrupted `dev workspace new`/`scratch` call can leave
		// behind — the same defensive filter internal/runtime's
		// providers already apply to their own versions directories, so
		// an orphaned temp directory is never counted as a real project.
		if strings.HasPrefix(name, ".") {
			continue
		}
		count++
	}
	return count, nil
}
