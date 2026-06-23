// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Matthias Eder

package main

import (
	"strings"
	"testing"
)

func TestValidateState(t *testing.T) {
	for _, valid := range []string{"open", "closed", "all"} {
		if err := validateState(valid); err != nil {
			t.Errorf("validateState(%q) = %v, want nil", valid, err)
		}
	}

	for _, invalid := range []string{"opne", "OPEN", "", "Open"} {
		err := validateState(invalid)
		if err == nil {
			t.Errorf("validateState(%q) = nil, want error", invalid)
			continue
		}
		// The error must name the allowed values so the user can fix it.
		for _, want := range []string{"open", "closed", "all"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("validateState(%q) error %q does not mention %q", invalid, err, want)
			}
		}
	}
}
