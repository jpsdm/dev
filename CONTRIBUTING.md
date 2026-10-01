# Contributing to `dev`

Thanks for considering a contribution. `dev` is a small Development
Environment Manager — see [README.md](README.md) for what it does and
[docs/superpowers/specs/](docs/superpowers/specs/) for the design rationale
behind its larger subsystems.

This document covers everything you need to build the project, understand
its conventions, and get a change merged. If something here is unclear or
out of date, opening an issue about *this document* is a welcome
contribution too.

## Before you start

- For anything beyond a small, obvious fix (a typo, an off-by-one, a
  clearly-missing test), **open an issue first** to discuss the approach
  before writing code. This project has fairly specific architectural
  conventions (see below) and a design discussion up front saves a
  rewritten PR later.
- Proposing a new language/runtime provider (Python, Rust, ...)? Use the
  **New language provider** issue template — it asks for the research
  (how the language actually distributes releases) a reviewer needs before
  any code gets written.
- Security issue? Don't open a public issue — see
  [SECURITY.md](SECURITY.md).

## Development setup

Requirements:

- Go 1.25 or later (`go.mod`'s floor; tracks
  `github.com/go-git/go-git/v5`'s own requirement, not a feature this
  project itself needs)
- No other runtime dependency — `dev` ships as a single static binary and
  the test suite doesn't need Node.js, Java, or Go itself pre-installed
  (the language-provider tests use `httptest.Server` fixtures, never real
  network calls)

```bash
git clone https://github.com/jpsdm/dev.git
cd dev
make build   # CGO_ENABLED=0, version injected via git describe
make test    # go test ./...
make lint    # golangci-lint (v2 config — see .golangci.yml)
make check   # fmt + vet + lint + test + build — run this before every commit
```

`make check` is the same gate `.github/workflows/ci.yml` runs on every
push and pull request. A PR that fails it won't be merged, so run it
locally first.

## Project layout

- `cmd/` — Cobra command definitions (`dev lang`, `dev workspace`,
  `dev setup`, `dev update`, ...).
- `internal/runtime/<lang>/` — one package per managed language, each
  implementing the `Runtime` interface (`internal/runtime/runtime.go`).
  `internal/runtime/node` and `internal/runtime/java` are the two
  reference implementations.
- `internal/providers/providers.go` — the single place every `Runtime`
  gets registered. Nothing else needs to change to add a language — see
  "Adding a language provider" below.
- `internal/installer`, `internal/downloader`, `internal/filesystem` —
  shared, security-relevant primitives (atomic extraction, checksum
  verification, atomic file writes). Reuse these; don't reimplement them
  in a provider.
- `docs/superpowers/specs/` and `docs/superpowers/plans/` — design specs
  and implementation plans for past subsystems, useful background reading
  before touching an area you're unfamiliar with.

## Code conventions

- **Go stdlib first.** This project's only third-party dependencies are
  Cobra (CLI), `charmbracelet/huh` (interactive prompts), and `go-git`
  (gitignore matching). A new dependency needs a real reason, not
  convenience.
- **Tests are required, and they exercise real behavior, not mocks.**
  `httptest.Server` for HTTP, real `archive/tar`/`archive/zip` bytes for
  archive tests, real temporary directories for filesystem tests. There is
  no mocking library in this codebase and PRs shouldn't introduce one.
- **Any externally-sourced string that becomes part of a filesystem path
  must be validated first.** A network response's filename field, a git
  tag, anything from outside the process — validate it via
  `internal/runtime.ValidVersionName` (or delegate to it) before it
  reaches `filepath.Join`. This project has had real path-injection
  findings caught by review; this rule is not optional.
- **Downloads are checksum-verified** via `internal/downloader.Download`,
  and **archive extraction is atomic and zip-slip-protected** via
  `internal/installer.ExtractAtomic`. Never add a second, ad hoc
  extraction path.
- **`gofmt` is enforced** (`make check` runs `gofmt -l`, and a
  fmt-on-save-equivalent is expected locally — don't submit unformatted
  Go).

## Adding a language provider

Every provider implements:

```go
type Runtime interface {
	Name() string
	ListRemoteVersions(ctx context.Context) ([]Version, error)
	ListInstalledVersions() ([]Version, error)
	CurrentVersion() (*Version, error)
	Install(ctx context.Context, name string) error
	Uninstall(name string) error
	Activate(name string) error
	BinaryPath(versionDir, binName string) (string, error)
	BinDir(versionDir string) (string, error)
}
```

A new provider lives at `internal/runtime/<lang>/<lang>.go`, gets one line
in `internal/providers.Register`, and needs **zero changes** to
`cmd/lang.go` or `internal/shell` — both are generic over the registry
(`internal/shell`'s `ComputePathEntries` and `cmd/env.go`'s
`activeBinDirs` iterate `langManager.Names()` generically to put a new
provider's active version on `PATH`). If a change you're making needs to
touch any of those, that's a sign the new provider doesn't actually fit
the `Runtime` interface as intended — worth raising in the issue before
writing more code.

The hard part is always the same: *how does this language actually
distribute its releases?* Research the real API/index live — don't guess
from memory of how a similar language's registry usually looks. Read
`internal/runtime/java/java.go` in full first; it's the most refined
reference (path-safety validation on every server-supplied field, not just
the obvious one, and an OS-parameterized binary-path function for
host-independent testing).

## Commit messages

This project uses [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<optional scope>): <short imperative summary>

<optional body — explain *why*, not *what*; the diff already shows what>
```

Common types: `feat`, `fix`, `docs`, `refactor`, `test`, `chore`, `ci`,
`build`, `perf`. Examples from this project's own shape of change:

```
feat(runtime/go): add Go language provider

fix(workspace): normalize nested-file paths to forward slashes in Clean

docs: document the new language provider issue template
```

Keep the summary line imperative and under ~72 characters. Use the body
for anything a reviewer would otherwise have to ask about — a non-obvious
tradeoff, why an approach was rejected, a link to the relevant spec.

(Commit history before this convention was adopted uses a different,
prose-based style — that's expected; don't rewrite old history to match.)

## Submitting a change

1. Fork the repo (or branch, if you have push access) and make your
   change on a branch — not `main`.
2. Keep the PR to one logical change. A bug fix and an unrelated
   refactor belong in separate PRs.
3. Add or update tests for the change (TDD is this project's convention —
   see "Code conventions" above).
4. Run `make check` locally and confirm it's clean.
5. Open the PR, fill out the template, and link the issue it addresses if
   there is one.
6. CI (`ci.yml`) runs automatically. A maintainer will review — expect
   feedback on convention adherence (path validation, real tests, scope)
   even when the logic itself is correct, since this project holds those
   fairly strictly.

## Contribution license

By submitting a pull request, you agree that your contribution is
licensed under this project's [MIT License](LICENSE).

## Code of Conduct

This project follows the [Code of Conduct](CODE_OF_CONDUCT.md). By
participating, you're expected to uphold it.
