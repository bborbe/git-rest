// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git

import (
	"bytes"
	"context"
	stderrors "errors"
	"log/slog"
	"strings"
	"testing"
)

// fakeClaimOrigin is a recording claimOrigin used by the internal list tests.
type fakeClaimOrigin struct {
	refs     []remoteRef
	messages map[string][]byte
	fetchErr map[string]error
	fetched  []string
}

func (f *fakeClaimOrigin) ListRefs(_ context.Context, _ string) ([]remoteRef, error) {
	return f.refs, nil
}

func (f *fakeClaimOrigin) FetchCommit(_ context.Context, sha string) error {
	f.fetched = append(f.fetched, sha)
	return f.fetchErr[sha]
}

func (f *fakeClaimOrigin) ReadCommitMessage(_ context.Context, sha string) ([]byte, error) {
	return f.messages[sha], nil
}

func captureSlog(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	return buf, func() { slog.SetDefault(prev) }
}

func newFakeClaimOrigin() *fakeClaimOrigin {
	const shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	return &fakeClaimOrigin{
		refs: []remoteRef{
			{SHA: shaA, Ref: "refs/claims/a"},
			{SHA: shaB, Ref: "refs/claims/b"},
		},
		messages: map[string][]byte{
			shaA: []byte(
				`{"cluster":"one","task_id":"t-1","lease_until":"2026-10-21T12:05:00Z","nonce":"n1"}`,
			),
			shaB: []byte(
				`{"cluster":"two","task_id":"t-2","lease_until":"2026-10-22T12:05:00Z","nonce":"n2"}`,
			),
		},
		fetchErr: map[string]error{},
	}
}

func TestClaimListFetchBySha(t *testing.T) {
	t.Run("fetches every listed commit by sha", testClaimListFetchesBySHA)
	t.Run("vanished sha is dropped with one warning", testClaimListVanishedSHA)
}

func testClaimListFetchesBySHA(t *testing.T) {
	const shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	fake := newFakeClaimOrigin()
	g := &git{origin: fake}
	claims, err := g.ListClaimRefs(context.Background(), "claims/")
	if err != nil {
		t.Fatalf("ListClaimRefs: %v", err)
	}
	if len(claims) != 2 {
		t.Fatalf("expected 2 claims, got %d", len(claims))
	}
	if len(fake.fetched) != 2 || fake.fetched[0] != shaA || fake.fetched[1] != shaB {
		t.Fatalf("expected fetch order [%s %s], got %v", shaA, shaB, fake.fetched)
	}
	if claims[0].Ref != "refs/claims/a" || claims[0].Cluster != "one" ||
		claims[0].TaskID != "t-1" {
		t.Fatalf("unexpected first claim: %+v", claims[0])
	}
}

func testClaimListVanishedSHA(t *testing.T) {
	const shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	fake := newFakeClaimOrigin()
	fake.fetchErr[shaB] = stderrors.New("couldn't find remote ref")
	g := &git{origin: fake}
	logs, restore := captureSlog(t)
	defer restore()
	claims, err := g.ListClaimRefs(context.Background(), "claims/")
	if err != nil {
		t.Fatalf("ListClaimRefs: %v", err)
	}
	if len(claims) != 1 || claims[0].Ref != "refs/claims/a" {
		t.Fatalf("expected only the surviving claim, got %+v", claims)
	}
	assertOneClaimWarning(t, logs.String())
}

func assertOneClaimWarning(t *testing.T, logs string) {
	t.Helper()
	warnings := 0
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		if !strings.Contains(line, "level=WARN") {
			continue
		}
		warnings++
		if !strings.Contains(line, "refs/claims/b") {
			t.Fatalf("warning does not name the dropped ref: %s", line)
		}
	}
	if warnings != 1 {
		t.Fatalf("expected exactly one warning, got %d in:\n%s", warnings, logs)
	}
}

func TestIsLeaseConflict(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{
			name:   "stale info",
			output: " ! [rejected]        abc -> x (stale info)\nerror: failed to push some refs",
			want:   true,
		},
		{
			name:   "remote ref does not exist",
			output: "error: remote ref does not exist",
			want:   true,
		},
		{
			name:   "cannot lock ref",
			output: "error: cannot lock ref 'refs/claims/x': is at abc",
			want:   true,
		},
		{
			name:   "remote rejected failed to update ref",
			output: " ! [remote rejected] abc -> x (failed to update ref)",
			want:   true,
		},
		{
			name:   "remote rejected reference already exists",
			output: " ! [remote rejected] abc -> x (reference already exists)",
			want:   true,
		},
		{
			name:   "remote rejected reference does not exist",
			output: " ! [remote rejected] abc -> x (reference does not exist)",
			want:   true,
		},
		{
			name:   "remote rejected expected reason",
			output: " ! [remote rejected] abc -> x (expected old value)",
			want:   true,
		},
		{
			name: "hook decline mentioning expected is not a conflict",
			output: "remote: expected commit message\n" +
				" ! [remote rejected] abc -> x (pre-receive hook declined)",
			want: false,
		},
		{
			name:   "transport failure",
			output: "fatal: unable to access 'https://example.com/x.git': Could not resolve host",
			want:   false,
		},
		{
			name:   "nothing to push",
			output: "Everything up-to-date",
			want:   false,
		},
		{
			name:   "empty output",
			output: "",
			want:   false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isLeaseConflict(test.output); got != test.want {
				t.Fatalf("isLeaseConflict(%q) = %v, want %v", test.output, got, test.want)
			}
		})
	}
}
