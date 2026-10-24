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
