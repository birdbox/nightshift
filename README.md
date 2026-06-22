# nightshift

An autonomous ticket-execution harness. Run it inside a git repository and it
finds GitHub issues to work on and (eventually) drives [Claude Code](https://www.anthropic.com/claude-code)
in isolated git worktrees to implement them and open pull requests.

nightshift treats the **current working directory as the project**, so it works
on any repo without per-project setup. Conventions live in each repo's own
`CLAUDE.md` and build scripts — nightshift points Claude at the repo and lets it
discover the rules rather than re-encoding them.

## Status: phase one

nightshift detects the repo, selects issues, and — with `--execute` — runs
Claude Code on each one in an isolated worktree to open a PR. Without
`--execute` it's a dry run that reports what it would do.

```
nightshift                 # dry run: issues assigned to you (@me), open
nightshift --label nightshift
nightshift 1234 1240       # specific issue numbers (bypass filters)

nightshift --execute       # create a worktree per issue and run Claude
nightshift --execute --yes # ...without the confirmation prompt
```

### Execution flags

- `--execute` — actually run Claude (otherwise dry run).
- `--yes` — skip the confirmation prompt.
- `--base <branch>` — base branch to branch from / target PRs at (default: repo default branch).
- `--model <name>` — Claude model alias or full name (default: claude's own default).
- `--keep` — keep worktrees after running, for inspection.
- `--worktree-root <dir>` — where worktrees are created (default: a temp dir).

Each issue gets a branch `nightshift/issue-<n>-<slug>` off `origin/<base>`. Claude
is told to read the repo's own `CLAUDE.md`/build config, run its own checks, and
open a PR with `Closes #<n>`. Ctrl+C cancels and cleans up the worktree.

## Requirements

- [`gh`](https://cli.github.com/) installed and authenticated (`gh auth login`).
  nightshift borrows gh's auth instead of managing its own token.
- `git`

## Build

```
go build -o nightshift .
```

## Roadmap

- Run agents **concurrently** across worktrees (capped), instead of sequentially.
- A Bubble Tea TUI for monitoring concurrent agents.
- Optional multi-project mode (a registry over per-repo config).
