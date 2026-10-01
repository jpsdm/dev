# dev

`dev` is a small, fast Development Environment Manager: a single Go binary (module `github.com/jpsdm/dev`) built with Cobra that installs/switches language runtime versions and manages a developer workspace.

## Build & test

```
make build   # CGO_ENABLED=0, ldflags-injected version (git describe)
make test
make lint    # golangci-lint
make check   # fmt + vet + lint + test + build — run this before every commit
```

No runtime dependency is required to run `dev` itself — it ships as a single static binary. Keep it that way: stdlib first, and only add a third-party dependency when it earns its place (this project uses Cobra, `charmbracelet/huh`, and `go-git` — nothing else).

## Code conventions

- **TDD is required.** Write the failing test first, watch it fail, then implement. See `superpowers:test-driven-development` if that skill is available in your session.
- **Tests exercise real behavior, not mocks.** `httptest.Server` for HTTP, real `archive/tar`/`archive/zip` bytes for archive tests, real temp directories for filesystem tests. This project has no mocking library and should not gain one.
- **Path-safety validation is mandatory for any externally-sourced identifier that becomes part of a filesystem path.** Any string that comes from a network response, a git tag, or user input and is later used in `filepath.Join` must be validated first — see `internal/runtime.ValidVersionName` and its callers for the established pattern (reject empty, `.`, `..`, and anything containing a path separator). This project has had two real path-injection findings caught by review; don't skip this.
- **Downloads are checksum-verified before use** (`internal/downloader.Download`), and **archive extraction is atomic and zip-slip-protected** (`internal/installer.ExtractAtomic`). Reuse these, don't reimplement.
- **Formatting is enforced automatically** — a project hook runs `gofmt -w` on every `.go` file after it's edited (see `.claude/settings.json`). `make check` still runs `gofmt -l` as the CI-equivalent gate.

## Architecture: the provider pattern

Every language runtime (Node.js, Java — Go/Python/Rust are deferred) implements `internal/runtime.Runtime`:

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

A new provider lives at `internal/runtime/<lang>/<lang>.go`, is registered with one line in `internal/providers.Register`, and needs **zero changes** to `cmd/lang.go` or `internal/shell` — both are generic over the registry. Use the `add-language-provider` skill (`.claude/skills/add-language-provider/`) when adding one; it walks the exact pattern `internal/runtime/node` and `internal/runtime/java` already establish.

## Development process

This project follows a spec → plan → implement cycle for anything beyond a trivial fix, using the `superpowers` skill plugin when available:

1. `superpowers:brainstorming` — classify the work (spike/bounded/architectural), design it collaboratively, write a spec to `docs/superpowers/specs/YYYY-MM-DD-<topic>-design.md` for architectural work.
2. `superpowers:writing-plans` — turn an approved spec into a task-by-task implementation plan at `docs/superpowers/plans/YYYY-MM-DD-<topic>.md`.
3. `superpowers:subagent-driven-development` or `superpowers:executing-plans` — execute the plan, task by task, with real test evidence per step.
4. A final whole-branch review before wrapping up (dispatched on the most capable available model, or a careful self-review if no subagent tool exists).
5. `superpowers:finishing-a-development-branch` — decide how to integrate. This repo now requires a PR (branch protection on `main`) — use the `open-pr` skill (`.claude/skills/open-pr/`) for the actual branch/commit-hygiene/PR-content mechanics specific to this repo.

**Minor findings that surface during a final review but aren't worth fixing immediately go in `docs/DEFERRED_MINORS.md`**, organized by `## Sub-project N — <name>` headings, one section per completed unit of work. Check it before assuming something is a known issue or a fresh one.

## Git conventions

- **This repo now works on a PR-based workflow** — branch protection on `main` requires a pull request (with a passing `ci.yml` check, and review) rather than a direct push. Do not commit directly to `main` going forward, including for the maintainer's own requests; use the `open-pr` skill (`.claude/skills/open-pr/`) to get work onto a branch, verify it, and open the PR. (This is a policy the repo owner set in GitHub's settings, which aren't independently readable from here without `gh` — take it as the standing rule regardless.) History predating this — including plenty of direct-to-`main` commits — is expected and not being rewritten.
- **Commit messages follow Conventional Commits** (`<type>(<scope>): <summary>` — `feat`, `fix`, `docs`, `refactor`, `test`, `chore`, `ci`, `build`, `perf`; body explains *why*, never a trailing summary of *what changed*, since the diff already shows that). This was adopted alongside the project's collaboration-readiness work; commit history before that point uses a different, prose-only style (short imperative subject, no type prefix) — that's expected, don't rewrite old history to match. See `CONTRIBUTING.md`'s "Commit messages" section for the full convention and examples.
- Use the `release-tag` skill (`.claude/skills/release-tag/`) to cut a version tag — it runs `make check` first and follows this project's exact `vX.Y.Z` scheme. Tagging still happens on `main` after a PR has merged, not on a feature branch.

## Releases

Tags matching `vX.Y.Z` trigger `.github/workflows/release.yml` (GoReleaser), which cross-compiles for linux/darwin/windows × amd64/arm64, packages archives with a `checksums.txt`, and creates a **draft** GitHub Release for manual review before publishing. See `.goreleaser.yml` for the exact build/archive/checksum config, and `docs/superpowers/specs/2026-09-28-release-automation-design.md` for the design rationale.

## Security review

This project has an installed `security-review` skill for general-purpose security auditing. For this codebase specifically, also load `.claude/skills/security-checklist/` first — it lists this project's own known risk patterns (path injection into filesystem paths, checksum-before-extract, zip-slip, shell-injection-free provider code) so a review has the right context instead of starting from a blank slate.
