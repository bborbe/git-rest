---
status: completed
summary: Instrumented claim-ref push, per-attempt branch push, and ls-remote into git_rest_git_operation_duration_seconds, widened its buckets with 30s/60s, added tests for success/failure/retry/cancel paths and a bucket test, and recorded an Unreleased changelog entry.
execution_id: git-rest-push-latency-exec-091-push-and-ls-remote-latency
dark-factory-version: v0.198.7
created: "2026-10-31T11:07:00Z"
queued: "2026-10-31T11:16:45Z"
started: "2026-10-31T13:53:20Z"
completed: "2026-10-31T14:00:33Z"
---

# Instrument push and ls-remote latency on the claim path

<summary>
- A push's duration becomes visible in the metrics, so a slow claim write is distinguishable from a fast one
- The claim-ref push is instrumented — the one that dies when the executor's 30 s caller cap expires
- Every retried branch push is instrumented once per attempt, so a retried push reports one sample per attempt, not one per call
- The ref listing against the origin is instrumented too
- A push that fails, or that its context kills mid-flight, still records its duration
- A push slower than 30 s shows up in its own time range instead of being lumped into the catch-all bucket
- Every existing bucket boundary and every existing label value stays exactly as it is
- New tests prove the observation fires on success, on failure, and once per retry attempt
</summary>

<objective>
Make a slow git push measurable. Today the git-operation duration histogram records neither a push nor a ref listing, so a 31 s claim push and a 3 s one are indistinguishable in the metrics — which is why a fix to the claim read-back recovery cannot be shown to work by measurement. Add the two observations and widen the histogram's buckets so a sample above 30 s is readable, without changing any existing label value or bucket boundary.
</objective>

<context>
Read `docs/dod.md` for the Definition of Done (it requires a `## Unreleased` CHANGELOG entry) and `README.md` for project conventions.

Read `pkg/metrics/metrics.go`. `GitOperationDuration` is a `HistogramVec` labelled `operation` with `Buckets: prometheus.DefBuckets`. `DefBuckets` stops at 10 seconds, so today a 30-second sample increments only the `+Inf` bucket — the value survives in `_sum`/`_count` but is invisible in every finite bucket. The `Metrics` interface method `ObserveGitOperation(operation string, duration float64)` already exists and does not change.

Read the eight existing call sites in `pkg/git/git.go` to copy their shape exactly — `WriteFile` and `Pull` are the clearest exemplars:

```go
start := g.currentDateTimeGetter.Now()
defer func() { g.metrics.ObserveGitOperation("write_file", time.Since(time.Time(start)).Seconds()) }()
```

The start value comes from the injected `libtime` getter, never `time.Now()`.

**The two push paths are disjoint — this is the whole point of the change.** Read all three of these before writing any code:

- `pkg/git/claim_refs.go` → `PutClaimRef` → `pushClaimRef`, and `DeleteClaimRef` → `pushClaimRef`. `pushClaimRef` runs `g.runCmdRaw(ctx, g.repoPath, "push", lease, "origin", sha+":"+ref)` and is documented *"It pushes exactly once and never retries."* **It does not call `pushWithRetry`.** This is the claim path, and it is the push that the executor's 30 s `http.Client` timeout kills.
- `pkg/git/git.go` → `pushWithRetry`. It has seven locked callers: `writeFileLocked`, `flushPendingLocked`, `RecoverPendingWrites`, `DeleteFile`, `pullMergeAndPush`, `commitAndPushMerge` and `syncWithUpstream`. It retries in-attempt, so it is the only site where "once per attempt" means anything. Instrumenting it covers all seven at once — do not instrument the call sites.
- Out of scope, stated so it is a decision and not an oversight: `rescueDirtyTree` pushes the `rescue/<ts>` branch with a bare `runCmd` and does not observe; `pullRebaseAndPush` pushes raw but is `//nolint:unused` with no production caller. Neither is touched by this prompt.

Instrumenting only one of them leaves the other's latency invisible. Both are required.

Read `pkg/git/claim_refs.go` → `gitOrigin.ListRefs` for the `ls-remote` call: `g.runCmdOutput(ctx, g.repoPath, "ls-remote", "--refs", "origin", pattern)`. It is the sole production `ClaimOrigin` implementation, so it is the claim flow's ref listing. Its `hasRemote` guard returns `ErrNoRemote` before any `ls-remote` runs — that early return must not observe, because no command ran.

Read the tests before writing them:

