// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

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
// internal unsafe-path test. It records per-method call counts and arguments
// so the test can assert on the unsafe_path counter and aborted merge outcome
// without importing the top-level mocks package (which would create an import
// cycle: pkg/git_test -> mocks -> pkg/git).
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
	matches, _ := filepath.Glob(filepath.Join(workDir, "_conflicts", "**", "*escape*"))
	if len(matches) != 0 {
		t.Fatalf(
			"no _conflicts/** entry may mention the escape attempt; got: %v",
			matches,
		)
	}
}
