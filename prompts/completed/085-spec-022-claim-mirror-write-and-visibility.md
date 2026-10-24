---
status: completed
spec: [022-claim-ref-local-mirror]
summary: Updated the local claim mirror on successful claim PUT/DELETE writes, with swallowed-and-logged mirror failures, plus read-after-write, cross-cluster, sync-state and failure-swallow tests and a CHANGELOG entry.
execution_id: git-rest-claim-mirror-exec-085-spec-022-claim-mirror-write-and-visibility
dark-factory-version: v0.198.7
created: "2026-10-24T15:44:42Z"
queued: "2026-10-24T15:56:05Z"
started: "2026-10-24T16:07:37Z"
completed: "2026-10-24T16:11:40Z"
branch: dark-factory/claim-ref-local-mirror
---

<summary>
- A claim created or renewed on this instance is visible to its very next read, without waiting for a pull.
- A claim released on this instance disappears from its next read immediately.
- Both read-after-write reads are served from the mirror, so the test cannot pass by quietly falling back to the live origin.
- A claim written by another cluster becomes visible after at most one pull cycle, not instantly.
- A failed local mirror update is logged and never fails the request; the origin remains authoritative and the next pull heals the mirror.
- Ref operations leave the clone's fetch configuration and its remote-tracking refs untouched.
</summary>

<objective>
Keep the local claim mirror consistent with writes performed through this instance, and prove both the read-after-write invariant and the cross-cluster visibility bound. The origin's compare-and-swap remains the only correctness mechanism; the local mirror update is a cache update that must never fail the request.
</objective>

<context>
Read the approved spec at `specs/in-progress/022-claim-ref-local-mirror.md` — especially § Desired Behavior 6 and 8, § Acceptance Criteria AC6, AC7, AC9, and § Failure Modes rows "Concurrent fetch overwrites a just-written mirror ref", "The local mirror update fails after a successful origin write", and "Another cluster writes between two pull cycles".

Read these files fully before editing:
- `pkg/git/claim_refs.go` — `PutClaimRef`, `DeleteClaimRef`, `pushClaimRef`, `ValidateClaimKey`, `claimKeyPrefix`. Note neither method takes `g.mu` and that must stay true.
- `pkg/git/claim_mirror.go` — from prompts 1–3: `claimMirrorNamespace`, `claimMirrorRefspec`, `listClaimRefsFromMirror`, `claimMirrorAge`.
- `pkg/git/export_test.go` — `NewGitWithClaimMirrorForTest`, `SetClaimMirrorFreshAtForTest`, `ClaimMirrorFreshAtForTest`.
- `pkg/git/claim_refs_test.go` — `newClaimFixture`, `mustGit`, `configureClaimIdentity`, `newClaimClient`, `listClaimRefs`, `claimLeaseUntil`, `claimEmptyTreeSHA`.
- `pkg/git/git_test.go` — `captureSlogLogs` (it lives there, at `pkg/git/git_test.go:1755`).

Coding docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — wrapping idioms.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — plain `testing` functions in `package git_test`.
</context>

<requirements>
## 1. Mirror-ref mapping and update helpers (`pkg/git/claim_mirror.go`)

Add:

```go
// claimMirrorRef maps a claim key ("claims/task/t1") to its mirror ref
// ("refs/claim-mirror/task/t1").
func claimMirrorRef(key string) string {
    return claimMirrorNamespace + strings.TrimPrefix(key, claimKeyPrefix)
}
```

```go
// updateClaimMirrorRef points the local mirror ref at sha after a successful
// origin write. A failure is logged and swallowed: the origin is authoritative
// and the next pull heals the mirror.
func (g *git) updateClaimMirrorRef(ctx context.Context, key, sha string) {
    if err := g.runCmd(ctx, g.repoPath, "update-ref", claimMirrorRef(key), sha); err != nil {
        slog.WarnContext(
            ctx,
            "git-rest: updating the local claim mirror after a write failed",
            "key", key,
            "sha", sha,
            "error", err,
        )
    }
}

// removeClaimMirrorRef deletes the local mirror ref after a successful origin
// delete. A failure is logged and swallowed.
func (g *git) removeClaimMirrorRef(ctx context.Context, key string) {
    if err := g.runCmd(ctx, g.repoPath, "update-ref", "-d", claimMirrorRef(key)); err != nil {
        slog.WarnContext(
            ctx,
            "git-rest: removing the local claim mirror ref after a delete failed",
            "key", key,
            "error", err,
        )
    }
}
```

