// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Matthias Eder

// Package github talks to the GitHub REST API directly over net/http so
// nightshift needs no external CLI at runtime. Authentication uses a token from
// the GITHUB_TOKEN (or GH_TOKEN) environment variable. The repository is
// inferred from the origin remote of the working directory.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/birdbox/nightshift/internal/git"
)

const apiBase = "https://api.github.com"

// Client is an authenticated GitHub REST client scoped to one repository.
type Client struct {
	http  *http.Client
	token string
	owner string
	repo  string
	login string // authenticated user's login, used to resolve "@me"
}

// NewClient builds a client for the repo whose origin remote lives in repoDir,
// authenticating with GITHUB_TOKEN/GH_TOKEN and verifying the token works.
func NewClient(ctx context.Context, repoDir string) (*Client, error) {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		token = os.Getenv("GH_TOKEN")
	}
	if token == "" {
		return nil, fmt.Errorf("no GitHub token found; set GITHUB_TOKEN to a personal access token with repo and issues access")
	}

	remote, err := git.RemoteURL(ctx, repoDir, "origin")
	if err != nil {
		return nil, fmt.Errorf("read origin remote: %w", err)
	}
	owner, repo, err := parseSlug(remote)
	if err != nil {
		return nil, err
	}

	c := &Client{
		http:  &http.Client{Timeout: 30 * time.Second},
		token: token,
		owner: owner,
		repo:  repo,
	}

	login, err := c.currentLogin(ctx)
	if err != nil {
		return nil, fmt.Errorf("validate GitHub token: %w", err)
	}
	c.login = login
	return c, nil
}

// Slug returns the "owner/name" of the client's repository.
func (c *Client) Slug() string { return c.owner + "/" + c.repo }

// parseSlug extracts owner/repo from an SSH or HTTPS GitHub remote URL.
func parseSlug(remote string) (owner, repo string, err error) {
	s := strings.TrimSpace(remote)
	switch {
	case strings.HasPrefix(s, "git@"):
		// scp-style: git@github.com:owner/repo.git
		at := strings.Index(s, "@")
		colon := strings.Index(s, ":")
		if colon < 0 {
			return "", "", fmt.Errorf("unrecognized remote URL %q", remote)
		}
		if host := s[at+1 : colon]; host != "github.com" {
			return "", "", fmt.Errorf("only github.com is supported (remote host %q)", host)
		}
		s = s[colon+1:]
	case strings.HasPrefix(s, "ssh://"), strings.HasPrefix(s, "https://"), strings.HasPrefix(s, "http://"):
		u, err := url.Parse(s)
		if err != nil {
			return "", "", fmt.Errorf("parse remote URL %q: %w", remote, err)
		}
		if u.Hostname() != "github.com" {
			return "", "", fmt.Errorf("only github.com is supported (remote host %q)", u.Hostname())
		}
		s = strings.TrimPrefix(u.Path, "/")
	default:
		return "", "", fmt.Errorf("unrecognized remote URL %q", remote)
	}

	s = strings.TrimSuffix(s, ".git")
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("could not parse owner/repo from remote %q", remote)
	}
	return parts[0], parts[1], nil
}

// do performs an API request, JSON-encoding body (if non-nil) and decoding the
// response into out (if non-nil). Non-2xx responses become errors.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, apiBase+path, reqBody)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GitHub API %s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(data)))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode %s response: %w", path, err)
		}
	}
	return nil
}

func (c *Client) currentLogin(ctx context.Context) (string, error) {
	var u struct {
		Login string `json:"login"`
	}
	if err := c.do(ctx, http.MethodGet, "/user", nil, &u); err != nil {
		return "", err
	}
	return u.Login, nil
}

// Label is a GitHub issue label.
type Label struct {
	Name string `json:"name"`
}

// User is a GitHub account.
type User struct {
	Login string `json:"login"`
}

// Issue is the subset of a GitHub issue nightshift cares about.
type Issue struct {
	Number    int     `json:"number"`
	Title     string  `json:"title"`
	URL       string  `json:"html_url"`
	State     string  `json:"state"`
	Body      string  `json:"body"`
	Labels    []Label `json:"labels"`
	Assignees []User  `json:"assignees"`
	// PullRequest is set by the API when this "issue" is actually a PR.
	PullRequest *struct{} `json:"pull_request,omitempty"`
}

// IsPullRequest reports whether the API returned a pull request rather than a
// plain issue (the /issues endpoint returns both).
func (i Issue) IsPullRequest() bool { return i.PullRequest != nil }

// LabelNames returns the issue's label names joined for display.
func (i Issue) LabelNames() string {
	names := make([]string, 0, len(i.Labels))
	for _, l := range i.Labels {
		names = append(names, l.Name)
	}
	return strings.Join(names, ", ")
}

// DefaultBranch returns the repository's default branch (e.g. main, develop).
func (c *Client) DefaultBranch(ctx context.Context) (string, error) {
	var r struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s", c.owner, c.repo), nil, &r); err != nil {
		return "", err
	}
	return r.DefaultBranch, nil
}

// ListOptions filters which issues ListIssues returns. Empty fields impose no
// filter on that dimension.
type ListOptions struct {
	Assignee string // "@me", a login, "*"/"none", or "" for any
	Label    string
	State    string // open, closed, all
	Limit    int
}

// ListIssues returns issues matching opts in the client's repository.
func (c *Client) ListIssues(ctx context.Context, opts ListOptions) ([]Issue, error) {
	q := url.Values{}
	if opts.State != "" {
		q.Set("state", opts.State)
	}
	if opts.Label != "" {
		q.Set("labels", opts.Label)
	}
	switch opts.Assignee {
	case "":
		// no assignee filter
	case "@me":
		q.Set("assignee", c.login)
	default:
		q.Set("assignee", opts.Assignee)
	}
	per := opts.Limit
	if per <= 0 || per > 100 {
		per = 100
	}
	q.Set("per_page", strconv.Itoa(per))

	var issues []Issue
	path := fmt.Sprintf("/repos/%s/%s/issues?%s", c.owner, c.repo, q.Encode())
	if err := c.do(ctx, http.MethodGet, path, nil, &issues); err != nil {
		return nil, err
	}

	// The /issues endpoint also returns pull requests; drop them and cap to Limit.
	out := issues[:0]
	for _, iss := range issues {
		if iss.IsPullRequest() {
			continue
		}
		out = append(out, iss)
		if opts.Limit > 0 && len(out) >= opts.Limit {
			break
		}
	}
	return out, nil
}

// GetIssue fetches a single issue by number.
func (c *Client) GetIssue(ctx context.Context, number int) (Issue, error) {
	var iss Issue
	path := fmt.Sprintf("/repos/%s/%s/issues/%d", c.owner, c.repo, number)
	if err := c.do(ctx, http.MethodGet, path, nil, &iss); err != nil {
		return Issue{}, err
	}
	return iss, nil
}

// CreatePR opens a pull request and returns its HTML URL.
func (c *Client) CreatePR(ctx context.Context, title, head, base, body string) (string, error) {
	reqBody := map[string]any{
		"title": title,
		"head":  head,
		"base":  base,
		"body":  body,
	}
	var resp struct {
		HTMLURL string `json:"html_url"`
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls", c.owner, c.repo)
	if err := c.do(ctx, http.MethodPost, path, reqBody, &resp); err != nil {
		return "", err
	}
	return resp.HTMLURL, nil
}
