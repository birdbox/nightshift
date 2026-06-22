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
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/birdbox/nightshift/internal/gh"
	"github.com/birdbox/nightshift/internal/runner"
)

const version = "0.1.0-dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "nightshift: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
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
		showVersion  = flag.Bool("version", false, "print version and exit")
	)
	flag.Usage = usage
	flag.Parse()

	if *showVersion {
		fmt.Println("nightshift " + version)
		return nil
	}

	// Cancel running agents (and trigger worktree cleanup) on Ctrl+C.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := gh.AuthStatus(ctx); err != nil {
		return fmt.Errorf("GitHub CLI not ready (try `gh auth login`): %w", err)
	}

	slug, err := gh.RepoSlug(ctx)
	if err != nil {
		return fmt.Errorf("could not determine a GitHub repo from the current directory: %w", err)
	}

	issues, selection, err := selectIssues(ctx, flag.Args(), *assignee, *label, *state, *limit)
	if err != nil {
		return err
	}

	fmt.Printf("Repository: %s\n", slug)
	fmt.Printf("Selection:  %s\n", selection)
	fmt.Printf("Found %d issue(s).\n", len(issues))
	printIssues(issues)

	if len(issues) == 0 {
		fmt.Println("\nNothing to do.")
		return nil
	}

	if !*execute {
		fmt.Println("\nDry run. Re-run with --execute to launch Claude on these issues.")
		return nil
	}

	return execIssues(ctx, issues, execConfig{
		slug:         slug,
		base:         *base,
		model:        *model,
		worktreeRoot: *worktreeRoot,
		keep:         *keep,
		yes:          *yes,
		concurrency:  *concurrency,
	})
}

type execConfig struct {
	slug         string
	base         string
	model        string
	worktreeRoot string
	keep         bool
	yes          bool
	concurrency  int
}

// execIssues resolves execution settings, confirms, and runs the issues through
// a bounded worker pool.
func execIssues(ctx context.Context, issues []gh.Issue, cfg execConfig) error {
	repoDir, err := os.Getwd()
	if err != nil {
		return err
	}

	base := cfg.base
	if base == "" {
		base, err = gh.DefaultBranch(ctx)
		if err != nil {
			return fmt.Errorf("could not determine the default branch (set --base): %w", err)
		}
	}

	worktreeRoot := cfg.worktreeRoot
	if worktreeRoot == "" {
		name := cfg.slug
		if i := strings.LastIndex(cfg.slug, "/"); i >= 0 {
			name = cfg.slug[i+1:]
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
		len(issues), cfg.slug, base, concurrency)
	if !cfg.yes && !confirm("Proceed?") {
		fmt.Println("Aborted.")
		return nil
	}

	opts := runner.Options{
		RepoDir:      repoDir,
		Slug:         cfg.slug,
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
func runPool(ctx context.Context, issues []gh.Issue, opts runner.Options, concurrency int) error {
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
		go func(iss gh.Issue) {
			defer wg.Done()
			defer func() { <-semaphore }()

			report("▶ #%d started — %s", iss.Number, iss.Title)
			res := runner.Execute(ctx, iss, opts)

			mu.Lock()
			if res.Err != nil {
				failures++
				fmt.Printf("✗ #%d failed in %s: %v (log: %s)\n",
					iss.Number, res.Elapsed.Round(time.Second), res.Err, res.LogPath)
			} else {
				fmt.Printf("✓ #%d done in %s (log: %s)\n",
					iss.Number, res.Elapsed.Round(time.Second), res.LogPath)
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

// selectIssues resolves the issues to act on. Explicit issue numbers as
// positional args bypass the filters; otherwise the filters apply.
func selectIssues(ctx context.Context, args []string, assignee, label, state string, limit int) ([]gh.Issue, string, error) {
	if len(args) > 0 {
		var issues []gh.Issue
		for _, a := range args {
			n, err := strconv.Atoi(strings.TrimPrefix(a, "#"))
			if err != nil {
				return nil, "", fmt.Errorf("invalid issue number %q", a)
			}
			iss, err := gh.GetIssue(ctx, n)
			if err != nil {
				return nil, "", err
			}
			issues = append(issues, iss)
		}
		return issues, "explicit issue numbers: " + strings.Join(args, ", "), nil
	}

	issues, err := gh.ListIssues(ctx, gh.ListOptions{
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

func printIssues(issues []gh.Issue) {
	for _, iss := range issues {
		fmt.Printf("\n  #%-5d %s\n", iss.Number, iss.Title)
		if names := iss.LabelNames(); names != "" {
			fmt.Printf("         labels: %s\n", names)
		}
		fmt.Printf("         %s\n", iss.URL)
	}
}

func confirm(prompt string) bool {
	fmt.Printf("%s [y/N] ", prompt)
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
	return answer == "y" || answer == "yes"
}

func usage() {
	fmt.Fprintf(os.Stderr, `nightshift %s — autonomous ticket-execution harness

Usage:
  nightshift [flags] [issue numbers...]

Run inside a git repository. With no issue numbers, nightshift selects issues
using the filter flags. With issue numbers, it acts on exactly those. Without
--execute it only reports what it would do.

Flags:
`, version)
	flag.PrintDefaults()
}
