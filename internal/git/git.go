// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Matthias Eder

// Package git wraps the git operations nightshift needs to run agents in
// isolated worktrees.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

func run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// Fetch updates the given branch from origin so worktrees branch off fresh state.
func Fetch(ctx context.Context, repoDir, branch string) error {
	_, err := run(ctx, repoDir, "fetch", "origin", branch)
	return err
}

// AddWorktree creates a worktree at path on the branch based on origin/base. If
// the branch already exists (a leftover from a prior run — RemoveWorktree keeps
// branches), it is reset to origin/base; nightshift owns these per-issue
// branches and each run starts from a fresh base. Callers should log the reuse.
//
// Hooks are disabled for this step (core.hooksPath points at a nonexistent
// path) so a project's post-checkout hook can't abort worktree creation. Such
// hooks are written for interactive dev setup — e.g. seeding a .env — and their
// non-zero exit propagates through "git worktree add" even when the worktree
// was created fine. nightshift's worktrees are throwaway and isolated, so we
// skip the hooks rather than let them fail the run.
func AddWorktree(ctx context.Context, repoDir, path, branch, base string) error {
	// -B (vs -b) creates or resets the branch, so a re-run reuses the leftover
	// branch instead of failing with "a branch named '...' already exists".
	_, err := run(ctx, repoDir, "-c", "core.hooksPath=/dev/null", "worktree", "add", "-B", branch, path, "origin/"+base)
	return err
}

// BranchExists reports whether a local branch of the given name exists.
func BranchExists(ctx context.Context, repoDir, branch string) (bool, error) {
	_, err := run(ctx, repoDir, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err == nil {
		return true, nil
	}
	// show-ref exits 1 when the ref is simply absent; treat only that as "no".
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// RemoveWorktree force-removes the worktree at path (the branch is preserved).
func RemoveWorktree(ctx context.Context, repoDir, path string) error {
	_, err := run(ctx, repoDir, "worktree", "remove", path, "--force")
	return err
}

// PruneWorktrees clears git's administrative entries for worktrees whose
// working directories no longer exist (e.g. after their root was deleted).
func PruneWorktrees(ctx context.Context, repoDir string) error {
	_, err := run(ctx, repoDir, "worktree", "prune")
	return err
}

// RemoteURL returns the URL configured for the named remote (e.g. "origin").
func RemoteURL(ctx context.Context, repoDir, remote string) (string, error) {
	out, err := run(ctx, repoDir, "remote", "get-url", remote)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// CommitCount returns how many commits HEAD has that ref does not.
func CommitCount(ctx context.Context, dir, ref string) (int, error) {
	out, err := run(ctx, dir, "rev-list", "--count", ref+"..HEAD")
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("parse commit count: %w", err)
	}
	return n, nil
}

// Push pushes branch to origin from the given directory, setting upstream.
func Push(ctx context.Context, dir, branch string) error {
	_, err := run(ctx, dir, "push", "-u", "origin", branch)
	return err
}
