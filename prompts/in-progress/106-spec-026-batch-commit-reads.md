---
status: approved
spec: [026-batch-claim-ref-commit-reads]
created: "2026-11-11T15:36:03Z"
queued: "2026-11-11T15:57:25Z"
branch: dark-factory/batch-claim-ref-commit-reads
---

<summary>
- A claim-ref listing now reads every commit message with one `git cat-file --batch` process instead of one process per matching ref.
- The live-origin path falls back to per-item reads only when the message batch fails, and still returns the complete listing.
- The mirror path keeps its existing behavior: a failed read returns "not served" and the caller takes the live origin; it gains no per-item fallback.
- The existing fetch-batch flag is renamed so a failed message batch cannot be confused with a failed fetch batch.
- A cancelled request still aborts with a wrapped `context.Canceled` and a nil result, before or during the batch.
- New tests count real git processes at 300 and 600 refs on both paths, prove the live fallback, prove the mirror fallback, and prove cancellation.
- The generated Counterfeiter mock is regenerated; no hand-written mock code.
- The HTTP surface, ordering, and `ClaimRef` fields are unchanged.
</summary>

<objective>
Make a claim-ref listing start a small, constant number of git processes regardless of how many refs match the prefix, on both the live-origin and mirror paths. Read all commit messages in one `git cat-file --batch` invocation, hoist that read out of the single-ref helper into both callers' loops, preserve the live path's per-item fallback and the mirror path's served=false fallback, and keep context cancellation semantics.
</objective>

<context>
There is no repo-root `CLAUDE.md` in this worktree — `.gitignore` lists `/CLAUDE.md`. Do not look for one. The definition of done is `docs/dod.md`.

Read these files before changing anything:
- `pkg/git/claim_refs.go` — `ClaimOrigin`, `gitOrigin`, `FetchCommits`, `ReadCommitMessage`, `listClaimRefsLive`, `fetchClaimCommits`, `readClaimRef`, `resolveFetchFailure`. The existing fetch-batch shape (`fetchClaimCommits` + `batchOK`) is the model to mirror.
- `pkg/git/claim_mirror.go` — `listClaimRefsFromMirror`, especially the per-ref `ReadCommitMessage` call and its `served=false` fallback.
- `pkg/git/git.go` — `runCmd`, `runCmdOutput`, `runCmdRaw`, the `git` struct, and the `#nosec G204` / `gitWaitDelay` / SSH-env conventions the runners share.
- `pkg/git/export_test.go` — `NewGitWithClaimOrigin`, `NewGitWithClaimMirrorForTest`, `SetClaimMirrorFreshAtForTest`.
- `pkg/git/claim_refs_list_test.go` — plain `testing.T`; `claimMessages`, `newClaimListFake`, `twoClaimRefs`, `failBatchAndB`, `testClaimListNoMatch`, and the golden fixture added by prompt 1.
- `pkg/git/claim_mirror_test.go` lines 29–34 and the fake helpers `newMirrorReadFake`, `newLiveFake`, `TestClaimMirrorMissingObjectFallsBack`, `TestClaimMirrorReadAfterWrite`.
- `pkg/git/claim_refs_internal_test.go` — plain `testing.T` in package `git`, for the parser unit test.
- `pkg/git/claim_refs_test.go` — `newClaimFixture`, `newClaimClient`, `mustGit`, `configureClaimIdentity`, `claimEmptyTreeSHA`.

Spec: `specs/in-progress/026-batch-claim-ref-commit-reads.md` — Desired Behavior, Failure Modes, Constraints, and Acceptance Criteria 1, 2, 4, 5.

Coding-plugin docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md` (rule `go-context/cancel-check-in-loop`)
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-mocking-guide.md`

The DoD's Ginkgo rule is overridden here by the spec's per-file constraint: `claim_mirror_test.go` and `claim_refs_list_test.go` stay plain `testing.T`, and the new tests in them stay plain `testing.T`.
</context>

<requirements>
1. Add a stdin-capable runner next to `runCmdOutput` in `pkg/git/git.go`:
   ```
   func (g *git) runCmdInput(ctx context.Context, dir string, stdin []byte, args ...string) ([]byte, error)
   ```
   It mirrors `runCmdOutput` exactly — `exec.CommandContext(ctx, "git", args...)`, the `#nosec G204` comment, `cmd.Dir = dir`, `cmd.WaitDelay = gitWaitDelay`, the same SSH `cmd.Env` block when `g.sshKeyPath != ""`, a stdout buffer and a stderr buffer — and additionally sets `cmd.Stdin = bytes.NewReader(stdin)`. On failure return `errors.Wrapf(ctx, err, "git %v: %s", args, stderr.String())`; on success return `stdout.Bytes()`.

