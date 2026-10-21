---
status: completed
spec: [018-leased-claim-ref-operations]
summary: Batched claim-ref fetching with re-list-verified drop/500 classification, generated Counterfeiter ClaimOrigin mock, shared IsObjectID validator, pre-initialised put/413 metric, and updated API docs
execution_id: git-rest-claim-refs-exec-072-spec-018-review-fixes
dark-factory-version: v0.198.7
created: "2026-10-21T18:49:05Z"
queued: "2026-10-21T18:53:34Z"
started: "2026-10-21T18:53:54Z"
completed: "2026-10-21T18:58:50Z"
---

<summary>
- Listing claim refs no longer hides real origin failures. A claim commit that cannot be fetched is dropped only when the origin confirms its ref was deleted or moved in the meantime; otherwise the listing fails with `500`.
- If the origin cannot even be asked again (transport or auth error), the listing also fails with `500` instead of silently returning a shorter list.
- Listing N claims costs one fetch round-trip to the origin instead of N; only when that combined fetch fails does git-rest fall back to fetching one commit at a time.
- The test double for the origin is a generated Counterfeiter mock instead of a hand-written fake; the existing listing tests keep every assertion they had.
- The object-id check is defined once and shared by the git layer and the HTTP layer, so the two can never disagree about what a valid `expected_sha` is.
- The "body too large" result of a claim-ref write (`413`) is exposed on `/metrics` at zero from process start, like the other pre-initialised results.
- The API docs say when a vanished claim is dropped and when the listing fails.
- No new endpoint, flag, metric family or retry is added.
</summary>

<objective>
Apply the local PR-review fixes for bborbe/git-rest#37 to the spec-018 claim-ref code. Claim listing must return `500` when a fetch fails while the ref still exists, drop an entry only when a re-list proves the ref moved or vanished, and fetch every listed claim commit in one batched `git fetch`. The listing tests must use a generated Counterfeiter mock. The HTTP layer must share the git layer's object-id validator, and the `{op="put",result="413"}` series must be pre-initialised.
</objective>

<context>
There is no repo-root `CLAUDE.md` in this worktree; do not look for one. Project conventions live in `docs/dod.md` and the coding-plugin guides below.

Read these coding-plugin guides before implementing (paths inside the YOLO container):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md`: public interface, private struct, sentinel errors, `errors.Is`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`: the `github.com/bborbe/errors` API (`errors.Wrap`, `errors.Wrapf`, `errors.Errorf` with `ctx`); never `fmt.Errorf`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`: Counterfeiter mocks, external test packages, coverage for new code
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md`: linter limits (funlen 80, nestif 4, golines 100), license headers
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md`: non-blocking `ctx.Done()` check in per-item loops
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`

Files to read in full before editing (anchor by symbol name, not line number):

- `pkg/git/claim_refs.go`: `remoteRef`, `claimOrigin`, `gitOrigin` (`ListRefs`, `FetchCommit`, `ReadCommitMessage`), `parseRemoteRefs`, `ListClaimRefs`, `readClaimRef`, `matchesClaimPrefix`, `expectedSHAPattern`, `validateExpectedSHA`.
- `pkg/git/git.go`: the `Git` interface (doc comment on `ListClaimRefs`), `New`, the `git` struct (field `origin claimOrigin`), and the helpers `runCmd(ctx context.Context, dir string, args ...string) error` and `runCmdOutput(ctx context.Context, dir string, args ...string) ([]byte, error)`.
- `pkg/git/claim_refs_internal_test.go` (`package git`): the hand-written `fakeClaimOrigin`, `newFakeClaimOrigin`, `captureSlog`, `TestClaimListFetchBySha` with subtests `testClaimListFetchesBySHA` and `testClaimListVanishedSHA`, `assertOneClaimWarning`, and `TestIsLeaseConflict`.
- `pkg/git/claim_refs_test.go` (`package git_test`): the real-repo tests, including `TestClaimListRealRepo`, which exercise the production `gitOrigin` against local bare repos.
- `pkg/git/export_test.go` (`package git`): the existing pattern for exposing internals to external tests (`NewYAMLMergeResolverWithSideTimes`).
- `pkg/git/git_suite_test.go`: the Ginkgo entry point `TestSuite`. Do not add a second suite.
- `pkg/handler/refs_validate.go`: `refExpectedSHAPattern`, `validateRefRequest`, `validateRefExpectedSHA`.
- `pkg/handler/refs_get.go`: `NewRefsGetHandler` maps a non-`ErrInvalidRefName` error from `ListClaimRefs` to `500` through `errors.Wrap`. No change is needed there.
- `pkg/metrics/metrics.go`: the `init` block that pre-initialises `RefRequestsTotal` with the `{get,put,delete} × {200,400,409,500}` loop.
- `pkg/metrics/metrics_test.go`: `Describe("RefRequestsTotal")`, which asserts `Expect(count).To(Equal(12))`.
- `claim_refs_http_test.go` (repo root): `refCounterSnapshot` and `expectOnlyRefCounterDelta`.
- `docs/api.md`: section `### List Claim Refs` and its status table.

