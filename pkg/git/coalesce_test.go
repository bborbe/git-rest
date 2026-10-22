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
	"time"

	libtime "github.com/bborbe/time"

	"github.com/bborbe/git-rest/mocks"
	"github.com/bborbe/git-rest/pkg/flusher"
	"github.com/bborbe/git-rest/pkg/git"
)

// newCoalesceFixtureWithMetrics builds a bare origin, a seed with notes/x.md
// and notes/y.md containing "v0\n", a work clone, and a git client configured
// with the given coalescing options. It returns the client, its FakeMetrics,
// the work clone path and the bare origin path.
func newCoalesceFixtureWithMetrics(
	t *testing.T,
	config git.CoalesceConfig,
) (git.Git, *mocks.FakeMetrics, string, string) {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	mustGit(t, root, "init", "--bare", bare)

	seed := filepath.Join(root, "seed")
	mustGit(t, root, "init", seed)
	configureClaimIdentity(t, seed)
	mustGit(t, seed, "checkout", "-b", "main")
	if err := os.MkdirAll(filepath.Join(seed, "notes"), 0750); err != nil {
		t.Fatalf("mkdir notes: %v", err)
	}
	for _, name := range []string{"x.md", "y.md"} {
		if err := os.WriteFile(
			filepath.Join(seed, "notes", name),
			[]byte("v0\n"),
			0600,
		); err != nil {
			t.Fatalf("write seed %s: %v", name, err)
		}
	}
	mustGit(t, seed, "add", "-A")
	mustGit(t, seed, "commit", "-m", "init")
	mustGit(t, seed, "remote", "add", "origin", bare)
	mustGit(t, seed, "push", "origin", "main")
	mustGit(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")

	work := filepath.Join(root, "work")
	mustGit(t, root, "clone", bare, work)
	configureClaimIdentity(t, work)

	fake := &mocks.FakeMetrics{}
	client := git.New(
		work,
		fake,
		libtime.NewCurrentDateTime(),
		"",
		git.NewMarkerResolver(work),
		git.WithCoalesce(config),
	)
	return client, fake, work, bare
}

// newCoalesceFixture is the three-value form of newCoalesceFixtureWithMetrics
// for tests that do not inspect the metrics fake.
func newCoalesceFixture(t *testing.T, config git.CoalesceConfig) (git.Git, string, string) {
	t.Helper()
	client, _, work, bare := newCoalesceFixtureWithMetrics(t, config)
	return client, work, bare
}

// originGitRestSubjects returns the git-rest commit subjects on branch that
// touched path. Non-git-rest commits (the seed's "init") are filtered out so
// callers count only commits produced by the code under test.
func originGitRestSubjects(t *testing.T, bare, branch, path string) []string {
	t.Helper()
	out := mustGit(t, bare, "log", "--format=%s", branch, "--", path)
	var subjects []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "git-rest:") {
			subjects = append(subjects, line)
		}
	}
	return subjects
}

// waitFor polls condition until it returns true or timeout elapses.
func waitFor(t *testing.T, timeout time.Duration, condition func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return condition()
}

// TestCoalescEnabled proves a burst of writes to one path lands as one commit
// carrying the last content.
func TestCoalescEnabled(t *testing.T) {
	ctx := context.Background()
	client, fake, _, bare := newCoalesceFixtureWithMetrics(t, git.CoalesceConfig{
		Enabled:  true,
		Delay:    20 * time.Millisecond,
		MaxDelay: time.Second,
	})
	buf, restore := captureSlogLogs()
	defer restore()

	for _, content := range []string{"A", "B", "C"} {
		if err := client.WriteFile(ctx, "notes/x.md", []byte(content)); err != nil {
			t.Fatalf("write %s: %v", content, err)
		}
	}
	if err := client.FlushPending(ctx); err != nil {
		t.Fatalf("flush pending: %v", err)
	}

	subjects := originGitRestSubjects(t, bare, "main", "notes/x.md")
	if len(subjects) != 1 {
		t.Fatalf("want exactly 1 commit, got %d: %v", len(subjects), subjects)
	}
	if subjects[0] != "git-rest: update notes/x.md" {
		t.Fatalf("unexpected subject %q", subjects[0])
	}
	if got := mustGit(t, bare, "show", "main:notes/x.md"); got != "C" {
		t.Fatalf("want content C, got %q", got)
	}
	if got := fake.IncWriteCoalescedCallCount(); got != 3 {
		t.Fatalf("want IncWriteCoalesced called 3 times, got %d", got)
	}
	if !strings.Contains(buf.String(), "git-rest: flushed pending writes") {
		t.Fatalf("flush log line missing from %q", buf.String())
	}
}

