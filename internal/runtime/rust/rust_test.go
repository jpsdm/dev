package rust

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jpsdm/dev/internal/cliutil"
)

func TestMapRustTarget(t *testing.T) {
	t.Parallel()
	cases := []struct{ os, arch, want string }{
		{"linux", "amd64", "x86_64-unknown-linux-gnu"},
		{"linux", "arm64", "aarch64-unknown-linux-gnu"},
		{"darwin", "amd64", "x86_64-apple-darwin"},
		{"darwin", "arm64", "aarch64-apple-darwin"},
		{"windows", "amd64", "x86_64-pc-windows-msvc"},
		{"windows", "arm64", "aarch64-pc-windows-msvc"},
	}
	for _, c := range cases {
		got, err := mapRustTarget(c.os, c.arch)
		if err != nil || got != c.want {
			t.Errorf("mapRustTarget(%s, %s) = %q, %v; want %q", c.os, c.arch, got, err, c.want)
		}
	}
}

func TestMapRustTarget_RejectsUnsupportedValues(t *testing.T) {
	t.Parallel()
	if _, err := mapRustTarget("plan9", "amd64"); err == nil {
		t.Error(`mapRustTarget("plan9", "amd64") returned nil error`)
	}
	if _, err := mapRustTarget("linux", "z80"); err == nil {
		t.Error(`mapRustTarget("linux", "z80") returned nil error`)
	}
}

func TestParseTag(t *testing.T) {
	t.Parallel()
	line, patch, ok := parseTag("1.85.12")
	if !ok || line != "1.85" || patch != 12 {
		t.Errorf(`parseTag("1.85.12") = (%q, %d, %v), want ("1.85", 12, true)`, line, patch, ok)
	}
	for _, bad := range []string{"", "1.85", "v1.85.0", "1.85.0-beta.1", "release-1.85.0", "1.x.0"} {
		if _, _, ok := parseTag(bad); ok {
			t.Errorf("parseTag(%q) ok = true, want false", bad)
		}
	}
}

func TestCompareLines_OrdersNumericallyNotLexically(t *testing.T) {
	t.Parallel()
	if compareLines("1.100", "1.99") <= 0 {
		t.Error(`compareLines("1.100", "1.99") <= 0, want > 0`)
	}
	if compareLines("1.9", "1.85") >= 0 {
		t.Error(`compareLines("1.9", "1.85") >= 0, want < 0`)
	}
}

const goodHash = "d8c21157e70d86c6e861d57d9c191a79f47b940b4fcd48c29440b2f4a3c8aa51"

// manifestFor builds a channel manifest in the real file's shape: the
// target section we want, decoys sharing its prefix, and the
// components sub-tables whose own "target =" lines must not be mistaken
// for part of the section.
func manifestFor(triple, url, hash string) string {
	return fmt.Sprintf(`manifest-version = "2"
date = "2026-10-01"

[pkg.rustc.target.%[1]s]
available = true
url = "https://example.invalid/rustc-decoy.tar.gz"
hash = "decoy"

[pkg.rust]
version = "1.85.0 (4d91de4e4 2025-02-17)"

[pkg.rust.target.other-unknown-triple]
available = true
url = "https://example.invalid/other.tar.gz"
hash = "other"

[pkg.rust.target.%[1]s]
available = true
url = %[2]q
hash = %[3]q
xz_url = "https://example.invalid/xz.tar.xz"
xz_hash = "xz"

[[pkg.rust.target.%[1]s.components]]
pkg = "rustc"
target = "%[1]s"

[pkg.rust-docs.target.%[1]s]
available = true
url = "https://example.invalid/docs-decoy.tar.gz"
hash = "decoy"
`, triple, url, hash)
}

