---
status: completed
spec: [018-leased-claim-ref-operations]
summary: 'Added the git-layer claim-ref API to pkg/git: live origin listing, lease-based create/renew/delete with ErrRefConflict classification, ref-name validation, regenerated FakeGit mock, and race/safety tests proving single-winner semantics and untouched served branch/sync state.'
execution_id: git-rest-claim-refs-exec-069-spec-018-claim-ref-git-layer
dark-factory-version: v0.198.7
created: "2026-10-21T18:06:52Z"
queued: "2026-10-21T18:17:00Z"
started: "2026-10-21T18:17:02Z"
completed: "2026-10-21T18:26:22Z"
branch: dark-factory/leased-claim-ref-operations
---

# Add the claim-ref git layer: origin listing, lease pushes and ref-name validation

<summary>
- git-rest gains a git-layer API to list, create/renew and delete claim refs under `refs/claims/` on the origin.
- Every create, renew and delete is a compare-and-swap against the origin; a lost race is reported as a distinct conflict error, never retried.
- Each claim commit is parentless, sits on the empty tree and carries a JSON message with the claim metadata plus a fresh random nonce, so two writes of the same claim never produce the same commit.
- Listing reads live from the origin, fetching each listed commit by its sha and never through the served branch's fetch refspec.
- Claim ref names are validated before any write or origin contact; traversal, empty components and refspec metacharacters are rejected.
- Two independent clones racing the same key against one bare origin produce exactly one winner, proven by repeated concurrent tests.
- The served branch, its sync refspec and its remote-tracking refs are provably untouched by claim operations.
- Claim metadata round-trips: the three caller fields are readable back, `nonce` is never exposed, and non-JSON messages degrade to empty metadata with a warning.
- No HTTP handler, route, metric, documentation or changelog change is made in this prompt.
</summary>

<objective>
Build the git-layer claim-ref operations that the HTTP layer will sit on: list claim refs live from the origin, push create/renew/delete with the origin's compare-and-swap lease, classify a lost race as a conflict, and validate claim ref names before anything is written. Prove the lease semantics with two independent clones against one local bare origin, and prove that no claim operation can touch the served branch or the served clone's sync state.
</objective>

<context>
There is no repo-root `CLAUDE.md` in this worktree — do not look for one. Project conventions live in `docs/dod.md` and the coding-plugin guides below.

