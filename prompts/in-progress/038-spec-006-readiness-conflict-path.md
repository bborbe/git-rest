---
status: approved
spec: [006-bug-pull-cannot-recover-from-divergence]
created: "2026-05-04T20:00:00Z"
queued: "2026-05-04T20:12:59Z"
branch: dark-factory/bug-pull-cannot-recover-from-divergence
---

<summary>
- Readiness immediately returns 503 on rebase content conflicts — no waiting for the freshness threshold
- The 503 body names the conflicting file path: `last pull failed: rebase conflict at <path>`
- The substring "Need to specify how to reconcile divergent branches" can never appear in the 503 body
- After a successful pull following a conflict, readiness returns to 200 automatically
- Transient errors (network, auth) continue to use the existing freshness-threshold approach — no change to their behavior
- Conflict-immediate 503 is tested in `pkg/puller/pull_state_test.go` using `*git.RebaseConflictError` directly (no bare-repo fixture needed)
- `make precommit` passes; CHANGELOG updated with the readiness improvement
</summary>

<objective>
Update `PullStateCache.ReadinessStatus()` in `pkg/puller/pull_state.go` to check for `*git.RebaseConflictError` and return 503 immediately — before the freshness threshold — with a body naming the conflicting file. Transient errors retain the existing stale-freshness behavior. Add tests in `pkg/puller/pull_state_test.go` for the new conflict-immediate path.

Precondition: `pkg/git/git.go` must define `RebaseConflictError`. Verify before starting:

```bash
grep -n "RebaseConflictError" /workspace/pkg/git/git.go
```

If missing, stop and report `status: failed` with message "RebaseConflictError not found — run 1-spec-006-pull-state-machine first".
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Read these coding guides before implementing (in `~/.claude/plugins/marketplaces/coding/docs/`):
- `go-patterns.md` — interface→struct→constructor; interfaces at point of use
- `go-error-wrapping-guide.md` — `errors.As` for type-checking; distinguish from error creation
- `go-testing-guide.md` — Ginkgo/Gomega patterns, external test packages (`package puller_test`)

Files to read in full before implementing:
- `pkg/puller/pull_state.go` — `PullStateCache`, `ReadinessStatus()`, `RecordPull()`; you will update `ReadinessStatus()`
- `pkg/puller/pull_state_test.go` — existing test suite for `PullStateCache`; you will add new `Describe` blocks
- `pkg/git/git.go` — `RebaseConflictError` struct and its `Error()` method (added by prompt 1); read these before implementing the type check
- `CHANGELOG.md` — append to existing `## Unreleased` section
</context>

<requirements>

## 1. Update imports in `pkg/puller/pull_state.go`

Add these two imports to the existing import block:

```go
import (
    stderrors "errors"         // for errors.As — type-checking only, not error creation
    "fmt"
    "sync"
    "time"

    libtime "github.com/bborbe/time"

    "github.com/bborbe/git-rest/pkg/git"
)
```

Note: `stderrors` is the stdlib `errors` aliased to avoid collision with the `github.com/bborbe/errors` wrapping package that other files in the project use. `errors.As` is a type-check operation, not error creation — using stdlib is correct here.

## 2. Update `RecordPull()` and `ReadinessStatus()` in `pkg/puller/pull_state.go`

Two changes:
1. Add a sticky `lastConflict *git.RebaseConflictError` field to the struct. Conflicts are sticky — they're only cleared by a successful pull, NOT by a subsequent transient error. Without stickiness, a transient network error after a conflict would overwrite `lastErr` and the conflict path would disappear from the readiness body until the next conflict re-asserts.
2. Update `RecordPull()` to set/clear `lastConflict` correctly, and `ReadinessStatus()` to check `lastConflict` first.