**Verified facts (do not re-derive or contradict):**

- Counterfeiter cannot generate a usable fake for the current unexported `claimOrigin`. Same-package output (`-o claim_origin_fake_test.go`) emits `git.remoteRef` references without an import, which does not compile. Output into `mocks/` cannot reference the unexported `remoteRef`. The working shape, verified with `go generate`, is this: export the interface as `ClaimOrigin` and the element type as `RemoteRef`, then put `//counterfeiter:generate -o ../../mocks/claim_origin.go --fake-name FakeClaimOrigin . ClaimOrigin` directly above `type ClaimOrigin interface`. `make generate` then writes `mocks/claim_origin.go` (package `mocks`, importing `github.com/bborbe/git-rest/pkg/git`).
- `mocks` imports `pkg/git`, so a `package git` (internal) test cannot import `mocks`: that is an import cycle. Tests that use `mocks.FakeClaimOrigin` must live in `package git_test` and reach the unexported `git` struct through a hook in `pkg/git/export_test.go`.
- `make generate` runs `rm -rf mocks`, recreates `mocks/mocks.go`, and runs `go generate -mod=mod ./...`. It regenerates every mock. Never hand-edit a file under `mocks/`.
- `git fetch --no-tags --no-write-fetch-head origin <sha>` already works for a single sha in `TestClaimListRealRepo`. Passing several shas as separate positional arguments to one `git fetch` uses the same want-by-sha mechanism.
- `git ls-remote --refs origin <pattern>` matches by ref-name tail, so the re-list result must be filtered by exact equality `RemoteRef.Ref == <listed ref>`.
- `docs/api.md` states no series count for `git_rest_ref_requests_total`, so the docs need no count change. `docs/deployment.md` says "the common op/result pairs are pre-initialised", which stays accurate.
- The `CHANGELOG.md` `## Unreleased` section already has the `feat:` bullet for the claim-ref endpoints. The feature is unreleased and these fixes refine it, so do not add a CHANGELOG bullet.
</context>

<requirements>

## 1. Export the origin seam and add the Counterfeiter directive (`pkg/git/claim_refs.go`, `pkg/git/git.go`)

1. Rename `remoteRef` to `RemoteRef` (fields `SHA string`, `Ref string` unchanged) and `claimOrigin` to `ClaimOrigin`. Update every reference: `parseRemoteRefs`, `gitOrigin` methods, `ListClaimRefs`, `readClaimRef`, and the `origin` field type in the `git` struct in `pkg/git/git.go`. Keep `gitOrigin` unexported and leave the `New` signature unchanged.
2. Change the `ClaimOrigin` method set to:
   ```go
   type ClaimOrigin interface {
   	ListRefs(ctx context.Context, pattern string) ([]RemoteRef, error)
   	FetchCommits(ctx context.Context, shas []string) error
   	ReadCommitMessage(ctx context.Context, sha string) ([]byte, error)
   }
   ```
   `FetchCommits` replaces `FetchCommit`. Remove `FetchCommit` entirely.
3. Directly above `type ClaimOrigin interface`, add a doc comment, an empty `//` line, and the directive. This matches the `Git` interface in `pkg/git/git.go`:
   ```go
   // ClaimOrigin abstracts the origin-contacting git operations used by claim-ref
   // listing. The production implementation is gitOrigin; tests inject a fake.
   //
   //counterfeiter:generate -o ../../mocks/claim_origin.go --fake-name FakeClaimOrigin . ClaimOrigin
   ```
