// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Matthias Eder

package runner

import (
	"strings"
	"testing"
)

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