```go
type PullStateCache struct {
	mu                 sync.RWMutex
	currentDateTime    libtime.CurrentDateTimeGetter
	lastSuccessAt      libtime.DateTime              // zero = no successful pull yet
	lastErr            error                         // most recent error (transient or conflict); nil = last pull succeeded
	lastConflict       *git.RebaseConflictError      // sticky: cleared only by a successful pull
	freshnessThreshold time.Duration
}

// RecordPull records the outcome of one pull attempt at the current time.
// On success: lastSuccessAt is updated, lastErr is cleared, AND lastConflict is cleared.
// On rebase conflict: lastConflict is set (sticky until next success), lastErr also stores it.
// On transient error: lastErr is updated; lastConflict is NOT touched (preserves conflict visibility
// in readiness body across intervening transient errors).
func (c *PullStateCache) RecordPull(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err == nil {
		c.lastSuccessAt = c.currentDateTime.Now()
		c.lastErr = nil
		c.lastConflict = nil
		return
	}

	c.lastErr = err

	var conflictErr *git.RebaseConflictError
	if stderrors.As(err, &conflictErr) {
		c.lastConflict = conflictErr
	}
	// Transient errors (non-conflict) do NOT clear lastConflict.
}

// ReadinessStatus returns (true, "ok") when the cache is healthy, or
// (false, reason) when the pod should not receive traffic.
//
// Rules (checked in order):
//  1. lastConflict is set → immediate 503 naming the conflict path (sticky until resolved by success)
//  2. lastSuccessAt is zero → "no successful pull yet"
//  3. time since lastSuccessAt > freshnessThreshold → stale; include last error if any
//  4. Otherwise → ready
func (c *PullStateCache) ReadinessStatus() (bool, string) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Rebase conflicts are hard errors: return 503 immediately, before the
	// freshness window expires. Sticky — only a successful pull clears this.
	if c.lastConflict != nil {
		return false, "last pull failed: rebase conflict at " + c.lastConflict.Path
	}

	if time.Time(c.lastSuccessAt).IsZero() {
		return false, "no successful pull yet"
	}
	age := time.Time(c.currentDateTime.Now()).Sub(time.Time(c.lastSuccessAt))
	if c.freshnessThreshold > 0 && age > c.freshnessThreshold {
		msg := fmt.Sprintf("last successful pull stale (%v ago)", age.Round(time.Second))
		if c.lastErr != nil {
			msg += ": last pull failed: " + c.lastErr.Error()
		}
		return false, msg
	}
	return true, "ok"
}
```

Leave all other code in `pkg/puller/pull_state.go` (license header, package decl, `PullStateWriter` interface, `NewPullStateCache` constructor) unchanged. Apply only the three changes above: imports, struct field, `RecordPull`, `ReadinessStatus`.

**Trade-off note for the doc-comment:** between "operator resolves conflict remotely" and "next successful local pull tick", `lastConflict` is shown even though the conflict no longer exists on disk. This is bounded by `PullInterval` and preferred over the alternative — transient errors silently masking unresolved conflicts.

## 3. Add conflict-immediate tests to `pkg/puller/pull_state_test.go`

Add a new `Describe` block inside the existing `var _ = Describe("PullStateCache", func() { ... })`. Place it after the existing `Describe("zero freshness threshold", ...)` block.

The test file is in `package puller_test` — it needs an import for `"github.com/bborbe/git-rest/pkg/git"` to construct `*git.RebaseConflictError` values.

Add to the import block in `pull_state_test.go`:
```go
"github.com/bborbe/git-rest/pkg/git"
```

New test block to append inside the existing top-level `Describe`:

```go
Describe("rebase conflict (immediate 503)", func() {
	Context("after a prior success, followed by a rebase conflict", func() {
		It("returns not-ready immediately with conflict path", func() {
			cache := puller.NewPullStateCache(clock, freshness)
			cache.RecordPull(nil) // prior success
			cache.RecordPull(&git.RebaseConflictError{Path: "tasks/foo.md"})
			ready, reason := cache.ReadinessStatus()
			Expect(ready).To(BeFalse())
			Expect(reason).To(Equal("last pull failed: rebase conflict at tasks/foo.md"))
		})

		It("returns 503 before the freshness threshold expires", func() {
			cache := puller.NewPullStateCache(clock, freshness)
			cache.RecordPull(nil)
			// Conflict immediately after success — clock NOT advanced
			cache.RecordPull(&git.RebaseConflictError{Path: "tasks/foo.md"})
			ready, _ := cache.ReadinessStatus()
			Expect(ready).To(BeFalse())
		})

		It("body does not contain the git config hint substring", func() {
			cache := puller.NewPullStateCache(clock, freshness)
			cache.RecordPull(nil)
			cache.RecordPull(&git.RebaseConflictError{Path: "tasks/foo.md"})
			_, reason := cache.ReadinessStatus()
			Expect(reason).NotTo(ContainSubstring("Need to specify how to reconcile divergent branches"))
		})

		It("body does not contain 'context canceled'", func() {
			cache := puller.NewPullStateCache(clock, freshness)
			cache.RecordPull(nil)
			cache.RecordPull(&git.RebaseConflictError{Path: "tasks/foo.md"})
			_, reason := cache.ReadinessStatus()
			Expect(reason).NotTo(ContainSubstring("context canceled"))
		})
	})

	Context("first-ever pull is a rebase conflict (no prior success)", func() {
		It("returns not-ready with conflict path (not 'no successful pull yet')", func() {
			cache := puller.NewPullStateCache(clock, freshness)
			cache.RecordPull(&git.RebaseConflictError{Path: "tasks/bar.md"})
			ready, reason := cache.ReadinessStatus()
			Expect(ready).To(BeFalse())
			Expect(reason).To(Equal("last pull failed: rebase conflict at tasks/bar.md"))
		})
	})

	Context("after conflict, a subsequent successful pull clears the error", func() {
		It("returns ready after the conflict is resolved", func() {
			cache := puller.NewPullStateCache(clock, freshness)
			cache.RecordPull(nil)
			cache.RecordPull(&git.RebaseConflictError{Path: "tasks/foo.md"})
			// Human resolves the conflict; next pull tick succeeds
			cache.RecordPull(nil)
			ready, reason := cache.ReadinessStatus()
			Expect(ready).To(BeTrue())
			Expect(reason).To(Equal("ok"))
		})
	})

	Context("transient error (non-conflict) within freshness window", func() {
		It("still returns ready (freshness-threshold behavior unchanged)", func() {
			cache := puller.NewPullStateCache(clock, freshness)
			cache.RecordPull(nil)
			// Transient network error — NOT a RebaseConflictError
			cache.RecordPull(fmt.Errorf("ssh: connect to host github.com port 22: Connection refused"))
			ready, _ := cache.ReadinessStatus()
			Expect(ready).To(BeTrue())
		})
	})

	Context("conflict followed by transient error (sticky)", func() {
		It("still surfaces the conflict path on readiness", func() {
			cache := puller.NewPullStateCache(clock, freshness)
			cache.RecordPull(nil)
			cache.RecordPull(&git.RebaseConflictError{Path: "tasks/foo.md"})
			cache.RecordPull(fmt.Errorf("ssh: connection refused"))
			ready, reason := cache.ReadinessStatus()
			Expect(ready).To(BeFalse())
			Expect(reason).To(Equal("last pull failed: rebase conflict at tasks/foo.md"))
		})

		It("clears the sticky conflict only on a subsequent successful pull", func() {
			cache := puller.NewPullStateCache(clock, freshness)
			cache.RecordPull(&git.RebaseConflictError{Path: "tasks/foo.md"})
			cache.RecordPull(fmt.Errorf("network error"))
			cache.RecordPull(nil)
			ready, reason := cache.ReadinessStatus()
			Expect(ready).To(BeTrue())
			Expect(reason).To(Equal("ok"))
		})
	})
})
```