4. Implement `(*gitOrigin).FetchCommits` as one `o.g.runCmd(ctx, o.g.repoPath, ...)` call with arguments `fetch`, `--no-tags`, `--no-write-fetch-head`, `origin`, then every sha in `shas` in the given order as separate arguments. Do not add a refspec, a `:` destination or any other flag. With an empty `shas` it returns `nil` without running git. Do not chunk the argument list.
5. Run `make generate`. It must produce `mocks/claim_origin.go` with `FakeClaimOrigin`, and regenerate `mocks/git.go` (only the `ListClaimRefs` doc comment changes there, if anything).

## 2. Batched fetch with a per-sha fallback (`ListClaimRefs`)

Restructure `ListClaimRefs` (and split helpers as needed to stay within funlen 80 / nestif 4). Keep these unchanged: the initial `ListRefs(ctx, "refs/"+prefix+"*")` call and its error wrap, `matchesClaimPrefix` filtering, sort by `Ref`, and the non-blocking `ctx.Done()` check per entry.

1. If no entry matched, return an empty non-nil `[]ClaimRef{}` without calling `FetchCommits`.
2. Otherwise call `g.origin.FetchCommits(ctx, shas)` exactly once, where `shas` holds the `SHA` of every matched entry in sorted-ref order.
3. **Batch succeeded:** for each entry, read and parse its commit message as today (`ReadCommitMessage`, `parseClaimMetadata`, non-JSON message → keep the entry with empty metadata and one WARN). Do not make any further `FetchCommits` call. A `ReadCommitMessage` error is still returned wrapped (→ `500`).
4. **Batch failed:** log the batch failure at `slog.DebugContext`, never at WARN, because the per-entry outcome below owns the WARN. Then handle each entry on its own: call `g.origin.FetchCommits(ctx, []string{entry.SHA})`. If that succeeds, read and parse as in step 3. If it fails, apply section 3.

## 3. Fetch failure classification (replaces the unconditional drop in `readClaimRef`)

When fetching one entry's sha fails:

1. Re-list that single ref: `g.origin.ListRefs(ctx, entry.Ref)`. Filter the result by exact `Ref == entry.Ref` equality.
2. **Ref absent** (no exact match) **or pointing at a different sha** (match with `SHA != entry.SHA`): drop the entry. Log exactly one `slog.WarnContext` line naming the ref (`"ref", entry.Ref`), the listed sha (`"sha", entry.SHA`) and the fetch error. Continue with the next entry. The listing still returns `200` (spec Desired Behavior 1: vanished/moved ref → drop + WARN, never `500`).
3. **Ref still at `entry.SHA`:** return an error that wraps the fetch error with `errors.Wrapf(ctx, ...)` and names the ref and sha. `ListClaimRefs` returns it, so `GET` answers `500`.
4. **Re-list itself fails** (transport, auth, `ErrNoRemote`): return an error that wraps the re-list error with `errors.Wrapf(ctx, ...)` and names the ref and the original fetch error in the message. `GET` answers `500`. No retry of any kind.
5. Update the doc comments on `ListClaimRefs` in `pkg/git/claim_refs.go` and on the `Git` interface method in `pkg/git/git.go` to say: one batched fetch of all listed shas; on failure, per-sha fetches; an entry is dropped with one WARN only when a re-list shows its ref vanished or moved; otherwise the error is returned.

## 4. Replace the hand-written fake with the generated mock

1. Add to `pkg/git/export_test.go`:
   ```go
   // NewGitWithClaimOrigin returns a Git whose claim-ref listing talks to origin
   // instead of the git binary. Test builds only.
   func NewGitWithClaimOrigin(origin ClaimOrigin) Git {
   	return &git{origin: origin}
   }
   ```
