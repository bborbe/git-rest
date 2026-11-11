---
status: completed
tags:
    - dark-factory
    - spec
    - bug
approved: "2026-11-09T10:56:10Z"
generating: "2026-11-09T10:56:11Z"
prompted: "2026-11-11T15:47:10Z"
verifying: "2026-11-11T16:29:24Z"
completed: "2026-11-11T16:29:31Z"
branch: dark-factory/batch-claim-ref-commit-reads
---

## Summary

- The git-rest server answers a claim-ref listing by starting one `git` process per matching ref, so a listing's cost grows with the number of refs under the prefix.
- On nukeprod the openclaw instance sits at 1345m of its 2000m CPU limit while its three siblings sit at 121m / 3m / 62m, and the largest prefix misses its caller's deadline on every attempt — 100 deadline errors per 30 minutes.
- The consequence is not slowness but wrong data: the caller's success-record snapshot resolves to zero records every time, so a record that already recorded success reads as absent and is re-driven.
- The repository already batches the equivalent write-path step and falls back per item only on failure. The read path was never given the same treatment.
- Fix shape: read the commit messages in one invocation, with the per-item fallback preserved, at every place the listing reads a message.

## Problem

A claim-ref listing is a lookup that has been implemented as a process fan-out. The server walks the matching refs and reads each ref's commit message through its own `git cat-file -p` invocation. For a prefix holding thousands of refs that is thousands of short-lived processes per request, and because callers re-list on a fixed cadence the cost is sustained rather than bursty.

The observable consequence is that the largest prefix never answers inside the caller's deadline, so the data behind it is permanently unavailable even though the underlying git objects are healthy. The caller treats that as "no records" rather than "could not read", which turns a latency defect into a correctness one.

This is the same defect class the repository already fixed on the write path: `FetchCommits` (`pkg/git/claim_refs.go:126–134`) batches every sha into one `git fetch` and `fetchClaimCommits` (`:215–228`) falls back per item only when the batch fails. The read path was left per-item.

## Goal

A claim-ref listing starts a **small, constant** number of git processes regardless of how many refs match the prefix — a listing over ten refs and a listing over ten thousand refs start the same number. Behaviour is otherwise unchanged: the same `[]ClaimRef` values, the same ordering, the same per-item fallback when a batch read fails, and the same context cancellation semantics.

## Non-goals

- Changing the claim-ref storage model, key shapes, or lease semantics.
- Adding caching, pagination, or a new endpoint.
- Changing the caller's deadline or reconcile interval. The server must fit the existing deadline, not the deadline fit the server.
- Touching the write path (`PutClaimRef`, `DeleteClaimRef`) or the flush/coalescing machinery.
- Cleaning up refs under `claims/success/`. Retaining them is correct; listing them must simply be cheap.

## Assumptions

- The mirror path is the hot path in production: the openclaw instance logs `served_from=mirror` for 265 of 277 listings in a 30-minute window. The live-origin path is colder but has the same per-ref shape and must be fixed too, or the defect returns the moment the mirror goes stale.
- The image's git supports `cat-file --batch`. The batch needs stdin, which the current output-only runner (`git.go:330`) does not supply, so the implementer must add a stdin-capable runner; if the batched invocation cannot be made to work, the listing must degrade to the per-item path rather than fail.
- The caller's deadline is fixed and not negotiable from this repo.

## Acceptance Criteria

