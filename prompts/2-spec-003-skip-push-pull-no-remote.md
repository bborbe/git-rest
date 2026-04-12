---
spec: ["003"]
status: created
created: "2026-04-12T17:47:00Z"
---

<summary>
- Pull operations silently skip when the repository has no remote configured
- Push operations silently skip after commit when no remote is configured
- The remote check is performed per-operation, not cached at startup
- The readiness endpoint returns 200 for clean local-only repos without a remote
- Repos with a remote continue to push and pull normally
- File create, update, and delete operations all work in local-only repos
</summary>

<objective>
Make push, pull, and readiness operations gracefully handle repos without a remote. Push and pull skip silently when `git remote` returns no remotes. Readiness reports healthy when the working tree is clean, regardless of remote existence. This enables git-rest to run as a fully local file versioning service.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Read coding guides before implementing (in `~/.claude/plugins/marketplaces/coding/docs/`):
- `go-error-wrapping-guide.md`: error wrapping rules

Files to read before making changes (read ALL first):
- `pkg/git/git.go` — `WriteFile` (~line 160), `DeleteFile` (~line 212), `Pull` (~line 322), `Status` (~line 370)
- `pkg/puller/puller.go` — `Run` method (~line 38), calls `g.Pull`
- `pkg/handler/readiness.go` — checks `status.Clean` and `status.NoPushPending`
- `main.go` — `bootstrap` method already handles init (added by prior prompt)
</context>

<requirements>

## 1. Add hasRemote helper method to git struct

In `pkg/git/git.go`, add a private method to check if the repo has any configured remotes:

```go
// hasRemote returns true if the repository has at least one configured remote.
// This is called per-operation to support repos that may gain a remote after startup.
func (g *git) hasRemote(ctx context.Context) bool {
    out, err := g.runCmdOutput(ctx, g.repoPath, "remote")
    if err != nil {
        return false
    }
    return strings.TrimSpace(string(out)) != ""
}
```

Note: this is called inside methods that already hold `g.mu`, so it must NOT acquire the mutex itself. The `runCmd`/`runCmdOutput` methods do not acquire the mutex — only the public methods do.

## 2. Skip push in WriteFile when no remote

In `pkg/git/git.go`, `WriteFile` method (~line 203), replace the unconditional push:

```go
// Before (current):
if err := g.runCmd(ctx, g.repoPath, "push"); err != nil {
    g.metrics.IncGitOperationError("write_file")
    return errors.Wrap(ctx, err, "git push")
}

// After:
if g.hasRemote(ctx) {
    if err := g.runCmd(ctx, g.repoPath, "push"); err != nil {
        g.metrics.IncGitOperationError("write_file")
        return errors.Wrap(ctx, err, "git push")
    }
}
```

## 3. Skip push in DeleteFile when no remote

In `pkg/git/git.go`, `DeleteFile` method (~line 242), apply the same pattern:

```go
if g.hasRemote(ctx) {
    if err := g.runCmd(ctx, g.repoPath, "push"); err != nil {
        g.metrics.IncGitOperationError("delete_file")
        return errors.Wrap(ctx, err, "git push")
    }
}
```

## 4. Skip pull when no remote

In `pkg/git/git.go`, `Pull` method (~line 322), add a remote check before pulling:

```go
func (g *git) Pull(ctx context.Context) error {
    start := g.currentDateTimeGetter.Now()
    defer func() {
        g.metrics.ObserveGitOperation("pull", time.Since(time.Time(start)).Seconds())
    }()

    g.mu.Lock()
    defer g.mu.Unlock()

    if !g.hasRemote(ctx) {
        return nil
    }

    if err := g.runCmd(ctx, g.repoPath, "pull"); err != nil {
        g.metrics.IncGitOperationError("pull")
        return errors.Wrap(ctx, err, "git pull")
    }
    return nil
}
```

## 5. Fix Status for repos without remote

In `pkg/git/git.go`, `Status` method (~line 370), the `git log @{u}..HEAD` command already handles no-upstream gracefully (errors → `NoPushPending = true`). Verify this works for repos with no remote at all — the `@{u}` reference will fail, which sets `NoPushPending = true`. This is the correct behavior: no remote means nothing to push.

No code change needed here if the current error handling already covers it, but verify by reading the code. If `git log @{u}..HEAD` produces an error for repos without any remote, the current fallback `s.NoPushPending = true` is correct.

## 6. Add tests for hasRemote

In `pkg/git/git_test.go`, add a `Describe("hasRemote")` block (testing via the public methods that depend on it):

- Repo with remote → push and pull work normally
- Repo without remote → push skipped after commit, pull returns nil

## 7. Add integration-style tests for local-only repo operations

In `pkg/git/git_test.go`, add test cases:

- **WriteFile in local-only repo**: create file → commit succeeds → no push error → file is readable
- **DeleteFile in local-only repo**: create file, then delete → commit succeeds → no push error → file gone
- **Pull in local-only repo**: returns nil (no error, no-op)

These tests use a repo created with `git init` (no remote added).

## 8. Add readiness test for local-only repo

In `pkg/handler/readiness_test.go`, add a test case:
- Git status returns `Clean: true, NoPushPending: true` (local repo with nothing pending) → handler returns 200

This should already pass with the existing mock-based tests, but verify and add if missing.

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- No new external dependencies
- Must not break spec 002 behavior (remote clone still works, push still happens for repos with remotes)
- Push skip must be per-operation (check `git remote` each time), not a global flag set at startup
- Use `errors.Wrap`/`errors.Wrapf` from `github.com/bborbe/errors` — never `fmt.Errorf` or bare `return err`
- Backward compatible: repos with a remote continue to push and pull normally
</constraints>

<verification>
make precommit
</verification>
