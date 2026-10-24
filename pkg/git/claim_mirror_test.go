// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bborbe/git-rest/mocks"
	"github.com/bborbe/git-rest/pkg/git"
)

// TestClaimMirrorConfigureFetch proves the startup step is idempotent: applied
// twice it leaves exactly the default heads refspec plus the claim mirror
// refspec, and enables pruning.
func TestClaimMirrorConfigureFetch(t *testing.T) {
	fixture := newClaimFixture(t)
	client := newClaimClient(fixture.cloneA)

	for i := 0; i < 2; i++ {
		if err := client.ConfigureClaimMirror(context.Background()); err != nil {
			t.Fatalf("ConfigureClaimMirror call %d: %v", i+1, err)
		}
	}

	lines := strings.Split(
		strings.TrimSpace(mustGit(t, fixture.cloneA, "config", "--get-all", "remote.origin.fetch")),
		"\n",
	)
	if len(lines) != 2 {
		t.Fatalf("want exactly 2 fetch refspecs, got %d: %q", len(lines), lines)
	}
	want := map[string]bool{
		"+refs/heads/*:refs/remotes/origin/*": false,
		"+refs/claims/*:refs/claim-mirror/*":  false,
	}
	for _, line := range lines {
		seen, ok := want[line]
		if !ok {
			t.Fatalf("unexpected fetch refspec %q", line)
		}
		if seen {
			t.Fatalf("duplicate fetch refspec %q", line)
		}
		want[line] = true
	}

	prune := strings.TrimSpace(mustGit(t, fixture.cloneA, "config", "--get", "remote.origin.prune"))
	if prune != "true" {
		t.Fatalf("want remote.origin.prune=true, got %q", prune)
	}
}

// TestClaimMirrorPullRefreshesAndPrunes proves the puller's existing fetch
// carries the claim namespace, prunes a deleted claim, and that only a
// successful fetch advances the claims-specific freshness time.
func TestClaimMirrorPullRefreshesAndPrunes(t *testing.T) {
	fixture := newClaimFixture(t)
	client := newClaimClient(fixture.cloneA)
	configureClaimIdentity(t, fixture.bare)
	ctx := context.Background()

	if err := client.ConfigureClaimMirror(ctx); err != nil {
		t.Fatalf("ConfigureClaimMirror: %v", err)
	}
	if err := client.Pull(ctx); err != nil {
		t.Fatalf("initial Pull: %v", err)
	}

	if got := strings.TrimSpace(
		mustGit(t, fixture.cloneA, "for-each-ref", "refs/claim-mirror/"),
	); got != "" {
		t.Fatalf("want an empty mirror before any claim, got %q", got)
	}

	sha := strings.TrimSpace(mustGit(
		t,
		fixture.bare,
		"commit-tree",
		claimEmptyTreeSHA,
		"-m",
		`{"cluster":"nuke","task_id":"t1","lease_until":"2026-10-25T00:00:00Z","nonce":"0123456789abcdef0123456789abcdef"}`,
	))
	mustGit(t, fixture.bare, "update-ref", "refs/claims/task/t1", sha)

	if err := client.Pull(ctx); err != nil {
		t.Fatalf("Pull after claim create: %v", err)
	}
	if got := strings.TrimSpace(
		mustGit(t, fixture.cloneA, "rev-parse", "refs/claim-mirror/task/t1"),
	); got != sha {
		t.Fatalf("want mirror ref at %s, got %q", sha, got)
	}

	mustGit(t, fixture.bare, "update-ref", "-d", "refs/claims/task/t1")
	if err := client.Pull(ctx); err != nil {
		t.Fatalf("Pull after claim delete: %v", err)
	}
	if got := strings.TrimSpace(
		mustGit(t, fixture.cloneA, "for-each-ref", "refs/claim-mirror/"),
	); got != "" {
		t.Fatalf("want the mirror pruned after the claim was deleted, got %q", got)
	}

	sha2 := strings.TrimSpace(mustGit(
		t,
		fixture.bare,
		"commit-tree",
		claimEmptyTreeSHA,
		"-m",
		`{"cluster":"nuke","task_id":"t2","lease_until":"2026-10-25T00:00:00Z","nonce":"0123456789abcdef0123456789abcdef"}`,
	))
	mustGit(t, fixture.bare, "update-ref", "refs/claims/task/t2", sha2)
	if err := client.Pull(ctx); err != nil {
		t.Fatalf("Pull after second claim create: %v", err)
	}
	freshBefore := git.ClaimMirrorFreshAtForTest(client)
	if freshBefore.IsZero() {
		t.Fatal("want a non-zero freshness time after a successful pull")
	}

	mustGit(t, fixture.cloneA, "remote", "set-url", "origin", "/nonexistent/origin.git")
	if err := client.Pull(ctx); err == nil {
		t.Fatal("want a pull error against a severed origin, got nil")
	}
	if got := git.ClaimMirrorFreshAtForTest(client); !got.Equal(freshBefore) {
		t.Fatalf(
			"want freshness unchanged after a failed fetch, got %v (before %v)",
			got,
			freshBefore,
		)
	}
}

// TestClaimMirrorConfigFailureKeepsServing proves a failed configuration step is
// returned to the caller without panicking, and that the client still answers
// claim reads through the live origin.
func TestClaimMirrorConfigFailureKeepsServing(t *testing.T) {
	fixture := newClaimFixture(t)
	gitDir := filepath.Join(fixture.cloneA, ".git")

	if err := os.Chmod(gitDir, 0o500); err != nil {
		t.Fatalf("chmod .git read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(gitDir, 0o700) })

	probe := filepath.Join(gitDir, ".probe")
	if err := os.WriteFile(probe, []byte("x"), 0o600); err == nil {
		_ = os.Remove(probe)
		t.Skip("running as root: cannot enforce a read-only .git directory")
	}

	fake := &mocks.FakeClaimOrigin{}
	fake.ListRefsStub = func(context.Context, string) ([]git.RemoteRef, error) {
		return nil, nil
	}
	g := git.NewGitWithClaimMirrorForTest(fixture.cloneA, fake)

	if err := g.ConfigureClaimMirror(context.Background()); err == nil {
		t.Fatal("want a configuration error against a read-only .git directory, got nil")
	}

	claims, err := g.ListClaimRefs(context.Background(), "claims/")
	if err != nil {
		t.Fatalf("ListClaimRefs after failed configuration: %v", err)
	}
	if claims == nil {
		t.Fatal("want a non-nil empty slice, got nil")
	}
	if len(claims) != 0 {
		t.Fatalf("want no claims, got %d", len(claims))
	}
	if got := fake.ListRefsCallCount(); got != 1 {
		t.Fatalf("want 1 live ListRefs call, got %d", got)
	}
}
