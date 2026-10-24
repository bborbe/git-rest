---
status: completed
spec: [022-claim-ref-local-mirror]
summary: Recorded a claims-specific last-successful-fetch timestamp in the pull path, advanced only when the claim refspec was present, with a derived staleness bound and tests.
execution_id: git-rest-claim-mirror-exec-083-spec-022-claim-mirror-pull-refresh
dark-factory-version: v0.198.7
created: "2026-10-24T15:44:42Z"
queued: "2026-10-24T15:56:01Z"
started: "2026-10-24T15:59:05Z"
completed: "2026-10-24T16:02:51Z"
branch: dark-factory/claim-ref-local-mirror
---

<summary>
- The puller's existing periodic fetch now also refreshes and prunes the local claim mirror.
- A successful fetch records when the mirror was last refreshed, so a later read can decide whether the mirror is fresh.
- A failed fetch does not advance that time, so a severed origin cannot make an older mirror look fresh.
- The freshness time is only advanced when the claim fetch refspec is actually present, so an unconfigured clone can never serve an empty mirror as if it were authoritative.
- The staleness bound is derived from the configured pull interval — twice the interval with a floor of 60 seconds — and is never a separate setting.
- A restart resets the in-memory time; the boot-time pull re-establishes it before the first request is served.
</summary>

<objective>
Record a claims-specific "last successful fetch" timestamp inside the pull path, advanced only by a `git fetch` that completed while the claim refspec was present, and expose it to tests. This is the signal the read path (next prompt) uses to decide mirror vs live origin. Do not change what a read returns yet.
</objective>

<context>
Read the approved spec at `specs/in-progress/022-claim-ref-local-mirror.md` — especially § Desired Behavior 2 and 3, § Acceptance Criteria AC2, and § Failure Modes rows "Pull succeeds at fetch and fails at merge / rescue", "Puller wedged", and "A fetch fails mid-cycle".

Read these files fully before editing:
- `pkg/git/git.go` — `GitOptions`, `GitOption`, `WithCoalesce`, `New`, the `git` struct, `Pull`, `syncWithUpstream`, `pullFetchSHAs`, `retryPush`. The fetch that refreshes the mirror is the one in `pullFetchSHAs`.
- `pkg/git/claim_mirror.go` — created by prompt 1. It holds `claimMirrorRefspec`, `claimMirrorNamespace`, `hasConfigLine`, and `ConfigureClaimMirror`. Extend it; do not create a second file.
- `pkg/git/export_test.go` — the test-only accessors pattern. `time` is already imported there.
- `pkg/git/claim_refs_test.go` — `newClaimFixture(t)`, `mustGit`, `configureClaimIdentity`, `newClaimClient`, `claimEmptyTreeSHA`.
- `pkg/git/git_test.go` — `noopMetrics`.
- `main.go` lines 392–412 (`createGitClient`) — the factory call where the pull interval is passed as a new option.
- `pkg/factory/factory.go` — `CreateGitClient` already forwards `...git.GitOption`; no factory change is needed.

