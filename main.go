// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Matthias Eder

// Command nightshift is an autonomous ticket-execution harness. Run inside a
// git repository, it finds GitHub issues to work on and drives Claude Code in
// isolated git worktrees to implement them and open pull requests.
//
// Without --execute it performs a dry run, reporting which issues it would act
// on. With --execute it creates a worktree per issue and runs Claude in it.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/birdbox/nightshift/internal/github"
	"github.com/birdbox/nightshift/internal/runner"
	"github.com/birdbox/nightshift/internal/secret"
)

const version = "0.1.0-dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "nightshift: "+err.Error())
		os.Exit(1)
	}
}

// run dispatches subcommands; anything that isn't a known subcommand falls
// through to the default execute/dry-run behavior.
func run() error {
	if len(os.Args) > 1 && os.Args[1] == "list" {
		return cmdList(os.Args[2:])
	}
	return runExec()
}

func runExec() error {
	var (
		assignee     = flag.String("assignee", "@me", "filter issues by assignee (gh syntax: @me, a login, or empty for any)")
		label        = flag.String("label", "", "filter issues by label")
		state        = flag.String("state", "open", "issue state: open, closed, all")
		limit        = flag.Int("limit", 20, "max issues to consider")
		execute      = flag.Bool("execute", false, "actually run Claude on the selected issues (default is a dry run)")
		yes          = flag.Bool("yes", false, "skip the confirmation prompt before executing")
		model        = flag.String("model", "", "Claude model to use (alias or full name); empty uses claude's default")
		base         = flag.String("base", "", "base branch to branch from and target PRs at (default: repo default branch)")
		keep         = flag.Bool("keep", false, "keep worktrees after running instead of removing them")
		worktreeRoot = flag.String("worktree-root", "", "parent directory for worktrees (default: a temp dir)")
		concurrency  = flag.Int("concurrency", 3, "max issues to work on at once")
		force        = flag.Bool("force", false, "act on issues even if they already have an open PR")
		token        = flag.String("token", "", "GitHub token to use for this run (overrides env and saved token)")
		logout       = flag.Bool("logout", false, "delete the saved GitHub token and exit")
		showVersion  = flag.Bool("version", false, "print version and exit")
	)
	flag.Usage = usage
	flag.Parse()

	// Let flags and issue numbers appear in any order. The stdlib flag package
	// stops at the first positional arg, so collect positionals and keep parsing
	// the remainder after each one.
	var positional []string
	for rest := flag.Args(); len(rest) > 0; rest = flag.Args() {
		positional = append(positional, rest[0])
		flag.CommandLine.Parse(rest[1:])
	}

	if *showVersion {
		fmt.Println("nightshift " + version)
		return nil
	}

	if *logout {
		if err := secret.Delete(); err != nil {
			return fmt.Errorf("remove saved token: %w", err)
		}
		p, _ := secret.Path()
		fmt.Printf("Removed any saved token (%s).\n", p)
		return nil
	}

	// Cancel running agents (and trigger worktree cleanup) on Ctrl+C.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	repoDir, err := os.Getwd()
	if err != nil {
		return err
	}

	client, err := authenticate(ctx, repoDir, *token)
	if err != nil {
		return err
	}

	issues, selection, err := selectIssues(ctx, client, positional, *assignee, *label, *state, *limit)
	if err != nil {
		return err
	}

	prByIssue, err := openPRsByIssue(ctx, client)
	if err != nil {
		return err
	}

	fmt.Printf("Repository: %s\n", client.Slug())
	fmt.Printf("Selection:  %s\n", selection)
	fmt.Printf("Found %d issue(s).\n", len(issues))
	printIssues(issues, prByIssue)

	if len(issues) == 0 {
		fmt.Println("\nNothing to do.")
		return nil
	}

	// Skip issues that already have an open PR, unless --force.
	actionable := issues
	if !*force {
		var act, skip []github.Issue
		for _, iss := range issues {
			if _, ok := prByIssue[iss.Number]; ok {
				skip = append(skip, iss)
			} else {
				act = append(act, iss)
			}
		}
		actionable = act
		if len(skip) > 0 {
			fmt.Printf("\nSkipping %d issue(s) with an open PR (use --force to run anyway):\n", len(skip))
			for _, iss := range skip {
				fmt.Printf("  #%d → %s\n", iss.Number, prByIssue[iss.Number].URL)
			}
		}
	}

	if !*execute {
		fmt.Println("\nDry run. Re-run with --execute to launch Claude on these issues.")
		return nil
	}

	if len(actionable) == 0 {
		fmt.Println("\nNothing to do.")
		return nil
	}

	return execIssues(ctx, client, repoDir, actionable, execConfig{
		base:         *base,
		model:        *model,
		worktreeRoot: *worktreeRoot,
		keep:         *keep,
		yes:          *yes,
		concurrency:  *concurrency,
	})
}

