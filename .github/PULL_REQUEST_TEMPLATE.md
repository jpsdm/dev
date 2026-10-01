## What does this PR do?

<!-- One or two sentences. Link a related issue if there is one (e.g. "Closes #123"). -->

## Checklist

- [ ] `make check` passes locally (fmt, vet, lint, test, build)
- [ ] Tests added/updated for the change — this project requires TDD (see [CONTRIBUTING.md](../CONTRIBUTING.md#code-conventions))
- [ ] Tests exercise real behavior, not mocks (`httptest.Server`, real archives, real temp dirs)
- [ ] Any externally-sourced string that reaches a filesystem path is validated (`internal/runtime.ValidVersionName` or equivalent)
- [ ] Commit messages follow [Conventional Commits](../CONTRIBUTING.md#commit-messages)
- [ ] No unrelated changes bundled in (one logical change per PR)

## Notes for reviewers

<!-- Anything a reviewer should know: design tradeoffs, things you're unsure about, alternatives you considered. -->
