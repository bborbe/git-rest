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

	"github.com/bborbe/git-rest/pkg/git"
)

// rescueFixtureBase is the base content of tasks/x.md, committed to the remote
// and synced into the work clone before the pending write. The file must exist
// on both sides at a common ancestor so the retry merge can succeed on
// non-overlapping hunks; an add/add of a brand-new path always conflicts, which
// would fail the flush before it ever reaches the blocking push.
const rescueFixtureBase = "---\nid: x\n---\n\nalpha\nbravo\ncharlie\ndelta\necho\nfoxtrot\n"

// setupFlushRescueFixture builds the four AC1 preconditions and returns the
// client and the bare origin. The common base is created first: tasks/x.md must
// exist in the local history too, so the remote divergence is a modification of
// the same path rather than an add/add conflict. An add/add conflict would fail
// the flush's retry merge immediately, before the flush could consume any
// budget, and the fixture would stop exercising the defect.
func setupFlushRescueFixture(t *testing.T) (git.Git, string) {
	t.Helper()
	client, work, bare := newCoalesceFixture(t, git.CoalesceConfig{
		Enabled:  true,
		Delay:    time.Hour,
		MaxDelay: time.Hour,
	})
	ctx := context.Background()

	remote := filepath.Join(t.TempDir(), "remote")
	mustGit(t, filepath.Dir(remote), "clone", bare, remote)
	configureClaimIdentity(t, remote)
	if err := os.MkdirAll(filepath.Join(remote, "tasks"), 0750); err != nil {
		t.Fatalf("mkdir remote tasks: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(remote, "tasks", "x.md"),
		[]byte(rescueFixtureBase),
		0600,
	); err != nil {
		t.Fatalf("write remote tasks/x.md: %v", err)
	}
	mustGit(t, remote, "add", "-A")
	mustGit(t, remote, "commit", "-m", "base tasks/x.md")
	mustGit(t, remote, "push", "origin", "main")
	mustGit(t, work, "fetch")
	mustGit(t, work, "merge", "--ff-only", "origin/main")

	// Precondition 1: a non-empty pending set and a dirty working tree on the
	// same path. Writing through the write path enqueues AND stages; a direct
	// disk write would leave the pending set empty and consume no budget. The
	// appended line is non-overlapping with the remote edit below.
	if err := client.WriteFile(
		ctx,
		"tasks/x.md",
		[]byte(rescueFixtureBase+"local\n"),
	); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Preconditions 2 and 3: the remote carries a commit the local branch does
	// not, touching tasks/x.md — the same path the pending write staged — in a
	// region that does not overlap the local append. The non-overlap matters:
	// the flush's retry path merges the remote in, and only a clean merge lets
	// the retry push become fast-forward and reach the blocking pre-receive.
	remoteContent := strings.Replace(rescueFixtureBase, "id: x", "id: remote", 1)
	if err := os.WriteFile(
		filepath.Join(remote, "tasks", "x.md"),
		[]byte(remoteContent),
		0600,
	); err != nil {
		t.Fatalf("rewrite remote tasks/x.md: %v", err)
	}
	mustGit(t, remote, "add", "-A")
	mustGit(t, remote, "commit", "-m", "remote change")
	mustGit(t, remote, "push", "origin", "main")

	// Precondition 4: install the stall only after the divergence push. It
	// sleeps past the whole 10 s pull context and rejects refs/heads/main while
	// allowing refs/heads/rescue/* immediately.
	installBlockingPreReceive(t, bare)
	return client, bare
}

// TestFlushRescueLandsAfterFlushDeadline reproduces the production starvation:
// a flush that cannot complete, a diverged remote touching the same path as the
// pending write, and a remote stall that outlasts the whole pull budget. On the
// fixed tree the flush is cut at its own deadline and the dirty-tree rescue
// still pushes a rescue/* ref to the remote. On the pre-fix tree the flush
// consumes the pull budget and the rescue never runs.
func TestFlushRescueLandsAfterFlushDeadline(t *testing.T) {
	client, bare := setupFlushRescueFixture(t)

	buf, restore := captureSlogLogs()
	defer restore()

	pullCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pullErr := client.Pull(pullCtx)

	// Assertion 1 — the rescue ref must exist on the REMOTE. This is first and
	// fatal so the pre-fix failure is reported as the missing rescue, not as a
	// downstream log mismatch. --branches=PATTERN returns 0 for no match, unlike
	// a bare glob revision argument which git rev-list rejects with a usage error.
	count := strings.TrimSpace(mustGit(t, bare, "rev-list", "--count", "--branches=rescue/*"))
	if count == "0" {
		t.Fatalf(
			"no rescue ref pushed to origin (count=%s), pullErr=%v, logs:\n%s",
			count,
			pullErr,
			buf.String(),
		)
	}

	// Assertion 2 — the rescue completed and logged its push.
	if !strings.Contains(buf.String(), "rescue branch pushed") {
		t.Fatalf("want rescue push log, logs:\n%s", buf.String())
	}

	// Assertion 3 — the rescue's git add -A was never killed: a killed command
	// surfaces its argv inside an error log line.
	if strings.Contains(buf.String(), "git [add -A]") {
		t.Fatalf("rescue git add -A was killed, logs:\n%s", buf.String())
	}

	// Assertion 4 — the flush was cut by its own deadline. The trailing
	// "before pull" distinguishes this from the SIGTERM shutdown path, which
	// logs the prefix "git-rest: flush deadline exceeded".
	if got := strings.Count(
		buf.String(),
		"git-rest: flush deadline exceeded before pull",
	); got < 1 {
		t.Fatalf("want flush cut-off log, got %d, logs:\n%s", got, buf.String())
	}

	// Assertion 5 — fixture self-guard: the flush actually failed. Without this
	// an empty pending set would return immediately, consume no budget, and let
	// assertion 1 pass on a tree that never exercised the defect.
	if !strings.Contains(buf.String(), "git-rest: flush pending writes before pull failed") {
		t.Fatalf("want flush failure log, logs:\n%s", buf.String())
	}
}
