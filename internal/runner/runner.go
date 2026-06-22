// Package runner orchestrates execution of a single issue: it creates an
// isolated git worktree, runs Claude Code inside it, and tears the worktree
// down afterward.
package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/birdbox/nightshift/internal/gh"
	"github.com/birdbox/nightshift/internal/git"
)

// Options configures issue execution.
type Options struct {
	RepoDir      string // the repository working directory (nightshift's CWD)
	Slug         string // owner/name
	Base         string // base branch to branch from and target the PR at
	Model        string // claude model alias/name; empty uses claude's default
	WorktreeRoot string // parent directory under which worktrees are created
	Keep         bool   // keep worktrees after running instead of removing them
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

// Execute runs the full worktree + Claude flow for a single issue.
func Execute(ctx context.Context, iss gh.Issue, opts Options) error {
	branch := fmt.Sprintf("nightshift/issue-%d-%s", iss.Number, branchSlug(iss.Title))
	worktreePath := filepath.Join(opts.WorktreeRoot, strings.ReplaceAll(branch, "/", "-"))

	fmt.Printf("\n=== #%d %s\n", iss.Number, iss.Title)
	fmt.Printf("    branch:   %s\n", branch)
	fmt.Printf("    worktree: %s\n", worktreePath)

	if err := git.Fetch(ctx, opts.RepoDir, opts.Base); err != nil {
		return fmt.Errorf("fetch origin/%s: %w", opts.Base, err)
	}
	if err := git.AddWorktree(ctx, opts.RepoDir, worktreePath, branch, opts.Base); err != nil {
		return fmt.Errorf("create worktree: %w", err)
	}

	if !opts.Keep {
		defer func() {
			// Use a fresh context so cleanup still runs if ctx was canceled.
			if err := git.RemoveWorktree(context.Background(), opts.RepoDir, worktreePath); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not remove worktree %s: %v\n", worktreePath, err)
			}
		}()
	}

	if err := runClaude(ctx, worktreePath, opts, iss, branch); err != nil {
		return fmt.Errorf("claude execution: %w", err)
	}
	return nil
}

func runClaude(ctx context.Context, worktreePath string, opts Options, iss gh.Issue, branch string) error {
	args := []string{"-p", buildPrompt(opts, iss, branch), "--dangerously-skip-permissions"}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = worktreePath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func buildPrompt(opts Options, iss gh.Issue, branch string) string {
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
4. Commit using the repository's commit convention (default to Conventional Commits if unclear).
5. Push the branch to origin and open a pull request targeting %q. Include "Closes #%d" in the PR body so the issue links and closes on merge.
6. As your final output, print the URL of the pull request you opened.

If you cannot complete the task, stop and explain what blocked you instead of opening a partial PR.`,
		opts.Slug, iss.Number, iss.Title, iss.URL, branch, opts.Base, body, opts.Base, iss.Number)
}