func TestParseManifestTarget_FindsTheRustSectionNotLookalikes(t *testing.T) {
	t.Parallel()
	m := manifestFor("x86_64-unknown-linux-gnu", "https://static.rust-lang.org/dist/rust-1.85.0-x86_64-unknown-linux-gnu.tar.gz", goodHash)

	url, hash, ok := parseManifestTarget(m, "x86_64-unknown-linux-gnu")
	if !ok {
		t.Fatal("parseManifestTarget() ok = false, want true")
	}
	if url != "https://static.rust-lang.org/dist/rust-1.85.0-x86_64-unknown-linux-gnu.tar.gz" {
		t.Errorf("url = %q", url)
	}
	if hash != goodHash {
		t.Errorf("hash = %q, want %q (not the xz hash or a decoy's)", hash, goodHash)
	}
}

func TestParseManifestTarget_NotFoundCases(t *testing.T) {
	t.Parallel()
	m := manifestFor("x86_64-unknown-linux-gnu", "https://h/rust.tar.gz", goodHash)
	if _, _, ok := parseManifestTarget(m, "aarch64-apple-darwin"); ok {
		t.Error("ok = true for a triple with no section")
	}
	unavailable := strings.Replace(m, "[pkg.rust.target.x86_64-unknown-linux-gnu]\navailable = true", "[pkg.rust.target.x86_64-unknown-linux-gnu]\navailable = false", 1)
	if _, _, ok := parseManifestTarget(unavailable, "x86_64-unknown-linux-gnu"); ok {
		t.Error("ok = true for a section marked available = false")
	}
	noHash := manifestFor("x86_64-unknown-linux-gnu", "https://h/rust.tar.gz", "")
	if _, _, ok := parseManifestTarget(noHash, "x86_64-unknown-linux-gnu"); ok {
		t.Error("ok = true for a section with an empty hash")
	}
}

type distFixture struct {
	tags     []string
	manifest map[string]string // version -> manifest body
}

func newGitHubAndDist(t *testing.T, f distFixture) *Rust {
	t.Helper()
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	mux.HandleFunc("/gh/releases", func(w http.ResponseWriter, r *http.Request) {
		var parts []string
		for _, tag := range f.tags {
			parts = append(parts, fmt.Sprintf(`{"tag_name":%q,"draft":false,"prerelease":false}`, tag))
		}
		fmt.Fprintf(w, "[%s]", strings.Join(parts, ","))
	})
	mux.HandleFunc("/dist/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/dist/")
		version := strings.TrimSuffix(strings.TrimPrefix(name, "channel-rust-"), ".toml")
		body, ok := f.manifest[version]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body)
	})
	return &Rust{githubURL: server.URL + "/gh", distURL: server.URL + "/dist"}
}

func hostTriple(t *testing.T) string {
	t.Helper()
	triple, err := rustTarget()
	if err != nil {
		t.Skipf("unsupported platform for this test: %v", err)
	}
	return triple
}

func TestListRemoteVersions_NewestLineFirstStableTagsOnly(t *testing.T) {
	t.Parallel()
	r := newGitHubAndDist(t, distFixture{tags: []string{"1.84.1", "1.85.0", "1.84.0", "1.9.0", "1.86.0-beta.1", "nightly", "1.100.0", "1.85.1"}})

	versions, err := r.ListRemoteVersions(context.Background())
	if err != nil {
		t.Fatalf("ListRemoteVersions() returned error: %v", err)
	}
	var got []string
	for _, v := range versions {
		got = append(got, v.Name)
	}
	if want := "1.100,1.85,1.84,1.9"; strings.Join(got, ",") != want {
		t.Errorf("ListRemoteVersions() = %v, want %s", got, want)
	}
}

func TestListRemoteVersions_ServerErrorReturnsError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	r := &Rust{githubURL: server.URL, distURL: server.URL}
	if _, err := r.ListRemoteVersions(context.Background()); err == nil {
		t.Fatal("ListRemoteVersions() returned nil error for a 500 response")
	}
}

