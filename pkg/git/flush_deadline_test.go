// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git_test

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bborbe/git-rest/pkg/git"
)

// installBlockingPreReceive installs a pre-receive hook on the bare origin that
// sleeps past a 10 s pull context and rejects updates to refs/heads/main, while
// allowing any other ref (notably refs/heads/rescue/*) immediately.
func installBlockingPreReceive(t *testing.T, bare string) {
	t.Helper()
	hooksDir := filepath.Join(bare, "hooks")
	if err := os.MkdirAll(hooksDir, 0750); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	// The loop bounds the block at 15 s (75 * 0.2 s), long enough to outlast the
	// whole 10 s pull context. It also stops as soon as the pushing client is
	// gone: killing the local git push leaves receive-pack (and this hook)
	// orphaned, holding the caller's stderr pipe open until the sleep ends. The
	// ancestor poll keeps the cut-flush timing observable without weakening the
	// 15 s bound.
	script := "#!/bin/sh\n" +
		"input=$(cat)\n" +
		"case \"$input\" in\n" +
		"  *refs/heads/main*)\n" +
		"    gp=$(ps -o ppid= -p \"$PPID\" 2>/dev/null | tr -d ' ')\n" +
		"    gpp=$(ps -o ppid= -p \"$gp\" 2>/dev/null | tr -d ' ')\n" +
		"    i=0\n" +
		"    while [ \"$i\" -lt 75 ]; do\n" +
		"      if [ -n \"$gpp\" ] && ! ps -p \"$gpp\" >/dev/null 2>&1; then\n" +
		"        exit 1\n" +
		"      fi\n" +
		"      sleep 0.2\n" +
		"      i=$((i+1))\n" +
		"    done\n" +
		"    echo \"remote rejected: refs/heads/main is locked\" >&2\n" +
		"    exit 1\n" +
		"    ;;\n" +
		"esac\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-receive"), []byte(script), 0750); err != nil {
		t.Fatalf("write pre-receive hook: %v", err)
	}
	// Point the bare remote at its OWN hooks directory. A global core.hooksPath
	// (set on this host to ~/.git-hooks) otherwise overrides $GIT_DIR/hooks, the
	// hook never runs, and the push succeeds instead of stalling — the same guard
	// installPreReceiveRejection and the coalesce fixtures already carry.
	mustGit(t, bare, "config", "core.hooksPath", "hooks")
}

// captureSlogLogsAtDebug is captureSlogLogs with a handler that emits Debug.
func captureSlogLogsAtDebug() (*bytes.Buffer, func()) {
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(
		slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
	)
	return buf, func() { slog.SetDefault(prev) }
}

// budgetLinePattern captures the three pre-formatted key=value pairs of the
// per-cycle budget line.
var budgetLinePattern = regexp.MustCompile(
	`git-rest: pull budget pull_start=([^\s"]+) pull_deadline=([^\s"]+) flush_deadline=([^\s"]+)`,
)

// TestFlushBudgetLine proves Pull logs exactly one budget line per cycle and
// that the flush deadline is at most a third of the remaining pull budget.
func TestFlushBudgetLine(t *testing.T) {
	client, _, _ := newCoalesceFixture(t, git.CoalesceConfig{
		Enabled:  true,
		Delay:    time.Hour,
		MaxDelay: time.Hour,
	})

	buf, restore := captureSlogLogs()
	defer restore()

	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Second)
	defer cancel()
	if err := client.Pull(ctx); err != nil {
		t.Fatalf("pull: %v", err)
	}

	if got := strings.Count(buf.String(), "git-rest: pull budget"); got != 1 {
		t.Fatalf("want exactly 1 budget line, got %d in %q", got, buf.String())
	}
	match := budgetLinePattern.FindStringSubmatch(buf.String())
	if match == nil {
		t.Fatalf("budget line not found in %q", buf.String())
	}
	pullStart, err := time.Parse(time.RFC3339Nano, match[1])
	if err != nil {
		t.Fatalf("parse pull_start %q: %v", match[1], err)
	}
	pullDeadline, err := time.Parse(time.RFC3339Nano, match[2])
	if err != nil {
		t.Fatalf("parse pull_deadline %q: %v", match[2], err)
	}
	flushDeadline, err := time.Parse(time.RFC3339Nano, match[3])
	if err != nil {
		t.Fatalf("parse flush_deadline %q: %v", match[3], err)
	}
	if flushDeadline.Sub(pullStart) > pullDeadline.Sub(pullStart)/3+time.Millisecond {
		t.Fatalf(
			"flush deadline %v exceeds a third of the pull budget %v",
			flushDeadline.Sub(pullStart),
			pullDeadline.Sub(pullStart),
		)
	}
	if !flushDeadline.Before(pullDeadline) {
		t.Fatalf("flush deadline %v not before pull deadline %v", flushDeadline, pullDeadline)
	}

	debugBuf, debugRestore := captureSlogLogsAtDebug()
	defer debugRestore()
	if err := client.Pull(context.Background()); err != nil {
		t.Fatalf("pull without deadline: %v", err)
	}
	if !strings.Contains(debugBuf.String(), "flush_deadline=none") {
		t.Fatalf("want flush_deadline=none in %q", debugBuf.String())
	}
	if !strings.Contains(debugBuf.String(), "git-rest: flush deadline skipped: no pull deadline") {
		t.Fatalf("want skipped-deadline log in %q", debugBuf.String())
	}
}

