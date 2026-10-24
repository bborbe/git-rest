---
status: failed
execution_id: git-rest-exec-082-widen-push-retry-backoff
dark-factory-version: v0.198.7
created: "2026-10-23T18:05:39Z"
queued: "2026-10-23T18:05:39Z"
started: "2026-10-23T19:10:50Z"
completed: "2026-10-23T19:23:15Z"
lastFailReason: 'validate completion report: completion report status: failed'
---

# Widen the push-retry backoff exponentially

<summary>
- A writer that loses a push race makes up to three push attempts today (two retries), waiting a flat 100–500 ms before each
- That is not enough: over 24 h the openclaw writer recorded 368 push retries on dev and 500 on prod, with 21 and 115 failed flushes
- The wait between attempts grows exponentially instead of staying flat, so a writer that loses inside a burst waits long enough for the branch to go quiet before re-colliding
- The attempt bound rises from three to five
- A first-attempt push is unchanged: no extra delay, no extra command, no extra log line, no counter increment
- The git lock is still held across the wait — the total added sleep is at most about 3 s, inside the flush's 20 s budget
- Existing merge, rescue and conflict behaviour is untouched
</summary>

<objective>
Make push contention converge under burst instead of repeating, by widening the inter-attempt wait exponentially with jitter so a losing writer waits out the burst rather than re-colliding inside it.
</objective>

<context>
Read CLAUDE.md for project conventions, and `docs/dod.md` for the Definition of Done (it requires a `## Unreleased` CHANGELOG entry).

Read `pkg/git/git.go` — the functions `pushWithRetry`, `retryPush`, `sleepPushJitter`, `logPushSucceededAfterRetry`, the constant `pushMaxAttempts`, and `isRetryablePushRejection`.

Read `pkg/git/export_test.go` — the existing `RetryPushForTest`, which establishes the export pattern this change reuses.

Read `specs/in-progress/021-flush-timeout-isolation.md` § "Where the time goes" — it measured this retry path and concluded the time is the git operation itself, not the jitter.

Applicable guides: `go-error-wrapping-guide.md`, `changelog-guide.md`, `go-context-cancellation-in-loops.md`. Do NOT apply `go-testing-guide.md`'s `no-testing-t-direct` rule here: `pkg/git` already carries six plain-`testing` files (`coalesce_test.go`, `coalesce_durability_test.go`, `flush_deadline_test.go`, `flush_rescue_test.go`, `claim_refs_test.go`, `claim_refs_list_test.go`) despite the Ginkgo suite, and requirement 7 follows that precedent.

Background, so the design is not re-derived:
- `pushWithRetry` is called from seven locked call sites, all holding `g.mu`: the four write/delete entry points (`writeFileLocked`, `flushPendingLocked`, `RecoverPendingWrites`, `DeleteFile`) and three on the `Pull` path (`syncWithUpstream`, `pullMergeAndPush`, `commitAndPushMerge`). The wait therefore runs under the lock — that is accepted and unchanged by this prompt; do not restructure the locking.
- The measurement this change targets: `increase(git_rest_push_retries_total{app="vault-obsidian-openclaw"}[24h])` read 2026-10-23 through each cluster's Prometheus apiserver proxy as 368 on nuke dev and 500 on nuke prod, against 21 and 115 `git_rest_write_flushes_total{outcome="error"}` in the same window. The metric is the collision count, not latency.
- The flush deadline — not the attempt count — still bounds the whole flush, so a wider attempt count trades more retries against more frequent deadline cut-offs, which spec 021 already handles (paths stay staged, the rescue still runs).
- This prompt supersedes exactly one of that spec's Constraints — "`pushWithRetry`'s three attempts stay". Its other constraint, "The write lock is still held across the flush", remains true and must not be changed. Do NOT edit `specs/in-progress/021-flush-timeout-isolation.md` in this prompt — it is `status: verifying` and its evidence is still being collected; the spec owner refreshes it when the spec closes. Three passages go stale then, not one: the superseded Constraints bullet, the Summary sentence "`pushWithRetry` makes three attempts", and the "Where the time goes" reference to `sleepPushJitter`. Its AC1/AC2/AC4 assertions do not depend on the attempt count and still hold.
</context>

<requirements>
1. Raise `pushMaxAttempts` in `pkg/git/git.go` from `3` to `5`, and rewrite its comment to state the real reason it is bounded: the wait runs under `g.mu`, so the bound keeps the total added sleep small against the flush deadline (at most about 3 s) rather than holding the lock indefinitely. Do not describe it as a tunable.

