// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// This file holds INTERNAL tests for unexported quarantine helpers
// (resolveConflictPaths, quarantineOne, unsafeConflictPath,
// quarantineDestPath) and pathological branches that cannot be reached
// via the external Pull API (e.g. crafted unsafe paths a real git merge
// cannot produce, all-fail-abort fixtures).
//
// END-TO-END integration tests for the successful quarantine path
// (resolver fails -> quarantine succeeds -> merge commits with
// `quarantined=[...]`, IncQuarantinedFiles fires, working tree clean)
// live in pkg/git/git_test.go:
//   - AC3 (single conflict, file quarantined) -> line 977
//   - AC1-AC6 + AC12 (1-of-11 happy path)    -> line 1067
//   - AC2 (resolver succeeds, no quarantine)  -> line 919
// Those tests use the external test package (package git_test) +
// mocks.FakeMetrics. The split is intentional and called out in
// unsafeTestMetrics's doc comment below.

package git

import (
	"context"
	stderrors "errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	libtime "github.com/bborbe/time"
)

// unsafeTestMetrics is a minimal metrics.Metrics implementation used by the
// internal tests in this file. It records per-method call counts and
// arguments so the tests can assert on counters and merge outcomes WITHOUT
// importing mocks.FakeMetrics.
//
// Why not Counterfeiter mocks: the tests in this file live in `package git`
// (internal) because they call unexported symbols (resolveConflictPaths,
// quarantineOne, quarantineDestPath, unsafeConflictPath). The generated mock
// at `mocks/metrics.go` already imports `pkg/git` (counterfeiter directive
// lives there), so importing `mocks` from inside `package git` would create
// a cycle (pkg/git -> mocks -> pkg/git). External-test-package tests in
// `git_test.go` correctly use mocks.FakeMetrics; these internal tests have
// to roll their own.
type unsafeTestMetrics struct {
	mu                      sync.Mutex
	incResolverFailureCalls []string
	incMergeOutcomeCalls    []string
}

func (u *unsafeTestMetrics) ObserveGitOperation(_ string, _ float64) {}

func (u *unsafeTestMetrics) IncGitOperationError(_ string) {}

func (u *unsafeTestMetrics) IncHTTPRequest(_, _, _ string) {}

func (u *unsafeTestMetrics) IncRebaseConflict() {}

func (u *unsafeTestMetrics) IncMergeOutcome(result string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.incMergeOutcomeCalls = append(u.incMergeOutcomeCalls, result)
}

func (u *unsafeTestMetrics) IncConflictPaths(_ int) {}

func (u *unsafeTestMetrics) IncResolverFailure(category string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.incResolverFailureCalls = append(u.incResolverFailureCalls, category)
}

func (u *unsafeTestMetrics) IncQuarantinedFiles() {}

func (u *unsafeTestMetrics) unsafePathCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	n := 0
	for _, c := range u.incResolverFailureCalls {
		if c == "unsafe_path" {
			n++
		}
	}
	return n
}

func (u *unsafeTestMetrics) quarantineIOCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	n := 0
	for _, c := range u.incResolverFailureCalls {
		if c == "quarantine_io_failed" {
			n++
		}
	}
	return n
}

func (u *unsafeTestMetrics) abortedCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	n := 0
	for _, c := range u.incMergeOutcomeCalls {
		if c == "aborted" {
			n++
		}
	}
	return n
}

// fakeResolver is a noop ConflictResolver used to drive the per-file loop into
// the abort branches without depending on MarkerResolver or YAMLMergeResolver.
type fakeResolver struct{}

func (fakeResolver) Resolve(_ context.Context, _ []string) error { return nil }

