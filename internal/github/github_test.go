// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Matthias Eder

package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newTestClient points a bare Client at an httptest server. It bypasses
// NewClient (which validates a real token against api.github.com) so the
// write-back methods can be exercised in isolation.
func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	old := apiBase
	apiBase = srv.URL
	t.Cleanup(func() { apiBase = old })
	return &Client{
		http:  srv.Client(),
		token: "test-token",
		owner: "birdbox",
		repo:  "nightshift",
	}
}

func TestCommentOnIssue(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		gotPath = r.URL.Path
		var payload struct {
			Body string `json:"body"`
		}
		json.NewDecoder(r.Body).Decode(&payload)
		gotBody = payload.Body
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"id":1}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	if err := c.CommentOnIssue(context.Background(), 5, "hello"); err != nil {
		t.Fatalf("CommentOnIssue: %v", err)
	}
	if want := "/repos/birdbox/nightshift/issues/5/comments"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotBody != "hello" {
		t.Errorf("body = %q, want %q", gotBody, "hello")
	}
}

func TestAddLabels(t *testing.T) {
	var called bool
	var gotLabels []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if want := "/repos/birdbox/nightshift/issues/7/labels"; r.URL.Path != want {
			t.Errorf("path = %q, want %q", r.URL.Path, want)
		}
		var payload struct {
			Labels []string `json:"labels"`
		}
		json.NewDecoder(r.Body).Decode(&payload)
		gotLabels = payload.Labels
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `[]`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	if err := c.AddLabels(context.Background(), 7, "in-progress"); err != nil {
		t.Fatalf("AddLabels: %v", err)
	}
	if len(gotLabels) != 1 || gotLabels[0] != "in-progress" {
		t.Errorf("labels = %v, want [in-progress]", gotLabels)
	}

	// With no labels the method must not touch the network.
	called = false
	if err := c.AddLabels(context.Background(), 7); err != nil {
		t.Fatalf("AddLabels(none): %v", err)
	}
	if called {
		t.Error("AddLabels with no labels made a request, want none")
	}
}

func TestRemoveLabel(t *testing.T) {
	t.Run("escapes label and deletes", func(t *testing.T) {
		var gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete {
				t.Errorf("method = %s, want DELETE", r.Method)
			}
			gotPath = r.URL.EscapedPath()
			w.WriteHeader(http.StatusOK)
			io.WriteString(w, `[]`)
		}))
		defer srv.Close()

		c := newTestClient(t, srv)
		if err := c.RemoveLabel(context.Background(), 9, "nightshift:in-progress"); err != nil {
			t.Fatalf("RemoveLabel: %v", err)
		}
		if want := "/repos/birdbox/nightshift/issues/9/labels/nightshift:in-progress"; gotPath != want {
			t.Errorf("path = %q, want %q", gotPath, want)
		}
	})

	t.Run("404 is idempotent success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"message":"Label does not exist"}`)
		}))
		defer srv.Close()

		c := newTestClient(t, srv)
		if err := c.RemoveLabel(context.Background(), 9, "absent"); err != nil {
			t.Errorf("RemoveLabel on missing label = %v, want nil", err)
		}
	})

	t.Run("other errors propagate", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"message":"boom"}`)
		}))
		defer srv.Close()

		c := newTestClient(t, srv)
		if err := c.RemoveLabel(context.Background(), 9, "x"); err == nil {
			t.Error("RemoveLabel on 500 = nil, want error")
		}
	})
}

func TestUpdateIssue(t *testing.T) {
	t.Run("sends only set fields", func(t *testing.T) {
		var gotPath string
		var payload map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPatch {
				t.Errorf("method = %s, want PATCH", r.Method)
			}
			gotPath = r.URL.Path
			json.NewDecoder(r.Body).Decode(&payload)
			w.WriteHeader(http.StatusOK)
			io.WriteString(w, `{"number":3}`)
		}))
		defer srv.Close()

		c := newTestClient(t, srv)
		err := c.UpdateIssue(context.Background(), 3, IssueUpdate{State: "closed", Labels: []string{}})
		if err != nil {
			t.Fatalf("UpdateIssue: %v", err)
		}
		if want := "/repos/birdbox/nightshift/issues/3"; gotPath != want {
			t.Errorf("path = %q, want %q", gotPath, want)
		}
		if payload["state"] != "closed" {
			t.Errorf("state = %v, want closed", payload["state"])
		}
		if _, ok := payload["title"]; ok {
			t.Errorf("title should be omitted, got %v", payload["title"])
		}
		// A non-nil (empty) Labels slice clears the labels and must be sent.
		if _, ok := payload["labels"]; !ok {
			t.Error("labels should be present (empty slice clears labels)")
		}
	})

	t.Run("no fields makes no request", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("UpdateIssue with no fields made a request, want none")
		}))
		defer srv.Close()

		c := newTestClient(t, srv)
		if err := c.UpdateIssue(context.Background(), 3, IssueUpdate{}); err != nil {
			t.Fatalf("UpdateIssue(empty): %v", err)
		}
	})
}

func TestIsRefsNotReadable(t *testing.T) {
	refs422 := &apiError{StatusCode: http.StatusUnprocessableEntity, msg: `422: {"message":"Validation Failed","errors":[{"message":"not all refs are readable"}]}`}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"transient refs 422", refs422, true},
		{"wrapped transient 422", fmt.Errorf("create pull request: %w", refs422), true},
		{"different 422", &apiError{StatusCode: http.StatusUnprocessableEntity, msg: "422: No commits between main and branch"}, false},
		{"refs message but not 422", &apiError{StatusCode: http.StatusInternalServerError, msg: "not all refs are readable"}, false},
		{"nil", nil, false},
		{"plain error", errors.New("not all refs are readable"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRefsNotReadable(tt.err); got != tt.want {
				t.Errorf("isRefsNotReadable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCreatePRRetriesOnUnreadableRefs(t *testing.T) {
	old := prRetryDelay
	prRetryDelay = time.Millisecond
	t.Cleanup(func() { prRetryDelay = old })

	t.Run("succeeds after transient 422s", func(t *testing.T) {
		var attempts int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts++
			if attempts < 3 {
				w.WriteHeader(http.StatusUnprocessableEntity)
				io.WriteString(w, `{"message":"Validation Failed","errors":[{"message":"not all refs are readable"}]}`)
				return
			}
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"html_url":"https://github.com/birdbox/nightshift/pull/42"}`)
		}))
		defer srv.Close()

		c := newTestClient(t, srv)
		url, err := c.CreatePR(context.Background(), "t", "head", "main", "body")
		if err != nil {
			t.Fatalf("CreatePR: %v", err)
		}
		if attempts != 3 {
			t.Errorf("attempts = %d, want 3", attempts)
		}
		if want := "https://github.com/birdbox/nightshift/pull/42"; url != want {
			t.Errorf("url = %q, want %q", url, want)
		}
	})

	t.Run("does not retry other 422s", func(t *testing.T) {
		var attempts int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts++
			w.WriteHeader(http.StatusUnprocessableEntity)
			io.WriteString(w, `{"message":"No commits between main and head"}`)
		}))
		defer srv.Close()

		c := newTestClient(t, srv)
		if _, err := c.CreatePR(context.Background(), "t", "head", "main", "body"); err == nil {
			t.Fatal("CreatePR = nil error, want failure")
		}
		if attempts != 1 {
			t.Errorf("attempts = %d, want 1 (no retry)", attempts)
		}
	})
}