Intentional: tests use plain `testing` (the spec's `-run` evidence commands need named Go tests); CHANGELOG is owned by prompt 3 — neither is a DoD blocker.

Read these coding-plugin guides before implementing (paths inside the YOLO container):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md` — public interface + private struct + `New*` constructor, sentinel errors, `errors.Is`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors` API, sentinel errors with the `stderrors` alias, never `fmt.Errorf`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo/Gomega for the existing suite, `package git_test`, coverage ≥80% for new code
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md` — linter limits (funlen 80, nestif 4, golines 100), license headers, gosec
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md` — non-blocking context check in per-item loops
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md` — new-code coverage and verification rules

Files to read in full before editing (anchor by symbol name, not line number):

- `pkg/git/git.go` — the `Git` interface, `New`, the `git` struct, and the subprocess helpers `runCmd`, `runCmdOutput`, `runCmdRaw`, `runCmdEnv`, `childEnv`, `hasRemote`. Reuse these helpers; do not invent a second git-execution layer.
- `pkg/git/git_test.go` — `runGit`, `initRepo`, `noopMetrics`, and the external-test style. Note `initRepo` returns a work repo plus a bare remote and configures a git identity.
- `pkg/git/git_suite_test.go` — the Ginkgo suite entry point. Do not add a second suite.
- `pkg/git/export_test.go` — the existing pattern for exposing internals to tests.
- `pkg/metrics/metrics.go` — the `Metrics` interface implemented by `noopMetrics` in `pkg/git/git_test.go`; this prompt does not change it.

**Verified facts — do not re-derive, do not contradict:**

- `pkg/git/git.go` declares the interface exactly as `//counterfeiter:generate -o ../../mocks/git.go --fake-name FakeGit . Git` above `type Git interface`. `make generate` / `go generate ./...` regenerates `mocks/git.go`; never hand-edit a mock.
- `git.New(repoPath string, m metrics.Metrics, currentDateTimeGetter libtime.CurrentDateTimeGetter, sshKeyPath SSHKeyPath, resolver ConflictResolver) Git` returns the interface. It currently constructs `&git{...}` directly.
- The empty tree object id is `4b825dc642cb6eb9a060e54bf8d69288fbee4904`.
- `git commit-tree <tree> -m <message>` creates a parentless commit and needs the repository's configured `user.name` / `user.email`.
- Verified against git 2.39.5 in this container:
  - `git push --force-with-lease=refs/claims/x: origin <sha>:refs/claims/x` fails with `! [rejected] ... (stale info)` when the ref already exists.
  - The same command with a non-empty lease fails with `(stale info)` when the current ref differs from the expected sha.
  - `git push --force-with-lease=refs/claims/x:<sha> origin :refs/claims/x` reports `(stale info)` when the ref is absent or holds a different sha.
  - `git ls-remote --refs origin 'refs/claims/*'` prints `<sha>\t<ref>` lines and exits 0 with no output when nothing matches.
  - `git fetch --no-tags --no-write-fetch-head origin <sha>` fetches the commit by sha, writes no `FETCH_HEAD`, and the object is then readable with `git cat-file -p <sha>`.
  - `git check-ref-format refs/claims/<key>` rejects `claims/../heads/main`, `claims/a..b`, `claims/x.lock`, `claims/a b`, `claims/a~1`, `claims/a:b`, `claims//x` and `claims/x/`, and accepts `claims/task/t-1`. It also accepts `heads/main`, so the `claims/` prefix must be enforced in Go, not left to `check-ref-format`.
- `git push` skips the lease check when the source sha already equals the current ref sha and reports `Everything up-to-date` with exit 0. The random nonce is what prevents that case from ever arising for two separate requests.
- The served clone's default fetch refspec is `+refs/heads/*:refs/remotes/origin/*`; claim commits are fetched by sha and never into `refs/remotes/origin/*`.

**Open question for the reviewer:** `expected_sha` is validated as a full-length lowercase hex object id. The repo uses SHA-1 (40 hex chars); the validator accepts 40 or 64 so a future SHA-256 repository is not rejected. If the reviewer wants exactly 40, tighten the regexp in one place (`validateExpectedSHA`).

**Scope:** this prompt touches `pkg/git/` (implementation + tests), the `Git` interface in `pkg/git/git.go`, and the regenerated `mocks/git.go`. It does not touch `pkg/handler/`, `pkg/factory/`, `pkg/metrics/`, `main.go`, `export_test.go`, docs or `CHANGELOG.md`.
</context>

<requirements>

## 1. Add the claim-ref types and sentinels in a new file `pkg/git/claim_refs.go`

Copy the three-line license header from `pkg/git/git.go` verbatim. Use `stderrors "errors"` for sentinels, matching `git.go`.

```go
// ClaimRef is one claim ref as read live from the origin. The JSON field set is
// the HTTP response contract: exactly these five keys, never a nonce.
type ClaimRef struct {
    Ref        string `json:"ref"`
    SHA        string `json:"sha"`
    Cluster    string `json:"cluster"`
    TaskID     string `json:"task_id"`
    LeaseUntil string `json:"lease_until"`
}

// ClaimRefRequest is the create/renew request. ExpectedSHA empty means the ref
// must not exist.
type ClaimRefRequest struct {
    ExpectedSHA string `json:"expected_sha"`
    Cluster     string `json:"cluster"`
    TaskID      string `json:"task_id"`
    LeaseUntil  string `json:"lease_until"`
}

var ErrRefConflict = stderrors.New("claim ref conflict")
var ErrInvalidRefName = stderrors.New("invalid claim ref name")
var ErrNoRemote = stderrors.New("no origin remote configured")
```

Add a doc comment to every exported symbol. Do not add JSON tags or fields beyond those shown.

## 2. Extend the `Git` interface in `pkg/git/git.go`

Add exactly these three methods to `type Git interface` (place them after `Init`):

```go
// ListClaimRefs returns the claim refs under refs/<prefix>, read live from the
// origin. A fetch that fails because the ref moved or was deleted between
// listing and fetching drops that entry and logs one WARN.
ListClaimRefs(ctx context.Context, prefix string) ([]ClaimRef, error)
// PutClaimRef creates or renews refs/<key> with the origin's compare-and-swap.
// It returns the new commit sha. A lease mismatch returns ErrRefConflict.
PutClaimRef(ctx context.Context, key string, request ClaimRefRequest) (string, error)
// DeleteClaimRef deletes refs/<key> with the origin's compare-and-swap.
// A lease mismatch or an absent ref returns ErrRefConflict.
DeleteClaimRef(ctx context.Context, key string, expectedSHA string) error
```

Do not change any existing method signature, do not change `New`'s signature, and do not change the counterfeiter directive. Regenerate `mocks/git.go` with `go generate ./...` (or `make generate`) after the interface edit; do not hand-edit it.

## 3. Add the origin seam and implement listing

In `pkg/git/claim_refs.go`, add an unexported seam so the list path is testable without a real origin:

```go
type remoteRef struct {
    SHA string
    Ref string
}

// claimOrigin abstracts the origin-contacting git operations used by claim-ref
// listing. The production implementation is gitOrigin; tests inject a fake.
type claimOrigin interface {
    ListRefs(ctx context.Context, pattern string) ([]remoteRef, error)
    FetchCommit(ctx context.Context, sha string) error
    ReadCommitMessage(ctx context.Context, sha string) ([]byte, error)
}
```

- Add a field `origin claimOrigin` to the private `git` struct.
- In `New`, construct the struct into a local variable, set `g.origin = &gitOrigin{g: g}`, then return it. Do not change `New`'s parameter list.
- Implement `gitOrigin` in the same file:
  - `ListRefs`: when `g.hasRemote(ctx)` is false, return a wrapped `ErrNoRemote` with message `no origin remote configured`. Otherwise run `git ls-remote --refs origin <pattern>` with `runCmdOutput`, parse each non-empty line by splitting on the first tab into sha and ref, and return the entries. Never shell out; pass arguments separately.
  - `FetchCommit`: `git fetch --no-tags --no-write-fetch-head origin <sha>` via `runCmd`. This must never write `FETCH_HEAD` and must never update `refs/remotes/origin/*`.
  - `ReadCommitMessage`: `git cat-file -p <sha>` via `runCmdOutput`, then return the bytes after the first blank line (`\n\n`). If there is no blank line, return the whole output.

Implement `ListClaimRefs` on `*git` as orchestration over the seam:

1. `entries, err := g.origin.ListRefs(ctx, "refs/"+prefix+"*")`; wrap any error.
2. Keep an entry only when it matches the prefix by whole path components:
   - prefix ends in `/`: `strings.HasPrefix(ref, "refs/"+prefix)`;
   - otherwise: `ref == "refs/"+prefix || strings.HasPrefix(ref, "refs/"+prefix+"/")`.
   This is what makes `claims/task/1` never match `claims/task/10` and `claims/task` never match `claims/tasks/1`.
3. Sort the kept entries by ref name.
4. Check `ctx.Done()` non-blockingly at the top of each per-entry iteration and return a wrapped cancellation error if done.
5. For each entry call `g.origin.FetchCommit(ctx, entry.SHA)`. On error, log one `slog.WarnContext` line naming the dropped ref and continue; this is the vanished-sha case and must never fail the whole list.
6. Call `g.origin.ReadCommitMessage(ctx, entry.SHA)`. On error, return a wrapped error (the commit was fetched, so a read failure is a real 500-class failure).
7. Parse the message with `parseClaimMetadata`. On a parse error, log one `slog.WarnContext` naming the ref and continue with empty `Cluster`, `TaskID`, `LeaseUntil`.
8. Append a `ClaimRef` with `Ref`, `SHA` and the parsed fields. Return a non-nil `[]ClaimRef` (use `make([]ClaimRef, 0, len(entries))`) so the HTTP layer can encode `[]`, never `null`.

Add unexported helpers `matchesClaimPrefix(ref, prefix string) bool` and `parseClaimMetadata(ctx context.Context, message []byte) (cluster, taskID, leaseUntil string, err error)` (or an equivalent named struct return). `parseClaimMetadata` JSON-decodes into a struct with only `cluster`, `task_id`, `lease_until`; unknown keys are ignored; a non-JSON message returns an error. `nonce` is never parsed into `ClaimRef`.

Keep every function under the linter's 80-line `funlen` limit by extracting the parse/filter/append steps into helpers.

## 4. Implement create/renew and delete with the origin lease

Add `PutClaimRef` and `DeleteClaimRef` on `*git`. Both take the full key including the `claims/` prefix, e.g. `claims/task/t-42`; the ref is `"refs/" + key`.

`PutClaimRef`:

1. Call `ValidateClaimKey(ctx, key)` and `validateExpectedSHA(ctx, request.ExpectedSHA)`; return their errors unchanged. Then, when `g.hasRemote(ctx)` is false, return `errors.Wrap(ctx, ErrNoRemote, "no origin remote configured")` before creating any commit — nothing is written locally.
2. Generate a nonce: 16 bytes from `crypto/rand`, rendered with `hex.EncodeToString` as 32 lowercase hex characters. Return a wrapped error if `rand.Read` fails.
3. Marshal exactly this message object with `encoding/json` (field order is not part of the contract, key set is): `{"cluster":…,"task_id":…,"lease_until":…,"nonce":…}` using the request's three values and the generated nonce. A `nonce` supplied by the caller is impossible here: `ClaimRefRequest` has no nonce field.
4. Create the commit with `runCmdOutput(ctx, g.repoPath, "commit-tree", emptyTreeSHA, "-m", string(message))` where `emptyTreeSHA` is the constant `4b825dc642cb6eb9a060e54bf8d69288fbee4904`. Trim the returned sha. This commit has no parent.
5. Push with `runCmdRaw(ctx, g.repoPath, "push", "--force-with-lease=refs/<key>:<expected_sha>", "origin", "<sha>:refs/<key>")`. The lease argument is `"--force-with-lease=" + ref + ":" + request.ExpectedSHA`; an empty expected sha is the valid `refs/<key>:` form meaning "must not exist".
6. On push failure, classify the combined output:
   - conflict when it contains `stale info`, `remote ref does not exist` or `cannot lock ref`, or has a `[remote rejected]` line whose parenthesised reason contains `expected`, `failed to update ref`, `reference already exists` or `reference does not exist`; other `remote:` hook lines never decide the class;
   - otherwise return the wrapped push error (includes `remote rejected` with a hook reason, transport/auth failures and timeouts).
   A conflict returns `errors.Wrapf(ctx, ErrRefConflict, "push %s rejected", ref)`. Do not retry any failure class.
7. Return the commit sha on success.

`DeleteClaimRef`:

1. Call `ValidateClaimKey(ctx, key)` and require a non-empty, valid `expectedSHA` via `validateExpectedSHA` (a delete without an expected sha is invalid; the HTTP layer also rejects it). Then, when `g.hasRemote(ctx)` is false, return `errors.Wrap(ctx, ErrNoRemote, "no origin remote configured")`.
2. Push with `runCmdRaw(ctx, g.repoPath, "push", "--force-with-lease="+ref+":"+expectedSHA, "origin", ":"+ref)`.
3. Classify failures with the same helper as `PutClaimRef`; a conflict or an absent ref returns a wrapped `ErrRefConflict`. Everything else is wrapped and returned.
4. Do not create a commit and do not write anything locally.

Both methods must be bound by the request context through the existing `exec.CommandContext` helpers, and must push each request at most once. Do not take `g.mu`: claim operations touch the origin and the object database only; the origin's compare-and-swap is the only correctness mechanism. State this in a short comment on the methods.

Extract the push-failure classifier as an unexported `isLeaseConflict(output string) bool` so both methods and the tests share it. Add a table test of `isLeaseConflict` in `pkg/git/claim_refs_internal_test.go` covering each conflict marker and non-conflict samples, including a hook-decline sample (a `remote:` line plus a `[remote rejected] ... (pre-receive hook declined)` line) containing the word "expected" in its `remote:` text that must classify as NOT a conflict.

## 5. Add claim ref-name and expected-sha validation

In `pkg/git/claim_refs.go`:

```go
// ValidateClaimKey rejects any key that is not a safe refs/claims/ ref name.
// The key is the full path below refs/, e.g. "claims/task/t-42".
func ValidateClaimKey(ctx context.Context, key string) error
```

Rules, in this order:

1. The key must start with `claims/`; otherwise return a wrapped `ErrInvalidRefName`.
2. The remainder after `claims/` must be non-empty.
3. Split the key on `/`; reject any empty component, `.` or `..` with a wrapped `ErrInvalidRefName`. (This runs before `check-ref-format` so the error is deterministic.)
4. Run `git check-ref-format refs/<key>` with `exec.CommandContext` and the arguments passed separately. A non-zero exit returns a wrapped `ErrInvalidRefName`. Do not use a shell and do not interpolate the key into a command string.
5. Return nil only when every rule passes.

Add `validateExpectedSHA(ctx context.Context, sha string) error`: empty is valid; otherwise it must match `^([0-9a-f]{40}|[0-9a-f]{64})$`; a mismatch returns a wrapped `ErrInvalidRefName`.

`ValidateClaimKey` must not contact the origin and must not write anything. `git check-ref-format` is the only subprocess it runs.

## 6. Add the external tests in a new file `pkg/git/claim_refs_test.go`

Use `package git_test`, the three-line license header, and **plain Go `testing` functions** — not Ginkgo `It` blocks — because the acceptance evidence runs `go test -run 'TestClaim…'`. The package already has a Ginkgo `TestSuite`; a package may contain both. Reuse `runGit` and `noopMetrics` from `git_test.go` where they fit; add `t.Helper()`-style helpers in the new file with distinct names (e.g. `mustGit`, `newClaimFixture`) so nothing collides.

Add a fixture helper:

- Create a bare origin in `t.TempDir()` (`git init --bare`), a seed clone with a committed `main` branch pushed to the origin, and two independent clones `a` and `b`.
- Configure `user.name` and `user.email` in the seed and both clones.
- Build one `git.Git` per clone with `git.New(clonePath, &noopMetrics{}, libtime.NewCurrentDateTime(), "", nil)`. The resolver is unused by claim operations, so nil is correct.
- Return the bare origin path and the two clients. Correctness in the race tests must come from two independent clones and the origin, never from an in-process lock.

Write these tests with the exact names below; the first group must exit 0 under `go test -race -count=1 -run 'TestClaim(Create|Renew|Delete)Race' ./pkg/git/...`:

1. `TestClaimCreateRace` — 100 iterations. Each iteration uses a fresh key `claims/race/create-<i>`, both clients `PutClaimRef` concurrently with empty `ExpectedSHA` and distinct `Cluster` values. Assert exactly one nil error and one `errors.Is(err, git.ErrRefConflict)`, and that `git -C <bare> rev-parse refs/claims/race/create-<i>` equals the winner's returned sha. Fail with `t.Fatalf` naming the iteration on any other outcome.
2. `TestClaimRenewRace` — 100 iterations. Each iteration creates a fresh key through client A and records its sha `S0`, then both clients concurrently `PutClaimRef` with `ExpectedSHA=S0` and distinct clusters. Assert exactly one winner and one conflict, and the origin ref equals the winner's sha. Then, once, a single renew with a stale `ExpectedSHA` returns `ErrRefConflict` and the origin sha is unchanged.
3. `TestClaimDeleteRace` — 100 iterations. Each iteration creates a fresh key at `S0`, then both clients concurrently `DeleteClaimRef(key, S0)`. Assert exactly one nil and one `ErrRefConflict`, and `git -C <bare> rev-parse --verify --quiet refs/claims/<key>` exits non-zero afterwards. Also assert a `PutClaimRef` and a `DeleteClaimRef` with a non-empty expected sha against an absent key each return `ErrRefConflict`, and a second delete of an already-deleted key returns `ErrRefConflict`.
4. `TestClaimCreateRaceIdenticalBody` — the create race with byte-identical `ClaimRefRequest` values (same cluster, task, lease, empty expected). Same one-winner/one-conflict assertion over 100 iterations.
5. `TestClaimRenewRaceIdenticalBody` — the renew race with byte-identical bodies (`ExpectedSHA=S0`). Same assertion over 100 iterations.

Then:

6. `TestClaimRepeatIdenticalCreate` — create with empty expected returns a sha `S1`; the byte-identical request again returns `ErrRefConflict`; the origin ref still equals `S1`.
7. `TestClaimNonceUnique` — create, delete, create on one key with byte-identical bodies. Both creates succeed and their shas differ. Read each commit with `git -C <bare> cat-file -p <sha>` and assert the two `nonce` values differ and each matches `^[0-9a-f]{32}$`. Then `json.Unmarshal` the body `{"cluster":"c","task_id":"t","lease_until":"2026-10-21T12:05:00Z","nonce":"deadbeefdeadbeefdeadbeefdeadbeef"}` into `git.ClaimRefRequest` (no error), `PutClaimRef` it on a fresh key (success), and assert the committed `nonce` matches `^[0-9a-f]{32}$` and differs from `deadbeefdeadbeefdeadbeefdeadbeef`.
8. `TestClaimRenewTimeoutRecovery` — create `S0`, renew with `ExpectedSHA=S0` to `S1`, discard the response, then repeat the byte-identical renew. The repeat returns `ErrRefConflict`; the origin ref still equals `S1`; `ListClaimRefs(ctx, "claims/")` returns the key with `SHA == S1` and the `(Cluster, TaskID, LeaseUntil)` triple the holder wrote; a follow-up renew with `ExpectedSHA=S1` succeeds.
9. `TestClaimMetadataRoundTrip` — `PutClaimRef` with `{Cluster:"nuke", TaskID:"t-42", LeaseUntil:"2026-10-21T12:05:00Z"}`. Assert `ListClaimRefs(ctx, "claims/")` returns exactly one entry whose JSON encoding has exactly the keys `ref`, `sha`, `cluster`, `task_id`, `lease_until` (marshal the struct and compare the key set; assert no `nonce` key), with `Ref == "refs/claims/<key>"`, `SHA` equal to the PUT result and the three written values. Then assert `git cat-file -p <sha>` shows the empty tree `4b825dc642cb6eb9a060e54bf8d69288fbee4904`, no `parent` line, and a message that JSON-decodes to exactly the four keys `cluster`, `task_id`, `lease_until`, `nonce`, with the first three holding the written values and `nonce` matching `^[0-9a-f]{32}$`.
10. `TestClaimListRealRepo` — against a local bare origin:
    - Build each fixture commit in the bare origin with `git commit-tree <empty-tree> -m <message>` (configure identity via `-c user.name=… -c user.email=…` on the command), then create the ref with `git update-ref`. `update-ref` fails against a commit object that does not exist in the bare origin, so always create the commit first. Assert a ref created this way (not through the client) appears in the next `ListClaimRefs`; delete it with `git update-ref -d` and assert it disappears with no pull cycle.
    - Message forms written this way: (a) the 4-key form → the three fields; (b) the 4 keys plus `"extra":"x"` → the three fields, extra ignored; (c) the legacy 3-key form without `nonce` → valid; (d) a non-JSON message → listed with its `ref` and `sha` and empty-string metadata.
    - Prefix fixtures `claims/task/1`, `claims/task/10`, `claims/tasks/1`, `claims/repo/a/b`: `prefix=claims/task/1` returns exactly `refs/claims/task/1`; `prefix=claims/task` returns exactly `refs/claims/task/1` and `refs/claims/task/10`; `prefix=claims/repo/a` returns exactly `refs/claims/repo/a/b`.
    - For every returned object, its metadata equals the message of `git cat-file -p <object.SHA>`.
    - With no claim refs, `ListClaimRefs` returns a non-nil empty slice (so the HTTP layer encodes `[]`).
11. `TestClaimServedBranchUntouched` — record `git -C <bare> rev-parse refs/heads/<served>` and the full `git -C <bare> for-each-ref refs/heads refs/tags` listing. Run create → renew → stale renew → delete through the client, then call `ValidateClaimKey` for every AC4 name in its percent-decoded form: `heads/main`, `tags/x`, `claims/../heads/main`, `claims/` (empty key), `claims` (no key), `claims/a..b`, `claims/x.lock`, `claims/a b`, `claims/a~1`, `claims/a:b`, `claims//x`, `claims/x/`. Every name in the table must return `errors.Is(err, git.ErrInvalidRefName)`; `heads/main` and `tags/x` fail the prefix rule. The percent-encoded spelling `claims/%2E%2E/heads/main` is decoded by `net/http` before it reaches this layer, so it is exercised at the HTTP level in the next prompt, not here. Re-read both recordings and assert byte-identical values.
12. `TestClaimSyncStateUnchanged` — record `git -C <clone> config --get-all remote.origin.fetch` and `git -C <clone> for-each-ref refs/remotes/origin` for both clones, run a `ListClaimRefs` and a `PutClaimRef`, and assert both recordings are byte-identical afterwards.
13. `TestClaimInvalidKeyRejected` — the AC4 name table from requirement 11 against `PutClaimRef` and `DeleteClaimRef`: each returns `ErrInvalidRefName`, and the bare origin's `for-each-ref` listing is byte-identical before and after the whole table.
14. `TestClaimNoRemote` — a repo with no remote (`git init` + identity): `ListClaimRefs`, `PutClaimRef` and `DeleteClaimRef` each return `errors.Is(err, git.ErrNoRemote)`, and `git count-objects -v` output is identical before and after the `PutClaimRef` call.

Use `errors.Is` from the standard library for sentinel assertions (as `pkg/git/git_test.go` does). Use `exec.Command` only in test helpers, with arguments passed separately.

## 7. Add the recording-fake list test in a new file `pkg/git/claim_refs_internal_test.go`

Use `package git` (internal test, like `pkg/git/resolve_conflict_merge_test.go`) so the fake seam is reachable. License header required.

Implement `func TestClaimListFetchBySha(t *testing.T)`:

- Define a fake `claimOrigin` that returns two `remoteRef` entries with shas `A` and `B` under `refs/claims/...`, records every `FetchCommit` sha in order, and returns a canned message per sha.
- Construct `&git{origin: fake}` (the list path must not need `repoPath`).
- Assert `ListClaimRefs(ctx, "claims/")` returns two entries and the fake recorded exactly `A` then `B` as fetched shas — never a ref name.
- Vanished-sha row: make `FetchCommit(B)` return an error. Assert `ListClaimRefs` still returns nil error, returns only the `A` entry, and logs exactly one WARN that names the dropped ref. Capture the log by installing a `slog.NewTextHandler` over a `bytes.Buffer` via `slog.SetDefault` and restore the previous default with `defer` (see the existing log-capture pattern near the end of `pkg/git/git_test.go`).

## 8. Self-check before finishing

Run every command in `<verification>` from the repo root. Confirm the race tests fail loudly on any iteration with zero or two winners, that the served-branch and sync-state recordings are byte-identical, and that `make precommit` exits 0. Walk each acceptance criterion assigned to this prompt (AC1, AC2, AC3, AC6, AC7 git side, AC8, AC9, AC16, AC17, AC18, AC19 git side, AC20) against the change.

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT modify `pkg/handler/`, `pkg/factory/`, `pkg/metrics/`, `main.go`, `export_test.go`, `docs/`, `README.md`, `CHANGELOG.md` or `specs/`.
- Do NOT add a scenario file under `scenarios/` — the spec says no new scenario.
- Do NOT add a configuration flag, environment variable or CLI argument.
- Do NOT change `git.New`'s parameter list, any existing `Git` method signature, the counterfeiter directive, or any existing metric name or label.
- Do NOT take `g.mu` in any claim-ref method; the origin's compare-and-swap is the only correctness mechanism, and claim operations must not stall the puller.
- Do NOT retry any push, for any failure class.
- Do NOT hand-edit `mocks/git.go`; regenerate it.
- Do NOT run `go mod vendor` and do NOT use `-mod=vendor` in any command — this repo does not commit `vendor/`.
- Use `github.com/bborbe/errors` (`errors.Wrap`, `errors.Wrapf`) for wrapping; no `fmt.Errorf`. Sentinel errors use the `stderrors "errors"` alias.
- Every new Go file carries the three-line BSD license header, or `addlicense` fails `make precommit`.
- Every git subprocess is bound by the request context through the existing `exec.CommandContext` helpers.
- Pass git arguments as separate `exec` arguments, never through a shell. Add `// #nosec G204` comments with a reason on new `exec.CommandContext` calls, matching `pkg/git/git.go`.
- Keep functions under the linter limits (`funlen` 80, `nestif` 4, lines 100). `golangci-lint` runs in `make precommit`.
- New code needs ≥80% statement coverage; the tests in requirements 6 and 7 cover every branch of the new functions, including both conflict and non-conflict push outcomes, vanished sha, non-JSON metadata and invalid names.
- The served branch and every ref outside `refs/claims/` are never written by these methods; `remote.origin.fetch` and `refs/remotes/origin/*` of the served clone are unchanged by any claim operation.
</constraints>

<verification>
Run from the repo root. The container's `.git` is masked, so this block contains no git commands against the project repo. The `git` commands inside the Go tests run against temporary repositories they create, which is correct and required.

The acceptance-evidence runs (must exit 0):

```bash
go test -race -count=1 -run 'TestClaim(Create|Renew|Delete)Race' ./pkg/git/...
go test -race -count=1 -run 'TestClaim(List(RealRepo|FetchBySha)|RepeatIdenticalCreate|NonceUnique|RenewTimeoutRecovery)' ./pkg/git/...
go test -race -count=1 -run 'TestClaim(ServedBranchUntouched|SyncStateUnchanged|InvalidKeyRejected|MetadataRoundTrip|NoRemote)' ./pkg/git/...
```

The new layer exists and uses the verified primitives (each must print at least one match):

```bash
test -f pkg/git/claim_refs.go
grep -n 'func ValidateClaimKey' pkg/git/claim_refs.go
grep -n 'func (g \*git) ListClaimRefs' pkg/git/claim_refs.go
grep -n 'func (g \*git) PutClaimRef' pkg/git/claim_refs.go
grep -n 'func (g \*git) DeleteClaimRef' pkg/git/claim_refs.go
grep -n 'ErrRefConflict' pkg/git/claim_refs.go
grep -n 'force-with-lease' pkg/git/claim_refs.go
grep -n 'no-write-fetch-head' pkg/git/claim_refs.go
grep -n '4b825dc642cb6eb9a060e54bf8d69288fbee4904' pkg/git/claim_refs.go
grep -n 'ListClaimRefs' pkg/git/git.go
grep -n 'PutClaimRef' mocks/git.go
```

Hard constraints on the new file:

```bash
! grep -q 'fmt.Errorf' pkg/git/claim_refs.go
! grep -q 'g.mu.Lock' pkg/git/claim_refs.go
```

Final gate:

```bash
ROOTDIR=/workspace make precommit
```

Must exit 0.
</verification>