- `pkg/git/git_test.go` — the Ginkgo v2 `Describe("Push retry", …)` block already holds a `mocks.FakeMetrics` in `fakeMetrics`, and its helpers `setupPullFixture`, `installRemoteAdvanceOnFetch`, `installPreReceiveRejection`, `installPushCountingHook` and `pushAttempts` are what the push specs build on. `mocks.FakeMetrics` records every call, so `ObserveGitOperationCallCount()` and `ObserveGitOperationArgsForCall(i)` are the assertions.
- `pkg/git/claim_refs_test.go` — plain Go tests over a real bare-origin fixture (`newClaimFixture`). `newClaimClient` hardcodes `&noopMetrics{}`; build the client inline with `git.New(...)` and a `&mocks.FakeMetrics{}` where a fake is needed.
- `pkg/metrics/metrics_test.go` — the existing metric tests show the suite conventions.

The coding guide `go-prometheus-metrics-guide` covers histogram buckets and label cardinality.
</context>

<requirements>
1. In `pkg/metrics/metrics.go`, add a package-level bucket list and use it for `GitOperationDuration`:

   `var gitOperationBuckets = append(append([]float64{}, prometheus.DefBuckets...), 30, 60)`

   Give it a doc comment saying why: `DefBuckets` stops at 10 s, so a push that outlives the executor's 30 s caller cap lands in `+Inf` only; the two extra bounds make such a sample readable, and copying `DefBuckets` leaves it unmodified. Set `Buckets: gitOperationBuckets` on `GitOperationDuration`. Do not change its `Name`, its `Help`, or the `operation` label name, and do not remove, reorder or re-value any existing bucket.

2. In `pkg/git/claim_refs.go`, instrument `pushClaimRef`. Take `start := g.currentDateTimeGetter.Now()` immediately before the `g.runCmdRaw(ctx, g.repoPath, "push", lease, "origin", sha+":"+ref)` call and call `g.metrics.ObserveGitOperation("push", time.Since(time.Time(start)).Seconds())` immediately after it returns, before branching on `err`. A lease conflict and a terminal failure must both observe. Add `"time"` to the file's imports.

3. In `pkg/git/git.go`, instrument `pushWithRetry` **per attempt**. Inside the `for attempt := 1; attempt <= pushMaxAttempts; attempt++` loop, leave the existing `select { case <-ctx.Done(): … }` guard where it is, then take the start value before `g.runCmdRaw(ctx, g.repoPath, args...)` and observe immediately after it returns, before any branch on `err`. Every attempt must observe: a success, a retryable rejection, a terminal failure, and a push killed by its context. Do **not** express this as a single `defer` on the function — that records one sample per call, not one per attempt.

4. In `pkg/git/claim_refs.go`, instrument `gitOrigin.ListRefs`. After the `hasRemote` guard, take the start value before the `runCmdOutput(..., "ls-remote", "--refs", "origin", pattern)` call and observe `"ls-remote"` immediately after it returns, before branching on `err`. The `ErrNoRemote` early return must stay unobserved.

   The label values are exactly `push` and `ls-remote` — hyphen, not underscore, matching the git subcommand.

5. Tests for the push paths, in `pkg/git/git_test.go` inside the existing `Describe("Push retry", …)` block, asserting on `fakeMetrics`:

   - A plain successful push through `Pull` records exactly one `push` sample (plus the one `pull` sample `Pull` itself records).
   - A terminal rejection (the existing `installPreReceiveRejection` shape) still records exactly one `push` sample while `Pull` returns the error.
   - A forced two-attempt retry (`installRemoteAdvanceOnFetch(workDir, true, "")` — the existing fetch-first shape that advances the remote once) records exactly two `push` samples — one per attempt.
   - A push still in flight when the context is cancelled still records a sample: install a `pre-receive` hook in the bare remote that sleeps longer than `gitWaitDelay` (use 5 s; the call returns after the 2 s `WaitDelay`, so the test costs about 2 s) and run the operation under a context with a short deadline (about 100 ms), so `runCmdRaw` returns on the cancelled context and the observation still runs. Guard the hook so its own work cannot re-enter the operation.

   `Pull` also records one `"pull"` sample (`pkg/git/git.go`, the `defer` in `Pull`), so never assert on `ObserveGitOperationCallCount()` directly. Add a small helper in the block that walks `ObserveGitOperationCallCount()`, keeps the samples whose `ObserveGitOperationArgsForCall(i)` operation is `"push"`, and assert on that filtered count — and on every kept sample's operation being `"push"`.

