// Package installer extracts downloaded archives into their final
// location without ever leaving a partially-extracted directory behind.
package installer

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jpsdm/dev/internal/filesystem"
)

// ExtractAtomic extracts the archive at archivePath (.tar.gz or .zip,
// chosen by file extension) into destDir. Extraction happens in a
// temporary sibling directory first; destDir is only ever replaced once
// extraction fully succeeds, so a failure leaves destDir exactly as it
// was (untouched if it didn't exist, unmodified if it did).
func ExtractAtomic(archivePath, destDir string) error {
	parent := filepath.Dir(destDir)
	if err := filesystem.EnsureDir(parent, 0o755); err != nil {
		return err
	}

	tmpDir, err := os.MkdirTemp(parent, ".tmp-extract-*")
	if err != nil {
		return fmt.Errorf("creating temp extraction dir in %s: %w", parent, err)
	}
	defer os.RemoveAll(tmpDir)

	var extractErr error
	switch {
	case strings.HasSuffix(archivePath, ".zip"):
		extractErr = extractZip(archivePath, tmpDir)
	case strings.HasSuffix(archivePath, ".tar.gz") || strings.HasSuffix(archivePath, ".tgz"):
		extractErr = extractTarGz(archivePath, tmpDir)
	default:
		return fmt.Errorf("unsupported archive format: %s", archivePath)
	}
	if extractErr != nil {
		return fmt.Errorf("extracting %s: %w", archivePath, extractErr)
	}

	// Swap destDir for the freshly extracted tmpDir. A plain
	// RemoveAll-then-Rename would leave a window where a mid-failure
	// destroys the old install without the new one being in place yet;
	// renaming the old directory aside first means the only
	// point-of-no-return is a single os.Rename, and the previous install
	// survives (at backupDir) if anything after that still fails.
	backupDir := destDir + ".old"
	if err := ReconcileStaleBackup(destDir, backupDir); err != nil {
		return fmt.Errorf("reconciling previous install state for %s: %w", destDir, err)
	}
	hadExisting := false
	if _, err := os.Stat(destDir); err == nil {
		if err := os.Rename(destDir, backupDir); err != nil {
			return fmt.Errorf("moving existing %s aside: %w", destDir, err)
		}
		hadExisting = true
	}
	if err := os.Rename(tmpDir, destDir); err != nil {
		if hadExisting {
			_ = os.Rename(backupDir, destDir) // best-effort restore; nothing more we can do if this also fails
		}
		return fmt.Errorf("moving extracted contents to %s: %w", destDir, err)
	}
	if hadExisting {
		os.RemoveAll(backupDir)
	}
	return nil
}

// ReconcileStaleBackup resolves any leftover backupDir from a previous
// atomic-swap run before that run's caller starts its own swap. If
// destDir is missing while backupDir exists, a prior swap was
// interrupted after moving destDir aside but before the final rename
// landed the new content — backupDir is the only copy of the install
// that exists, so it is restored into destDir rather than deleted,
// ensuring this run (or a failure partway through it) can never lose
// both the old and new content. If destDir already exists, any
// backupDir alongside it is from a swap that fully completed and is
// safe to discard. Exported so callers with their own rename-aside swap
// logic (e.g. internal/runtime/node's extractStrippingTopLevel) can
// apply the same recovery instead of duplicating it.
func ReconcileStaleBackup(destDir, backupDir string) error {
	_, statErr := os.Stat(destDir)
	if statErr != nil && !os.IsNotExist(statErr) {
		// destDir's status is genuinely unknown (e.g. a permission error
		// on its parent) — neither "exists" nor "missing" branch below is
		// safe to assume, so report the error rather than guessing.
		return fmt.Errorf("checking %s: %w", destDir, statErr)
	}
	if os.IsNotExist(statErr) {
		if _, err := os.Stat(backupDir); err == nil {
			if err := os.Rename(backupDir, destDir); err != nil {
				return fmt.Errorf("restoring interrupted install from %s: %w", backupDir, err)
			}
		}
		return nil
	}
	os.RemoveAll(backupDir)
	return nil
}

// safeJoin joins destDir with an archive entry's name, rejecting any
// attempt to escape destDir via ".." path segments (zip-slip protection).
func safeJoin(destDir, name string) string {
	return filepath.Join(destDir, filepath.Clean("/" + name)[1:])
}