// TestResolveConflictPathsUnsafePath verifies the AC11 behaviour: a path that
// escapes the repo root (e.g. "../escape.md") is rejected by the
// unsafeConflictPath pre-flight check, the unsafe_path counter increments
// exactly once, the aborted merge outcome is recorded, no file is written
// outside the repo, and no _conflicts/** entry mentions the escape attempt.
//
// This test lives in `package git` (internal) because it constructs *git
// directly and calls the unexported resolveConflictPaths helper with a
// crafted conflictPaths list that a real git merge cannot produce. The
// top-level mocks package cannot be imported here because it would create an
// import cycle (mocks -> pkg/git).
func TestResolveConflictPathsUnsafePath(t *testing.T) {
	ctx := context.Background()
	workDir, err := os.MkdirTemp("", "git-resolve-conflict-paths-*")
	if err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(workDir) })

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = workDir
		out, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("%s %v: %s", "git", args, string(out))
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	run("commit", "--allow-empty", "-q", "-m", "init")

	metrics := &unsafeTestMetrics{}
	repo, ok := New(
		workDir, metrics, libtime.NewCurrentDateTime(), "", fakeResolver{},
	).(*git)
	if !ok {
		t.Fatal("New did not return *git")
	}

	err = repo.resolveConflictPaths(ctx, []string{"../escape.md"})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !stderrors.Is(err, ErrConflictResolutionFailed) {
		t.Fatalf("expected wrapped ErrConflictResolutionFailed, got: %v", err)
	}

	if got := metrics.unsafePathCount(); got != 1 {
		t.Fatalf("unsafe_path counter must increment exactly once, got %d", got)
	}
	if got := metrics.abortedCount(); got != 1 {
		t.Fatalf("aborted outcome must be recorded exactly once, got %d", got)
	}

	externalPath := filepath.Clean(filepath.Join(workDir, "..", "escape.md"))
	if _, statErr := os.Stat(externalPath); !os.IsNotExist(statErr) {
		t.Fatalf(
			"escape path must not exist on disk outside the repo root (stat err: %v)",
			statErr,
		)
	}
	// Negative-evidence check: _conflicts/ MUST NOT exist after an unsafe-path
	// abort. The validateConflictPathsSafe pre-flight runs BEFORE
	// ensureConflictsDir, so the directory is never created on this path.
	// (Earlier impl used `filepath.Glob("_conflicts/**")` which is silently
	// broken in Go — `**` is not a recursive wildcard there — giving false
	// safety assurance. Stat is the precise check.)
	if _, statErr := os.Stat(filepath.Join(workDir, "_conflicts")); !os.IsNotExist(statErr) {
		t.Fatalf(
			"_conflicts/ must not exist after unsafe-path abort (stat err: %v)",
			statErr,
		)
	}
}

// TestUnsafeConflictPathEdges covers the empty-path and absolute-path
// short-circuit branches of unsafeConflictPath that the integration test
// (which only exercises "../escape.md") does not reach.
func TestUnsafeConflictPathEdges(t *testing.T) {
	repoRoot := "/tmp/some-repo"
	cases := []struct {
		name       string
		path       string
		wantUnsafe bool
		wantReason string
	}{
		{"empty path", "", true, "empty path"},
		{"absolute path", "/etc/passwd", true, "absolute path"},
		{"clean relative path stays safe", "tasks/foo.md", false, ""},
		{"nested clean path stays safe", "a/b/c.md", false, ""},
	}
	for _, tc := range cases {
		gotUnsafe, gotReason := unsafeConflictPath(repoRoot, tc.path)
		if gotUnsafe != tc.wantUnsafe {
			t.Errorf("%s: unsafe = %v, want %v", tc.name, gotUnsafe, tc.wantUnsafe)
		}
		if gotReason != tc.wantReason {
			t.Errorf("%s: reason = %q, want %q", tc.name, gotReason, tc.wantReason)
		}
	}
}

// TestQuarantineDestPath covers the .md vs non-.md split, the root-dir vs
// nested-dir paths, and the timestamp-insertion position. Pure function; no
// fixture needed.
func TestQuarantineDestPath(t *testing.T) {
	const ts int64 = 1700000000
	cases := []struct {
		name string
		path string
		want string
	}{
		{
			"md at repo root",
			"note.md",
			filepath.Join("_conflicts", "note.1700000000.md"),
		},
		{
			"md in nested dir",
			"tasks/build/note.md",
			filepath.Join("_conflicts", "tasks/build", "note.1700000000.md"),
		},
		{
			"non-md at repo root",
			"foo.bin",
			filepath.Join("_conflicts", "foo.bin.1700000000.quarantined"),
		},
		{
			"non-md in nested dir",
			"a/b/foo.bin",
			filepath.Join("_conflicts", "a/b", "foo.bin.1700000000.quarantined"),
		},
		{
			"extensionless at repo root",
			"README",
			filepath.Join("_conflicts", "README.1700000000.quarantined"),
		},
	}
	for _, tc := range cases {
		got := quarantineDestPath(tc.path, ts)
		if got != tc.want {
			t.Errorf("%s: quarantineDestPath(%q) = %q, want %q", tc.name, tc.path, got, tc.want)
		}
	}
}

// failingResolver always returns an error, driving the per-file loop into the
// quarantine branch (which then also fails because the path does not exist on
// disk). Used by TestResolveConflictPathsAllFailAbort.
type failingResolver struct{}

func (failingResolver) Resolve(_ context.Context, _ []string) error {
	return stderrors.New("resolver intentionally fails")
}