Coding docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md` — functional options and struct field conventions.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — error wrapping idioms.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — plain `testing` functions in `package git_test`.
</context>

<requirements>
## 1. Add the pull interval to `GitOptions` (`pkg/git/git.go`)

Add a field to `GitOptions`:

```go
// ClaimMirrorPullInterval is the configured pull interval. The claim-mirror
// staleness bound is derived from it (twice the interval, floor 60 s).
// Zero uses the floor.
ClaimMirrorPullInterval libtime.Duration
```

Add the option next to `WithCoalesce`:

```go
// WithClaimMirrorPullInterval sets the pull interval the claim-mirror
// staleness bound is derived from.
func WithClaimMirrorPullInterval(interval libtime.Duration) GitOption {
    return func(opts *GitOptions) {
        opts.ClaimMirrorPullInterval = interval
    }
}
```

Add fields to the `git` struct:

```go
pullInterval       libtime.Duration
claimMirrorMu      sync.RWMutex
claimMirrorFreshAt time.Time
```

In `New`, set `pullInterval: gitOptions.ClaimMirrorPullInterval` when constructing `&git{...}`.

## 2. Freshness helpers (`pkg/git/claim_mirror.go`)

Add constants and helpers. `context`, `strings`, `time`, and `log/slog` must be imported in this file; add only the ones missing.

```go
const (
    // claimMirrorStalenessFactor multiplies the pull interval to derive the
    // staleness bound.
    claimMirrorStalenessFactor = 2
    // claimMirrorMinStaleness is the floor of the staleness bound.
    claimMirrorMinStaleness = 60 * time.Second
)
```

```go
// claimMirrorStalenessBound returns twice the configured pull interval, with a
// floor of 60 seconds. It is derived, never configured separately.
func (g *git) claimMirrorStalenessBound() time.Duration {
    bound := claimMirrorStalenessFactor * time.Duration(g.pullInterval)
    if bound < claimMirrorMinStaleness {
        return claimMirrorMinStaleness
    }
    return bound
}

// hasClaimRefspec reports whether the clone's origin fetch currently carries
// the claim mirror refspec.
func (g *git) hasClaimRefspec(ctx context.Context) bool {
    out, err := g.runCmdRaw(ctx, g.repoPath, "config", "--get-all", "remote.origin.fetch")
    if err != nil {
        return false
    }
    return hasConfigLine(out, claimMirrorRefspec)
}

// recordClaimMirrorFetch advances the last claims-specific fetch time. It is
// called only after a successful git fetch, and only when the claim refspec was
// present for that fetch: a clone that never got the refspec must never look
// fresh, otherwise it could answer an empty mirror authoritatively.
func (g *git) recordClaimMirrorFetch(ctx context.Context, at time.Time) {
    if !g.hasClaimRefspec(ctx) {
        return
    }
    g.claimMirrorMu.Lock()
    g.claimMirrorFreshAt = at
    g.claimMirrorMu.Unlock()
}

// claimMirrorFreshAtTime returns the recorded time, zero when never recorded.
func (g *git) claimMirrorFreshAtTime() time.Time {
    g.claimMirrorMu.RLock()
    defer g.claimMirrorMu.RUnlock()
    return g.claimMirrorFreshAt
}
```

## 3. Record after a successful fetch in `pullFetchSHAs` (`pkg/git/git.go`)

In `pullFetchSHAs`, immediately after the `git fetch` succeeds and BEFORE the `rev-parse`/`merge-base` calls, add:

```go
g.recordClaimMirrorFetch(ctx, time.Now())
```

Rationale to preserve in a comment: the freshness time describes the fetch step only. A pull whose merge, rescue or push later fails still leaves a genuinely refreshed mirror, so the record must not depend on the state machine. A failed fetch returns from the branch above and never reaches this line, so the previous value is retained.

Do not add a record call in `retryPush` or `pushWithRetry`. The canonical fetch step of a pull is the one in `pullFetchSHAs`.

## 4. Pass the interval in `createGitClient` (`main.go`)

Add `git.WithClaimMirrorPullInterval(a.PullInterval)` to the `factory.CreateGitClient(...)` call in `createGitClient`, after `git.WithCoalesce(a.coalesceConfig())`.

## 5. Test-only accessors (`pkg/git/export_test.go`)

Add:

```go
// SetClaimMirrorFreshAtForTest sets the recorded last-claims-fetch time.
// Test builds only.
func SetClaimMirrorFreshAtForTest(g Git, at time.Time) {
    impl, ok := g.(*git)
    if !ok {
        panic("SetClaimMirrorFreshAtForTest: Git is not a *git")
    }
    impl.claimMirrorMu.Lock()
    impl.claimMirrorFreshAt = at
    impl.claimMirrorMu.Unlock()
}

