// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bborbe/git-rest/mocks"
	"github.com/bborbe/git-rest/pkg/git"
)

// TestFlushRetainsCommitOnRejectedPush proves the whole chain of the fix: a
// coalesced flush whose push is rejected keeps its commit, and the next pull
// merges that retained commit with the diverged remote, resolving the
// modify/modify conflict through the pull path's conflict machinery instead of
// rescuing a dirty tree.
func TestFlushRetainsCommitOnRejectedPush(t *testing.T) {
	ctx := context.Background()
	client, fake, work, bare := newCoalesceFixtureWithMetrics(t, git.CoalesceConfig{
		Enabled:  true,
		Delay:    time.Hour,
		MaxDelay: time.Hour,
	})

	if err := client.WriteFile(ctx, "notes/x.md", []byte("LOCAL\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Advance the remote on the SAME path AFTER the local write, so the push is
	// rejected and the retry merge conflicts on notes/x.md.
	remote := filepath.Join(t.TempDir(), "remote")
	mustGit(t, filepath.Dir(remote), "clone", bare, remote)
	configureClaimIdentity(t, remote)
	if err := os.WriteFile(
		filepath.Join(remote, "notes", "x.md"), []byte("REMOTE\n"), 0o600,
	); err != nil {
		t.Fatalf("write remote notes/x.md: %v", err)
	}
	mustGit(t, remote, "add", "-A")
	mustGit(t, remote, "commit", "-m", "remote change")
	mustGit(t, remote, "push", "origin", "main")

	buf, restore := captureSlogLogs()
	defer restore()

	if err := client.FlushPending(ctx); err == nil {
		t.Fatalf("want flush to fail on a non-fast-forward push, logs:\n%s", buf.String())
	}

	if got := strings.TrimSpace(
		mustGit(t, work, "log", "-1", "--format=%s"),
	); got != "git-rest: update notes/x.md" {
		t.Fatalf("want retained commit at HEAD, got %q, logs:\n%s", got, buf.String())
	}
	if status := strings.TrimSpace(
		mustGit(t, work, "status", "--porcelain"),
	); status != "" {
		t.Fatalf("want clean work tree after failed push, got %q, logs:\n%s", status, buf.String())
	}
	if got := strings.TrimSpace(
		mustGit(t, work, "rev-list", "--count", "origin/main..HEAD"),
	); got != "1" {
		t.Fatalf("want 1 local commit above origin, got %q, logs:\n%s", got, buf.String())
	}
	pendingCalls := fake.SetWritePendingCallCount()
	if pendingCalls == 0 {
		t.Fatal("want SetWritePending to have been called")
	}
	if got := fake.SetWritePendingArgsForCall(pendingCalls - 1); got != 0 {
		t.Fatalf("want pending count 0 after failed push, got %d", got)
	}
	if !strings.Contains(buf.String(), "git-rest: flush push failed, commit retained") {
		t.Fatalf("want retained-commit log in %q", buf.String())
	}

	if err := client.Pull(ctx); err != nil {
		t.Fatalf("pull: %v, logs:\n%s", err, buf.String())
	}

	assertRetainedCommitMergedByPull(t, fake, work, bare, buf.String())
}

// assertRetainedCommitMergedByPull checks the pull merged the retained commit
// through the conflict machinery rather than rescuing a dirty tree.
func assertRetainedCommitMergedByPull(
	t *testing.T,
	fake *mocks.FakeMetrics,
	work, bare, logs string,
) {
	t.Helper()
	if got := strings.TrimSpace(
		mustGit(t, bare, "rev-list", "--count", "--branches=rescue/*"),
	); got != "0" {
		t.Fatalf("want no rescue ref on origin, got %q, logs:\n%s", got, logs)
	}
	mergePattern := regexp.MustCompile(`^merge: resolved=\[notes/x\.md\] quarantined=\[\]$`)
	if got := strings.TrimSpace(
		mustGit(t, work, "log", "-1", "--format=%s"),
	); !mergePattern.MatchString(got) {
		t.Fatalf("want merge commit resolving notes/x.md, got %q, logs:\n%s", got, logs)
	}
	if got := strings.TrimSpace(
		mustGit(t, work, "rev-list", "--count", "origin/main..HEAD"),
	); got != "0" {
		t.Fatalf("want merge commit pushed, ahead=%q, logs:\n%s", got, logs)
	}
	if status := strings.TrimSpace(
		mustGit(t, work, "status", "--porcelain"),
	); status != "" {
		t.Fatalf("want clean work tree after pull, got %q, logs:\n%s", status, logs)
	}
	mergeCalls := fake.IncMergeOutcomeCallCount()
	if mergeCalls == 0 {
		t.Fatal("want IncMergeOutcome to have been called")
	}
	if got := fake.IncMergeOutcomeArgsForCall(mergeCalls - 1); got != "resolved" {
		t.Fatalf("want last merge outcome resolved, got %q", got)
	}
	conflictCalls := fake.IncConflictPathsCallCount()
	if conflictCalls == 0 {
		t.Fatal("want IncConflictPaths to have been called")
	}
	if got := fake.IncConflictPathsArgsForCall(conflictCalls - 1); got != 1 {
		t.Fatalf("want 1 conflicted path, got %d", got)
	}
	if got := fake.IncPullRescueCallCount(); got != 0 {
		t.Fatalf("want no pull rescue, got %d", got)
	}
}