func TestResolveRelease_PicksGreatestPatchAndReadsManifest(t *testing.T) {
	t.Parallel()
	triple := hostTriple(t)
	url := "https://static.rust-lang.org/dist/2025-02-20/rust-1.85.10-" + triple + ".tar.gz"
	r := newGitHubAndDist(t, distFixture{
		tags:     []string{"1.85.2", "1.85.10", "1.85.9", "1.84.0"},
		manifest: map[string]string{"1.85.10": manifestFor(triple, url, goodHash)},
	})

	rel, err := r.resolveRelease(context.Background(), "1.85")
	if err != nil {
		t.Fatalf("resolveRelease() returned error: %v", err)
	}
	if rel.Version != "1.85.10" {
		t.Errorf("rel.Version = %q, want %q (numeric, not lexical, patch comparison)", rel.Version, "1.85.10")
	}
	if rel.URL != url || rel.SHA256 != goodHash {
		t.Errorf("rel = %+v, want URL %q and hash %q", rel, url, goodHash)
	}
	if rel.Filename != "rust-1.85.10-"+triple+".tar.gz" {
		t.Errorf("rel.Filename = %q", rel.Filename)
	}
}

func TestResolveRelease_UnknownLineReturnsEmptyReleaseNoError(t *testing.T) {
	t.Parallel()
	r := newGitHubAndDist(t, distFixture{tags: []string{"1.85.0"}})
	rel, err := r.resolveRelease(context.Background(), "9.9")
	if err != nil || rel.Version != "" {
		t.Errorf("resolveRelease() = %+v, %v; want zero value, nil", rel, err)
	}
}

func TestResolveRelease_NoBuildForThisPlatformReturnsEmptyReleaseNoError(t *testing.T) {
	t.Parallel()
	hostTriple(t)
	r := newGitHubAndDist(t, distFixture{
		tags:     []string{"1.85.0"},
		manifest: map[string]string{"1.85.0": manifestFor("some-other-triple", "https://h/rust-1.85.0-x.tar.gz", goodHash)},
	})
	rel, err := r.resolveRelease(context.Background(), "1.85")
	if err != nil || rel.Version != "" {
		t.Errorf("resolveRelease() = %+v, %v; want zero value, nil", rel, err)
	}
}

func TestResolveRelease_MissingManifestReturnsError(t *testing.T) {
	t.Parallel()
	hostTriple(t)
	r := newGitHubAndDist(t, distFixture{tags: []string{"1.85.0"}})
	if _, err := r.resolveRelease(context.Background(), "1.85"); err == nil {
		t.Fatal("resolveRelease() returned nil error when the version's manifest is a 404")
	}
}

func TestResolveRelease_RejectsPathInjectionShapedFilename(t *testing.T) {
	t.Parallel()
	triple := hostTriple(t)
	for _, url := range []string{
		"https://static.rust-lang.org/dist/..",
		"https://static.rust-lang.org/dist/.",
		"https://static.rust-lang.org/dist/rust-1.85.0-" + triple + ".zip",
		"",
	} {
		r := newGitHubAndDist(t, distFixture{
			tags:     []string{"1.85.0"},
			manifest: map[string]string{"1.85.0": manifestFor(triple, url, goodHash)},
		})
		rel, err := r.resolveRelease(context.Background(), "1.85")
		if err == nil && rel.Version != "" {
			t.Errorf("resolveRelease() accepted manifest url %q: %+v", url, rel)
		}
	}
}

func TestResolveRelease_RejectsMalformedHash(t *testing.T) {
	t.Parallel()
	triple := hostTriple(t)
	for _, hash := range []string{"deadbeef", strings.Repeat("g", 64), goodHash + "00"} {
		r := newGitHubAndDist(t, distFixture{
			tags:     []string{"1.85.0"},
			manifest: map[string]string{"1.85.0": manifestFor(triple, "https://h/rust-1.85.0-"+triple+".tar.gz", hash)},
		})
		rel, err := r.resolveRelease(context.Background(), "1.85")
		if err == nil && rel.Version != "" {
			t.Errorf("resolveRelease() accepted hash %q: %+v", hash, rel)
		}
	}
}