type execConfig struct {
	base         string
	model        string
	worktreeRoot string
	keep         bool
	yes          bool
	concurrency  int
}

// execIssues resolves execution settings, confirms, and runs the issues through
// a bounded worker pool.
func execIssues(ctx context.Context, client *github.Client, repoDir string, issues []github.Issue, cfg execConfig) error {
	slug := client.Slug()

	base := cfg.base
	if base == "" {
		var err error
		base, err = client.DefaultBranch(ctx)
		if err != nil {
			return fmt.Errorf("could not determine the default branch (set --base): %w", err)
		}
	}

	worktreeRoot := cfg.worktreeRoot
	if worktreeRoot == "" {
		name := slug
		if i := strings.LastIndex(slug, "/"); i >= 0 {
			name = slug[i+1:]
		}
		worktreeRoot = filepath.Join(os.TempDir(), "nightshift", name)
	}
	if err := os.MkdirAll(worktreeRoot, 0o755); err != nil {
		return fmt.Errorf("create worktree root: %w", err)
	}

	concurrency := cfg.concurrency
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > len(issues) {
		concurrency = len(issues)
	}

	fmt.Printf("\nAbout to launch Claude on %d issue(s) in %s, branching from %q, %d at a time.\n",
		len(issues), slug, base, concurrency)
	if !cfg.yes && !confirm("Proceed?") {
		fmt.Println("Aborted.")
		return nil
	}

	opts := runner.Options{
		Client:       client,
		RepoDir:      repoDir,
		Slug:         slug,
		Base:         base,
		Model:        cfg.model,
		WorktreeRoot: worktreeRoot,
		Keep:         cfg.keep,
		// With one worker, tee live output to the console; otherwise logs only.
		Stream: concurrency == 1,
	}

	return runPool(ctx, issues, opts, concurrency)
}

// runPool runs issues through a bounded pool of workers, printing one status
// line per lifecycle event and a final summary.
func runPool(ctx context.Context, issues []github.Issue, opts runner.Options, concurrency int) error {
	var (
		mu        sync.Mutex // serializes console writes and the failure counter
		failures  int
		wg        sync.WaitGroup
		semaphore = make(chan struct{}, concurrency)
	)

	report := func(format string, args ...any) {
		mu.Lock()
		fmt.Printf(format+"\n", args...)
		mu.Unlock()
	}

	for _, iss := range issues {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		semaphore <- struct{}{}
		go func(iss github.Issue) {
			defer wg.Done()
			defer func() { <-semaphore }()

			report("▶ #%d started — %s", iss.Number, iss.Title)
			res := runner.Execute(ctx, iss, opts)

			mu.Lock()
			switch {
			case res.Err != nil:
				failures++
				fmt.Printf("✗ #%d failed in %s: %v (log: %s)\n",
					iss.Number, res.Elapsed.Round(time.Second), res.Err, res.LogPath)
			case res.PRURL != "":
				fmt.Printf("✓ #%d done in %s → %s\n",
					iss.Number, res.Elapsed.Round(time.Second), res.PRURL)
			default:
				fmt.Printf("✓ #%d done in %s (%s; log: %s)\n",
					iss.Number, res.Elapsed.Round(time.Second), res.Note, res.LogPath)
			}
			mu.Unlock()
		}(iss)
	}

	wg.Wait()

	if ctx.Err() != nil {
		fmt.Fprintln(os.Stderr, "\ninterrupted.")
	}
	fmt.Printf("\nDone. %d succeeded, %d failed.\n", len(issues)-failures, failures)
	if failures > 0 {
		return fmt.Errorf("%d issue(s) failed", failures)
	}
	return nil
}

