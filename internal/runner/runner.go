// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Matthias Eder

// Package runner orchestrates execution of a single issue: it creates an
// isolated git worktree, runs Claude Code in it, then pushes the branch and
// opens a pull request via the GitHub API. Each issue's output is written to
// its own log so multiple issues can run concurrently without interleaving on
// the console.
package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/birdbox/nightshift/internal/git"
	"github.com/birdbox/nightshift/internal/github"
)

// Options configures issue execution.
type Options struct {
	Client       *github.Client // GitHub API client (PR creation)
	RepoDir      string         // the repository working directory (nightshift's CWD)
	Slug         string         // owner/name
	Base         string         // base branch to branch from and target the PR at
	Model        string         // claude model alias/name; empty uses claude's default
	WorktreeRoot string         // parent directory under which worktrees and logs live
	Keep         bool           // keep worktrees after running instead of removing them
	Stream       bool           // also tee output to stdout (used when running one at a time)

	// WriteBack opts into mutating the issue: a comment summarizing the outcome
	// (PR URL on success, failure reason on error) and, if InProgressLabel is
	// set, that label is applied while the agent works and removed afterward.
	WriteBack       bool
	InProgressLabel string
}

// Result reports the outcome of executing a single issue.
type Result struct {
	Issue   github.Issue
	Branch  string
	LogPath string
	PRURL   string // set when a pull request was opened
	Note    string // human-readable note when no PR was opened but no error occurred
	Elapsed time.Duration
	Err     error
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// branchSlug turns an issue title into a short, filesystem- and ref-safe slug.
func branchSlug(title string) string {
	s := nonSlug.ReplaceAllString(strings.ToLower(title), "-")
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	if s == "" {
		s = "issue"
	}
	return s
}

// Execute runs the full worktree + Claude + PR flow for a single issue. It never
// returns an error directly; the outcome (including any error) is reported in
// the Result so callers can run many issues concurrently and aggregate.
func Execute(ctx context.Context, iss github.Issue, opts Options) Result {
	start := time.Now()
	branch := fmt.Sprintf("nightshift/issue-%d-%s", iss.Number, branchSlug(iss.Title))
	worktreePath := filepath.Join(opts.WorktreeRoot, strings.ReplaceAll(branch, "/", "-"))
	logPath := filepath.Join(opts.WorktreeRoot, fmt.Sprintf("issue-%d.log", iss.Number))

	res := Result{Issue: iss, Branch: branch, LogPath: logPath}
	// out is the run's log sink; it stays io.Discard until the log file exists so
	// the finish closure (captured below) can always write to it safely.
	var out io.Writer = io.Discard
	finish := func(err error) Result {
		res.Err = err
		res.Elapsed = time.Since(start)
		if opts.WriteBack {
			commentOutcome(out, opts, iss, res)
		}
		return res
	}

	logFile, err := os.Create(logPath)
	if err != nil {
		return finish(fmt.Errorf("create log file: %w", err))
	}
	defer logFile.Close()

	out = logFile
	if opts.Stream {
		out = io.MultiWriter(logFile, os.Stdout)
	}

	fmt.Fprintf(out, "=== #%d %s\n", iss.Number, iss.Title)
	fmt.Fprintf(out, "    branch:   %s\n", branch)
	fmt.Fprintf(out, "    worktree: %s\n\n", worktreePath)

	if err := git.Fetch(ctx, opts.RepoDir, opts.Base); err != nil {
		return finish(logErr(out, fmt.Errorf("fetch origin/%s: %w", opts.Base, err)))
	}
	if err := git.AddWorktree(ctx, opts.RepoDir, worktreePath, branch, opts.Base); err != nil {
		return finish(logErr(out, fmt.Errorf("create worktree: %w", err)))
	}

	if !opts.Keep {
		defer func() {
			// Use a fresh context so cleanup still runs if ctx was canceled.
			if err := git.RemoveWorktree(context.Background(), opts.RepoDir, worktreePath); err != nil {
				fmt.Fprintf(out, "warning: could not remove worktree: %v\n", err)
			}
		}()
	}

	// Mark the issue in-progress while the agent works, and clear the marker when
	// done so it never lingers after the run. Best effort: a labeling failure must
	// not abort otherwise-good work.
	if opts.WriteBack && opts.InProgressLabel != "" {
		if err := opts.Client.AddLabels(ctx, iss.Number, opts.InProgressLabel); err != nil {
			fmt.Fprintf(out, "warning: could not add label %q to issue #%d: %v\n", opts.InProgressLabel, iss.Number, err)
		}
		defer func() {
			// Fresh context so the label is cleared even if ctx was canceled.
			rctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := opts.Client.RemoveLabel(rctx, iss.Number, opts.InProgressLabel); err != nil {
				fmt.Fprintf(out, "warning: could not remove label %q from issue #%d: %v\n", opts.InProgressLabel, iss.Number, err)
			}
		}()
	}

	if err := runClaude(ctx, worktreePath, opts, iss, branch, out); err != nil {
		return finish(logErr(out, fmt.Errorf("claude execution: %w", err)))
	}

	// The agent commits its work; nightshift pushes and opens the PR.
	n, err := git.CommitCount(ctx, worktreePath, "origin/"+opts.Base)
	if err != nil {
		return finish(logErr(out, fmt.Errorf("count commits: %w", err)))
	}
	if n == 0 {
		res.Note = "agent produced no commits; no PR opened"
		fmt.Fprintf(out, "\n%s\n", res.Note)
		return finish(nil)
	}

	fmt.Fprintf(out, "\nPushing %s and opening a pull request...\n", branch)
	if err := git.Push(ctx, worktreePath, branch); err != nil {
		return finish(logErr(out, fmt.Errorf("push branch: %w", err)))
	}

	body := fmt.Sprintf("Closes #%d\n\nOpened automatically by nightshift.", iss.Number)
	prURL, err := opts.Client.CreatePR(ctx, iss.Title, branch, opts.Base, body)
	if err != nil {
		return finish(logErr(out, fmt.Errorf("create pull request: %w", err)))
	}
	res.PRURL = prURL
	fmt.Fprintf(out, "Pull request: %s\n", prURL)
	return finish(nil)
}

func logErr(out io.Writer, err error) error {
	fmt.Fprintf(out, "\nERROR: %v\n", err)
	return err
}

// commentOutcome posts a comment on the issue summarizing how the run ended.
// It uses a fresh context so the comment lands even when the run's context was
// canceled, and only logs (never returns) its own failures — write-back is a
// courtesy that must not change the run's result.
func commentOutcome(out io.Writer, opts Options, iss github.Issue, res Result) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := opts.Client.CommentOnIssue(ctx, iss.Number, outcomeComment(res)); err != nil {
		fmt.Fprintf(out, "warning: could not comment on issue #%d: %v\n", iss.Number, err)
	}
}