2. In `pkg/git/claim_refs_internal_test.go`, delete `fakeClaimOrigin`, its methods, `newFakeClaimOrigin`, `captureSlog`, `TestClaimListFetchBySha`, `testClaimListFetchesBySHA`, `testClaimListVanishedSHA` and `assertOneClaimWarning`. Keep `TestIsLeaseConflict` unchanged. Remove imports that become unused.
3. Create `pkg/git/claim_refs_list_test.go` in `package git_test`, with the three-line BSD license header copied from `pkg/git/claim_refs.go`. Use plain `testing`, like the other `TestClaim*` tests, so the spec's `-run` evidence commands keep matching. Use `&mocks.FakeClaimOrigin{}` from `github.com/bborbe/git-rest/mocks` and `git.NewGitWithClaimOrigin(fake)`. Configure behaviour with `ListRefsStub` (keyed on the `pattern` argument), `FetchCommitsStub` (keyed on the `shas` argument) and `ReadCommitMessageStub` (keyed on sha). Assert with `FetchCommitsCallCount()`, `FetchCommitsArgsForCall(i)` and `ListRefsArgsForCall(i)`. Capture logs with the existing `captureSlogLogs()` helper in `pkg/git/git_test.go` (same package `git_test`, returns `(*bytes.Buffer, func())`, Info level); do not add a second capture helper. Re-create the one-warning assertion `assertOneClaimWarning` in this file. Do not call `t.Parallel()` in these subtests: the helper swaps the global `slog.Default()`.
4. Keep the test function name `TestClaimListFetchBySha` (spec AC8 runs it by name) with these subtests. The two existing ones must keep their assertions:
   - **"fetches every listed commit by sha"** (existing): two listed refs `refs/claims/a`→sha A, `refs/claims/b`→sha B, both with valid JSON messages. Assert 2 claims, the shas fetched are exactly `[A, B]` in that order (now the args of the single `FetchCommits` call), and the first claim is `Ref "refs/claims/a"`, `Cluster "one"`, `TaskID "t-1"`.
   - **"vanished sha is dropped with one warning"** (existing): the batch fails, the per-sha fetch of B fails, and the re-list of `refs/claims/b` returns no entry. Assert no error, exactly one claim `refs/claims/a`, and exactly one WARN line, which names `refs/claims/b`.
   - **"moved sha is dropped with one warning"** (new): same as the vanished row, but the re-list returns `refs/claims/b` at a different sha. Same assertions.
   - **"fetch failure with ref still present returns error"** (new): the batch fails, the per-sha fetch of B fails, and the re-list returns `refs/claims/b` at sha B. Assert `ListClaimRefs` returns a non-nil error and no claims. Assert the second `ListRefs` call used pattern `refs/claims/b`.
   - **"re-list failure returns error"** (new): the batch fails, the per-sha fetch of B fails, and `ListRefs("refs/claims/b")` returns an error. Assert a non-nil error. Assert no WARN line names `refs/claims/b` as dropped.
   - **"one batched fetch for N claims"** (new): five listed refs with distinct shas and valid messages. Assert `FetchCommitsCallCount() == 1`, its single argument equals all five shas in sorted-ref order, and five claims are returned.
   - **"no matched refs makes no fetch"** (new): `ListRefs` returns nothing. Assert an empty non-nil result and `FetchCommitsCallCount() == 0`.
5. `TestClaimListRealRepo` and every other test in `pkg/git/claim_refs_test.go` must pass unchanged. They exercise the real batched `git fetch` against a local bare origin.

## 5. One shared object-id validator (`pkg/git`, `pkg/handler/refs_validate.go`)

1. In `pkg/git/claim_refs.go`, add an exported predicate with a doc comment:
   ```go
   // IsObjectID reports whether sha is a full-length lowercase hex object id
   // (40 hex for SHA-1, 64 hex for SHA-256).
   func IsObjectID(sha string) bool
   ```
   It returns `expectedSHAPattern.MatchString(sha)`. Make `validateExpectedSHA` use `IsObjectID`. Its behaviour stays unchanged: empty is accepted, and anything else invalid wraps `ErrInvalidRefName`.
2. In `pkg/handler/refs_validate.go`, delete `refExpectedSHAPattern` and the `regexp` import. Replace both `refExpectedSHAPattern.MatchString(...)` uses in `validateRefRequest` and `validateRefExpectedSHA` with `git.IsObjectID(...)`. Error messages and status codes stay unchanged.
3. Add a table test `TestClaimIsObjectID` (plain `testing`, `package git_test`, in `pkg/git/claim_refs_list_test.go`). Rows: 40 lowercase hex → true; 64 lowercase hex → true; empty → false; 39 hex → false; 41 hex → false; 40 hex with one uppercase letter → false; 40 chars containing `:` → false; 40 chars containing `g` → false.
4. The existing handler `400` rows for a bad `expected_sha` (`pkg/handler/refs_test.go`, `claim_refs_http_test.go`) must pass unchanged.

## 6. Pre-initialise the `413` series (`pkg/metrics/metrics.go`)

