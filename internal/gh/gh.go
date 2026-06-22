// Package gh wraps the GitHub CLI (`gh`) so nightshift can borrow the user's
// existing authentication instead of managing tokens itself. Phase one shells
// out to gh rather than depending on go-github; this can be swapped for the
// API later without changing callers.
package gh

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// run executes a gh subcommand and returns stdout, surfacing stderr on error.
func run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// AuthStatus returns an error if gh is not installed or not authenticated.
func AuthStatus(ctx context.Context) error {
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("the GitHub CLI (gh) is not installed or not on PATH")
	}
	_, err := run(ctx, "auth", "status")
	return err
}

// RepoSlug returns the "owner/name" of the repo in the current directory,
// which gh derives from the git remotes.
func RepoSlug(ctx context.Context) (string, error) {
	out, err := run(ctx, "repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// DefaultBranch returns the repository's default branch (e.g. main, develop).
func DefaultBranch(ctx context.Context) (string, error) {
	out, err := run(ctx, "repo", "view", "--json", "defaultBranchRef", "-q", ".defaultBranchRef.name")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Label is a GitHub issue label.
type Label struct {
	Name string `json:"name"`
}

// User is a GitHub account (assignee/author).
type User struct {
	Login string `json:"login"`
}

// Issue is the subset of a GitHub issue nightshift cares about.
type Issue struct {
	Number    int     `json:"number"`
	Title     string  `json:"title"`
	URL       string  `json:"url"`
	State     string  `json:"state"`
	Body      string  `json:"body"`
	Labels    []Label `json:"labels"`
	Assignees []User  `json:"assignees"`
}

// LabelNames returns the issue's label names joined for display.
func (i Issue) LabelNames() string {
	names := make([]string, 0, len(i.Labels))
	for _, l := range i.Labels {
		names = append(names, l.Name)
	}
	return strings.Join(names, ", ")
}

const issueFields = "number,title,url,state,body,labels,assignees"

// ListOptions filters which issues ListIssues returns. Empty fields are omitted
// from the gh invocation (i.e. no filter on that dimension).
type ListOptions struct {
	Assignee string // gh syntax, e.g. "@me" or a login; "" means any assignee
	Label    string
	State    string // open, closed, all
	Limit    int
}

// ListIssues returns issues matching opts in the current repository.
func ListIssues(ctx context.Context, opts ListOptions) ([]Issue, error) {
	args := []string{"issue", "list", "--json", issueFields}
	if opts.Assignee != "" {
		args = append(args, "--assignee", opts.Assignee)
	}
	if opts.Label != "" {
		args = append(args, "--label", opts.Label)
	}
	if opts.State != "" {
		args = append(args, "--state", opts.State)
	}
	if opts.Limit > 0 {
		args = append(args, "--limit", strconv.Itoa(opts.Limit))
	}
	out, err := run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var issues []Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parse issue list: %w", err)
	}
	return issues, nil
}

// GetIssue fetches a single issue by number.
func GetIssue(ctx context.Context, number int) (Issue, error) {
	out, err := run(ctx, "issue", "view", strconv.Itoa(number), "--json", issueFields)
	if err != nil {
		return Issue{}, err
	}
	var issue Issue
	if err := json.Unmarshal(out, &issue); err != nil {
		return Issue{}, fmt.Errorf("parse issue %d: %w", number, err)
	}
	return issue, nil
}
