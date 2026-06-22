// Package git wraps the git operations nightshift needs to run agents in
// isolated worktrees.
package git

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
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

// AddWorktree creates a worktree at path on a new branch based on origin/base.
func AddWorktree(ctx context.Context, repoDir, path, branch, base string) error {
	_, err := run(ctx, repoDir, "worktree", "add", "-b", branch, path, "origin/"+base)
	return err
}

// RemoveWorktree force-removes the worktree at path (the branch is preserved).
func RemoveWorktree(ctx context.Context, repoDir, path string) error {
	_, err := run(ctx, repoDir, "worktree", "remove", path, "--force")
	return err
}