// ClaimMirrorFreshAtForTest returns the recorded last-claims-fetch time.
// Test builds only.
func ClaimMirrorFreshAtForTest(g Git) time.Time {
    impl, ok := g.(*git)
    if !ok {
        panic("ClaimMirrorFreshAtForTest: Git is not a *git")
    }
    impl.claimMirrorMu.RLock()
    defer impl.claimMirrorMu.RUnlock()
    return impl.claimMirrorFreshAt
}
```

## 6. Test `TestClaimMirrorPullRefreshesAndPrunes` (`pkg/git/claim_mirror_test.go`)

Add to the existing file from prompt 1. It must be a top-level `func TestClaimMirrorPullRefreshesAndPrunes(t *testing.T)`.

Setup:
- `fixture := newClaimFixture(t)`.
- `client := newClaimClient(fixture.cloneA)`.
- `configureClaimIdentity(t, fixture.bare)` so `commit-tree` works in the bare origin.
- `client.ConfigureClaimMirror(context.Background())` must return nil.
- `client.Pull(context.Background())` must return nil.

Then, in order:

1. **Empty mirror.** `strings.TrimSpace(mustGit(t, fixture.cloneA, "for-each-ref", "refs/claim-mirror/")) == ""`.
2. **One pull adds a claim.** Create the claim commit directly in the bare origin (never through git-rest):
   ```go
   sha := strings.TrimSpace(mustGit(t, fixture.bare, "commit-tree", claimEmptyTreeSHA, "-m",
       `{"cluster":"nuke","task_id":"t1","lease_until":"2026-10-25T00:00:00Z","nonce":"0123456789abcdef0123456789abcdef"}`))
   mustGit(t, fixture.bare, "update-ref", "refs/claims/task/t1", sha)
   ```
   `client.Pull(ctx)`; assert `strings.TrimSpace(mustGit(t, fixture.cloneA, "rev-parse", "refs/claim-mirror/task/t1")) == sha`.
3. **One pull prunes a deleted claim.** `mustGit(t, fixture.bare, "update-ref", "-d", "refs/claims/task/t1")`; `client.Pull(ctx)`; assert `for-each-ref refs/claim-mirror/` is empty again.
4. **A successful pull advances the freshness time.** Re-add a claim (`refs/claims/task/t2`) exactly as in step 2, `client.Pull(ctx)`, then `freshBefore := git.ClaimMirrorFreshAtForTest(client)`; assert `!freshBefore.IsZero()`.
5. **A failed fetch does not advance it.** `mustGit(t, fixture.cloneA, "remote", "set-url", "origin", "/nonexistent/origin.git")`; `err := client.Pull(ctx)` must be non-nil; assert `git.ClaimMirrorFreshAtForTest(client).Equal(freshBefore)`.

Do NOT add a `ListClaimRefs` assertion here. The mirror-served read is shipped by prompt 3, which extends this same test with the severed-origin read; adding it now would make this prompt's own test fail.
</requirements>

<constraints>
- The `GET /api/v1/refs` response body, key set, ordering and status codes are unchanged.
- No new metric, no new endpoint, no new query parameter, no new CLI flag. The staleness bound is derived from the configured pull interval only.
- The freshness time is process-local, in memory; a restart resets it and the boot pull re-establishes it.
- A failed fetch must never advance the freshness time.
- The mirror lives in `refs/claim-mirror/*`; a ref operation never writes configuration and never writes `refs/remotes/origin/*`.
- Git is driven by the system `git` binary; every git subprocess on the request path is bound by the request context.
- The read path must not take `g.mu`; `recordClaimMirrorFetch` runs under `Pull`'s existing mutex hold and takes only `claimMirrorMu`.
- `autoRelease: false` — the release tag is cut manually after merge.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
```
set -e
GOFLAGS=-buildvcs=false go test -mod=mod -count=1 -run 'TestClaimMirrorPullRefreshesAndPrunes$' -v ./pkg/git/
GOFLAGS=-buildvcs=false go test -mod=mod -count=1 ./pkg/git/... ./pkg/puller/...
make test
```
All must exit 0. `make test` exercises the existing pull/push/coalesce suites, which must be unaffected by the extra fetch-time record.
</verification>