// TestFlushDeadlineCut proves a flush that cannot finish is cut at its own
// deadline, keeps the pending batch staged and does not fail the pull.
func TestFlushDeadlineCut(t *testing.T) {
	client, work, bare := newCoalesceFixture(t, git.CoalesceConfig{
		Enabled:  true,
		Delay:    time.Hour,
		MaxDelay: time.Hour,
	})
	ctx := context.Background()
	if err := client.WriteFile(ctx, "tasks/x.md", []byte("L")); err != nil {
		t.Fatalf("write: %v", err)
	}
	installBlockingPreReceive(t, bare)

	buf, restore := captureSlogLogs()
	defer restore()

	pullCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started := time.Now()
	err := client.Pull(pullCtx)
	elapsed := time.Since(started)

	if err != nil {
		t.Fatalf("pull after cut flush: %v", err)
	}
	if status := mustGit(t, work, "status", "--porcelain"); !strings.Contains(
		status,
		"tasks/x.md",
	) {
		t.Fatalf("want tasks/x.md still staged, status %q", status)
	}
	if !strings.Contains(buf.String(), "git-rest: flush deadline exceeded before pull") {
		t.Fatalf("want cut-off log in %q", buf.String())
	}
	if elapsed >= 6*time.Second {
		t.Fatalf("flush was not bounded: pull took %v", elapsed)
	}
}

// TestFlushHealthyRemoteUnchanged proves a flush that fits the budget still
// lands before the merge, exactly once, with no cut-off log.
func TestFlushHealthyRemoteUnchanged(t *testing.T) {
	client, _, bare := newCoalesceFixture(t, git.CoalesceConfig{
		Enabled:  true,
		Delay:    time.Hour,
		MaxDelay: time.Hour,
	})
	ctx := context.Background()

	buf, restore := captureSlogLogs()
	defer restore()

	if err := client.WriteFile(ctx, "tasks/x.md", []byte("H")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := client.Pull(ctx); err != nil {
		t.Fatalf("pull: %v", err)
	}

	subjects := originGitRestSubjects(t, bare, "main", "tasks/x.md")
	if len(subjects) != 1 {
		t.Fatalf("want exactly 1 commit, got %d: %v", len(subjects), subjects)
	}
	pattern := regexp.MustCompile(`^git-rest: (create|update) tasks/x\.md$`)
	if !pattern.MatchString(subjects[0]) {
		t.Fatalf("unexpected subject %q", subjects[0])
	}
	if got := strings.Count(buf.String(), "git-rest: flush deadline exceeded before pull"); got != 0 {
		t.Fatalf("want no cut-off log, got %d in %q", got, buf.String())
	}
	if !strings.Contains(buf.String(), "git-rest: flushed pending writes") {
		t.Fatalf("want flush log in %q", buf.String())
	}
}

// TestFlushCoalescingOff proves the off-switch leaves the pull path untouched.
func TestFlushCoalescingOff(t *testing.T) {
	client, _, bare := newCoalesceFixture(t, git.CoalesceConfig{Enabled: false})
	ctx := context.Background()

	buf, restore := captureSlogLogs()
	defer restore()

	// tasks/ paths require frontmatter on overwrite; the content differs so each
	// write produces a commit.
	for _, content := range []string{
		"---\nid: a\n---\nA",
		"---\nid: b\n---\nB",
	} {
		if err := client.WriteFile(ctx, "tasks/x.md", []byte(content)); err != nil {
			t.Fatalf("write %q: %v", content, err)
		}
	}
	if err := client.Pull(ctx); err != nil {
		t.Fatalf("pull: %v", err)
	}

	if got := strings.Count(buf.String(), "git-rest: flushed pending writes"); got != 0 {
		t.Fatalf("want no flush log, got %d in %q", got, buf.String())
	}
	if got := strings.Count(buf.String(), "git-rest: flush deadline exceeded before pull"); got != 0 {
		t.Fatalf("want no cut-off log, got %d in %q", got, buf.String())
	}
	subjects := originGitRestSubjects(t, bare, "main", "tasks/x.md")
	if len(subjects) != 2 {
		t.Fatalf("want 2 commits, got %d: %v", len(subjects), subjects)
	}
}
