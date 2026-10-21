// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bborbe/git-rest/mocks"
	"github.com/bborbe/git-rest/pkg/git"
)

const (
	claimShaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	claimShaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	claimShaC = "cccccccccccccccccccccccccccccccccccccccc"
	claimShaD = "dddddddddddddddddddddddddddddddddddddddd"
	claimShaE = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
)

// claimMessages holds a valid JSON claim message for every sha the listing
// tests use.
var claimMessages = map[string][]byte{
	claimShaA: []byte(
		`{"cluster":"one","task_id":"t-1","lease_until":"2026-10-21T12:05:00Z","nonce":"n1"}`,
	),
	claimShaB: []byte(
		`{"cluster":"two","task_id":"t-2","lease_until":"2026-10-22T12:05:00Z","nonce":"n2"}`,
	),
	claimShaC: []byte(
		`{"cluster":"three","task_id":"t-3","lease_until":"2026-10-23T12:05:00Z","nonce":"n3"}`,
	),
	claimShaD: []byte(
		`{"cluster":"four","task_id":"t-4","lease_until":"2026-10-24T12:05:00Z","nonce":"n4"}`,
	),
	claimShaE: []byte(
		`{"cluster":"five","task_id":"t-5","lease_until":"2026-10-25T12:05:00Z","nonce":"n5"}`,
	),
}

// newClaimListFake returns a FakeClaimOrigin that lists refs and reads the
// canned claim messages, with a successful batched fetch.
func newClaimListFake(refs []git.RemoteRef) *mocks.FakeClaimOrigin {
	fake := &mocks.FakeClaimOrigin{}
	fake.ListRefsStub = func(_ context.Context, _ string) ([]git.RemoteRef, error) {
		return refs, nil
	}
	fake.FetchCommitsStub = func(_ context.Context, _ []string) error { return nil }
	fake.ReadCommitMessageStub = func(_ context.Context, sha string) ([]byte, error) {
		return claimMessages[sha], nil
	}
	return fake
}

// twoClaimRefs is the two-entry origin used by the fetch-failure subtests.
func twoClaimRefs() []git.RemoteRef {
	return []git.RemoteRef{
		{SHA: claimShaA, Ref: "refs/claims/a"},
		{SHA: claimShaB, Ref: "refs/claims/b"},
	}
}

// failBatchAndB makes the batched fetch and B's own fetch fail while A's own
// fetch succeeds, so a per-sha fallback reaches the re-list step for B.
func failBatchAndB(fake *mocks.FakeClaimOrigin) {
	fake.FetchCommitsStub = func(_ context.Context, shas []string) error {
		if len(shas) == 1 && shas[0] == claimShaA {
			return nil
		}
		return errors.New("couldn't find remote ref")
	}
}

func TestClaimListFetchBySha(t *testing.T) {
	t.Run("fetches every listed commit by sha", testClaimListFetchesBySHA)
	t.Run("vanished sha is dropped with one warning", testClaimListVanishedSHA)
	t.Run("moved sha is dropped with one warning", testClaimListMovedSHA)
	t.Run("fetch failure with ref still present returns error", testClaimListFetchFailureRefPresent)
	t.Run("re-list failure returns error", testClaimListRellistFailure)
	t.Run("one batched fetch for N claims", testClaimListOneBatch)
	t.Run("no matched refs makes no fetch", testClaimListNoMatch)
}

func testClaimListFetchesBySHA(t *testing.T) {
	fake := newClaimListFake(twoClaimRefs())
	g := git.NewGitWithClaimOrigin(fake)
	claims, err := g.ListClaimRefs(context.Background(), "claims/")
	if err != nil {
		t.Fatalf("ListClaimRefs: %v", err)
	}
	if len(claims) != 2 {
		t.Fatalf("expected 2 claims, got %d", len(claims))
	}
	if fake.FetchCommitsCallCount() != 1 {
		t.Fatalf("expected one batched fetch, got %d", fake.FetchCommitsCallCount())
	}
	_, shas := fake.FetchCommitsArgsForCall(0)
	if strings.Join(shas, ",") != claimShaA+","+claimShaB {
		t.Fatalf("expected fetch order [%s %s], got %v", claimShaA, claimShaB, shas)
	}
	if claims[0].Ref != "refs/claims/a" || claims[0].Cluster != "one" ||
		claims[0].TaskID != "t-1" {
		t.Fatalf("unexpected first claim: %+v", claims[0])
	}
}