6. Tests for the claim push, in `pkg/git/claim_refs_test.go`, plain Go tests over `newClaimFixture`:

   - A successful `PutClaimRef` against the fixture origin observes `push` exactly once. This is the claim path, so it must be `push` and not `ls-remote`.
   - A failing claim push still observes `push`: point the clone's `origin` at a path that does not exist (`git remote set-url origin <tempdir>/absent.git`), call `PutClaimRef`, expect an error, and assert the observation fired.

7. Tests for `ls-remote`, in `pkg/git/claim_refs_test.go`, plain Go tests:

   - A successful `ListClaimRefs` against the fixture origin observes `ls-remote`.
   - A failing `ls-remote` still observes it: repoint `origin` at a path that does not exist, call `ListClaimRefs`, expect an error, assert the observation fired.
   - A repository with no remote at all observes nothing for `ls-remote` — run `git remote remove origin` on the clone, then call `ListClaimRefs`, expect `ErrNoRemote`, and assert no `ls-remote` sample was recorded (the guard returns before the command runs).

8. Bucket test in `pkg/metrics/metrics_test.go`, asserting against the **registered** `metrics.GitOperationDuration` rather than a locally constructed `HistogramOpts`:

   - Observe `31.0` under a label value used by no other test in the package.
   - Read the metric with `GitOperationDuration.GetMetricWithLabelValues(...)`, type-assert the observer to `prometheus.Histogram` using the **comma-ok** form (`hist, ok := obs.(prometheus.Histogram)`; `.golangci.yml` enables `forcetypeassert`, so a single-value assertion fails lint), write it into a `dto.Metric` (`github.com/prometheus/client_model/go`), and assert the cumulative count at upper bound `30` is 0 while the cumulative count at upper bound `60` is 1 — i.e. the sample landed above 30 s and inside a finite bucket. Look buckets up by `GetUpperBound()`, never by slice index.
   - Assert the bucket boundaries include `30` and `60` and still include `10` with its original value, so the change is provably additive.
   - `client_model` is already in the module graph as an indirect requirement of `client_golang`. Importing it directly is allowed and adds no new module: `make precommit`'s `ensure` target runs `go mod tidy`, which will promote it from the `// indirect` block to a direct require — that go.mod edit is expected, keep it. Do not add any other module.

9. Add a `CHANGELOG.md` entry under `## Unreleased`, in the existing one-line style with its `feat:`/`fix:` prefix, stating that `git_rest_git_operation_duration_seconds` now carries `push` and `ls-remote`, that the claim-ref push and each branch-push attempt are observed, and that the histogram gained 30 s and 60 s buckets so a sample above 30 s is readable.

   The file has no `## Unreleased` section today — the top section is `## v0.37.0`. Create `## Unreleased` as the first `##` section, immediately after the SemVer preamble and above `## v0.37.0`, never above the `# Changelog` title. Never append the bullet under an existing `## vX.Y.Z` heading: a released section must not gain an unreleased entry.

   Before you finish, re-run `<verification>` and confirm it passes, then walk each numbered requirement above against the change and confirm it is present.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- The label values are exactly `push` and `ls-remote`
- Do not change `GitOperationDuration`'s name, help text, or label name; do not remove, reorder or re-value any existing bucket
- Errors are wrapped with `github.com/bborbe/errors` — never `fmt.Errorf`, never a bare `return err`
- Timestamps come from the injected `libtime` getter, never `time.Now()`
- `context.Background()` must not appear in `pkg/` — take `ctx` from the caller
- Do not add a new Go module dependency — promoting the already-present `client_model` from indirect to direct (requirement 8) is not a new module and is expected
- Do not touch the executor, the 30 s caller cap, or any other git-rest behaviour — no request deadline, no batching, no fetch-refspec change
- No `go mod vendor`, no `-mod=vendor` in any command
</constraints>

<verification>
Run from the repo root. The daemon runs with `hideGit=true`, which masks `.git` inside the container — this block contains no git commands. `ROOTDIR=/workspace` is the dark-factory convention for that case and is inert here (the Makefile does not reference it).

Run `ROOTDIR=/workspace make precommit` — must pass.

Then confirm the observations exist and are spelled correctly:

- `grep -n 'ObserveGitOperation("push"' pkg/git/git.go pkg/git/claim_refs.go` must print at least two lines, one in each file.
- `grep -n 'ObserveGitOperation("ls-remote"' pkg/git/claim_refs.go` must print exactly one line.
- `grep -n '30, 60' pkg/metrics/metrics.go` must print the bucket list.
- `awk '/^## /{sec=$0} /git_rest_git_operation_duration_seconds/{print sec}' CHANGELOG.md` must print exactly one line, `## Unreleased` — a bare `ls-remote` pattern also matches the existing `## v0.36.1` entry, so it cannot pin the section.
</verification>
