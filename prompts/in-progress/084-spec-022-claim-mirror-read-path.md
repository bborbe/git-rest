---
status: approved
spec: [022-claim-ref-local-mirror]
created: "2026-10-24T15:44:42Z"
queued: "2026-10-24T15:56:03Z"
branch: dark-factory/claim-ref-local-mirror
---

<summary>
- A claim-ref read is answered from the local mirror when the mirror is fresh, with no network call on the request path.
- A read served from the mirror returns exactly what the live path returns: the same claim refs, metadata, sort order, empty-array shape and status codes.
- Prefix matching keeps its whole-path-component rule on the local path, and a returned ref is always the claim ref, never the mirror ref.
- A stale, never-populated, unconfigured or object-missing mirror falls back to the existing live origin path instead of answering with partial or empty data.
- Every read logs exactly one INFO line naming its source, and the mirror path also logs its age in seconds.
- A mirrored read against a severed origin still returns the claim list, which is the defect this spec exists to fix.
</summary>

<objective>
Make `ListClaimRefs` serve from the local `refs/claim-mirror/*` namespace when the claims-specific freshness signal is inside the staleness bound, falling back to the unchanged live origin path in every other case. The response contract must be byte-for-byte the same as the live path.
</objective>

<context>
Read the approved spec at `specs/in-progress/022-claim-ref-local-mirror.md` — especially § Desired Behavior 3, 4, 5 and 7, § Acceptance Criteria AC3, AC4, AC5, AC8, § Failure Modes rows "Origin unreachable while the mirror is fresh", "Origin unreachable and the mirror is stale", "Refspec absent or the configuration step failed", "Puller wedged", "Claim commit object missing locally", and "Wall clock steps backwards".

Read these files fully before editing:
- `pkg/git/claim_refs.go` — `ListClaimRefs`, `parseRemoteRefs`, `readClaimRef`, `parseClaimMetadata`, `matchesClaimPrefix`, `claimKeyPrefix`, `ClaimRef`, `RemoteRef`, `ClaimOrigin`. The existing body of `ListClaimRefs` becomes the live path; its behaviour must not change.
- `pkg/git/claim_mirror.go` — from prompts 1 and 2: `claimMirrorRefspec`, `claimMirrorNamespace`, `hasConfigLine`, `claimMirrorStalenessBound`, `claimMirrorFreshAtTime`. Extend it.
- `pkg/git/git.go` — `runCmd`, `runCmdOutput`, `runCmdRaw`.
- `pkg/git/export_test.go` — `NewGitWithClaimMirrorForTest`, `SetClaimMirrorFreshAtForTest`, `ClaimMirrorFreshAtForTest`.
- `pkg/git/claim_refs_list_test.go` — `newClaimListFake`, the live-path test patterns.
- `pkg/git/git_test.go` — `captureSlogLogs` (it lives there, at `pkg/git/git_test.go:1755`, not in `claim_refs_list_test.go`).
- `pkg/git/claim_refs_test.go` — `newClaimFixture`, `mustGit`, `configureClaimIdentity`, `newClaimClient`, `claimEmptyTreeSHA`, `claimMessages`.
- `pkg/handler/refs_get.go` and `pkg/handler/refs_validate.go` — `NewRefsGetHandler`, `validateRefPrefix`. The 400 for a glob-bearing prefix is handler-side and must stay.