`git update-ref -d` on an absent ref succeeds silently, so the delete path needs no existence check.

## 2. Update the mirror on successful writes (`pkg/git/claim_refs.go`)

In `PutClaimRef`, after `g.pushClaimRef(ctx, key, request.ExpectedSHA, sha)` returns nil and before `return sha, nil`, add:

```go
g.updateClaimMirrorRef(ctx, key, sha)
```

In `DeleteClaimRef`, replace the direct `return g.pushClaimRef(ctx, key, expectedSHA, "")` with:

```go
if err := g.pushClaimRef(ctx, key, expectedSHA, ""); err != nil {
    return err
}
g.removeClaimMirrorRef(ctx, key)
return nil
```

Neither method may take `g.mu`, and neither may advance the freshness time. A local mirror update is not a fetch; a read after a write on a never-pulled instance still takes the live path and sees the origin's state.

## 3. Tests (append to `pkg/git/claim_mirror_test.go`, `package git_test`)

All are top-level `func TestClaimMirror…(t *testing.T)`.

### 3.1 `TestClaimMirrorReadAfterWrite`

Use a hybrid client so mirror-served reads are provable: a real repository plus a fake origin that counts calls.

- `fixture := newClaimFixture(t)`; `configureClaimIdentity(t, fixture.bare)`.
- `real := newClaimClient(fixture.cloneA)`; `real.ConfigureClaimMirror(ctx)`; `real.Pull(ctx)` so the clone has a populated, fresh mirror.
- `fake := &mocks.FakeClaimOrigin{}` with `ReadCommitMessageStub` returning `{"cluster":"nuke","task_id":"t1","lease_until":"2026-10-25T00:00:00Z","nonce":"x"}` for any sha.
- `g := git.NewGitWithClaimMirrorForTest(fixture.cloneA, fake)`; `git.SetClaimMirrorFreshAtForTest(g, time.Now())`; then `freshAt := git.ClaimMirrorFreshAtForTest(g)` — the freshness value a write must never advance.
- `logs, restore := captureSlogLogs()`; defer `restore()`.
- `sha, err := g.PutClaimRef(ctx, "claims/task/t1", git.ClaimRefRequest{Cluster: "nuke", TaskID: "t1", LeaseUntil: "2026-10-25T00:00:00Z"})`; err nil.
- Assert `git.ClaimMirrorFreshAtForTest(g).Equal(freshAt)` — a write is not a fetch.
- `claims, err := g.ListClaimRefs(ctx, "claims/")`; err nil; exactly one entry with `Ref == "refs/claims/task/t1"` and `SHA == sha`; `fake.ListRefsCallCount() == 0`; at least one log line contains `served_from=mirror`.
- `err = g.DeleteClaimRef(ctx, "claims/task/t1", sha)`; nil.
- Assert `git.ClaimMirrorFreshAtForTest(g).Equal(freshAt)` again.
- `claims, err = g.ListClaimRefs(ctx, "claims/")`; err nil; empty; `fake.ListRefsCallCount()` still `0`; a `served_from=mirror` line is still present.

Do not call `Pull` between the write and the read.

### 3.2 `TestClaimMirrorCrossCluster`

Uses two independent clones of one bare origin, each with its own real client, modelling two clusters.