2. Add the batched method to the `ClaimOrigin` interface in `pkg/git/claim_refs.go`, immediately after `ReadCommitMessage` (keep `ReadCommitMessage`):
   ```
   ReadCommitMessages(ctx context.Context, shas []string) (map[string][]byte, error)
   ```

3. Implement `ReadCommitMessages` on `*gitOrigin`:
   - Empty `shas` returns `map[string][]byte{}, nil` without invoking git.
   - Build stdin as `strings.Join(shas, "\n") + "\n"` and run one `runCmdInput(ctx, o.g.repoPath, []byte(input), "cat-file", "--batch")`; wrap a command failure with `errors.Wrap(ctx, err, "git cat-file --batch")`.
   - Parse the output into a map keyed by the input sha, using the same message semantics as `ReadCommitMessage`: cut the object content at the first `\n\n`; when no separator exists, use the whole content.
   - Treat a missing object (`<sha> missing`), a malformed header, a bad size, or truncated output as an error so the caller takes its path's fallback.
   - Put the parsing in a named helper `parseBatchCommitMessages(out []byte, shas []string) (map[string][]byte, error)` (no `context.Context`); `ReadCommitMessages` wraps its error with `errors.Wrap(ctx, err, "parse git cat-file --batch output")`. Use `stderrors.New` / `fmt.Errorf` inside the parser (add the `fmt`, `bytes`, and `strconv` imports as needed; `claim_refs.go` already aliases stdlib `errors` as `stderrors`).
   - The parser consumes exactly one header line plus `<size>` bytes plus one separator newline per input sha, in input order.

4. Restructure `listClaimRefsLive` in `pkg/git/claim_refs.go`:
   - Add an entry check: if `ctx.Err() != nil`, return `nil, errors.Wrap(ctx, ctx.Err(), "claim ref listing cancelled")`.
   - Rename the fetch-batch local `batchOK := g.fetchClaimCommits(ctx, matched)` to `fetchOK := ...`.
   - After the fetch batch and before the loop, call a new helper `readClaimCommits(ctx, matched) (map[string][]byte, bool)`. It collects the matched shas, calls `g.origin.ReadCommitMessages` once, and on error logs once at debug (`"claim ref batch message read failed; falling back to per-sha reads"`, with `"error", err`) and returns `nil, false`; on success returns the map and true.
   - Keep the existing top-of-iteration `select { case <-ctx.Done(): ... default: }` check unchanged (rule `go-context/cancel-check-in-loop`).
   - Change `readClaimRef` to `readClaimRef(ctx context.Context, entry RemoteRef, fetchOK bool, batchOK bool, messages map[string][]byte) (ClaimRef, bool, error)`:
     - Keep the existing `if !fetchOK { g.origin.FetchCommits(ctx, []string{entry.SHA}); ... resolveFetchFailure ... }` branch unchanged.
     - When `batchOK`, take `messages[entry.SHA]`; if the sha is absent from the map, return a wrapped error (`errors.Wrapf` over `stderrors.New("claim commit absent from batch")`).
     - When `!batchOK`, call `g.origin.ReadCommitMessage(ctx, entry.SHA)` and keep the existing error wrap. This must be the **only** `origin.ReadCommitMessage(` call site left in non-test, non-mock code.
     - Keep the `parseClaimMetadata` call and the unparseable-metadata WARN exactly as they are.
     - Do not change `resolveFetchFailure` or the vanished/moved-ref drop behavior: a ref that vanishes between listing and fetch is still dropped with exactly one WARN. `testClaimListVanishedSHA` and `testClaimListMovedSHA` must stay green.

5. Restructure `listClaimRefsFromMirror` in `pkg/git/claim_mirror.go`:
   - Add an entry check for `ctx.Err()` returning `nil, false, errors.Wrap(ctx, ctx.Err(), "claim ref listing cancelled")`.
   - After `matched` is built and sorted, collect the shas and call `g.origin.ReadCommitMessages` **once**, before the loop.
   - On error: if `ctx.Err() != nil`, return `nil, false, errors.Wrap(ctx, ctx.Err(), "claim ref listing cancelled")`; otherwise log the existing WARN ("reading a mirrored claim commit failed; using the live origin") and return `nil, false, nil`. Do **not** add a per-item fallback on the mirror path.
   - Keep the top-of-iteration `select` check unchanged.
   - In the loop, read the message from the map; if the sha is absent, log the same WARN and return `nil, false, nil`.
   - Keep the `ClaimRef` construction, `parseClaimMetadata`, the unparseable-metadata WARN, and the append behavior unchanged.

6. Regenerate the Counterfeiter mock. Do not hand-edit `mocks/claim_origin.go`; `make precommit` runs `go generate ./...`, which regenerates it from the interface directive. Confirm the regenerated `FakeClaimOrigin` exposes `ReadCommitMessagesStub`, `ReadCommitMessagesCallCount`, `ReadCommitMessagesArgsForCall`, and `ReadCommitMessagesReturns`.