func testClaimListVanishedSHA(t *testing.T) {
	fake := newClaimListFake(twoClaimRefs())
	failBatchAndB(fake)
	fake.ListRefsStub = func(_ context.Context, pattern string) ([]git.RemoteRef, error) {
		if pattern == "refs/claims/*" {
			return twoClaimRefs(), nil
		}
		return nil, nil
	}
	g := git.NewGitWithClaimOrigin(fake)
	logs, restore := captureSlogLogs()
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

func testClaimListMovedSHA(t *testing.T) {
	fake := newClaimListFake(twoClaimRefs())
	failBatchAndB(fake)
	fake.ListRefsStub = func(_ context.Context, pattern string) ([]git.RemoteRef, error) {
		if pattern == "refs/claims/*" {
			return twoClaimRefs(), nil
		}
		return []git.RemoteRef{{SHA: claimShaC, Ref: "refs/claims/b"}}, nil
	}
	g := git.NewGitWithClaimOrigin(fake)
	logs, restore := captureSlogLogs()
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

func testClaimListFetchFailureRefPresent(t *testing.T) {
	fake := newClaimListFake(twoClaimRefs())
	failBatchAndB(fake)
	fake.ListRefsStub = func(_ context.Context, pattern string) ([]git.RemoteRef, error) {
		if pattern == "refs/claims/*" {
			return twoClaimRefs(), nil
		}
		return []git.RemoteRef{{SHA: claimShaB, Ref: "refs/claims/b"}}, nil
	}
	g := git.NewGitWithClaimOrigin(fake)
	claims, err := g.ListClaimRefs(context.Background(), "claims/")
	if err == nil {
		t.Fatalf("expected an error, got claims %+v", claims)
	}
	if len(claims) != 0 {
		t.Fatalf("expected no claims, got %+v", claims)
	}
	if fake.ListRefsCallCount() < 2 {
		t.Fatalf("expected a re-list call, got %d calls", fake.ListRefsCallCount())
	}
	_, pattern := fake.ListRefsArgsForCall(1)
	if pattern != "refs/claims/b" {
		t.Fatalf("expected re-list pattern refs/claims/b, got %q", pattern)
	}
}

func testClaimListRellistFailure(t *testing.T) {
	fake := newClaimListFake(twoClaimRefs())
	failBatchAndB(fake)
	fake.ListRefsStub = func(_ context.Context, pattern string) ([]git.RemoteRef, error) {
		if pattern == "refs/claims/*" {
			return twoClaimRefs(), nil
		}
		return nil, errors.New("origin unreachable")
	}
	g := git.NewGitWithClaimOrigin(fake)
	logs, restore := captureSlogLogs()
	defer restore()
	claims, err := g.ListClaimRefs(context.Background(), "claims/")
	if err == nil {
		t.Fatalf("expected an error, got claims %+v", claims)
	}
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "level=WARN") && strings.Contains(line, "refs/claims/b") {
			t.Fatalf("a failed re-list must not log a drop warning: %s", line)
		}
	}
}

func testClaimListOneBatch(t *testing.T) {
	refs := []git.RemoteRef{
		{SHA: claimShaA, Ref: "refs/claims/a"},
		{SHA: claimShaB, Ref: "refs/claims/b"},
		{SHA: claimShaC, Ref: "refs/claims/c"},
		{SHA: claimShaD, Ref: "refs/claims/d"},
		{SHA: claimShaE, Ref: "refs/claims/e"},
	}
	fake := newClaimListFake(refs)
	g := git.NewGitWithClaimOrigin(fake)
	claims, err := g.ListClaimRefs(context.Background(), "claims/")
	if err != nil {
		t.Fatalf("ListClaimRefs: %v", err)
	}
	if len(claims) != 5 {
		t.Fatalf("expected 5 claims, got %d", len(claims))
	}
	if fake.FetchCommitsCallCount() != 1 {
		t.Fatalf("expected one batched fetch, got %d", fake.FetchCommitsCallCount())
	}
	_, shas := fake.FetchCommitsArgsForCall(0)
	want := strings.Join(
		[]string{claimShaA, claimShaB, claimShaC, claimShaD, claimShaE},
		",",
	)
	if strings.Join(shas, ",") != want {
		t.Fatalf("batched fetch shas %v, want %s", shas, want)
	}
}

func testClaimListNoMatch(t *testing.T) {
	fake := newClaimListFake(nil)
	g := git.NewGitWithClaimOrigin(fake)
	claims, err := g.ListClaimRefs(context.Background(), "claims/")
	if err != nil {
		t.Fatalf("ListClaimRefs: %v", err)
	}
	if claims == nil || len(claims) != 0 {
		t.Fatalf("expected a non-nil empty slice, got %#v", claims)
	}
	if fake.FetchCommitsCallCount() != 0 {
		t.Fatalf("expected no fetch, got %d", fake.FetchCommitsCallCount())
	}
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

func TestClaimIsObjectID(t *testing.T) {
	tests := []struct {
		name string
		sha  string
		want bool
	}{
		{name: "40 lowercase hex", sha: strings.Repeat("a", 40), want: true},
		{name: "64 lowercase hex", sha: strings.Repeat("a", 64), want: true},
		{name: "empty", sha: "", want: false},
		{name: "39 hex", sha: strings.Repeat("a", 39), want: false},
		{name: "41 hex", sha: strings.Repeat("a", 41), want: false},
		{name: "one uppercase letter", sha: strings.Repeat("a", 39) + "A", want: false},
		{name: "colon", sha: strings.Repeat("a", 39) + ":", want: false},
		{name: "non-hex letter", sha: strings.Repeat("a", 39) + "g", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := git.IsObjectID(test.sha); got != test.want {
				t.Fatalf("IsObjectID(%q) = %v, want %v", test.sha, got, test.want)
			}
		})
	}
}