1. In the `init` block, after the `{get,put,delete} × {200,400,409,500}` loop, add `RefRequestsTotal.WithLabelValues("put", "413").Add(0)` with a one-line comment: only `PUT` carries a body, so `413` is a put-only result. Do not pre-initialise `get/413` or `delete/413`. Add no other label value.
2. In `pkg/metrics/metrics_test.go` `Describe("RefRequestsTotal")`: change the `It` description from "twelve" to "thirteen" and include `put/413`. Change `Expect(count).To(Equal(12))` to `Equal(13)`. Additionally assert `seen["put/413"]` is true. Keep the existing loop over the 12 base pairs.
3. In `claim_refs_http_test.go` `refCounterSnapshot`: also record `put/413` (capacity 13), so the auth-rejection and per-request delta checks cover the new series. `expectOnlyRefCounterDelta` stays unchanged.

## 7. Docs (`docs/api.md`)

In `### List Claim Refs`, after the sentence about non-JSON messages, add two sentences. First: a claim whose ref is deleted or moved between listing and fetching is dropped from the result, and a warning is logged. Second: if a claim commit cannot be fetched while its ref still points at it, or the origin cannot be re-listed, the request answers `500`. Change the `500` row of that section's status table to: `Origin unreachable, no origin configured, or a listed claim commit could not be fetched`. Do not change any other section. Do not add a series count.

## 8. Self-check

Run every command in `<verification>` from the repo root and confirm each exits 0. Walk spec AC8 (ii) (fetch by sha; vanished-sha row → `200` with one WARN), AC10 (series pre-initialised at 0; increments move only the asserted pair) and AC15 against the change.

</requirements>

<constraints>
- Do NOT commit; dark-factory handles git.
- Existing tests must still pass. The two existing `TestClaimListFetchBySha` subtests keep their assertions; only the fake they use changes.
- Never hand-edit files under `mocks/`; regenerate with `make generate`.
- Every new Go file carries the three-line BSD license header, or `addlicense` fails `make precommit`.
- Use `github.com/bborbe/errors` (`errors.Wrap` / `errors.Wrapf` / `errors.Errorf` with `ctx`) for every new error; never `fmt.Errorf`.
- Claim commits are fetched by sha only: keep `--no-tags --no-write-fetch-head`, no refspec, never into `refs/remotes/origin/*`, never by ref name. `remote.origin.fetch` and `refs/remotes/origin/*` of the served clone stay unchanged (spec AC9).
- No server-side retry of any ref push or fetch beyond the single batched-then-per-sha fallback specified here. A fetch failure for a ref that still exists is answered `500`, not retried.
- A ref that moved or was deleted between list and fetch is dropped with exactly one WARN naming the ref and answered `200`, never `500` (spec Desired Behavior 1).
- Every git subprocess stays bound by the request context; do not detach or extend contexts.
- Do NOT change the `Git` interface method set, the `New` signature, the HTTP routes, status mapping in `pkg/handler/refs_get.go`, the file endpoints, the puller, readiness or the probes.
- Do NOT add a configuration flag, environment variable or CLI argument. `refs/claims/` is invariant.
- Do NOT put the claim key, the prefix or the method into a metric label. Existing metric names, labels and pre-initialised series are unchanged; the `put/413` series is additive.
- Do NOT add a CHANGELOG bullet; the existing `## Unreleased` `feat:` bullet covers this unreleased feature.
- Do NOT edit `specs/`.
- Git is driven by the system `git` binary, matching the existing client.
</constraints>

<verification>
Run from the repo root. The container's `.git` is masked, so this block contains no git commands against the project repo. The `git` commands inside the tests run against temporary repositories they create.

Generated mock exists and the hand-written fake is gone (each must exit 0):

```bash
test -f mocks/claim_origin.go
grep -n 'type FakeClaimOrigin struct' mocks/claim_origin.go
grep -n 'counterfeiter:generate -o ../../mocks/claim_origin.go --fake-name FakeClaimOrigin . ClaimOrigin' pkg/git/claim_refs.go
! grep -q 'fakeClaimOrigin' pkg/git/claim_refs_internal_test.go
! grep -q 'refExpectedSHAPattern' pkg/handler/refs_validate.go
grep -n 'git.IsObjectID' pkg/handler/refs_validate.go
grep -n '"put", "413"' pkg/metrics/metrics.go
grep -n 'FetchCommitsCallCount' pkg/git/claim_refs_list_test.go
```

Focused runs (must exit 0):

```bash
go test -race -count=1 -run 'TestClaim' ./...
go test -race -count=1 -run 'TestClaimList(RealRepo|FetchBySha)' ./...
go test -race -count=1 ./pkg/git/... ./pkg/handler/... ./pkg/metrics/...
```

Full gate (must exit 0):

```bash
ROOTDIR=/workspace make precommit
```
</verification>