// buildInstallerTarGz builds a tar.gz in the shape of Rust's combined
// installer: one top-level directory holding a `components` list,
// install.sh, and one directory per component, each with its own
// manifest.in.
func buildInstallerTarGz(t *testing.T, topLevel string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, content := range files {
		hdr := &tar.Header{Name: topLevel + "/" + name, Mode: 0o755, Size: int64(len(content))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("writing tar header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("writing tar content: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar writer: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("closing gzip writer: %v", err)
	}
	return buf.Bytes()
}

func installerFiles() map[string]string {
	return map[string]string{
		"components":                            "rustc\nrust-std-x\ncargo\nrust-docs\n",
		"install.sh":                            "#!/bin/sh\n",
		"rust-installer-version":                "3\n",
		"rustc/manifest.in":                     "file:bin/rustc\n",
		"rustc/bin/rustc":                       "fake rustc",
		"rustc/lib/librustc_driver.so":          "driver",
		"rust-std-x/manifest.in":                "dir:lib/rustlib/x\n",
		"rust-std-x/lib/rustlib/x/lib/libstd.r": "std",
		"cargo/manifest.in":                     "file:bin/cargo\n",
		"cargo/bin/cargo":                       "fake cargo",
		"rust-docs/manifest.in":                 "dir:share/doc/rust\n",
		"rust-docs/share/doc/rust/index.html":   "docs",
	}
}

func writeArchive(t *testing.T, dir, name string, body []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	return p
}

func readString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

func TestExtractRust_MergesComponentsIntoOneTreeWithoutDocsOrInstallerFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	archive := writeArchive(t, dir, "rust.tar.gz", buildInstallerTarGz(t, "rust-1.85.0-x", installerFiles()))
	destDir := filepath.Join(dir, "dest")

	if err := extractRust(archive, destDir); err != nil {
		t.Fatalf("extractRust() returned error: %v", err)
	}

	if got := readString(t, filepath.Join(destDir, "bin", "rustc")); got != "fake rustc" {
		t.Errorf("bin/rustc = %q", got)
	}
	if got := readString(t, filepath.Join(destDir, "bin", "cargo")); got != "fake cargo" {
		t.Errorf("bin/cargo = %q", got)
	}
	if got := readString(t, filepath.Join(destDir, "lib", "rustlib", "x", "lib", "libstd.r")); got != "std" {
		t.Errorf("rust-std content = %q", got)
	}
	if got := readString(t, filepath.Join(destDir, "lib", "librustc_driver.so")); got != "driver" {
		t.Errorf("lib/librustc_driver.so = %q", got)
	}
	for _, unwanted := range []string{"install.sh", "components", "manifest.in", "rustc", "rust-docs", filepath.Join("share", "doc")} {
		if _, err := os.Lstat(filepath.Join(destDir, unwanted)); !os.IsNotExist(err) {
			t.Errorf("%s exists in the installed tree, want it absent", unwanted)
		}
	}
}

func TestExtractRust_ReplacesExistingDestDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	destDir := filepath.Join(dir, "dest")
	for _, rustc := range []string{"v1", "v2"} {
		files := installerFiles()
		files["rustc/bin/rustc"] = rustc
		archive := writeArchive(t, dir, rustc+".tar.gz", buildInstallerTarGz(t, "rust-top", files))
		if err := extractRust(archive, destDir); err != nil {
			t.Fatalf("extractRust(%s) returned error: %v", rustc, err)
		}
	}
	if got := readString(t, filepath.Join(destDir, "bin", "rustc")); got != "v2" {
		t.Errorf("bin/rustc = %q, want %q", got, "v2")
	}
	if _, err := os.Stat(destDir + ".old"); !os.IsNotExist(err) {
		t.Error("backup directory left behind after a successful replace")
	}
}

