---
status: approved
spec: ["003"]
created: "2026-04-12T18:00:00Z"
queued: "2026-04-12T17:53:40Z"
---

<summary>
- File write and delete operations succeed in a local-only repository — push is silently skipped when no remote is configured
- The periodic puller does not produce errors for local-only repositories — pull is silently skipped when no remote is configured
- Remote detection is per-operation: each push/pull checks for a configured remote immediately before the network call
- Repositories with a configured remote continue to push and pull exactly as before
- The readiness endpoint already returns 200 for clean local-only repos (no additional changes needed — Status already sets NoPushPending=true when no upstream)
- Existing tests are updated to reflect the new non-error behavior for pull without remote
- New tests cover write, delete, and pull in a local-only repo
</summary>

<objective>
Make push and pull operations gracefully no-op when the git repository has no configured remote. Write and delete operations should commit locally and skip push; the puller should skip pull. All changes are per-operation checks — not a global flag.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Read coding guides before implementing (in `~/.claude/plugins/marketplaces/coding/docs/`):
- `go-error-wrapping-guide.md`: error wrapping rules — never fmt.Errorf
- `go-patterns.md`: method patterns, mutex usage
- `go-testing-guide.md`: Ginkgo/Gomega test patterns

Files to read before making changes:
- `pkg/git/git.go` — `Pull` (~line 322), `WriteFile` (~line 160), `DeleteFile` (~line 212), `runCmdOutput` (~line 137)
- `pkg/git/git_test.go` — the `"Git with no remote configured"` Describe block (around line 422) which currently expects Pull to return an error — this must change
- `pkg/puller/puller.go` — the pull loop warning log (already uses `slog.WarnContext` on pull error)
</context>

<requirements>

## 1. Add hasRemote helper to git struct

In `pkg/git/git.go`, add a private helper method (no mutex — callers hold it when needed, or call before locking):
```go
// hasRemote reports whether the repository has at least one configured remote.
// It runs git remote and returns true when the output is non-empty.
// Errors are treated as "no remote" to avoid blocking operations.
func (g *git) hasRemote(ctx context.Context) bool {
	out, err := g.runCmdOutput(ctx, g.repoPath, "remote")
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}
```

Important: `hasRemote` calls `runCmdOutput` which does NOT acquire the mutex. It must only be called from within a method that already holds `g.mu`, OR before the lock is taken. In the implementations below it is called INSIDE the locked section, after `g.mu.Lock()`.

## 2. Skip push in WriteFile when no remote

In `pkg/git/git.go`, in `WriteFile` (~line 198), the current push call is:
```go
if err := g.runCmd(ctx, g.repoPath, "push"); err != nil {
    g.metrics.IncGitOperationError("write_file")
    return errors.Wrap(ctx, err, "git push")
}
```

Replace it with:
```go
if g.hasRemote(ctx) {
    if err := g.runCmd(ctx, g.repoPath, "push"); err != nil {
        g.metrics.IncGitOperationError("write_file")
        return errors.Wrap(ctx, err, "git push")
    }
}
```

## 3. Skip push in DeleteFile when no remote

In `pkg/git/git.go`, in `DeleteFile` (~line 242), the current push call is:
```go
if err := g.runCmd(ctx, g.repoPath, "push"); err != nil {
    g.metrics.IncGitOperationError("delete_file")
    return errors.Wrap(ctx, err, "git push")
}
```

Replace it with:
```go
if g.hasRemote(ctx) {
    if err := g.runCmd(ctx, g.repoPath, "push"); err != nil {
        g.metrics.IncGitOperationError("delete_file")
        return errors.Wrap(ctx, err, "git push")
    }
}
```

## 4. Skip pull in Pull when no remote

In `pkg/git/git.go`, in `Pull` (~line 322), replace:
```go
func (g *git) Pull(ctx context.Context) error {
	start := g.currentDateTimeGetter.Now()
	defer func() {
		g.metrics.ObserveGitOperation("pull", time.Since(time.Time(start)).Seconds())
	}()

	g.mu.Lock()
	defer g.mu.Unlock()

	if err := g.runCmd(ctx, g.repoPath, "pull"); err != nil {
		g.metrics.IncGitOperationError("pull")
		return errors.Wrap(ctx, err, "git pull")
	}
	return nil
}
```