- [ ] **A listing's git process count does not grow with the number of refs, on either path, and the commit-message read is a single invocation.** For a prefix matching 300 refs, a listing starts **exactly one** `cat-file` process; the same assertion at 600 refs records the **same total on that path**. This runs **twice** — once with a fresh claim mirror configured (mirror path) and once with `claimMirrorFreshAt` unset (live-origin path) — because the mirror is the production hot path but the live path has the same shape. ⚠️ **The totals are compared per path, never across paths**: the live path starts 4 git processes (`remote`, `ls-remote`, `fetch`, one `cat-file --batch`) and the mirror path 2 (`for-each-ref`, one `cat-file --batch`), and no change here makes those equal. Live sits at 4 with **no headroom**, so a fifth git call on that path fails this criterion. Evidence: an integration test runs against a temporary repository with a `git` shim on `PATH` that appends its argv to a counter file; it asserts exactly one `cat-file` line, the expected per-path total (4 live / 2 mirror), the same total at both sizes on each path, and that the listing returned 300 / 600 `ClaimRef` values. A test that counts interface calls instead of processes does not satisfy this — an implementation that loops `ReadCommitMessage` inside the new method passes an interface-count test while starting one process per ref. The counter file is truncated **after boot and after the mirror reaches fresh, immediately before the `GET`**, with the background puller **parked** (long `-pull-interval`, or stopped after the first successful sync) so a pull landing inside one measurement window cannot flake the two-size comparison.
- [ ] **No caller invokes the per-ref commit-message read on the hot path, and the only surviving call is the live-path fallback.** Evidence: `grep -rn 'ReadCommitMessage(' --include='*.go' . | grep -v '_test.go' | grep -v '/mocks/'` returns **exactly 3 lines** — the interface declaration, the `gitOrigin` method, and one `origin.ReadCommitMessage(` call site — and that single call site sits inside the `!batchOK` fallback branch of the live-path loop. The mirror path's per-ref loop contains **no** `origin.ReadCommitMessage(` call. Three lines, not two, because the fallback criterion below requires a per-item fallback to exist; a spec demanding zero call sites and a working fallback would be unsatisfiable. ⚠️ **`batchOK` here means the *message-read* batch result.** The existing fetch-batch flag at `claim_refs.go:193` is renamed `fetchOK` in the same change, so "failed message batch" and "failed fetch batch" cannot be confused — under the old shared name the fallback branch would read as unreachable.
- [ ] **The listing returns identical values, in identical order, to the pre-fix implementation, against a provably pre-fix golden.** Evidence: `pkg/git/testdata/claim_refs_golden.json` is committed in a commit that **precedes** the implementation commit — `git log --oneline -- pkg/git/testdata/claim_refs_golden.json` lists a commit that is an ancestor of the implementation commit, and the prompt records the exact command used to generate it from a clean `origin/master` checkout; a test deep-equals the post-fix listing against it over a fixture with mixed ref shapes, and `go test -run 'TestClaimMirror|TestListClaimRefs' ./pkg/git` passes. A golden generated from the post-fix tree does not satisfy this: it makes the deep-equal vacuous.
- [ ] **On the live-origin path, a failed message-read batch falls back to per-item reads and still returns a complete listing.** Evidence: a test that fails **`fake.ReadCommitMessagesStub`** — the new batched method by name, never `FetchCommitsStub`, or the test would pass against the unmodified code — asserts `fake.ReadCommitMessageCallCount() == len(refs)` **and** that every ref is present in the result. (The mirror path's fallback is different and unchanged — see Desired Behavior 3.)
- [ ] **A cancelled context still aborts the listing with a wrapped error, and the per-iteration cancellation check survives the restructure.** Evidence: tests cancelling **before** the listing starts and **mid-listing** assert the returned result is `nil` (not merely empty) and the error wraps `context.Canceled`.
- [ ] **The rung-1 probe script exists, derives its numbers from the shim counter, and discriminates pre-fix from post-fix.** Evidence: `bash hack/claim-ref-listing-probe.sh` exits 0 and prints `cat-file invocations: 1` and `refs returned: 300`, **both read from the shim's counter file and the response body, not hard-coded**; run against the **pre-fix** binary (a `git stash`-free checkout of `origin/master`, built separately) the same script prints `cat-file invocations: 300`, which is the Reproduction's stated pre-fix figure — a script that prints `1` on both builds fails; a second invocation with a short pull interval prints `served_from=mirror` and still `cat-file invocations: 1`.
- [ ] **`CHANGELOG.md` gains a `## Unreleased` section carrying the entry.** `origin/master` carries **no** `## Unreleased` heading — the release bot renames it to `## vX.Y.Z` and does not re-add it — so the heading must be created, not merely appended to. Evidence: `awk '/^## Unreleased/{f=1;next} /^## v/{f=0} f && /claim ref/' CHANGELOG.md` returns ≥ 1 line. (`grep -n Unreleased` does not satisfy this: it is the heading the work must create.)
- [ ] **Post-Deploy (Rung-3):** on nukeprod, `refs?prefix=claims%2Fsuccess%2F` produces **zero** `context deadline exceeded` in a 30-minute window **that begins after the rollout completes**, and `vault-obsidian-openclaw-0` CPU stays at or below **400m** across that window — evidence: after `kubectlnukeprod -n prod rollout status statefulset/vault-obsidian-openclaw`, wait ≥ 30 minutes, then `kubectlnukeprod -n agent logs deploy/agent-task-executor-openclaw --since=30m | grep 'success_record_snapshot_failed' | grep -c 'context deadline exceeded'` returns 0, and `kubectlnukeprod -n prod top pod` sampled **six times across the window** reads ≤ 400m every time for `vault-obsidian-openclaw-0` (a single sample cannot establish "stays"; the pre-fix reading is 1345m).
  - `deploy_check:` `kubectlnukeprod -n prod get pod vault-obsidian-openclaw-0 -o jsonpath='{.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(git fetch --tags -q && git describe --tags --abbrev=0 origin/master)`
  - Precondition: `git tag --contains <fix-merge-commit>` is non-empty, and the nuke `image.tag` is pinned to that same tag. Without both, the gate can fail a correct deploy or pass a stale one.

## Verification

See `docs/verifying-specs.md` for the rung ladder. This change is confined to `pkg/git/` with no HTTP-shape change, for which that doc's selection table marks rung 3 as not normally required; it is taken anyway because the defect is a throughput failure that only appears at production ref counts, and rung 2 (dev) is skipped for the same reason — a dev vault has nowhere near 300 `claims/success/` refs, so it cannot reproduce the symptom. Rung 1 is mandatory and is the only rung that discriminates.

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — format, generate, test and lint clean
- `go test -run 'TestClaimMirror|TestListClaimRefs|TestClaimRefBatch' ./pkg/git -count=1` — the batching, fallback and cancellation tests
- `grep -rn 'ReadCommitMessage(' --include='*.go' . | grep -v '_test.go' | grep -v '/mocks/'` — returns exactly **3** lines (interface declaration, `gitOrigin` method, the one live-path fallback call)
- `awk '/^## Unreleased/{f=1;next} /^## v/{f=0} f && /claim ref/' CHANGELOG.md` — returns ≥ 1 line

### Operator-executable (runs on the host after PR merge, spec verification ladder)

**Rung 1 — local reproduction against a real repo:**

- `make build` — fresh binary
- `bash hack/claim-ref-listing-probe.sh` — seeds a temp bare origin with 300 `refs/claims/success/<task>/<phase>/<n>` refs whose commits carry the `claimCommitMessage` JSON payload, puts a counting `git` shim first on `PATH` (the shim must exec the real binary by absolute path, not `command -v git`, or it recurses), boots the binary against a clone of that origin on a spare port, and `GET`s `/api/v1/refs?prefix=claims%2Fsuccess%2F`. Prints the recorded `cat-file` count and the ref count. A fresh boot has no claim mirror, so this run exercises the **live-origin** path.
- Re-run the same probe with a short pull interval so the claim mirror syncs first, and assert the log carries `served_from=mirror` — the **mirror** path is the one in the production incident, and the first run does not reach it.

**Deployment (required before rung 3 — the release tag alone publishes no image):**

- `make buca` — builds and publishes the image. Must run from a checkout whose HEAD is exactly at the release tag: `check-version-tag` (`Makefile:212`) refuses otherwise.
- **Bump the pin as a committed change, not a local edit.** `VERSION ?=` in `bborbe/nuke`'s `git-rest/Makefile` is the operative pin (`upgrade:` passes `--set image.tag=$(VERSION)`, overriding the chart values). Land it as a PR on **`master`**, merge it, then sync the deployment worktree with `git fetch && git merge --ff-only origin/master` — the deployment worktrees are read-only and permit only that sync plus `make apply`. ⚠️ **`master`, not `prod`:** every historical `git-rest` pin bump in `bborbe/nuke` is a master commit, `origin/prod` is stale (thousands of commits behind), and the worktree tracks `master` content — a PR on `prod` would land where nothing reads it while the very next step merges `origin/master`. A local uncommitted edit is the same drift the pin exists to prevent, and a command-line `VERSION=` override is worse.
- `/nuke-deploy prod git-rest` from the synced worktree.

**Rung 3 — production:**

- `kubectlnukeprod -n prod rollout status statefulset/vault-obsidian-openclaw` — the roll completed
- `kubectlnukeprod -n prod get pod vault-obsidian-openclaw-0 -o jsonpath='{.spec.containers[0].image}'` — carries the release tag
- `kubectlnukeprod -n agent logs deploy/agent-task-executor-openclaw --since=30m | grep 'success_record_snapshot_failed' | grep -c 'context deadline exceeded'` — reads 0 after a ≥ 30-minute post-rollout window
- `kubectlnukeprod -n prod top pod` — `vault-obsidian-openclaw-0` reads ≤ 400m on six samples across the window

## Desired Behavior

1. A listing reads every matched commit message through **one** git invocation, not one invocation per ref.
2. The message read moves **out of** the single-ref helper into the caller's loop: all shas are read once before the loop and passed down as a map, mirroring the existing `fetchClaimCommits`/`fetchOK` shape at `claim_refs.go:193` and `:215`. Passing a one-element slice to a batched call from inside the single-ref helper does not satisfy this — it changes no cost.
3. **Fallback differs by path and stays as it is today.** On the live-origin path a failed **message-read** batch (a new `batchOK` flag, distinct from the renamed fetch flag `fetchOK`) degrades to per-item reads, preserving the existing `fetchClaimCommits` behaviour. On the mirror path a failed read returns `served=false` and the caller takes the live origin, exactly as `claim_mirror.go:174–182` does now — the mirror does **not** gain a per-item fallback. Changing that would alter mirror semantics under the guise of a performance fix.
4. Ordering and content are unchanged: refs sorted by ref name, each carrying the same `Ref`, `SHA`, cluster, task id and lease fields as before.
5. Context cancellation is checked at the top of every iteration and on entry, so a cancelled request aborts promptly rather than after the batch completes.
6. A prefix with twice the refs starts the same number of git processes; the listing's process count no longer varies with prefix size on either path.

## Constraints

- **Never code outside the dark-factory pipeline** (`CLAUDE.md` § Dark Factory Workflow).
- Counterfeiter mocks are generated, never hand-written: adding a method to the `ClaimOrigin` interface means `go generate ./...` runs as part of `make precommit`.
- ⚠️ **Test style is per-file, and two of the files this change touches are deliberately NOT Ginkgo.** `pkg/git/claim_mirror_test.go` and `pkg/git/claim_refs_list_test.go` are plain `testing.T`, and `claim_mirror_test.go:29–34` records why: a Ginkgo conversion would silently turn every existing assertion into a no-op run. New tests added to those files stay plain `testing.T` and are selected with `go test -run`. The Ginkgo/`DescribeTable` rule applies to **new files** only. Converting either existing file is out of scope and must not be done to satisfy a style rule. ⚠️ **The process-count, fallback and cancellation tests go into those existing plain-`testing.T` files, not a new Ginkgo file** — a Ginkgo entry point is `TestSuite`, which `go test -run` cannot select, so a `-run` filter matching nothing exits 0 and the tests would pass vacuously. Name them so the filters cannot select nothing: `TestListClaimRefsGolden` (deep-equal against the golden), `TestClaimRefBatchProcessCount` (the shim counter, both paths, both sizes), `TestClaimRefBatchFallback` (the `!batchOK` per-item path), `TestClaimRefBatchCancellation` (nil result, wrapped error).
- The context-cancellation check at the top of each loop iteration must survive the restructure (coding rule `go-context/cancel-check-in-loop`).
- The existing per-item fallback semantics in `fetchClaimCommits` are the model to copy for the live path; do not invent a second fallback style.
- Do not change the HTTP surface: same route, same JSON shape, same status codes.
- ≥ 80% coverage on new code.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| Batched read exits non-zero on the live path | Fall back to per-item reads; log once; return the full listing | Observed by: `fake.ReadCommitMessageCallCount() == len(refs)` and every ref present in the result |
| Batched read fails on the mirror path | Return `served=false`; caller takes the live origin | Observed by: the mirror-path test asserts `served == false` and the live path ran |
| A ref vanishes between listing and read | Drop that ref with one WARN, as today | Observed by: the result omits that ref and the WARN count is 1, not N |
| Batch output malformed or truncated | Treat as a batch failure and take the path's fallback | Same probe as the failed-batch rows |
| Context cancelled mid-batch | Return a wrapped `context.Canceled`; no partial result | Observed by: the error wraps `context.Canceled` and the result is `nil` |
| Prefix matches zero refs | Return an empty slice without invoking the batched `cat-file` read | Observed by: the shim records 0 `cat-file` invocations and the body is `[]` |

## Security / Abuse Cases

- The prefix is already validated by `validateRefPrefix` before reaching the listing; the batching change must not widen it. Batch input is drawn from git's own ref output, not from the request body.
- No new user-controlled value reaches a shell: the batched invocation passes shas as argv, as the existing `FetchCommits` does.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Pre-fix golden fixture and its deep-equal test — captured against **unmodified** code, so it must pass before any mechanism lands | — | 3 | — |
| 2 | Batched read: interface method, the `fetchOK` rename, **mock regeneration**, the read hoisted out of the single-ref helper into both callers' loops, the live-path fallback, and `make precommit` | 1, 2, 3, 4, 5, 6 | 1, 2, 4, 5 | prompt 1 |
| 3 | The `hack/claim-ref-listing-probe.sh` harness and the CHANGELOG entry | — | 6, 7 | prompt 2 |

Rationale: the golden must be its own prompt because dark-factory produces **one commit per prompt** — a golden committed in the same prompt as the implementation would share that commit, so "the golden precedes the implementation" would be unverifiable and the deep-equal vacuous. Prompt 2 lands the mechanism and the wiring together, because a batched method with no caller leaves the defect in place and reads as done; it also owns mock regeneration, without which it does not compile. Prompt 3 adds the rung-1 harness, without which the only discriminating verification has nothing to run, plus the changelog entry. The Rung-3 AC is verified by the operator after deploy, not by any prompt.

## Do-Nothing Option

The per-ref fan-out stays. The success-record snapshot keeps resolving to zero on every reconcile, so records that already carry a success record are re-driven indefinitely; the pod keeps burning roughly two-thirds of a core on process churn, and every future caller that lists a growing prefix inherits the same wall. The cost grows with the number of refs, so the current 100-errors-per-30-minutes reading is a point on a curve, not a steady state.

## Reproduction

**Expected:** a listing over a large prefix answers inside the caller's deadline and starts a constant number of git processes.
**Actual:** it starts one `git cat-file -p` per matching ref, misses the deadline every time, and the caller records zero records.

### Rung 1 — local, reproducible on any host

```
make build
bash hack/claim-ref-listing-probe.sh
# seeds a bare origin with 300 refs/claims/success/<task>/<phase>/<n> refs,
# puts a counting git shim first on PATH, boots the binary on a spare port,
# GETs /api/v1/refs?prefix=claims%2Fsuccess%2F
# pre-fix:  prints "cat-file invocations: 300"  and "refs returned: 300"
# post-fix: prints "cat-file invocations: 1"    and "refs returned: 300"
```

### Rung 3 — nukeprod, read-only, 2026-11-09

```
kubectlnukeprod -n prod top pod
# vault-obsidian-openclaw-0            1345m        419Mi
# vault-obsidian-personal-0             121m         79Mi
# vault-obsidian-seibert-personal-0       3m         16Mi
# vault-obsidian-trading-0               62m         37Mi

kubectlnukeprod -n agent logs deploy/agent-task-executor-openclaw --since=30m \
  | grep 'success_record_snapshot_failed' | grep -c 'context deadline exceeded'
# 100

kubectlnukeprod -n agent logs deploy/agent-task-executor-openclaw --since=30m \
  | grep 'success_record_snapshot_failed' | head -1
# error=list all success records: GET refs prefix claims/success/:
# Get "http://vault-obsidian-openclaw.prod.svc.cluster.local:9090/api/v1/refs?prefix=claims%2Fsuccess%2F":
# context deadline exceeded

kubectlnukeprod -n prod logs vault-obsidian-openclaw-0 --since=10m | grep -c 'git-core/git prune'
# 20
```

The listing is served from the mirror (`served_from=mirror`, 265 reads per 30 minutes), so the cost is not the network: it is the per-ref message read at `claim_mirror.go:166–172`, each a `cat-file -p` process. The same read sits in the single-ref helper at `claim_refs.go:246`, reached from the live-origin loop at `claim_refs.go:195`.

Verified against `git-rest` `origin/master` @ `1cc2e0f` (local checkout 25 commits behind) and `bborbe/git-rest:v0.41.2` on nukeprod.
