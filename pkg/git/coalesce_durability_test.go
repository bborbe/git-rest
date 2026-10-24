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

	"github.com/bborbe/git-rest/pkg/git"
)

// assertRetainedCommitLog checks the failed-flush WARN record names the
// retained commit's SHA and its path count, and that the SHA is HEAD.
func assertRetainedCommitLog(t *testing.T, work, logs string) {
	t.Helper()
	if !strings.Contains(logs, "git-rest: flush push failed, commit retained") {
		t.Fatalf("want retained-commit log in %q", logs)
	}
	if !strings.Contains(logs, "paths=1") {
		t.Fatalf("want paths=1 in %q", logs)
	}
	match := regexp.MustCompile(`commit=([0-9a-f]{40})`).FindStringSubmatch(logs)
	if match == nil {
		t.Fatalf("want commit SHA in %q", logs)
	}
	if want := strings.TrimSpace(mustGit(t, work, "rev-parse", "HEAD")); match[1] != want {
		t.Fatalf("logged commit %q != HEAD %q", match[1], want)
	}
}

// TestFlushPushFailureKeepsPending proves a failed coalesced push retains its
// commit and clears the pending set, so the same batch is never re-committed on
// top of its own commit, and the next pull delivers the retained commit once the
// remote accepts pushes again.
func TestFlushPushFailureKeepsPending(t *testing.T) {
	ctx := context.Background()
	client, fake, work, bare := newCoalesceFixtureWithMetrics(t, git.CoalesceConfig{
		Enabled:  true,
		Delay:    20 * time.Millisecond,
		MaxDelay: time.Second,
	})

	if err := client.WriteFile(ctx, "notes/x.md", []byte("P")); err != nil {
		t.Fatalf("write: %v", err)
	}

	hook := filepath.Join(bare, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil { //nolint:gosec
		t.Fatalf("write pre-receive hook: %v", err)
	}
	// Point the bare remote at its OWN hooks directory. A global core.hooksPath
	// (set on this host to ~/.git-hooks) otherwise overrides $GIT_DIR/hooks, the
	// hook never runs, the push succeeds and this test fails while the code under
	// test is correct. Same guard as installPreReceiveRejection.
	mustGit(t, bare, "config", "core.hooksPath", "hooks")

	buf, restore := captureSlogLogs()
	defer restore()

	if err := client.FlushPending(ctx); err == nil {
		t.Fatal("want flush to fail while the origin rejects pushes")
	}

	if got := strings.TrimSpace(
		mustGit(t, work, "log", "-1", "--format=%s"),
	); got != "git-rest: update notes/x.md" {
		t.Fatalf("want retained commit at HEAD, got %q", got)
	}
	if status := strings.TrimSpace(
		mustGit(t, work, "status", "--porcelain"),
	); status != "" {
		t.Fatalf("want clean work tree after failed push, got %q", status)
	}
	if got := strings.TrimSpace(
		mustGit(t, work, "rev-list", "--count", "origin/main..HEAD"),
	); got != "1" {
		t.Fatalf("want 1 local commit above origin, got %q", got)
	}
	if got := originGitRestSubjects(t, bare, "main", "notes/x.md"); len(got) != 0 {
		t.Fatalf("want no git-rest commit on origin, got %v", got)
	}
	pendingCalls := fake.SetWritePendingCallCount()
	if pendingCalls == 0 {
		t.Fatal("want SetWritePending to have been called")
	}
	if got := fake.SetWritePendingArgsForCall(pendingCalls - 1); got != 0 {
		t.Fatalf("want pending count 0 after failed push, got %d", got)
	}
	flushCalls := fake.IncWriteFlushCallCount()
	if flushCalls == 0 {
		t.Fatal("want IncWriteFlush to have been called")
	}
	if got := fake.IncWriteFlushArgsForCall(flushCalls - 1); got != "error" {
		t.Fatalf("want last flush outcome error, got %q", got)
	}
	assertRetainedCommitLog(t, work, buf.String())

	if err := os.Remove(hook); err != nil {
		t.Fatalf("remove pre-receive hook: %v", err)
	}
	if err := client.Pull(ctx); err != nil {
		t.Fatalf("pull: %v", err)
	}

	assertRetainedCommitDelivered(t, work, bare)
}

// assertRetainedCommitDelivered checks the pull pushed the retained commit to
// the origin once the hook stopped rejecting pushes.
func assertRetainedCommitDelivered(t *testing.T, work, bare string) {
	t.Helper()
	subjects := originGitRestSubjects(t, bare, "main", "notes/x.md")
	if len(subjects) != 1 {
		t.Fatalf("want exactly 1 git-rest commit on origin, got %d: %v", len(subjects), subjects)
	}
	if subjects[0] != "git-rest: update notes/x.md" {
		t.Fatalf("unexpected subject %q", subjects[0])
	}
	if got := mustGit(t, bare, "show", "main:notes/x.md"); got != "P" {
		t.Fatalf("want content P on origin, got %q", got)
	}
	if got := strings.TrimSpace(
		mustGit(t, work, "rev-list", "--count", "origin/main..HEAD"),
	); got != "0" {
		t.Fatalf("want retained commit delivered, ahead=%q", got)
	}
	if status := strings.TrimSpace(
		mustGit(t, work, "status", "--porcelain"),
	); status != "" {
		t.Fatalf("want clean work tree after delivery, got %q", status)
	}
}

// TestDeleteFlushesPendingFirst proves a delete never jumps ahead of a pending
// write: the write commit lands below the delete commit.
func TestDeleteFlushesPendingFirst(t *testing.T) {
	ctx := context.Background()
	client, work, bare := newCoalesceFixture(t, git.CoalesceConfig{
		Enabled:  true,
		Delay:    time.Hour,
		MaxDelay: time.Hour,
	})

	if err := client.WriteFile(ctx, "notes/x.md", []byte("I")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := client.DeleteFile(ctx, "notes/x.md"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	local := strings.Split(
		strings.TrimSpace(mustGit(t, work, "log", "-2", "--format=%s")),
		"\n",
	)
	want := []string{"git-rest: delete notes/x.md", "git-rest: update notes/x.md"}
	if len(local) != 2 || local[0] != want[0] || local[1] != want[1] {
		t.Fatalf("want local subjects %v, got %v", want, local)
	}

	origin := strings.Split(
		strings.TrimSpace(mustGit(t, bare, "log", "-2", "--format=%s", "main")),
		"\n",
	)
	if len(origin) != 2 || origin[0] != want[0] || origin[1] != want[1] {
		t.Fatalf("want origin subjects %v, got %v", want, origin)
	}

	if status := strings.TrimSpace(
		mustGit(t, work, "status", "--porcelain"),
	); status != "" {
		t.Fatalf("want clean work tree, got %q", status)
	}
}

// TestRecoverPendingWrites proves a single staged path left behind by a crash
// is committed and pushed on boot with the single-path message, and that the
// index and work tree end clean.
func TestRecoverPendingWrites(t *testing.T) {
	ctx := context.Background()
	client, fake, work, bare := newCoalesceFixtureWithMetrics(t, git.CoalesceConfig{
		Enabled: true,
	})
	buf, restore := captureSlogLogs()
	defer restore()

	if err := os.WriteFile(
		filepath.Join(work, "notes", "x.md"), []byte("SURVIVOR"), 0o600,
	); err != nil {
		t.Fatalf("write staged file: %v", err)
	}
	mustGit(t, work, "add", "notes/x.md")

	if err := client.RecoverPendingWrites(ctx); err != nil {
		t.Fatalf("recover: %v", err)
	}

	if got := mustGit(t, bare, "show", "main:notes/x.md"); got != "SURVIVOR" {
		t.Fatalf("want SURVIVOR on origin, got %q", got)
	}
	if got := strings.TrimSpace(
		mustGit(t, bare, "log", "-1", "--format=%s", "main"),
	); got != "git-rest: update notes/x.md" {
		t.Fatalf("unexpected subject %q", got)
	}
	if got := strings.TrimSpace(
		mustGit(t, work, "diff", "--cached", "--name-only"),
	); got != "" {
		t.Fatalf("want clean index, got %q", got)
	}
	if got := strings.TrimSpace(
		mustGit(t, work, "status", "--porcelain"),
	); got != "" {
		t.Fatalf("want clean work tree, got %q", got)
	}
	if !strings.Contains(buf.String(), "git-rest: recovered pending writes") {
		t.Fatalf("recovery log line missing from %q", buf.String())
	}
	pendingCalls := fake.SetWritePendingCallCount()
	if pendingCalls == 0 {
		t.Fatal("want SetWritePending to have been called")
	}
	if got := fake.SetWritePendingArgsForCall(pendingCalls - 1); got != 0 {
		t.Fatalf("want pending count 0 after recovery, got %d", got)
	}
}

// TestRecoverPendingWritesTwoPaths proves a crash that staged two paths is
// recovered with the batch message.
func TestRecoverPendingWritesTwoPaths(t *testing.T) {
	ctx := context.Background()
	client, work, bare := newCoalesceFixture(t, git.CoalesceConfig{Enabled: true})

	if err := os.WriteFile(
		filepath.Join(work, "notes", "x.md"), []byte("X2"), 0o600,
	); err != nil {
		t.Fatalf("write x: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(work, "notes", "y.md"), []byte("Y2"), 0o600,
	); err != nil {
		t.Fatalf("write y: %v", err)
	}
	mustGit(t, work, "add", "notes/x.md", "notes/y.md")

	if err := client.RecoverPendingWrites(ctx); err != nil {
		t.Fatalf("recover: %v", err)
	}

	if got := strings.TrimSpace(
		mustGit(t, bare, "log", "-1", "--format=%s", "main"),
	); got != "git-rest: update 2 files" {
		t.Fatalf("unexpected subject %q", got)
	}
}

// TestRecoverPendingWritesNewPath proves a crash that staged one new path is
// recovered with the create message.
func TestRecoverPendingWritesNewPath(t *testing.T) {
	ctx := context.Background()
	client, work, bare := newCoalesceFixture(t, git.CoalesceConfig{Enabled: true})

	if err := os.WriteFile(
		filepath.Join(work, "notes", "new.md"), []byte("NEW"), 0o600,
	); err != nil {
		t.Fatalf("write new: %v", err)
	}
	mustGit(t, work, "add", "notes/new.md")

	if err := client.RecoverPendingWrites(ctx); err != nil {
		t.Fatalf("recover: %v", err)
	}

	if got := strings.TrimSpace(
		mustGit(t, bare, "log", "-1", "--format=%s", "main"),
	); got != "git-rest: create notes/new.md" {
		t.Fatalf("unexpected subject %q", got)
	}
	if got := mustGit(t, bare, "show", "main:notes/new.md"); got != "NEW" {
		t.Fatalf("want NEW on origin, got %q", got)
	}
}

// TestClaimRefsStaySynchronousWhileWritePending proves a claim-ref PUT reaches
// the origin immediately and never flushes the file-write buffer.
func TestClaimRefsStaySynchronousWhileWritePending(t *testing.T) {
	ctx := context.Background()
	client, work, bare := newCoalesceFixture(t, git.CoalesceConfig{
		Enabled:  true,
		Delay:    time.Hour,
		MaxDelay: time.Hour,
	})

	if err := client.WriteFile(ctx, "notes/x.md", []byte("W")); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := client.PutClaimRef(ctx, "claims/probe", git.ClaimRefRequest{
		Cluster: "dev",
		TaskID:  "probe",
	}); err != nil {
		t.Fatalf("put claim ref: %v", err)
	}

	refs := mustGit(t, work, "ls-remote", "origin", "refs/claims/*")
	if !strings.Contains(refs, "refs/claims/probe") {
		t.Fatalf("want refs/claims/probe on origin, got %q", refs)
	}
	if got := originGitRestSubjects(t, bare, "main", "notes/x.md"); len(got) != 0 {
		t.Fatalf("claim PUT must not flush the pending write, got %v", got)
	}

	if err := client.FlushPending(ctx); err != nil {
		t.Fatalf("flush pending: %v", err)
	}
	if got := originGitRestSubjects(t, bare, "main", "notes/x.md"); len(got) != 1 {
		t.Fatalf("want the file commit after flush, got %v", got)
	}
}
