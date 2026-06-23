// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Matthias Eder

package runner

import (
	"errors"
	"strings"
	"testing"
)

func TestOutcomeComment(t *testing.T) {
	tests := []struct {
		name string
		res  Result
		want string // substring the comment must contain
	}{
		{
			name: "error reports the failure reason",
			res:  Result{Err: errors.New("claude execution: exit status 1")},
			want: "exit status 1",
		},
		{
			name: "success reports the PR URL",
			res:  Result{PRURL: "https://github.com/birdbox/nightshift/pull/42"},
			want: "https://github.com/birdbox/nightshift/pull/42",
		},
		{
			name: "no PR reports the note",
			res:  Result{Note: "agent produced no commits; no PR opened"},
			want: "no commits",
		},
		{
			name: "no PR and no note still produces a comment",
			res:  Result{},
			want: "no pull request",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := outcomeComment(tt.res)
			if got == "" {
				t.Fatal("outcomeComment returned empty string")
			}
			if !strings.Contains(got, tt.want) {
				t.Errorf("outcomeComment(%+v) = %q, want it to contain %q", tt.res, got, tt.want)
			}
		})
	}

	// An error takes precedence over any PR URL that may also be set.
	got := outcomeComment(Result{Err: errors.New("push failed"), PRURL: "https://x/pull/1"})
	if !strings.Contains(got, "push failed") || strings.Contains(got, "pull/1") {
		t.Errorf("error outcome should report the error, not a PR URL; got %q", got)
	}
}

func TestBranchSlug(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  string
	}{
		{
			name:  "normal title",
			title: "Fix flaky upload test",
			want:  "fix-flaky-upload-test",
		},
		{
			name:  "punctuation and symbols collapse to single dashes",
			title: "Fix: flaky   upload!! (test) #42",
			want:  "fix-flaky-upload-test-42",
		},
		{
			name:  "leading and trailing junk is trimmed",
			title: "--- Hello, world! ---",
			want:  "hello-world",
		},
		{
			// The slug "one-two-...-eight-nine" exceeds 40 chars; the cut at 40
			// lands on the dash before "nine", which must be trimmed off so the
			// result never ends in a dash.
			name:  "long title is truncated without a trailing dash",
			title: "one two three four five six seven eight nine",
			want:  "one-two-three-four-five-six-seven-eight",
		},
		{
			name:  "empty title falls back to issue",
			title: "",
			want:  "issue",
		},
		{
			name:  "all-symbol title falls back to issue",
			title: "!@#$ %^&* ()",
			want:  "issue",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := branchSlug(tt.title)
			if got != tt.want {
				t.Errorf("branchSlug(%q) = %q, want %q", tt.title, got, tt.want)
			}
			if len(got) > 40 {
				t.Errorf("branchSlug(%q) = %q, length %d exceeds 40", tt.title, got, len(got))
			}
			if strings.HasSuffix(got, "-") {
				t.Errorf("branchSlug(%q) = %q, must not end in a dash", tt.title, got)
			}
		})
	}
}