// TestResolveConflictPathsAllFailAbort verifies the pathological "every path
// fails BOTH resolve and quarantine" branch — the function aborts the merge
// and returns wrapped ErrConflictResolutionFailed.
//
// All paths are safe (no unsafe-path) AND non-existent on disk (so
// quarantineOne's os.ReadFile fails for each), giving the all-fail abort.
func TestResolveConflictPathsAllFailAbort(t *testing.T) {
	ctx := context.Background()
	workDir, err := os.MkdirTemp("", "git-allfail-abort-*")
	if err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(workDir) })

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = workDir
		out, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("%s %v: %s", "git", args, string(out))
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	run("commit", "--allow-empty", "-q", "-m", "init")

	metrics := &unsafeTestMetrics{}
	repo, ok := New(
		workDir, metrics, libtime.NewCurrentDateTime(), "", failingResolver{},
	).(*git)
	if !ok {
		t.Fatal("New did not return *git")
	}

	err = repo.resolveConflictPaths(ctx, []string{"missing-a.md", "missing-b.md"})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !stderrors.Is(err, ErrConflictResolutionFailed) {
		t.Fatalf("expected wrapped ErrConflictResolutionFailed, got: %v", err)
	}
	if got := metrics.abortedCount(); got != 1 {
		t.Fatalf("aborted outcome must be recorded exactly once, got %d", got)
	}
	if got := metrics.quarantineIOCount(); got != 2 {
		t.Fatalf(
			"quarantine_io_failed must increment once per failed path (got %d, want 2)",
			got,
		)
	}

	// Defense-in-depth: _conflicts/ was created by ensureConflictsDir (the
	// paths passed pre-flight) but no quarantine succeeded, so the directory
	// must be empty after the abort.
	conflictsEntries, _ := os.ReadDir(filepath.Join(workDir, "_conflicts"))
	if len(conflictsEntries) != 0 {
		names := make([]string, 0, len(conflictsEntries))
		for _, e := range conflictsEntries {
			names = append(names, e.Name())
		}
		t.Fatalf(
			"_conflicts/ must be empty after all-fail abort; got: %v",
			names,
		)
	}
}

// TestQuarantineOneGitRmFails verifies the git-rm-failure branch of
// quarantineOne increments the quarantine_io_failed counter and returns
// false. Fixture: file exists on disk (so os.ReadFile succeeds) but is NOT
// tracked by git (so `git rm` fails with "pathspec did not match").
func TestQuarantineOneGitRmFails(t *testing.T) {
	ctx := context.Background()
	workDir, err := os.MkdirTemp("", "git-quarantineone-rmfail-*")
	if err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(workDir) })

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = workDir
		out, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("%s %v: %s", "git", args, string(out))
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	run("commit", "--allow-empty", "-q", "-m", "init")

	// File on disk but unstaged -> ReadFile OK, `git rm` fails.
	untracked := filepath.Join(workDir, "untracked.md")
	if writeErr := os.WriteFile(untracked, []byte("---\nbody\n---\n"), 0o600); writeErr != nil {
		t.Fatalf("write untracked: %v", writeErr)
	}

	metrics := &unsafeTestMetrics{}
	repo, ok := New(
		workDir, metrics, libtime.NewCurrentDateTime(), "", fakeResolver{},
	).(*git)
	if !ok {
		t.Fatal("New did not return *git")
	}

	got := repo.quarantineOne(ctx, "untracked.md", 1700000000)
	if got {
		t.Fatal("quarantineOne must return false when git rm fails")
	}
	if c := metrics.quarantineIOCount(); c != 1 {
		t.Fatalf("quarantine_io_failed must increment exactly once, got %d", c)
	}
}

// TestQuarantineOneSourceMissing verifies the quarantine_io_failed counter
// fires when os.ReadFile cannot read the source file (e.g. source already
// removed mid-merge). The per-file loop continues; this test calls
// quarantineOne directly to isolate the failure branch.
func TestQuarantineOneSourceMissing(t *testing.T) {
	ctx := context.Background()
	workDir, err := os.MkdirTemp("", "git-quarantineone-missing-*")
	if err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(workDir) })

	metrics := &unsafeTestMetrics{}
	repo, ok := New(
		workDir, metrics, libtime.NewCurrentDateTime(), "", fakeResolver{},
	).(*git)
	if !ok {
		t.Fatal("New did not return *git")
	}

	// Source file deliberately absent — os.ReadFile inside quarantineOne fails.
	got := repo.quarantineOne(ctx, "missing.md", 1700000000)
	if got {
		t.Fatal("quarantineOne must return false when source file is missing")
	}
	if c := metrics.quarantineIOCount(); c != 1 {
		t.Fatalf("quarantine_io_failed must increment exactly once, got %d", c)
	}
}