2. Replace `sleepPushJitter` with an exponential, jittered delay. Add a function `pushBackoff(attempt int) time.Duration` returning the wait before retry attempt `attempt` (attempt numbering unchanged — the first retry is attempt 2):
   - base delay 100 ms at attempt 2, doubled for each further attempt (100, 200, 400, 800 ms at attempts 2–5)
   - add a random jitter of 0–100 % of that base, so two writers waiting the same number of attempts do not wake together
   - no cap: with `pushMaxAttempts = 5` the largest base is 800 ms, so the total wait is at most 3 s with jitter
   - clamp `attempt < 2` to the attempt-2 base — a negative shift amount panics at runtime, so the function is total
   - deterministic apart from the jitter: no clock, no context, no logging
   - carry a doc comment stating the base sequence, the jitter ceiling, and that the clamp makes the function total
   Keep the `//nolint:gosec` comment on the `math/rand` call with its "jitter, not security-sensitive" justification.

3. Rename the waiting helper to `sleepPushBackoff(ctx context.Context, attempt int) error`, keeping the existing shape: `time.NewTimer` with `defer timer.Stop()`, and a `select` on `ctx.Done()` returning the context error on cancellation. It computes the delay with `pushBackoff(attempt)`.

4. Call it from `retryPush` in place of `sleepPushJitter`, passing the attempt number. Keep the rest of `retryPush` unchanged — the upstream resolution, `fetch`, `mergeWithDirtyTreeRescue` and the `IncPushRetry` call all continue to run in the same order under the same lock. Do not change the locking, and do not change `RetryPushForTest`.

5. Add two hooks to `pkg/git/export_test.go`, beside `RetryPushForTest` and in the same style, since the new test file is `package git_test` and cannot reach the unexported functions otherwise:
   - `PushBackoffForTest(attempt int) time.Duration` returning `pushBackoff(attempt)`
   - `SleepPushBackoffForTest(ctx context.Context, attempt int) error` returning `sleepPushBackoff(ctx, attempt)`

6. Update the existing test `It("bounds retries at three push attempts")` in `pkg/git/git_test.go` (~line 3279) to the new bound: rename it to `bounds retries at five push attempts`, expect `pushAttempts(countFile)` to equal 5 and `fakeMetrics.IncPushRetryCallCount()` to equal 4. Leave every other existing test unchanged.

7. Add `pkg/git/push_backoff_test.go` using plain `testing` in `package git_test` (this is the established local pattern — `coalesce_test.go` and `flush_deadline_test.go` are already plain `testing` in that package, so it is not a `docs/dod.md` deviation), following the style of `coalesce_test.go`:
   - `TestPushBackoff` — call `git.PushBackoffForTest(attempt)`. Table test asserting the base sequence for attempts 2–5 (100, 200, 400, 800 ms), that the result never falls below the base, that it never exceeds base + base (the jitter ceiling), and that `attempt` values below 2 clamp to the attempt-2 base. Repeat each case enough times to exercise the jitter range rather than a single sample.
   - `TestSleepPushBackoff` — assert the wiring, not just the formula: `git.SleepPushBackoffForTest(ctx, 5)` returns after at least 800 ms and under 2 s, and with an already-cancelled context it returns the context error immediately. `TestPushBackoff` alone cannot catch `sleepPushBackoff` passing the wrong attempt to `pushBackoff`, because the retry-count test only observes how many attempts ran, never how long each waited.

8. Add one bullet under `## Unreleased` in `CHANGELOG.md`. The file has no `## Unreleased` section today, so create it as the first `##` section — immediately after the SemVer preamble and before `## v0.34.2`, never above the `# Changelog` title. Follow the file's existing bullet style: `- fix: Widen the push-retry backoff exponentially (100/200/400/800 ms with jitter, up to five attempts), so a contended writer waits out a burst instead of re-colliding inside it`.

9. Before finishing, re-run the verification commands below and confirm they pass, then walk each numbered requirement against the change.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Errors wrapped with `github.com/bborbe/errors` — never `fmt.Errorf`
- `context.Background()` must not appear in `pkg/`
- Timestamps come from the injected `libtime` getter, never `time.Now()` inside `pkg/`
- Logging uses `log/slog` (`slog.InfoContext` and friends) — no `fmt.Print*`
- `pkg/git/yaml_merge_resolver.go` stays on `glog`; do not mix the two loggers in one file
- Prometheus counters stay pre-initialised in `init()`
- Do not change the locking, the pending-write map, `preFlushSHA` handling, or the dirty-tree rescue — this change touches only the delay and the attempt bound
- Existing tests must still pass, apart from the one explicitly updated in requirement 6
</constraints>

<verification>
Run each of these — all must pass:

```
go test -mod=mod ./pkg/git -run 'TestPushBackoff' -count=1 -v
make precommit
```
</verification>