- `fixture := newClaimFixture(t)`.
- `clientA := newClaimClient(fixture.cloneA)`, `clientB := newClaimClient(fixture.cloneB)`; call `ConfigureClaimMirror(ctx)` on both; call `Pull(ctx)` on both.
- `shaA, err := clientA.PutClaimRef(ctx, "claims/task/t9", git.ClaimRefRequest{Cluster: "cluster-a", TaskID: "t9", LeaseUntil: claimLeaseUntil})`; err nil.
- `claimsB, err := clientB.ListClaimRefs(ctx, "claims/")`; err nil; assert it does NOT contain `refs/claims/task/t9` (B's mirror is fresh from its own pull).
- `clientB.Pull(ctx)` must return nil.
- `claimsB, err = clientB.ListClaimRefs(ctx, "claims/")`; err nil; assert exactly one entry with `Ref == "refs/claims/task/t9"` and `SHA == shaA`.

### 3.3 `TestClaimMirrorSyncStateUnchanged`

- `fixture := newClaimFixture(t)`; `client := newClaimClient(fixture.cloneA)`; `client.ConfigureClaimMirror(ctx)`; `client.Pull(ctx)` so reads are mirror-served and no fetch runs during the test.
- Capture:
  ```go
  beforeFetch := mustGit(t, fixture.cloneA, "config", "--get-all", "remote.origin.fetch")
  beforeRefs := mustGit(t, fixture.cloneA, "for-each-ref", "refs/remotes/origin")
  ```
  Assert `strings.TrimSpace(beforeRefs) != ""` and that it contains `refs/remotes/origin/` — the clone carries `refs/remotes/origin/HEAD` and one ref per branch, so the comparison can actually fail.
- Perform a `GET`, a `PUT` and a `DELETE`:
  - `client.ListClaimRefs(ctx, "claims/")`
  - `sha, err := client.PutClaimRef(ctx, "claims/task/sync", git.ClaimRefRequest{Cluster: "nuke", TaskID: "sync", LeaseUntil: claimLeaseUntil})`
  - `client.DeleteClaimRef(ctx, "claims/task/sync", sha)`
- Re-read both snapshots and assert byte equality with the before values.

### 3.4 `TestClaimMirrorUpdateFailureSwallowed`

The guarantee "a failed local mirror update never fails the request" is the whole point of swallowing the error, so it needs its own test — the success paths in 3.1 cannot reach that branch.

- `fixture := newClaimFixture(t)`; `configureClaimIdentity(t, fixture.bare)`.
- `real := newClaimClient(fixture.cloneA)`; `real.ConfigureClaimMirror(ctx)`; `real.Pull(ctx)` — do this **before** blocking the namespace, or the pull itself will fail.
- Block the mirror write deterministically: create a **regular file** at `filepath.Join(fixture.cloneA, ".git", "refs", "claim-mirror")`, so `git update-ref refs/claim-mirror/...` cannot create that directory. Register `t.Cleanup` to remove it.
- `fake := &mocks.FakeClaimOrigin{}`; `g := git.NewGitWithClaimMirrorForTest(fixture.cloneA, fake)`; `git.SetClaimMirrorFreshAtForTest(g, time.Now())`.
- `logs, restore := captureSlogLogs()`; defer `restore()`.
- `sha, err := g.PutClaimRef(ctx, "claims/task/fail", git.ClaimRefRequest{Cluster: "nuke", TaskID: "fail", LeaseUntil: claimLeaseUntil})`; **`err` must be nil** and `sha` non-empty — the origin write succeeded, so the request succeeded.
- Assert the origin write stands: `strings.TrimSpace(mustGit(t, fixture.bare, "rev-parse", "refs/claims/task/fail")) == sha`.
- Assert at least one log line contains `updating the local claim mirror after a write failed`.
- Assert the local mirror ref was NOT written: `strings.TrimSpace(mustGit(t, fixture.cloneA, "for-each-ref", "refs/claim-mirror/task/fail")) == ""`.
</requirements>

<constraints>
- The `GET /api/v1/refs` response body, key set, ordering and status codes are unchanged.
- No new metric, no new endpoint, no new query parameter, no new CLI flag.
- A ref operation never writes configuration and never writes `refs/remotes/origin/*`; the served branch is never fetched into, reset or pushed by a ref read.
- The local mirror update runs after the origin write and must never change the request's success or failure: a failed `git update-ref` is logged and swallowed.
- Neither `PutClaimRef` nor `DeleteClaimRef` takes `g.mu`.
- A local mirror update does not advance the freshness time.
- The mirror lives in `refs/claim-mirror/*`, a namespace the default heads refspec cannot reach.
- Git is driven by the system `git` binary; every git subprocess on the request path is bound by the request context.
- `autoRelease: false` — the release tag is cut manually after merge.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass, including `TestClaimCreateRace`, `TestClaimRenewRace`, `TestClaimDeleteRace`, and the root-level claim-ref HTTP tests.
</constraints>

<verification>
```
set -e
COUNT=$(GOFLAGS=-buildvcs=false go test -mod=mod -count=1 -run 'TestClaimMirror(ReadAfterWrite|CrossCluster|SyncStateUnchanged|UpdateFailureSwallowed)$' -v ./pkg/git/ | grep -cE '^=== RUN   TestClaimMirror[A-Za-z]+$' || true)
echo "mirror tests run: $COUNT"
test "$COUNT" -ge 4
GOFLAGS=-buildvcs=false go test -mod=mod -count=1 ./pkg/git/... ./pkg/handler/...
make test
```
`set -e` plus the `COUNT` check are load-bearing: `go test -run` exits 0 when it matches **no** test, so without the count a renamed test would turn this prompt's verification into a silent pass. Requirements: every line exits 0 and `COUNT` is at least 4.
</verification>