7. Update every test fake so the new batched method is stubbed consistently with its per-item sibling:
   - `pkg/git/claim_refs_list_test.go` `newClaimListFake`: add `ReadCommitMessagesStub` returning `claimMessages[sha]` for each requested sha (nil, nil for an unknown sha).
   - `pkg/git/claim_refs_list_test.go` `goldenClaimFixture` (added by prompt 1): add `ReadCommitMessagesStub` returning a map built from the same messages, including the raw `not json` message for `claimShaB`. Do not change `pkg/git/testdata/claim_refs_golden.json`.
   - `pkg/git/claim_mirror_test.go` `newMirrorReadFake`: add `ReadCommitMessagesStub` returning a map for all requested shas, or an error if any requested sha is unknown (preserves the missing-object model).
   - `pkg/git/claim_mirror_test.go` `newLiveFake` (inside `TestClaimMirrorStaleFallsBack`): add the batched stub for `claimShaA`.
   - `pkg/git/claim_mirror_test.go` `TestClaimMirrorMissingObjectFallsBack`: the batched stub must fail for the mirror ref's sha and succeed for `claimShaB`.
   - `pkg/git/claim_mirror_test.go` `TestClaimMirrorReadAfterWrite`: add a batched stub returning the valid message for any sha.
   - Search for any other `ReadCommitMessageStub` and update it. Keep the per-item stubs; the live fallback still uses them.

8. Add these tests, all plain `testing.T` so `go test -run` can select them by name:
   - `TestClaimRefBatchFallback` in `pkg/git/claim_refs_list_test.go`: use at least three refs from `newClaimListFake`; override `ReadCommitMessagesStub` to return an error (never fail `FetchCommitsStub`, or the test would pass against the unmodified code); assert `fake.ReadCommitMessagesCallCount() == 1`, `fake.ReadCommitMessageCallCount() == len(refs)`, and every ref is present in the result.
   - `TestClaimRefBatchCancellation` in `pkg/git/claim_refs_list_test.go`, two subtests:
     - "before the listing starts": cancel the context before `ListClaimRefs`; assert the result is `nil` (not merely empty) and `errors.Is(err, context.Canceled)`.
     - "mid-listing": a `ReadCommitMessagesStub` that calls `cancel()` and returns success; assert the result is `nil` and `errors.Is(err, context.Canceled)`. This proves the per-iteration check survived the restructure.
   - `TestClaimRefBatchProcessCount` in `pkg/git/claim_mirror_test.go`: measure the live and mirror paths at 300 and 600 refs with a real `git` shim on `PATH`.
     - For each path and size, build a fresh `newClaimFixture(t)`, call `configureClaimIdentity(t, fixture.bare)`, and seed `size` refs named `refs/claims/success/task<N>/phase<N>/<N>` in the bare origin. Create a small set (for example four) of distinct claim commits with `git commit-tree <claimEmptyTreeSHA> -m '<json claimCommitMessage>'` and create all refs in one `git update-ref --stdin` process (add a small `mustGitInput` helper; do not spawn one `update-ref` per ref).
     - Live path: `newClaimClient(fixture.cloneA)`. Mirror path: `newClaimClient(fixture.cloneA)`, then `ConfigureClaimMirror` and `Pull` (which sets freshness). Do not start a background puller; nothing can pull during the measurement.
     - Resolve the real git absolute path with `exec.LookPath("git")` **before** changing `PATH`. Write an executable `git` shim (mode 0o755) into a temp dir that appends `"$*"` to a counter file and `exec`s the real absolute path. The shim must not call `command -v git` (recursion). Prepend the shim dir to `PATH` for the measurement only; save the old `PATH` and restore it after the measurement (do not use `t.Parallel`).
     - Truncate the counter file immediately before the GET (after boot / after `Pull`), not earlier.
     - Issue a real `GET /api/v1/refs?prefix=claims%2Fsuccess%2F` through `libhttp.NewJSONErrorHandler(handler.NewRefsGetHandler(client))` with `httptest`, assert status 200, and decode the body into `[]git.ClaimRef`.
     - Read the counter: total git processes = non-empty lines; `cat-file` processes = lines beginning with `cat-file`. Assert exactly one `cat-file` line per listing; assert the per-path total is 4 for live (`remote`, `ls-remote`, `fetch`, one `cat-file --batch`) and 2 for mirror (`for-each-ref`, one `cat-file --batch`); assert the total is the same at 300 and 600 on each path; assert 300 / 600 refs returned. Also assert the log carries `served_from=origin` on the live path and `served_from=mirror` on the mirror path (use `captureSlogLogs`).
   - `TestClaimRefBatchParse` in `pkg/git/claim_refs_internal_test.go` (package `git`, plain `testing.T`): table-test `parseBatchCommitMessages` with (a) two valid commit objects, (b) a `<sha> missing` entry, (c) a truncated object, and (d) a commit with no `\n\n` separator. Assert the map contents for the valid cases and an error for the invalid ones.

