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
nightshift list            # show issues in a table (read-only)
nightshift list --state all --assignee ""

nightshift                 # dry run: issues assigned to you (@me), open
nightshift --label nightshift
nightshift 1234 1240       # specific issue numbers (bypass filters)

nightshift --execute       # create a worktree per issue and run Claude
nightshift --execute --yes # ...without the confirmation prompt
```

### `nightshift list`

A read-only, tabular view of issues — never creates worktrees or PRs. Takes the
same selection flags (`--assignee`, `--label`, `--state`, `--limit`) and accepts
explicit issue numbers:

```
birdbox/nightshift — state=open, assignee=@me

#   STATE  AGE  TITLE                                  LABELS
#4  open   2h   fix: validate --state with a clear error
#2  open   3d   feat: add --clean flag to prune old worktree logs
```

### Avoiding duplicate work

Before acting, nightshift fetches open pull requests and links them back to
issues — both by its own branch convention (`nightshift/issue-<n>-…`) and by
closing keywords (`Closes #<n>`) in PR bodies, so human-made PRs count too. Any
issue that already has an open PR is **skipped** (and shown with its PR in
`list` and dry-run output). Override with `--force` to run anyway.

### Execution flags

- `--execute` — actually run Claude (otherwise dry run).
- `--yes` — skip the confirmation prompt.
- `--force` — act on issues even if they already have an open PR.
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

These logs (and any leftover worktrees) accumulate under the worktree root and
are never removed automatically. Clear them with:

```
nightshift --clean
```

This deletes this repo's worktree root, prunes git's stale worktree entries, and
prints what it removed. Pass `--worktree-root <dir>` to clean a non-default
location.

## Requirements

- `git` (with push access to the repo's `origin`, e.g. an SSH key).
- `claude` (Claude Code CLI), authenticated.
- A GitHub token with access to the repo's issues and pull requests. nightshift
  talks to the GitHub REST API directly — no `gh` CLI required.

  A fine-grained token needs **Issues: read** and **Pull requests: read/write**
  (Metadata: read is implied) on the target repo; a classic token needs `repo`
  (or `public_repo` for a public repo). The branch is pushed over SSH, so the
  token does not need Contents access unless your `origin` uses HTTPS.

### Providing the token

nightshift looks for a token in this order:

1. The `--token` flag (one-off override).
2. `GITHUB_TOKEN`, then `GH_TOKEN` (handy for CI).
3. A saved token at `~/.config/nightshift/token` (mode `0600`).
4. An interactive prompt (input is hidden), which offers to save it for next time.

So the first interactive run just prompts you once and remembers it. If a saved
token later expires or is revoked, nightshift detects the rejection, explains,
re-prompts, and overwrites the stored token. Remove a saved token with:

```
nightshift --logout
```

## Building

A plain build produces a binary reporting version `dev`:

```sh
go build -o nightshift .
```

To stamp a real version, use the Makefile, which derives it from git
(`git describe --tags --always --dirty`, falling back to `dev`):

```sh
make build
./nightshift --version      # e.g. nightshift v0.2.0
```

The version is injected at link time via `-ldflags "-X main.version=<v>"`.
Override it explicitly if needed:

```sh
make build VERSION=1.2.3
# or directly:
go build -ldflags "-X main.version=1.2.3" .
```

Other Makefile targets: `make test` (runs `go test ./...`) and `make install`
(version-stamped `go install`).

## Cross-platform builds

nightshift is pure Go (standard library only, no cgo), so it cross-compiles to a
single static binary for any target using just `GOOS`/`GOARCH` — no C toolchain
required. Setting `CGO_ENABLED=0` guarantees a fully static binary.

Supported targets:

| OS    | Arch          | `GOOS`   | `GOARCH` |
| ----- | ------------- | -------- | -------- |
| Linux | x86-64        | `linux`  | `amd64`  |
| Linux | ARM64         | `linux`  | `arm64`  |
| macOS | Intel         | `darwin` | `amd64`  |
| macOS | Apple Silicon | `darwin` | `arm64`  |

Build a single target, e.g. Linux ARM64:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -ldflags "-X main.version=$(git describe --tags --always --dirty)" \
  -o dist/nightshift-linux-arm64 .
```

> **Windows** is not supported yet. It compiles (`GOOS=windows`), but nightshift
> shells out to `stty` for hidden token entry, which doesn't exist on Windows, so
> the token prompt would echo. Untested otherwise.

## Releases

A release is a git tag plus one binary per target. The build stamps the version
from the tag automatically.

1. Tag and push:

   ```sh
   git tag v0.2.0
   git push origin v0.2.0
   ```

2. Build the matrix:

   ```sh
   VERSION=$(git describe --tags --always --dirty)
   LDFLAGS="-X main.version=$VERSION"
   mkdir -p dist
   for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
     os=${target%/*}; arch=${target#*/}
     CGO_ENABLED=0 GOOS=$os GOARCH=$arch \
       go build -ldflags "$LDFLAGS" -o "dist/nightshift-$VERSION-$os-$arch" .
   done
   (cd dist && sha256sum nightshift-* > SHA256SUMS)
   ```

3. Upload everything in `dist/` (binaries + `SHA256SUMS`) to the GitHub release
   for the tag.

## Roadmap

Planned work is tracked by milestone in [ROADMAP.md](ROADMAP.md) — release
distribution, issue write-back, a monitoring TUI, multi-project mode, and the
road to 1.0.

## License

nightshift is free software, licensed under the **GNU General Public License
v3.0 or later** (GPL-3.0-or-later). See [LICENSE](LICENSE) for the full text.

This means you're free to use, study, share, and modify it — but if you
distribute a modified version, you must release your changes under the same
license. Forks stay open.