// TestCoalescDisabled proves the off-switch restores one commit per write.
func TestCoalescDisabled(t *testing.T) {
	ctx := context.Background()
	client, fake, _, bare := newCoalesceFixtureWithMetrics(t, git.CoalesceConfig{
		Enabled: false,
	})

	for _, content := range []string{"A", "B", "C"} {
		if err := client.WriteFile(ctx, "notes/x.md", []byte(content)); err != nil {
			t.Fatalf("write %s: %v", content, err)
		}
	}

	subjects := originGitRestSubjects(t, bare, "main", "notes/x.md")
	if len(subjects) != 3 {
		t.Fatalf("want 3 commits, got %d: %v", len(subjects), subjects)
	}
	for _, subject := range subjects {
		if subject != "git-rest: update notes/x.md" {
			t.Fatalf("unexpected subject %q", subject)
		}
	}
	if got := mustGit(t, bare, "show", "main:notes/x.md"); got != "C" {
		t.Fatalf("want content C, got %q", got)
	}
	if got := fake.IncWriteCoalescedCallCount(); got != 0 {
		t.Fatalf("want no coalescing metrics when disabled, got %d", got)
	}
}

// TestCoalescTwoPaths proves a burst spanning two paths lands as one commit
// with the batch message.
func TestCoalescTwoPaths(t *testing.T) {
	ctx := context.Background()
	client, _, bare := newCoalesceFixture(t, git.CoalesceConfig{
		Enabled:  true,
		Delay:    20 * time.Millisecond,
		MaxDelay: time.Second,
	})

	if err := client.WriteFile(ctx, "notes/x.md", []byte("A")); err != nil {
		t.Fatalf("write x: %v", err)
	}
	if err := client.WriteFile(ctx, "notes/y.md", []byte("B")); err != nil {
		t.Fatalf("write y: %v", err)
	}
	if err := client.FlushPending(ctx); err != nil {
		t.Fatalf("flush pending: %v", err)
	}

	if got := strings.TrimSpace(
		mustGit(t, bare, "log", "-1", "--format=%s", "main"),
	); got != "git-rest: update 2 files" {
		t.Fatalf("unexpected subject %q", got)
	}
	names := strings.Fields(
		mustGit(t, bare, "show", "--name-only", "--format=", "main"),
	)
	if len(names) != 2 || names[0] != "notes/x.md" || names[1] != "notes/y.md" {
		t.Fatalf("unexpected file list %v", names)
	}
}

// TestCoalescCreateOnly proves a create-only write uses the create message.
func TestCoalescCreateOnly(t *testing.T) {
	ctx := context.Background()
	client, _, bare := newCoalesceFixture(t, git.CoalesceConfig{
		Enabled:  true,
		Delay:    20 * time.Millisecond,
		MaxDelay: time.Second,
	})

	if err := client.WriteFileIfAbsent(ctx, "notes/new.md", []byte("N")); err != nil {
		t.Fatalf("write if absent: %v", err)
	}
	if err := client.FlushPending(ctx); err != nil {
		t.Fatalf("flush pending: %v", err)
	}

	subjects := originGitRestSubjects(t, bare, "main", "notes/new.md")
	if len(subjects) != 1 {
		t.Fatalf("want exactly 1 commit, got %d: %v", len(subjects), subjects)
	}
	if subjects[0] != "git-rest: create notes/new.md" {
		t.Fatalf("unexpected subject %q", subjects[0])
	}
	if got := mustGit(t, bare, "show", "main:notes/new.md"); got != "N" {
		t.Fatalf("want content N, got %q", got)
	}
}

