package metrics

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
}

func scaffold(t *testing.T, root string) {
	t.Helper()
	for _, name := range []string{"src", "scratch", "archive", "base"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
}

func TestCollect_PerDirectorySizesAndProjectCounts(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	scaffold(t, root)
	writeFile(t, filepath.Join(root, "src", "api", "main.go"), 100)
	writeFile(t, filepath.Join(root, "src", "web", "index.html"), 50)
	writeFile(t, filepath.Join(root, "scratch", "spike", "note.txt"), 30)
	writeFile(t, filepath.Join(root, "archive", "old", "readme.md"), 20)
	writeFile(t, filepath.Join(root, "base", "README.md"), 10)

	report, err := Collect(root)
	if err != nil {
		t.Fatalf("Collect() returned error: %v", err)
	}

	want := map[string]DirStats{
		"src":     {Name: "src", ProjectCount: 2, Size: 150},
		"scratch": {Name: "scratch", ProjectCount: 1, Size: 30},
		"archive": {Name: "archive", ProjectCount: 1, Size: 20},
		"base":    {Name: "base", ProjectCount: -1, Size: 10},
	}
	if len(report.Directories) != 4 {
		t.Fatalf("Directories = %+v, want exactly 4 entries", report.Directories)
	}
	for _, got := range report.Directories {
		w, ok := want[got.Name]
		if !ok {
			t.Errorf("unexpected directory %q in report", got.Name)
			continue
		}
		if got != w {
			t.Errorf("Directories[%q] = %+v, want %+v", got.Name, got, w)
		}
	}
	if report.TotalSize != 210 {
		t.Errorf("TotalSize = %d, want 210", report.TotalSize)
	}
}

func TestCollect_IgnoresLeftoverTempDirectoriesAsProjects(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	scaffold(t, root)
	writeFile(t, filepath.Join(root, "src", "api", "main.go"), 10)
	// A leftover ".tmp-<name>-*" directory, exactly like one an
	// interrupted `dev workspace new` can leave behind — deliberately
	// made larger than the real project, so it would incorrectly win
	// LargestProject if it weren't filtered out.
	writeFile(t, filepath.Join(root, "src", ".tmp-orphan-abc123", "big.bin"), 10000)

	report, err := Collect(root)
	if err != nil {
		t.Fatalf("Collect() returned error: %v", err)
	}

	var src DirStats
	for _, d := range report.Directories {
		if d.Name == "src" {
			src = d
		}
	}
	if src.ProjectCount != 1 {
		t.Errorf("src ProjectCount = %d, want 1 (the leftover .tmp-* directory must not be counted)", src.ProjectCount)
	}
	if report.LargestProject.Name != "src/api" {
		t.Errorf("LargestProject.Name = %q, want %q (the leftover .tmp-* directory must not win despite being larger)", report.LargestProject.Name, "src/api")
	}
}

func TestCollect_FindsLargestProjectAndFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	scaffold(t, root)
	writeFile(t, filepath.Join(root, "src", "small", "a.txt"), 10)
	writeFile(t, filepath.Join(root, "src", "big", "a.txt"), 500)
	writeFile(t, filepath.Join(root, "src", "big", "b.txt"), 500)
	writeFile(t, filepath.Join(root, "archive", "medium", "a.txt"), 200)
	writeFile(t, filepath.Join(root, "base", "huge-template-file.bin"), 900)

	report, err := Collect(root)
	if err != nil {
		t.Fatalf("Collect() returned error: %v", err)
	}

	if report.LargestProject.Name != "src/big" || report.LargestProject.Size != 1000 {
		t.Errorf("LargestProject = %+v, want {src/big 1000}", report.LargestProject)
	}
	if report.LargestFile.Path != "base/huge-template-file.bin" || report.LargestFile.Size != 900 {
		t.Errorf("LargestFile = %+v, want {base/huge-template-file.bin 900}", report.LargestFile)
	}
}

func TestCollect_TiesResolveAlphabeticallyFirst(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	scaffold(t, root)
	writeFile(t, filepath.Join(root, "src", "zeta", "a.txt"), 100)
	writeFile(t, filepath.Join(root, "src", "alpha", "a.txt"), 100)

	report, err := Collect(root)
	if err != nil {
		t.Fatalf("Collect() returned error: %v", err)
	}
	if report.LargestProject.Name != "src/alpha" {
		t.Errorf("LargestProject.Name = %q, want %q (alphabetically-first on a tie)", report.LargestProject.Name, "src/alpha")
	}
}

func TestCollect_DoesNotFollowSymlinks(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	scaffold(t, root)
	writeFile(t, filepath.Join(root, "src", "api", "real.txt"), 50)

	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "huge.bin"), 10_000_000)

	linkPath := filepath.Join(root, "src", "api", "link-to-outside")
	if err := os.Symlink(outside, linkPath); err != nil {
		t.Skipf("cannot create symlinks on this system: %v", err)
	}

	report, err := Collect(root)
	if err != nil {
		t.Fatalf("Collect() returned error: %v", err)
	}

	for _, d := range report.Directories {
		if d.Name == "src" && d.Size >= 10_000_000 {
			t.Errorf("src size = %d, want it to exclude the symlinked directory's 10MB target", d.Size)
		}
	}
	if report.LargestFile.Size >= 10_000_000 {
		t.Errorf("LargestFile = %+v, want it to never see the symlinked target's content", report.LargestFile)
	}
}