// authenticate resolves a GitHub token and returns a verified client. It tries,
// in order: the --token flag, GITHUB_TOKEN/GH_TOKEN, the saved token file, and
// finally an interactive prompt. If a saved or entered token is rejected (e.g.
// expired or revoked), it explains and re-prompts, then overwrites the stored
// token with the working one.
func authenticate(ctx context.Context, repoDir, tokenFlag string) (*github.Client, error) {
	if tokenFlag != "" {
		return newClientChecked(ctx, repoDir, tokenFlag, "the --token flag")
	}
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		return newClientChecked(ctx, repoDir, t, "the GITHUB_TOKEN environment variable")
	}
	if t := os.Getenv("GH_TOKEN"); t != "" {
		return newClientChecked(ctx, repoDir, t, "the GH_TOKEN environment variable")
	}

	saved, err := secret.Load()
	if err != nil {
		return nil, fmt.Errorf("read saved token: %w", err)
	}
	token := saved
	fromFile := saved != ""
	prompted := false

	for {
		if token == "" {
			if !secret.Interactive() {
				p, _ := secret.Path()
				return nil, fmt.Errorf("no GitHub token found; set GITHUB_TOKEN or save one at %s (run interactively to be prompted)", p)
			}
			fmt.Println("nightshift needs a GitHub token (issues: read, pull requests: write).")
			t, err := secret.PromptToken("Token: ")
			if err != nil {
				return nil, fmt.Errorf("read token: %w", err)
			}
			if t == "" {
				return nil, fmt.Errorf("no token entered")
			}
			token, fromFile, prompted = t, false, true
		}

		client, err := github.NewClient(ctx, repoDir, token)
		if err == nil {
			if prompted {
				maybeSave(token)
			}
			return client, nil
		}

		// Only a credential rejection is worth re-prompting for; other errors
		// (network, not a GitHub repo, missing scope) won't be fixed by retrying.
		if errors.Is(err, github.ErrBadCredentials) && secret.Interactive() {
			if fromFile {
				fmt.Println("Your saved GitHub token was rejected (expired or revoked). Let's set a new one.")
			} else {
				fmt.Println("That token was rejected. Try again.")
			}
			token = ""
			continue
		}
		return nil, err
	}
}

// newClientChecked builds a client and, on a credential rejection, returns a
// message naming where the bad token came from instead of re-prompting (the
// source is non-interactive, so the user must fix it themselves).
func newClientChecked(ctx context.Context, repoDir, token, source string) (*github.Client, error) {
	client, err := github.NewClient(ctx, repoDir, token)
	if err != nil {
		if errors.Is(err, github.ErrBadCredentials) {
			return nil, fmt.Errorf("the GitHub token in %s is invalid or expired — update or unset it", source)
		}
		return nil, err
	}
	return client, nil
}

// maybeSave offers to persist a freshly entered, working token.
func maybeSave(token string) {
	p, _ := secret.Path()
	if !confirmDefault(fmt.Sprintf("Save this token to %s for next time?", p), true) {
		return
	}
	if err := secret.Save(token); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not save token: %v\n", err)
		return
	}
	fmt.Println("Saved. nightshift will reuse it (remove it with `nightshift --logout`).")
}

var (
	// branchIssueRE matches nightshift's own branch naming, e.g. nightshift/issue-12-foo.
	branchIssueRE = regexp.MustCompile(`^nightshift/issue-(\d+)-`)
	// closesRE matches GitHub's closing keywords, e.g. "Closes #12", "fixes #3".
	closesRE = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\s+#(\d+)`)
)

// openPRsByIssue maps issue numbers to an open PR addressing them, so nightshift
// can avoid acting on issues that already have work in flight.
func openPRsByIssue(ctx context.Context, client *github.Client) (map[int]github.PullRequest, error) {
	prs, err := client.ListOpenPRs(ctx)
	if err != nil {
		return nil, err
	}
	return mapPRsToIssues(prs), nil
}

// mapPRsToIssues links each open PR to the issue(s) it addresses, via nightshift's
// branch convention and any closing keyword in the PR body.
func mapPRsToIssues(prs []github.PullRequest) map[int]github.PullRequest {
	m := make(map[int]github.PullRequest)
	for _, pr := range prs {
		add := func(n int) {
			if _, ok := m[n]; !ok {
				m[n] = pr
			}
		}
		if mt := branchIssueRE.FindStringSubmatch(pr.Head.Ref); mt != nil {
			if n, err := strconv.Atoi(mt[1]); err == nil {
				add(n)
			}
		}
		for _, mt := range closesRE.FindAllStringSubmatch(pr.Body, -1) {
			if n, err := strconv.Atoi(mt[1]); err == nil {
				add(n)
			}
		}
	}
	return m
}