Add `"fmt"` to the test file's imports if not already present (needed for the transient error test case).

## 4. Append to CHANGELOG `## Unreleased` entry

The `## Unreleased` section was created by prompt 1. Append a second bullet to it:

```markdown
- fix: `PullStateCache.ReadinessStatus()` now returns 503 immediately on `*git.RebaseConflictError`, naming the conflict path in the body (`last pull failed: rebase conflict at <path>`). Transient errors (network, auth) retain the freshness-threshold approach. A subsequent successful pull restores readiness automatically.
```

If `## Unreleased` does not yet exist (prompt 1 didn't run), create it first.

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass — all existing `PullStateCache` Ginkgo tests must remain green
- MUST NOT change the public HTTP contract: `/readiness` returns 200 or 503 (status codes unchanged)
- `pkg/puller/pull_state.go` imports `pkg/git` for `*git.RebaseConflictError` only — no other `pkg/git` symbols used in this file
- The import must be `stderrors "errors"` (stdlib aliased) for `errors.As` — do NOT use `github.com/bborbe/errors` for `errors.As`
- `context.Background()` must NOT appear in `pkg/` — only in test files
- The conflict check (rule 1) MUST appear before the `lastSuccessAt.IsZero()` check (rule 2) in `ReadinessStatus()` — so conflicts surface immediately even if no prior success
- `RecordPull` is NOT changed — it continues to store any error in `lastErr` and update `lastSuccessAt` only on nil
- Do NOT change `pkg/puller/puller.go` — the puller already calls `RecordPull(err)` and the new behavior flows automatically
- Do NOT change any handler files — the 503 body is generated by `PullStateCache.ReadinessStatus()`, not by the handler
- Test file is `package puller_test` (external) — it imports both `pkg/puller` and `pkg/git`
</constraints>

<verification>
Run tests:
```bash
cd /workspace && make test
```
Must pass — all existing and new pull_state tests green.

Spot-check new conflict tests:
```bash
cd /workspace && go test ./pkg/puller/... -v -run "rebase conflict"
```
Expected: all It blocks pass.

Verify sticky-conflict check appears before the zero-check in ReadinessStatus:
```bash
grep -n "lastConflict\|IsZero" /workspace/pkg/puller/pull_state.go
```
Expected: `c.lastConflict != nil` line number is LOWER than `time.Time(c.lastSuccessAt).IsZero()` line number inside `ReadinessStatus`.

Verify the import is present:
```bash
grep -n 'git-rest/pkg/git' /workspace/pkg/puller/pull_state.go
```
Expected: one match.

Final validation:
```bash
cd /workspace && make precommit
```
Must pass with exit code 0.
</verification>
