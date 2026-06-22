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
- `--concurrency <n>` — how many issues to work on at once (default 3).
- `--base <branch>` — base branch to branch from / target PRs at (default: repo default branch).
- `--model <name>` — Claude model alias or full name (default: claude's own default).
- `--keep` — keep worktrees after running, for inspection.
- `--worktree-root <dir>` — where worktrees and logs are created (default: a temp dir).

Each issue gets a branch `nightshift/issue-<n>-<slug>` off `origin/<base>`. Claude
is told to read the repo's own `CLAUDE.md`/build config, run its own checks, and
commit — then **nightshift** pushes the branch and opens the PR (with `Closes
#<n>`) via the GitHub API. If the agent produces no commits, no PR is opened.
Ctrl+C cancels in-flight agents and cleans up their worktrees.

### Concurrency and logs

Issues run through a bounded worker pool. Because several agents' output would be
unreadable interleaved, each agent writes its full output to a log file
(`<worktree-root>/issue-<n>.log`) and the console shows one status line per event:

```
▶ #123 started — Fix flaky upload test
▶ #128 started — Add rate limiting to /api/search
✓ #123 done in 3m12s (log: /tmp/nightshift/nightshift/issue-123.log)
✗ #128 failed in 1m4s: claude execution: exit status 1 (log: .../issue-128.log)

Done. 1 succeeded, 1 failed.
```

With `--concurrency 1`, output is also teed live to the console.

## Requirements

- `git` (with push access to the repo's `origin`, e.g. an SSH key).
- `claude` (Claude Code CLI), authenticated.
- A GitHub token in `GITHUB_TOKEN` (or `GH_TOKEN`) with access to the repo's
  issues and pull requests. nightshift talks to the GitHub REST API directly —
  no `gh` CLI required.

  ```
  export GITHUB_TOKEN=ghp_...        # a personal access token
  ```

  A fine-grained token needs Contents (read/write), Pull requests (read/write),
  and Issues (read) on the target repo; a classic token needs `repo`.

## Build

```
go build -o nightshift .
```

## Roadmap

- A Bubble Tea TUI for monitoring concurrent agents (tailing the per-issue logs).
- Optional multi-project mode (a registry over per-repo config).

## License

nightshift is free software, licensed under the **GNU General Public License
v3.0 or later** (GPL-3.0-or-later). See [LICENSE](LICENSE) for the full text.

This means you're free to use, study, share, and modify it — but if you
distribute a modified version, you must release your changes under the same
license. Forks stay open.
