# nightshift roadmap

nightshift is a **developer-invoked CLI**. This roadmap is about sharpening that
tool — distribution, feedback, observability, and reach — not turning it into an
unattended service. Each milestone maps to a release tag; the sequence is a
rough intent, not a contract.

For where the tool is today, see the [README](README.md). Items already tracked
as issues link to them.

## v0.3 — Release distribution

Make building the release matrix a single command. Releases stay **manual** for
now (tag, run the build, upload) — automation comes later (see v0.6).

- **`make dist` target** ([#10](https://github.com/birdbox/nightshift/issues/10)) —
  cross-compile `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64` with
  `CGO_ENABLED=0`, stamp the version via `-ldflags`, emit
  `dist/nightshift-<version>-<os>-<arch>` plus `dist/SHA256SUMS`, and add a
  cleanup target. Replaces the manual loop documented in the README.

*Outcome:* `make dist` produces all four binaries and `SHA256SUMS` in one step.

## v0.4 — Issue write-back & reporting

Close the loop on each issue nightshift works, instead of only opening a PR.

- **Write back to issues** ([#5](https://github.com/birdbox/nightshift/issues/5)) —
  comment the PR URL on success and the failure reason on error; optionally mark
  an issue in-progress while an agent works it. Strictly **opt-in** via a flag so
  dry runs and conservative users never mutate issues unexpectedly.

*Outcome:* a run leaves a clear trail on the issues themselves, not just in the
console and PRs.

## v0.5 — Live monitoring TUI

Replace the scrolling status lines with a real view of concurrent agents.

- **Terminal dashboard** — a TUI that tails the per-issue logs and shows each
  agent's status at a glance (running / done / failed), so concurrent runs are
  legible without grepping log files.

> **Dependency note.** CLAUDE.md is stdlib-first and discourages third-party
> modules. A TUI is the deliberate, documented exception: any dependency it
> needs stays **isolated to the TUI layer** (ideally behind a build tag) so the
> core stays dependency-light. This is a conscious trade-off, not drift.

*Outcome:* `nightshift --execute` on many issues is watchable in real time.

## v0.6 — Multi-project mode & release automation

Broaden reach and remove the last manual step from shipping.

- **Multi-project mode** — a registry over per-repo config so one invocation can
  sweep several repositories, rather than running once per checkout.
- **Release-on-tag CI** ([#11](https://github.com/birdbox/nightshift/issues/11)) —
  a GitHub Actions workflow that, on pushing a `v*` tag, builds the matrix via
  `make dist` and publishes the binaries to the GitHub Release. Depends on v0.3.

*Outcome:* nightshift runs across a fleet of repos, and pushing a tag ships a
release with no manual build.

## v1.0 — Stability & ergonomics

Harden the surface and smooth the rough edges before committing to stability.

- **Config file** — defaults for model, concurrency, labels, worktree-root, etc.,
  so common setups don't need a wall of flags. Flags still override.
- **API pagination** — lift the silent 100-item caps on issue and PR listing in
  `internal/github` so large repos aren't quietly truncated.
- **Agent-failure resilience** — bounded retries and clearer partial-failure
  reporting when an agent or a push/PR step fails mid-pool.
- **Hardened CLI surface** — a documented, stable flag/command set; revisit
  Windows support (the `stty`-based hidden token prompt is the current gap).

*Outcome:* a dependable 1.0 that behaves predictably on large repos and degrades
gracefully on failure.

## Out of scope (for now)

Recorded as deliberate non-goals so the name doesn't pull scope creep —
nightshift stays a tool you invoke, not a service that runs itself:

- **Unattended daemon / scheduled overnight runner.** No background process or
  cron-driven autonomy. You run nightshift; it does a batch and exits.
- **Notifications** (Slack, email, webhooks). Reporting lives on the issues and
  PRs (v0.4), not in external channels.

These may be revisited if the tool's use warrants it, but they are not on the
path to 1.0.
