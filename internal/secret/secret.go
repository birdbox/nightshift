// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Matthias Eder

// Package secret resolves and stores the GitHub token nightshift uses. The
// token is kept in a 0600 file under the user's config directory — the same
// permission model as gh, git, and the AWS CLI (protected by file permissions,
// not encrypted at rest).
package secret

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func dir() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "nightshift"), nil
}

// Path returns the location of the token file.
func Path() (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "token"), nil
}

// Load reads the saved token, returning "" (and no error) when none is stored.
func Load() (string, error) {
	p, err := Path()
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// Save writes the token to the config file with 0600 permissions, creating the
// config directory (0700) if needed. An existing token is overwritten.
func Save(token string) error {
	d, err := dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(d, "token"), []byte(strings.TrimSpace(token)+"\n"), 0o600)
}

// Delete removes the saved token, if any.
func Delete() error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Interactive reports whether stdin is a real terminal we can prompt on.
func Interactive() bool {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		// A pipe or regular file is definitely not interactive.
		return false
	}
	// A char device might still be /dev/null; stty -g only succeeds on a tty.
	cmd := exec.Command("stty", "-g")
	cmd.Stdin = os.Stdin
	return cmd.Run() == nil
}

// PromptToken reads a token from the terminal without echoing it.
func PromptToken(prompt string) (string, error) {
	fmt.Print(prompt)
	restore, _ := disableEcho()
	line, err := readLine()
	if restore != nil {
		restore()
	}
	fmt.Println() // the user's Enter wasn't echoed; move to the next line
	return strings.TrimSpace(line), err
}

// readLine reads a single line from stdin one byte at a time, so it never
// buffers past the newline (which would swallow input from a later prompt).
func readLine() (string, error) {
	var sb strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			switch buf[0] {
			case '\n':
				return sb.String(), nil
			case '\r':
				// ignore
			default:
				sb.WriteByte(buf[0])
			}
		}
		if err != nil {
			return sb.String(), err
		}
	}
}

// disableEcho turns off terminal echo via stty and returns a function that
// restores it. If stty is unavailable the prompt still works, just with echo on.
func disableEcho() (func(), error) {
	off := exec.Command("stty", "-echo")
	off.Stdin = os.Stdin
	if err := off.Run(); err != nil {
		return nil, err
	}
	return func() {
		on := exec.Command("stty", "echo")
		on.Stdin = os.Stdin
		_ = on.Run()
	}, nil
}