func TestExtractRust_BadArchivesLeaveDestDirUntouched(t *testing.T) {
	t.Parallel()
	cases := map[string]func() map[string]string{
		"component name escapes the tree": func() map[string]string {
			f := installerFiles()
			f["components"] = "rustc\n../../evil\n"
			return f
		},
		"component name with a separator": func() map[string]string {
			f := installerFiles()
			f["components"] = "rustc\nsub/dir\n"
			return f
		},
		"listed component is missing": func() map[string]string {
			f := installerFiles()
			f["components"] = "rustc\nghost\n"
			return f
		},
		"no components file": func() map[string]string {
			f := installerFiles()
			delete(f, "components")
			return f
		},
		"no bin directory results": func() map[string]string {
			return map[string]string{
				"components":                    "rust-std-x\n",
				"rust-std-x/lib/rustlib/x/libx": "x",
			}
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			destDir := filepath.Join(dir, "dest")
			if err := os.MkdirAll(destDir, 0o755); err != nil {
				t.Fatalf("setup: %v", err)
			}
			if err := os.WriteFile(filepath.Join(destDir, "keep.txt"), []byte("keep me"), 0o644); err != nil {
				t.Fatalf("setup: %v", err)
			}
			archive := writeArchive(t, dir, "bad.tar.gz", buildInstallerTarGz(t, "rust-top", build()))

			if err := extractRust(archive, destDir); err == nil {
				t.Fatal("extractRust() returned nil error")
			}
			if got := readString(t, filepath.Join(destDir, "keep.txt")); got != "keep me" {
				t.Errorf("destDir content = %q, want it untouched", got)
			}
			if _, err := os.Stat(filepath.Join(dir, "evil")); !os.IsNotExist(err) {
				t.Error("a file escaped the install tree")
			}
		})
	}
}

func TestExtractRust_TwoTopLevelEntriesLeavesDestDirUntouched(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	destDir := filepath.Join(dir, "dest")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for _, n := range []string{"one/f", "two/f"} {
		_ = tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: 1})
		_, _ = tw.Write([]byte("x"))
	}
	_ = tw.Close()
	_ = gw.Close()
	archive := writeArchive(t, dir, "bad.tar.gz", buf.Bytes())

	if err := extractRust(archive, destDir); err == nil {
		t.Fatal("extractRust() returned nil error for a two-top-level-entry archive")
	}
}

func TestListInstalledVersions_SkipsHiddenAndBackupDirsAndFiles(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	r := New()
	if got, err := r.ListInstalledVersions(); err != nil || len(got) != 0 {
		t.Fatalf("ListInstalledVersions() with nothing installed = %+v, %v; want empty", got, err)
	}
	base := filepath.Join(devHome, "versions", "rust")
	for _, d := range []string{"1.85", "1.84", ".tmp-rust-extract-1", "1.83.old"} {
		if err := os.MkdirAll(filepath.Join(base, d), 0o755); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, "stray"), nil, 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	got, err := r.ListInstalledVersions()
	if err != nil {
		t.Fatalf("ListInstalledVersions() returned error: %v", err)
	}
	names := map[string]bool{}
	for _, v := range got {
		names[v.Name] = true
	}
	if len(got) != 2 || !names["1.85"] || !names["1.84"] {
		t.Errorf("ListInstalledVersions() = %+v, want exactly 1.85 and 1.84", got)
	}
}