func TestCollect_IgnoresContentOutsideKnownTopLevelDirs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	scaffold(t, root)
	writeFile(t, filepath.Join(root, "src", "api", "main.go"), 50)
	// A stray file directly at the workspace root.
	writeFile(t, filepath.Join(root, "README.md"), 20)
	// A stray directory at the workspace root, containing a file
	// larger than anything in the legitimate scaffolded content
	// above — this is the reviewer's original reproduction shape
	// (Total reporting far less than a stray file that still becomes
	// LargestFile).
	writeFile(t, filepath.Join(root, "notes", "big.bin"), 5000)

	report, err := Collect(root)
	if err != nil {
		t.Fatalf("Collect() returned error: %v", err)
	}

	if report.TotalSize != 50 {
		t.Errorf("TotalSize = %d, want 50 (stray root content must not count)", report.TotalSize)
	}
	if report.TotalFiles != 1 {
		t.Errorf("TotalFiles = %d, want 1 (stray root content must not count)", report.TotalFiles)
	}
	// The four scaffolded top-level dirs (src, scratch, archive,
	// base) plus src/api = 5. The stray "notes" directory must not
	// be counted at all (it's skipped via SkipDir before it can add
	// to TotalDirs), and README.md is a file so never affects it.
	if report.TotalDirs != 5 {
		t.Errorf("TotalDirs = %d, want 5 (stray root directory must not count)", report.TotalDirs)
	}
	if report.LargestFile.Path != "src/api/main.go" || report.LargestFile.Size != 50 {
		t.Errorf("LargestFile = %+v, want {src/api/main.go 50} (the stray notes/big.bin, though larger, must never be seen)", report.LargestFile)
	}
	for _, d := range report.Directories {
		if d.Name != "src" && d.Size != 0 {
			t.Errorf("Directories[%q].Size = %d, want 0", d.Name, d.Size)
		}
	}
}

func TestCollect_LargestFileTieBreaksAlphabeticallyDespiteWalkOrder(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	scaffold(t, root)
	// A directory named "api" and a sibling file named "api.txt" at
	// the same level inside src/. filepath.WalkDir visits "api"'s
	// entire subtree (so src/api/deep.txt) before it reaches the
	// sibling file src/api.txt, even though, as a plain Go string
	// comparison, "src/api.txt" < "src/api/deep.txt": at the first
	// differing byte, '.' (0x2E) sorts before '/' (0x2F). A strict
	// `size > largestFile.Size` update (walk-order dependent) would
	// keep "src/api/deep.txt" since it's visited first; the correct,
	// genuinely-alphabetical answer is "src/api.txt".
	writeFile(t, filepath.Join(root, "src", "api", "deep.txt"), 100)
	writeFile(t, filepath.Join(root, "src", "api.txt"), 100)

	report, err := Collect(root)
	if err != nil {
		t.Fatalf("Collect() returned error: %v", err)
	}

	want := "src/api.txt"
	if report.LargestFile.Path != want || report.LargestFile.Size != 100 {
		t.Errorf("LargestFile = %+v, want {%s 100}", report.LargestFile, want)
	}
}

func TestCollect_EmptyWorkspaceReturnsZeroedReport(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	scaffold(t, root)

	report, err := Collect(root)
	if err != nil {
		t.Fatalf("Collect() returned error: %v", err)
	}

	for _, d := range report.Directories {
		if d.Size != 0 {
			t.Errorf("Directories[%q].Size = %d, want 0", d.Name, d.Size)
		}
		if d.Name == "base" {
			if d.ProjectCount != -1 {
				t.Errorf("base ProjectCount = %d, want -1", d.ProjectCount)
			}
		} else if d.ProjectCount != 0 {
			t.Errorf("Directories[%q].ProjectCount = %d, want 0", d.Name, d.ProjectCount)
		}
	}
	if report.TotalSize != 0 || report.TotalFiles != 0 {
		t.Errorf("TotalSize/TotalFiles = %d/%d, want 0/0", report.TotalSize, report.TotalFiles)
	}
	if report.TotalDirs != 4 {
		t.Errorf("TotalDirs = %d, want 4 (just the four scaffolded, empty top-level directories)", report.TotalDirs)
	}
	if report.LargestProject.Name != "" {
		t.Errorf("LargestProject.Name = %q, want empty (no projects)", report.LargestProject.Name)
	}
	if report.LargestFile.Path != "" {
		t.Errorf("LargestFile.Path = %q, want empty (no files)", report.LargestFile.Path)
	}
}