// safeMkdirAll creates dir (an absolute path under destDir) one path
// component at a time, refusing to follow any existing symlink component
// whose target resolves outside destDir. Without this, a prior archive
// entry's symlink can silently redirect a later entry's write to an
// arbitrary location on disk (a "tar symlink chain" attack): os.MkdirAll
// alone happily walks through an existing symlink to create directories
// beyond it, and a plain lexical check on each entry's own name (which is
// what the existing zip-slip and symlink-target checks do) cannot see
// this, because the escaping entry's own name contains no "..".
func safeMkdirAll(destDir, dir string) error {
	rel, err := filepath.Rel(destDir, dir)
	if err != nil {
		return fmt.Errorf("computing relative path for %s: %w", dir, err)
	}
	if rel == "." {
		return os.MkdirAll(destDir, 0o755)
	}

	current := destDir
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				if mkErr := os.Mkdir(current, 0o755); mkErr != nil {
					return mkErr
				}
				continue
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return fmt.Errorf("resolving symlink %s: %w", current, err)
			}
			relToDest, err := filepath.Rel(destDir, resolved)
			if err != nil || relToDest == ".." || strings.HasPrefix(relToDest, ".."+string(filepath.Separator)) {
				return fmt.Errorf("archive entry path %s traverses through a symlink escaping the destination directory", dir)
			}
			continue
		}
		if !info.IsDir() {
			return fmt.Errorf("archive entry path %s traverses through non-directory %s", dir, current)
		}
	}
	return nil
}

// validateSymlinkTarget ensures a symlink at target, once resolved, would
// stay within destDir — rejecting absolute link targets and any relative
// target that escapes destDir via "..". This is the standard mitigation
// for tar/zip "symlink follow" extraction attacks: without it, a later
// archive entry whose path traverses through this symlink could write
// outside destDir even though its own name contains no "..".
func validateSymlinkTarget(destDir, target, linkname string) error {
	if filepath.IsAbs(linkname) {
		return fmt.Errorf("absolute symlink target %q is not allowed", linkname)
	}
	resolved := filepath.Join(filepath.Dir(target), linkname)
	rel, err := filepath.Rel(destDir, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("symlink target %q escapes the destination directory", linkname)
	}
	return nil
}

func extractTarGz(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("opening gzip stream: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading tar entry: %w", err)
		}

		target := safeJoin(destDir, hdr.Name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := safeMkdirAll(destDir, target); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := safeMkdirAll(destDir, filepath.Dir(target)); err != nil {
				return err
			}
			if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("refusing to overwrite existing symlink at %s", target)
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		case tar.TypeSymlink:
			if err := safeMkdirAll(destDir, filepath.Dir(target)); err != nil {
				return err
			}
			if err := validateSymlinkTarget(destDir, target, hdr.Linkname); err != nil {
				return fmt.Errorf("rejecting symlink %s: %w", hdr.Name, err)
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		default:
			// Hard links (tar.TypeLink) and any other entry type
			// (devices, FIFOs, ...) are silently skipped rather than
			// erroring: none of the archives this package extracts
			// (Node.js releases and similar language-runtime tarballs)
			// legitimately contain them, and treating an unexpected one
			// as fatal would only turn a harmless, ignorable entry into
			// an install failure.
		}
	}
}

func extractZip(archivePath, destDir string) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		target := safeJoin(destDir, f.Name)
		if f.FileInfo().IsDir() {
			if err := safeMkdirAll(destDir, target); err != nil {
				return err
			}
			continue
		}

		if err := safeMkdirAll(destDir, filepath.Dir(target)); err != nil {
			return err
		}
		if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to overwrite existing symlink at %s", target)
		}

		if f.Mode()&os.ModeSymlink != 0 {
			rc, err := f.Open()
			if err != nil {
				return err
			}
			linkname, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return err
			}
			if err := validateSymlinkTarget(destDir, target, string(linkname)); err != nil {
				return fmt.Errorf("rejecting symlink %s: %w", f.Name, err)
			}
			if err := os.Symlink(string(linkname), target); err != nil {
				return err
			}
			continue
		}

		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return err
		}
		_, copyErr := io.Copy(out, rc)
		rc.Close()
		out.Close()
		if copyErr != nil {
			return copyErr
		}
	}
	return nil
}