9. Keep the zero-refs early return in both listing functions (`if len(matched) == 0 { ... }`) so a zero-match prefix never invokes `ReadCommitMessages`. Extend `testClaimListNoMatch` to also assert `fake.ReadCommitMessagesCallCount() == 0`.

10. Update the doc comments on `ClaimOrigin`, `listClaimRefsLive`, `fetchClaimCommits`, `readClaimRef`, `readClaimCommits`, `ReadCommitMessages`, and `listClaimRefsFromMirror` to describe the batched read and the per-path fallback.

11. Do not change the HTTP surface, the `ClaimRef` JSON fields, the ordering, the write path (`PutClaimRef`, `DeleteClaimRef`), or the flush/coalescing machinery.

12. `make precommit` must pass.

<!-- REVIEWER NOTE: `TestClaimRefBatchProcessCount` lives in `pkg/git` and calls `ListClaimRefs` through the real handler rather than booting `main`, because the spec requires the process-count test in the plain-`testing.T` `pkg/git` files (a Ginkgo `TestSuite` entry point cannot be selected by `go test -run`). The GET is the same listing path; the shim counter and per-path totals are identical. No background puller is started, so the "parked puller" requirement is satisfied by construction. -->
</requirements>

<constraints>
- Never code outside the dark-factory pipeline; do not commit — dark-factory handles git.
- Counterfeiter mocks are generated, never hand-written: adding a method to `ClaimOrigin` means `go generate ./...` runs as part of `make precommit`.
- Test style is per-file. `pkg/git/claim_mirror_test.go` and `pkg/git/claim_refs_list_test.go` are deliberately plain `testing.T` (see `claim_mirror_test.go` lines 29–34). New tests added to them stay plain `testing.T` and are selected with `go test -run`. Do not convert either file to Ginkgo; a Ginkgo entry point is `TestSuite`, which `go test -run` cannot select, so a filter matching nothing would exit 0 and the tests would pass vacuously.
- The context-cancellation check at the top of each loop iteration must survive the restructure (coding rule `go-context/cancel-check-in-loop`).
- The existing per-item fetch fallback semantics in `fetchClaimCommits` are the model to copy for the live message fallback; do not invent a second fallback style.
- Do not change the HTTP surface: same route, same JSON shape, same status codes.
- ≥ 80% coverage on new code.
- Errors are wrapped with `github.com/bborbe/errors`, never `fmt.Errorf`, in non-test code (the internal parser may use `stderrors.New`/`fmt.Errorf`; its ctx-taking caller wraps it).
- Non-test `pkg/git` code must not call `context.Background()`; contexts are passed in.
- Security: batch input is drawn from git's own ref output, not from the request body. Pass shas to `git cat-file --batch` via stdin (or as argv), never through a shell. Do not widen `validateRefPrefix`.
- Logging uses `log/slog`.
- Precommit budgets: funlen 80/50, gocognit 20, nestif 4, maintidx 20, dupl, golines 100. If `readClaimRef` or the parser grows past them, extract a named helper.
- Existing tests must still pass.
</constraints>

<verification>
Run from the repository root inside the container:

```
go test ./pkg/git -run 'TestClaimMirror|TestListClaimRefs|TestClaimRefBatch' -count=1
go test ./pkg/git -run '^TestClaimRefBatchProcessCount$' -count=1 -v
make precommit
```

All must exit 0. `make precommit` is the authoritative gate; if it fails, fix the failure and re-run only the failing target, then run `make precommit` once more.

Then confirm the observable outcomes. Each line must print `ok`; the grep must print exactly three lines:

```
grep -rn 'ReadCommitMessage(' --include='*.go' . | grep -v '_test.go' | grep -v '/mocks/'
```

Expect exactly:
- the `ClaimOrigin` interface declaration in `pkg/git/claim_refs.go`,
- the `func (o *gitOrigin) ReadCommitMessage` definition in `pkg/git/claim_refs.go`,
- exactly one `g.origin.ReadCommitMessage(` call site, inside the `!batchOK` branch of `readClaimRef`.

The mirror path must contain no `origin.ReadCommitMessage(` call.

```
grep -q 'ReadCommitMessages(ctx context.Context, shas \[\]string) (map\[string\]\[\]byte, error)' pkg/git/claim_refs.go && echo ok
grep -q 'func (fake \*FakeClaimOrigin) ReadCommitMessages(' mocks/claim_origin.go && echo ok
go test ./pkg/git -run '^TestClaimRefBatchFallback$|^TestClaimRefBatchCancellation$|^TestClaimRefBatchParse$' -count=1
```
</verification>