Coding docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md` — non-blocking `select` on `ctx.Done()` in the per-claim loop.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — wrapping idioms.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — plain `testing` functions in `package git_test`.
</context>

<requirements>
## 1. Split `ListClaimRefs` into a dispatcher and a live path (`pkg/git/claim_refs.go`)

Rename the existing `ListClaimRefs` body to:

```go
// listClaimRefsLive lists claim refs from the origin. This is the pre-mirror
// behaviour, unchanged.
func (g *git) listClaimRefsLive(ctx context.Context, prefix string) ([]ClaimRef, error)
```

It must keep its exact current behaviour: `g.origin.ListRefs(ctx, "refs/"+prefix+"*")`, prefix filter, sort, batched `FetchCommits`, per-entry fetch fallback, `resolveFetchFailure`, metadata parsing. Do not change any of it.

Add the dispatcher:

```go
// ListClaimRefs returns the claim refs under refs/<prefix>. It serves a fresh
// local claim mirror without touching the network and falls back to the live
// origin when the mirror is stale, absent, unconfigured or missing an object.
func (g *git) ListClaimRefs(ctx context.Context, prefix string) ([]ClaimRef, error) {
    if age, fresh := g.claimMirrorAge(); fresh {
        claims, served, err := g.listClaimRefsFromMirror(ctx, prefix)
        if err != nil {
            return nil, err
        }
        if served {
            slog.InfoContext(
                ctx,
                "git-rest: claim refs read",
                "served_from", "mirror",
                "age_s", int64(age.Seconds()),
            )
            return claims, nil
        }
    }
    slog.InfoContext(ctx, "git-rest: claim refs read", "served_from", "origin")
    return g.listClaimRefsLive(ctx, prefix)
}
```

Exactly one INFO line is emitted per request: the mirror branch returns before the origin line, and every fallback path reaches the origin line once.

## 2. Mirror read (`pkg/git/claim_mirror.go`)

Add the claim-ref namespace constant. Constant expressions may concatenate constants:

```go
// claimRefNamespace is the origin namespace mirrored into claimMirrorNamespace.
const claimRefNamespace = "refs/" + claimKeyPrefix
```

Add:

```go
// claimMirrorAge reports the age of the last claims-specific fetch and whether
// the mirror is fresh enough to serve. A never-recorded time, or one older than
// the staleness bound, reports false. A negative age (wall clock stepped
// backwards) is clamped to zero so it cannot be mistaken for stale.
func (g *git) claimMirrorAge() (time.Duration, bool) {
    freshAt := g.claimMirrorFreshAtTime()
    if freshAt.IsZero() {
        return 0, false
    }
    age := time.Since(freshAt)
    if age < 0 {
        age = 0
    }
    if age > g.claimMirrorStalenessBound() {
        return 0, false
    }
    return age, true
}
```

```go
// listClaimRefsFromMirror lists claim refs from the local mirror. served=false
// means the caller must take the live path; it is returned for every condition
// that would make a mirror answer untrustworthy, and never together with a
// partial result.
func (g *git) listClaimRefsFromMirror(
    ctx context.Context,
    prefix string,
) (claims []ClaimRef, served bool, err error)
```

Implementation, in this order:

1. `out, err := g.runCmdOutput(ctx, g.repoPath, "for-each-ref", "--format=%(objectname) %(refname)", claimMirrorNamespace)`. On error: `slog.WarnContext(ctx, "git-rest: listing the local claim mirror failed; using the live origin", "error", err)` and return `nil, false, nil`. A broken local read must not fail a request the origin could still answer.
2. For each non-empty line, `strings.Cut(line, " ")` into `sha` and `ref`; skip lines without the separator. Build `claimRef := claimRefNamespace + strings.TrimPrefix(ref, claimMirrorNamespace)`. Keep it only when `matchesClaimPrefix(claimRef, prefix)`, as `RemoteRef{SHA: sha, Ref: claimRef}`.
3. Sort the kept entries by `Ref`.
4. If none matched, return `[]ClaimRef{}, true, nil` — a configured-and-genuinely-empty mirror answers `[]`, and the caller must not fall back.
5. For each entry, non-blocking `select` on `ctx.Done()`; on cancellation return `nil, false, errors.Wrap(ctx, ctx.Err(), "claim ref listing cancelled")`.
6. Read the message with `message, err := g.origin.ReadCommitMessage(ctx, entry.SHA)`. On error log `slog.WarnContext(ctx, "git-rest: reading a mirrored claim commit failed; using the live origin", "ref", entry.Ref, "sha", entry.SHA, "error", err)` and return `nil, false, nil`. This is also the missing-object case: `ReadCommitMessage` runs `git cat-file -p` against the local object database, so a mirror ref whose commit object is absent fails here.
7. Build `ClaimRef{Ref: entry.Ref, SHA: entry.SHA}`, parse the metadata with `parseClaimMetadata`. On a parse error, log the same WARN the live path logs (`"claim ref has unparseable metadata"` with `ref`, `sha`, `error`) and append the claim with empty metadata — the live path does exactly this. On success set `Cluster`, `TaskID`, `LeaseUntil`.
8. Return `result, true, nil`.

Do not take `g.mu`. Reading a mirror ref concurrently with the fetch that updates it is safe: git publishes the object before the ref.

## 3. Extend `TestClaimMirrorPullRefreshesAndPrunes` (`pkg/git/claim_mirror_test.go`)

After the step that severs the origin and asserts the failed `Pull`, add the AC2 read-back:

- `claims, err := client.ListClaimRefs(context.Background(), "claims/")`; err nil; exactly one entry with `Ref == "refs/claims/task/t2"` and its `SHA` equal to the re-added sha. The origin is severed, so this can only be answered by the mirror.

## 4. Tests (append to `pkg/git/claim_mirror_test.go`, `package git_test`)

All are top-level `func TestClaimMirror…(t *testing.T)`. Use `captureSlogLogs()` (from `pkg/git/git_test.go`) for log assertions, and `mocks.FakeClaimOrigin` for call counting. Build hybrid clients with `git.NewGitWithClaimMirrorForTest(repoPath, fake)` plus `git.SetClaimMirrorFreshAtForTest(g, time.Now())`; such a client has a real repository but a fake origin, so a mirror-served read is provably network-free.

Helper to add to this file:

```go
// newMirrorRepo returns a git repository whose mirror namespace holds refs
// pointing at real claim commits, keyed by claim ref ("refs/claims/…").
func newMirrorRepo(t *testing.T, messages map[string]string) (repoPath string, shas map[string]string)
```

Implement it with `git init`, `configureClaimIdentity`, then for each claim ref `refs/claims/<key>` create a commit with `mustGit(t, repo, "commit-tree", claimEmptyTreeSHA, "-m", message)` and `mustGit(t, repo, "update-ref", "refs/claim-mirror/<key>", sha)`. Return the repo path and a claim-ref→sha map.

### 4.1 `TestClaimMirrorServesWithoutNetwork`

- `repo, shas := newMirrorRepo(t, map[string]string{"refs/claims/task/t1": `{"cluster":"nuke","task_id":"t1","lease_until":"2026-10-25T00:00:00Z"}`})`.
- `fake := &mocks.FakeClaimOrigin{}` with `ReadCommitMessageStub` returning the JSON message for the sha in `shas`.
- `g := git.NewGitWithClaimMirrorForTest(repo, fake)`; `git.SetClaimMirrorFreshAtForTest(g, time.Now())`.
- `logs, restore := captureSlogLogs()`; defer `restore()`.
- `claims, err := g.ListClaimRefs(ctx, "claims/")`; err nil; one entry with `Ref == "refs/claims/task/t1"`, `SHA == shas["refs/claims/task/t1"]`, `Cluster == "nuke"`.
- Assert `fake.ListRefsCallCount() == 0` and `fake.FetchCommitsCallCount() == 0`.
- Assert exactly one log line contains `served_from=mirror`, and that line matches the regexp `age_s=[0-9]+`.

### 4.2 `TestClaimMirrorSeveredOrigin`

- `fixture := newClaimFixture(t)`; `client := newClaimClient(fixture.cloneA)`; `configureClaimIdentity(t, fixture.bare)`; `client.ConfigureClaimMirror(ctx)`.
- Create `refs/claims/task/t1` directly in the bare origin (commit-tree + update-ref, JSON message).
- `client.Pull(ctx)`.
- `before := listClaimRefs(t, client, "claims/")` (helper in `claim_refs_test.go`); assert one entry.
- `mustGit(t, fixture.cloneA, "remote", "set-url", "origin", "/nonexistent/origin.git")`.
- `after := listClaimRefs(t, client, "claims/")`; assert `reflect.DeepEqual(before, after)`.

### 4.3 `TestClaimMirrorPrefixSemantics`

- `repo, shas := newMirrorRepo(t, ...)` with claim refs `refs/claims/task/1`, `refs/claims/task/10`, `refs/claims/tasks/1`, `refs/claims/repo/a/b`, each with a distinct JSON message (`task_id` matching the key).
- `fake := &mocks.FakeClaimOrigin{}` with a `ReadCommitMessageStub` returning the message for each sha.
- Fresh hybrid client as in 4.1.
- Assert, one by one:
  - `"claims/task/1"` → exactly `["refs/claims/task/1"]`.
  - `"claims/task"` → exactly `["refs/claims/task/1", "refs/claims/task/10"]`, never `refs/claims/tasks/1`.
  - `"claims/repo/a"` → exactly `["refs/claims/repo/a/b"]`.
  - `"claims/"` → all four, sorted by `ref`.
- For every result, assert every `Ref` starts with `"refs/claims/"` and none contains `"refs/claim-mirror/"`.
- Assert `fake.ListRefsCallCount() == 0`.

### 4.4 `TestClaimMirrorStaleFallsBack`

Covers the unset and older-than-bound cases in one test. Build a separate hybrid client for each case so the call counts are independent.

- `repo, _ := newMirrorRepo(t, ...)` with one claim.
- Live-path fake: `ListRefsStub` returns `[]git.RemoteRef{{SHA: claimShaA, Ref: "refs/claims/a"}}`, `FetchCommitsStub` returns nil, `ReadCommitMessageStub` returns a valid JSON message.
- **Unset:** `g := git.NewGitWithClaimMirrorForTest(repo, fake)` with no freshness set. `claims, err := g.ListClaimRefs(ctx, "claims/")`; err nil; the live entry is returned; `fake.ListRefsCallCount() == 1`; exactly one log line contains `served_from=origin`.
- **Older than the bound:** build a second fake with the same stubs, `g2 := git.NewGitWithClaimMirrorForTest(repo, fake2)`, then `git.SetClaimMirrorFreshAtForTest(g2, time.Now().Add(-time.Hour))` (the floor is 60 s). `g2.ListClaimRefs(ctx, "claims/")` returns the live entry; `fake2.ListRefsCallCount() == 1`; `served_from=origin` logged.

### 4.5 `TestClaimMirrorRefspecAbsentFallsBack`

- `fixture := newClaimFixture(t)`; `client := newClaimClient(fixture.cloneA)` — deliberately do NOT call `ConfigureClaimMirror`.
- Add a claim ref directly in the bare origin; `client.Pull(ctx)` must return nil (a fetch without the claim refspec still succeeds).
- Assert `git.ClaimMirrorFreshAtForTest(client).IsZero()`.
- `mustGit(t, fixture.cloneA, "remote", "set-url", "origin", "/nonexistent/origin.git")`.
- `claims, err := client.ListClaimRefs(ctx, "claims/")`; err must be non-nil; `strings.Contains(err.Error(), "ls-remote")` must be true; `claims` must be nil or empty — never `[]` treated as success.

### 4.6 `TestClaimMirrorMissingObjectFallsBack`

- Create a repository with `git init` and `configureClaimIdentity`.
- Write a loose mirror ref by hand so it points at a nonexistent object (bypassing `update-ref` validation):
  ```go
  refDir := filepath.Join(repo, ".git", "refs", "claim-mirror", "task")
  os.MkdirAll(refDir, 0o750)
  os.WriteFile(filepath.Join(refDir, "t1"), []byte(strings.Repeat("a", 40)+"\n"), 0o600)
  ```
- Live-path fake returning one entry (`claimShaB`, `refs/claims/b`) with a valid message.
- Fresh hybrid client; `claims, err := g.ListClaimRefs(ctx, "claims/")`; err nil; the live path's full list is returned; `fake.ListRefsCallCount() == 1`; one log line with `served_from=origin`.

### 4.7 `TestClaimMirrorResponseContract`

- `repo, shas := newMirrorRepo(t, ...)` with two claims: one valid JSON message, one with message `not json`.
- Fake `ReadCommitMessageStub` returns the raw message per sha.
- Fresh hybrid client.
- `claims, err := g.ListClaimRefs(ctx, "claims/")`; err nil; two entries.
- `body, err := json.Marshal(claims)`; for each object decode into `map[string]json.RawMessage` and assert the key set is exactly `ref`, `sha`, `cluster`, `task_id`, `lease_until`; assert no `nonce` key.
- The non-JSON claim keeps its `ref` and `sha` and has empty `cluster`, `task_id`, `lease_until`.
- Empty case: `emptyRepo, _ := newMirrorRepo(t, map[string]string{})`; fresh hybrid client; `claims, err := g.ListClaimRefs(ctx, "claims/")`; `json.Marshal(claims)` must equal `[]` (not `null`).
- Handler-level contract: `fakeGit := &mocks.FakeGit{}`; `h := libhttp.NewJSONErrorHandler(handler.NewRefsGetHandler(fakeGit))`; `req := httptest.NewRequest(http.MethodGet, "/api/v1/refs?prefix=claims/*", nil)`; `rec := httptest.NewRecorder()`; `h.ServeHTTP(rec, req)`; assert `rec.Code == http.StatusBadRequest` and `fakeGit.ListClaimRefsCallCount() == 0` (the glob is rejected before any git command).
- Status 200: build a handler over the fresh hybrid client and assert a `GET /api/v1/refs?prefix=claims/` returns `200`.
</requirements>

<constraints>
- The `GET /api/v1/refs` response body, key set, ordering and status codes are unchanged; the `400` for a glob-bearing or invalid prefix is unchanged and must still run before any git command.
- No new metric, no new endpoint, no new query parameter, no new CLI flag.
- A mirror-served read must perform no `ClaimOrigin.ListRefs` and no `ClaimOrigin.FetchCommits` call.
- A read served from the mirror must never return a partial list: any local failure falls back to the live path.
- A configured and genuinely empty mirror answers `[]`; an unconfigured or stale mirror must never answer `[]` from an empty mirror.
- The read path must not take `g.mu`.
- The mirror lives in `refs/claim-mirror/*`; a ref operation never writes configuration and never writes `refs/remotes/origin/*`.
- Git is driven by the system `git` binary; every git subprocess on the request path is bound by the request context.
- `autoRelease: false` — the release tag is cut manually after merge.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass, including spec 018's `TestClaimListRealRepo` and `TestClaimListFetchBySha`, which run with no fresh mirror and therefore take the live path.
</constraints>

<verification>
```
set -e
GOFLAGS=-buildvcs=false go test -mod=mod -count=1 -run 'TestClaimMirror(ServesWithoutNetwork|SeveredOrigin|PrefixSemantics|StaleFallsBack|RefspecAbsentFallsBack|MissingObjectFallsBack|ResponseContract|PullRefreshesAndPrunes)$' -v ./pkg/git/
GOFLAGS=-buildvcs=false go test -mod=mod -count=1 ./pkg/git/... ./pkg/handler/...
make test
```
All must exit 0.
</verification>