func TestCurrentVersion_NilWhenNoneActiveAndReadsMarker(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	r := New()
	if got, err := r.CurrentVersion(); err != nil || got != nil {
		t.Fatalf("CurrentVersion() = %+v, %v; want nil, nil", got, err)
	}
	if err := os.MkdirAll(filepath.Join(devHome, "current"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(devHome, "current", "rust"), []byte("1.85\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	got, err := r.CurrentVersion()
	if err != nil || got == nil || got.Name != "1.85" {
		t.Errorf("CurrentVersion() = %+v, %v; want Name=1.85", got, err)
	}
}

// installFixture serves a resolvable Rust 1.85.0 for this host whose
// tarball is archive, advertised with checksum.
func installFixture(t *testing.T, archive []byte, checksum string) (*Rust, *int) {
	t.Helper()
	triple := hostTriple(t)
	downloads := 0
	filename := "rust-1.85.0-" + triple + ".tar.gz"

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	mux.HandleFunc("/gh/releases", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"tag_name":"1.85.0"}]`)
	})
	mux.HandleFunc("/dist/channel-rust-1.85.0.toml", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, manifestFor(triple, server.URL+"/dist/2025-02-20/"+filename, checksum))
	})
	mux.HandleFunc("/dist/2025-02-20/"+filename, func(w http.ResponseWriter, r *http.Request) {
		downloads++
		_, _ = w.Write(archive)
	})
	return &Rust{githubURL: server.URL + "/gh", distURL: server.URL + "/dist"}, &downloads
}

func TestInstall_DownloadsVerifiesAndExtracts(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	archive := buildInstallerTarGz(t, "rust-top", installerFiles())
	sum := sha256.Sum256(archive)
	r, _ := installFixture(t, archive, hex.EncodeToString(sum[:]))

	if err := r.Install(context.Background(), "1.85"); err != nil {
		t.Fatalf("Install() returned error: %v", err)
	}
	dest := filepath.Join(devHome, "versions", "rust", "1.85")
	if got := readString(t, filepath.Join(dest, "bin", "rustc")); got != "fake rustc" {
		t.Errorf("bin/rustc = %q", got)
	}
	if got := readString(t, filepath.Join(dest, ".dev-release")); got != "1.85.0" {
		t.Errorf(".dev-release = %q, want %q", got, "1.85.0")
	}
}

func TestInstall_ChecksumMismatchFailsAndInstallsNothing(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	archive := buildInstallerTarGz(t, "rust-top", installerFiles())
	r, _ := installFixture(t, archive, strings.Repeat("0", 64))

	if err := r.Install(context.Background(), "1.85"); err == nil {
		t.Fatal("Install() returned nil error despite a checksum mismatch")
	}
	if _, err := os.Stat(filepath.Join(devHome, "versions", "rust", "1.85")); !os.IsNotExist(err) {
		t.Error("version directory exists after a failed, checksum-mismatched install")
	}
}

func TestInstall_AlreadyInstalledSkipsRedownload(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	archive := buildInstallerTarGz(t, "rust-top", installerFiles())
	sum := sha256.Sum256(archive)
	r, downloads := installFixture(t, archive, hex.EncodeToString(sum[:]))

	for i := 0; i < 2; i++ {
		if err := r.Install(context.Background(), "1.85"); err != nil {
			t.Fatalf("Install() #%d returned error: %v", i+1, err)
		}
	}
	if *downloads != 1 {
		t.Errorf("archive downloaded %d times, want 1", *downloads)
	}
}

func TestInstall_OfflineFallbackUsesCachedReleaseWhenResolveFails(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	dest := filepath.Join(devHome, "versions", "rust", "1.85")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dest, ".dev-release"), []byte("1.85.0"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	var buf bytes.Buffer
	origStdout := cliutil.Stdout
	cliutil.Stdout = &buf
	defer func() { cliutil.Stdout = origStdout }()

	if err := (&Rust{githubURL: server.URL, distURL: server.URL}).Install(context.Background(), "1.85"); err != nil {
		t.Fatalf("Install() returned error, want offline fallback success: %v", err)
	}
	if !strings.Contains(buf.String(), "1.85.0") {
		t.Errorf("output = %q, want it to mention the cached release 1.85.0", buf.String())
	}
}

func TestInstall_NoOfflineFallbackWhenNothingCached(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	if err := (&Rust{githubURL: server.URL, distURL: server.URL}).Install(context.Background(), "1.85"); err == nil {
		t.Fatal("Install() returned nil error with nothing cached and the server down")
	}
}

func TestInstall_UnknownLineFailsWithoutCreatingDirectory(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	hostTriple(t)
	r := newGitHubAndDist(t, distFixture{tags: []string{"1.85.0"}})
	if err := r.Install(context.Background(), "9.9"); err == nil {
		t.Fatal("Install() returned nil error for an unknown line")
	}
	if _, err := os.Stat(filepath.Join(devHome, "versions", "rust", "9.9")); !os.IsNotExist(err) {
		t.Error("version directory created for an unknown line")
	}
}

func TestInstall_RejectsPathInjectionShapedName(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	for _, name := range []string{"", ".", "..", "../x", `a\b`} {
		if err := New().Install(context.Background(), name); err == nil {
			t.Errorf("Install(%q) returned nil error", name)
		}
	}
}

func TestUninstall_RemovesDirectoryAndClearsActiveMarker(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	if err := os.MkdirAll(filepath.Join(devHome, "versions", "rust", "1.85"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	r := New()
	if err := r.Activate("1.85"); err != nil {
		t.Fatalf("Activate() returned error: %v", err)
	}
	if err := r.Uninstall("1.85"); err != nil {
		t.Fatalf("Uninstall() returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(devHome, "versions", "rust", "1.85")); !os.IsNotExist(err) {
		t.Error("version directory still exists after Uninstall")
	}
	if got, _ := r.CurrentVersion(); got != nil {
		t.Errorf("CurrentVersion() = %+v after uninstalling the active version, want nil", got)
	}
}

func TestUninstall_LeavesOtherActiveVersionAlone(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("DEV_HOME", devHome)
	for _, v := range []string{"1.85", "1.84"} {
		if err := os.MkdirAll(filepath.Join(devHome, "versions", "rust", v), 0o755); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	r := New()
	if err := r.Activate("1.84"); err != nil {
		t.Fatalf("Activate() returned error: %v", err)
	}
	if err := r.Uninstall("1.85"); err != nil {
		t.Fatalf("Uninstall() returned error: %v", err)
	}
	if got, _ := r.CurrentVersion(); got == nil || got.Name != "1.84" {
		t.Errorf("CurrentVersion() = %+v, want 1.84 untouched", got)
	}
}

func TestUninstallAndActivate_NotInstalledReturnError(t *testing.T) {
	t.Setenv("DEV_HOME", t.TempDir())
	r := New()
	if err := r.Uninstall("1.85"); err == nil {
		t.Error("Uninstall() of a missing version returned nil error")
	}
	if err := r.Activate("1.85"); err == nil {
		t.Error("Activate() of a missing version returned nil error")
	}
}

func TestBinaryPathForOS(t *testing.T) {
	t.Parallel()
	if got, want := binaryPathForOS("windows", "/v", "rustc"), filepath.Join("/v", "bin", "rustc.exe"); got != want {
		t.Errorf("windows = %q, want %q", got, want)
	}
	for _, osName := range []string{"linux", "darwin"} {
		if got, want := binaryPathForOS(osName, "/v", "rustc"), filepath.Join("/v", "bin", "rustc"); got != want {
			t.Errorf("%s = %q, want %q", osName, got, want)
		}
	}
}

func TestBinaryPath_ChecksExistence(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r := New()
	if _, err := r.BinaryPath(dir, "rustc"); err == nil {
		t.Fatal("BinaryPath() returned nil error for a missing binary")
	}
	osName, err := rustOS()
	if err != nil {
		t.Skipf("unsupported OS for this test: %v", err)
	}
	want := binaryPathForOS(osName, dir, "rustc")
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(want, nil, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if got, err := r.BinaryPath(dir, "rustc"); err != nil || got != want {
		t.Errorf("BinaryPath() = %q, %v; want %q", got, err, want)
	}
}

func TestBinDir_IsBinSubdir(t *testing.T) {
	t.Parallel()
	got, err := New().BinDir("/v")
	if err != nil || got != filepath.Join("/v", "bin") {
		t.Errorf("BinDir() = %q, %v; want %q", got, err, filepath.Join("/v", "bin"))
	}
}

func TestName(t *testing.T) {
	t.Parallel()
	if New().Name() != "rust" {
		t.Errorf("Name() = %q, want %q", New().Name(), "rust")
	}
}