func TestUnchangedWriteStagesNothing(t *testing.T) {
	ctx := context.Background()
	client, fake, _, bare := newCoalesceFixtureWithMetrics(t, git.CoalesceConfig{
		Enabled:  true,
		Delay:    20 * time.Millisecond,
		MaxDelay: time.Second,
	})
	buf, restore := captureSlogLogs()
	defer restore()

	for i := 0; i < 3; i++ {
		if err := client.WriteFile(ctx, "notes/x.md", []byte("A")); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if err := client.FlushPending(ctx); err != nil {
		t.Fatalf("flush pending: %v", err)
	}

	subjects := originGitRestSubjects(t, bare, "main", "notes/x.md")
	if len(subjects) != 1 {
		t.Fatalf("want exactly 1 commit, got %d: %v", len(subjects), subjects)
	}
	if got := fake.IncWriteCoalescedCallCount(); got != 1 {
		t.Fatalf("want IncWriteCoalesced called once, got %d", got)
	}
	const unchanged = "write file: no changes to commit (content unchanged)"
	if got := strings.Count(buf.String(), unchanged); got != 2 {
		t.Fatalf("want unchanged log twice, got %d in %q", got, buf.String())
	}
}

func TestFlushBeforePull(t *testing.T) {
	ctx := context.Background()
	client, work, bare := newCoalesceFixture(t, git.CoalesceConfig{
		Enabled:  true,
		Delay:    time.Hour,
		MaxDelay: time.Hour,
	})

	if err := client.WriteFile(ctx, "notes/x.md", []byte("D")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := client.Pull(ctx); err != nil {
		t.Fatalf("pull: %v", err)
	}

	if status := mustGit(t, work, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Fatalf("work tree not clean after pull: %q", status)
	}
	subjects := originGitRestSubjects(t, bare, "main", "notes/x.md")
	if len(subjects) < 1 || !strings.HasPrefix(subjects[0], "git-rest:") {
		t.Fatalf("want a git-rest commit on origin, got %v", subjects)
	}
	rescue := strings.TrimSpace(
		mustGit(t, bare, "for-each-ref", "--format=%(refname)", "refs/heads/rescue/"),
	)
	if rescue != "" {
		t.Fatalf("unexpected rescue branch on origin: %q", rescue)
	}
}

func TestFlushTimerFires(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client, _, bare := newCoalesceFixture(t, git.CoalesceConfig{
		Enabled:  true,
		Delay:    50 * time.Millisecond,
		MaxDelay: time.Second,
	})

	if err := client.WriteFile(ctx, "notes/x.md", []byte("T")); err != nil {
		t.Fatalf("write: %v", err)
	}

	go func() {
		_ = flusher.New(client, libtime.Duration(10*time.Millisecond)).Run(ctx)
	}()

	if !waitFor(t, 2*time.Second, func() bool {
		return len(originGitRestSubjects(t, bare, "main", "notes/x.md")) >= 1
	}) {
		t.Fatal("timer never flushed the pending write")
	}
	subjects := originGitRestSubjects(t, bare, "main", "notes/x.md")
	if subjects[0] != "git-rest: update notes/x.md" {
		t.Fatalf("unexpected subject %q", subjects[0])
	}
}

func TestFlushMaxDelayCeiling(t *testing.T) {
	ctx := context.Background()
	client, _, bare := newCoalesceFixture(t, git.CoalesceConfig{
		Enabled:  true,
		Delay:    time.Hour,
		MaxDelay: 10 * time.Millisecond,
	})

	if err := client.WriteFile(ctx, "notes/x.md", []byte("M")); err != nil {
		t.Fatalf("write: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := client.FlushPendingIfDue(ctx); err != nil {
		t.Fatalf("flush if due: %v", err)
	}

	subjects := originGitRestSubjects(t, bare, "main", "notes/x.md")
	if len(subjects) != 1 {
		t.Fatalf("want exactly 1 commit, got %d: %v", len(subjects), subjects)
	}
	if subjects[0] != "git-rest: update notes/x.md" {
		t.Fatalf("unexpected subject %q", subjects[0])
	}
}
