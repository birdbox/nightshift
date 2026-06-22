# nightshift — agent guide

nightshift is a single-binary Go CLI that drives Claude Code over GitHub issues
in isolated git worktrees. It treats the current working directory as the project.

## Conventions

- **Standard library first.** This tool is deliberately dependency-light; the
  binary shells out to `gh`, `git`, and `claude` rather than pulling in SDKs.
  Do not add third-party modules without a strong reason — prefer the stdlib.
- **Package layout:** `main.go` is the CLI surface (flags, orchestration);
  reusable logic lives under `internal/` (`gh`, `git`, `runner`).
- **Errors:** wrap with `fmt.Errorf("...: %w", err)` and surface useful context.
- **Comments** explain *why*, not *what*; match the surrounding style.
- Keep external-facing behavior (branches it pushes, PRs it opens) reversible and
  clearly logged.

## Verification (run before committing)

From the repository root:

```
go build ./...   # must compile
go vet ./...     # must be clean
go test ./...    # must pass (add tests for new pure functions)
gofmt -l .       # must print nothing (run `gofmt -w .` to fix)
```

## Commit / PR conventions

- Conventional Commits (`feat:`, `fix:`, `refactor:`, `docs:`, `test:`).
- PRs target `main`. Reference the issue with `Closes #<n>` in the body.
- Keep changes focused on the issue; don't touch unrelated files.