// outcomeComment renders the issue comment body for a finished run: the PR URL
// on success, the failure reason on error, or the no-PR note otherwise.
func outcomeComment(res Result) string {
	switch {
	case res.Err != nil:
		return fmt.Sprintf("🌙 nightshift could not complete this issue:\n\n```\n%v\n```", res.Err)
	case res.PRURL != "":
		return fmt.Sprintf("🌙 nightshift opened a pull request: %s", res.PRURL)
	default:
		note := res.Note
		if note == "" {
			note = "no pull request was opened"
		}
		return fmt.Sprintf("🌙 nightshift ran but opened no pull request (%s).", note)
	}
}

func runClaude(ctx context.Context, worktreePath string, opts Options, iss github.Issue, branch string, out io.Writer) error {
	args := []string{"-p", buildPrompt(opts, iss, branch), "--dangerously-skip-permissions"}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = worktreePath
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}

func buildPrompt(opts Options, iss github.Issue, branch string) string {
	body := strings.TrimSpace(iss.Body)
	if body == "" {
		body = "(no description provided)"
	}
	return fmt.Sprintf(`You are working autonomously on a GitHub issue inside an isolated git worktree.

Repository: %s
Issue #%d: %s
Issue URL: %s
You are already on branch %q, which is based on origin/%s. Do not switch branches.

--- Issue body ---
%s
------------------

Do the following:
1. Read the repository's own conventions first — CLAUDE.md, AGENTS.md, CONTRIBUTING, and the build config (package.json scripts, Makefile, go.mod, etc.). Follow them. Do not assume a stack.
2. Explore the relevant code, then implement a focused, minimal change that fully resolves the issue. Do not touch unrelated files.
3. Run the repository's own verification (format, lint, type-check, tests) as defined by its tooling, and fix problems until everything passes.
4. Commit your work using the repository's commit convention (default to Conventional Commits if unclear). Do NOT push and do NOT open a pull request — nightshift pushes the branch and opens the PR for you.
5. As your final output, briefly summarize what you changed.

If you cannot complete the task, stop and explain what blocked you instead of committing a broken change.`,
		opts.Slug, iss.Number, iss.Title, iss.URL, branch, opts.Base, body)
}
