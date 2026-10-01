// Package workspace manages the user's development workspace: a
// scaffold of src/scratch/archive/base directories, and creating new
// projects from the base/ template. It never prompts or touches
// config — all interactive I/O and config persistence live in
// cmd/workspace.go, which resolves a root path before calling here.
package workspace

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jpsdm/dev/internal/filesystem"
)

const (
	srcDir     = "src"
	scratchDir = "scratch"
	archiveDir = "archive"
	baseDir    = "base"
)

// EnsureScaffold creates root's src/, scratch/, archive/, and base/
// subdirectories if they don't already exist. Safe to call on every
// invocation — never disturbs a subdirectory that's already there.
func EnsureScaffold(root string) error {
	for _, name := range []string{srcDir, scratchDir, archiveDir, baseDir} {
		if err := filesystem.EnsureDir(filepath.Join(root, name), 0o755); err != nil {
			return err
		}
	}
	return nil
}

// ValidProjectName reports an error if name could escape root when
// used as a path component: empty, ".", "..", or containing a path
// separator. Mirrors internal/runtime.ValidVersionName's checks;
// duplicated here rather than imported — internal/workspace importing
// internal/runtime for a generic string check would be a backwards,
// purely coincidental dependency between two unrelated domains.
func ValidProjectName(name string) error {
	if name == "" {
		return fmt.Errorf("project name must not be empty")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("invalid project name: %q", name)
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("invalid project name: %q", name)
	}
	return nil
}

// New creates root/src/<name> by copying root/base/'s contents into
// it, and returns the created directory's path. Returns an error,
// without touching the filesystem, if name is invalid or
// root/src/<name> already exists.
func New(root, name string) (string, error) {
	return createFromBase(root, srcDir, name)
}

// Scratch does the same as New, under root/scratch/<name>.
func Scratch(root, name string) (string, error) {
	return createFromBase(root, scratchDir, name)
}

func createFromBase(root, kind, name string) (string, error) {
	if err := ValidProjectName(name); err != nil {
		return "", err
	}
	dest := filepath.Join(root, kind, name)
	if filesystem.Exists(dest) {
		return "", fmt.Errorf("project %q already exists at %s", name, dest)
	}

	parent := filepath.Join(root, kind)
	// EnsureScaffold always runs before this in the normal cmd/workspace.go
	// flow, so parent already exists there — but any other caller
	// (a future 3b/3c consumer, or a test) that skips that step gets a
	// clear failure from MkdirTemp instead of a confusing one.
	if err := filesystem.EnsureDir(parent, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(parent, ".tmp-"+name+"-*")
	if err != nil {
		return "", fmt.Errorf("creating temp directory: %w", err)
	}
	defer os.RemoveAll(tmp)

	base := filepath.Join(root, baseDir)
	if err := copyTree(base, tmp); err != nil {
		return "", fmt.Errorf("copying template: %w", err)
	}
	// os.MkdirTemp always creates tmp at mode 0700, regardless of
	// base/'s own mode — match base/'s mode (falling back to the
	// scaffold's usual 0755 if base/ doesn't exist, the same default
	// EnsureScaffold itself uses) so the created project directory
	// isn't inconsistently more restrictive than its own subdirectories,
	// which do get base/'s real permissions via copyTree.
	mode := os.FileMode(0o755)
	if info, err := os.Stat(base); err == nil {
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("reading info for %s: %w", base, err)
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return "", fmt.Errorf("setting mode on %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		return "", fmt.Errorf("moving %s into place: %w", dest, err)
	}
	return dest, nil
}

// copyTree recursively copies src's contents into dst (which must
// already exist), preserving hidden files, nested directories, and
// file permissions. A missing src is not an error — dst is simply left
// empty, since an unpopulated base/ is a valid starting state. A
// symlink in src pointing at a regular file is dereferenced (its
// target's content is copied as a regular file) rather than recreated
// as a symlink; a symlink pointing at a directory fails cleanly
// instead (os.Stat/os.Open on a directory via copyFile's file-copy
// path errors, leaving nothing partial behind — the same atomicity
// guarantee holds, it just doesn't succeed for that case).
func copyTree(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("reading %s: %w", src, err)
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return fmt.Errorf("reading info for %s: %w", srcPath, err)
			}
			if err := os.MkdirAll(dstPath, info.Mode().Perm()); err != nil {
				return fmt.Errorf("creating %s: %w", dstPath, err)
			}
			if err := copyTree(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}

		if err := copyFile(srcPath, dstPath); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("reading info for %s: %w", src, err)
	}

	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening %s: %w", src, err)
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("creating %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("copying %s to %s: %w", src, dst, err)
	}
	return out.Close()
}