With:
```go
func (g *git) Pull(ctx context.Context) error {
	start := g.currentDateTimeGetter.Now()
	defer func() {
		g.metrics.ObserveGitOperation("pull", time.Since(time.Time(start)).Seconds())
	}()

	g.mu.Lock()
	defer g.mu.Unlock()

	if !g.hasRemote(ctx) {
		slog.DebugContext(ctx, "git pull skipped: no remote configured")
		return nil
	}

	if err := g.runCmd(ctx, g.repoPath, "pull"); err != nil {
		g.metrics.IncGitOperationError("pull")
		return errors.Wrap(ctx, err, "git pull")
	}
	return nil
}
```

`pkg/git/git.go` does NOT currently import `log/slog`. Add it to the import block.

## 5. Update existing no-remote Pull test

In `pkg/git/git_test.go`, in the `"Git with no remote configured"` Describe block, the test currently expects Pull to return an error:
```go
It("Pull returns error", func() {
    err := noRemoteGit.Pull(ctx)
    Expect(err).To(HaveOccurred())
})
```

Change this test to expect no error:
```go
It("Pull succeeds and skips when no remote configured", func() {
    err := noRemoteGit.Pull(ctx)
    Expect(err).NotTo(HaveOccurred())
})
```

## 6. Add new no-remote tests for WriteFile and DeleteFile

In `pkg/git/git_test.go`, in the existing `"Git with no remote configured"` Describe block, add:

```go
It("WriteFile succeeds without pushing when no remote configured", func() {
    // Write a file — should commit locally without error
    err := noRemoteGit.WriteFile(ctx, "local.txt", []byte("local content"))
    Expect(err).NotTo(HaveOccurred())

    // File must be readable
    content, err := noRemoteGit.ReadFile(ctx, "local.txt")
    Expect(err).NotTo(HaveOccurred())
    Expect(content).To(Equal([]byte("local content")))

    // Commit must exist in git log
    out, err := exec.Command("git", "-C", noRemoteDir, "log", "--oneline", "-1").Output()
    Expect(err).NotTo(HaveOccurred())
    Expect(string(out)).To(ContainSubstring("git-rest: create local.txt"))
})

It("DeleteFile succeeds without pushing when no remote configured", func() {
    // Create a file first
    err := noRemoteGit.WriteFile(ctx, "todelete.txt", []byte("bye"))
    Expect(err).NotTo(HaveOccurred())

    // Delete it — should commit locally without error
    err = noRemoteGit.DeleteFile(ctx, "todelete.txt")
    Expect(err).NotTo(HaveOccurred())

    // File must be gone
    _, err = noRemoteGit.ReadFile(ctx, "todelete.txt")
    Expect(err).To(MatchError(git.ErrNotFound))
})
```

The `"Git with no remote configured"` BeforeEach already sets up `noRemoteDir` and `noRemoteGit`. These tests can reference them directly.

## 7. Add CHANGELOG entry

In `CHANGELOG.md`, add a bullet under the `## Unreleased` section (created by the prior prompt):
```
- feat: Skip push and pull operations gracefully when no remote is configured
```

If `## Unreleased` does not exist yet, create it above `## v0.10.0`.

## 8. Verify readiness is already correct

The readiness handler uses `Status`. In `pkg/git/git.go`, `Status` already contains:
```go
// Check for commits not yet pushed; if no upstream is configured, treat as no push pending.
out, err = g.runCmdOutput(ctx, g.repoPath, "log", "@{u}..HEAD", "--oneline")
if err != nil {
    s.NoPushPending = true
} else {
    s.NoPushPending = strings.TrimSpace(string(out)) == ""
}
```

This means `NoPushPending=true` for a repo with no upstream — which causes the readiness handler to return 200 for a clean local-only repo. No change is needed here. Just verify with the existing `"Status sets NoPushPending=true when no upstream"` test.

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass (except the Pull-returns-error test which is intentionally updated)
- Remote detection is per-operation — no global flag
- Must not break spec 002 behavior: repos with a remote continue to push and pull normally
- No new external dependencies
- Use `errors.Wrap`/`errors.Wrapf` from `github.com/bborbe/errors` — never `fmt.Errorf`
- `context.Background()` must NOT appear in `pkg/` — only in `main.go`
- Push skip must be conditional on `hasRemote`, not on any global flag
</constraints>

<verification>
Run `make precommit` — must pass.

Key test cases to verify manually if needed:
```bash
# No-remote repo: write should succeed
REPO=$(mktemp -d)
git -C "$REPO" init
git -C "$REPO" config user.email test@test.com
git -C "$REPO" config user.name Test
go run . --listen :18080 --repo "$REPO" &
sleep 1
curl -s -X POST http://localhost:18080/api/v1/files/hello.txt -d 'hello'
curl -s http://localhost:18080/readiness  # should return ok
kill %1
```
</verification>