// selectIssues resolves the issues to act on. Explicit issue numbers as
// positional args bypass the filters; otherwise the filters apply.
func selectIssues(ctx context.Context, client *github.Client, args []string, assignee, label, state string, limit int) ([]github.Issue, string, error) {
	if len(args) > 0 {
		var issues []github.Issue
		for _, a := range args {
			n, err := strconv.Atoi(strings.TrimPrefix(a, "#"))
			if err != nil {
				return nil, "", fmt.Errorf("invalid issue number %q", a)
			}
			iss, err := client.GetIssue(ctx, n)
			if err != nil {
				return nil, "", err
			}
			if iss.IsPullRequest() {
				return nil, "", fmt.Errorf("#%d is a pull request, not an issue", n)
			}
			issues = append(issues, iss)
		}
		return issues, "explicit issue numbers: " + strings.Join(args, ", "), nil
	}

	issues, err := client.ListIssues(ctx, github.ListOptions{
		Assignee: assignee,
		Label:    label,
		State:    state,
		Limit:    limit,
	})
	if err != nil {
		return nil, "", err
	}

	parts := []string{"state=" + state}
	if assignee != "" {
		parts = append(parts, "assignee="+assignee)
	}
	if label != "" {
		parts = append(parts, "label="+label)
	}
	return issues, strings.Join(parts, ", "), nil
}

// cmdList implements `nightshift list`: a read-only, tabular view of issues.
func cmdList(argv []string) error {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	assignee := fs.String("assignee", "@me", "filter by assignee (@me, a login, or empty for any)")
	label := fs.String("label", "", "filter by label")
	state := fs.String("state", "open", "issue state: open, closed, all")
	limit := fs.Int("limit", 20, "max issues to show")
	token := fs.String("token", "", "GitHub token to use (overrides env and saved token)")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "nightshift list — show issues without running anything\n\n"+
			"Usage:\n  nightshift list [flags] [issue numbers...]\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(argv); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	repoDir, err := os.Getwd()
	if err != nil {
		return err
	}
	client, err := authenticate(ctx, repoDir, *token)
	if err != nil {
		return err
	}

	issues, selection, err := selectIssues(ctx, client, fs.Args(), *assignee, *label, *state, *limit)
	if err != nil {
		return err
	}
	prByIssue, err := openPRsByIssue(ctx, client)
	if err != nil {
		return err
	}
	printIssueList(client.Slug(), selection, issues, prByIssue)
	return nil
}

// printIssueList renders issues as an aligned table, marking ones with an open PR.
func printIssueList(slug, selection string, issues []github.Issue, prs map[int]github.PullRequest) {
	fmt.Printf("%s — %s\n\n", slug, selection)
	if len(issues) == 0 {
		fmt.Println("No matching issues.")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "#\tSTATE\tPR\tAGE\tTITLE\tLABELS")
	for _, iss := range issues {
		pr := "-"
		if p, ok := prs[iss.Number]; ok {
			pr = fmt.Sprintf("#%d", p.Number)
		}
		fmt.Fprintf(w, "#%d\t%s\t%s\t%s\t%s\t%s\n",
			iss.Number, iss.State, pr, humanizeAge(iss.CreatedAt), truncate(iss.Title, 60), iss.LabelNames())
	}
	w.Flush()
	fmt.Printf("\n%d issue(s).\n", len(issues))
}

// humanizeAge renders how long ago t was, compactly (e.g. "3d", "5h").
func humanizeAge(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	switch d := time.Since(t); {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// truncate shortens s to at most n runes, adding an ellipsis when cut.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func printIssues(issues []github.Issue, prs map[int]github.PullRequest) {
	for _, iss := range issues {
		fmt.Printf("\n  #%-5d %s\n", iss.Number, iss.Title)
		if names := iss.LabelNames(); names != "" {
			fmt.Printf("         labels: %s\n", names)
		}
		if pr, ok := prs[iss.Number]; ok {
			fmt.Printf("         open PR: %s\n", pr.URL)
		}
		fmt.Printf("         %s\n", iss.URL)
	}
}

func confirm(prompt string) bool { return confirmDefault(prompt, false) }

func confirmDefault(prompt string, def bool) bool {
	hint := "[y/N]"
	if def {
		hint = "[Y/n]"
	}
	fmt.Printf("%s %s ", prompt, hint)
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return def
	}
	answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
	if answer == "" {
		return def
	}
	return answer == "y" || answer == "yes"
}

func usage() {
	fmt.Fprintf(os.Stderr, `nightshift %s — autonomous ticket-execution harness

Usage:
  nightshift [flags] [issue numbers...]   select issues and (with --execute) run agents
  nightshift list [flags]                 show issues in a table, run nothing

Run inside a git repository. With no issue numbers, nightshift selects issues
using the filter flags. With issue numbers, it acts on exactly those. Without
--execute it only reports what it would do.

Flags:
`, version)
	flag.PrintDefaults()
}
