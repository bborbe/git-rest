---
status: completed
tags:
    - dark-factory
    - spec
    - bug
approved: "2026-11-13T09:14:14Z"
generating: "2026-11-13T09:18:27Z"
prompted: "2026-11-13T09:28:56Z"
verifying: "2026-11-13T09:53:22Z"
completed: "2026-11-13T09:53:29Z"
branch: dark-factory/scope-claim-ref-listing-by-prefix
---

## Summary

- A claim-ref listing walks the **entire** claim namespace with one `git for-each-ref`, then applies the caller's `prefix` in Go. The cost is set by the size of the namespace, not by the size of the answer.
- The namespace holds roughly **7,000 refs** (104 under `claims/redrive/`, about 6,400 under `claims/success/`), so the walk costs a flat several-hundred-millisecond floor no matter what was asked for.
- Measured on sun 2026-11-13 through the API-server proxy, with a same-service control: `/healthz` **62 ms**; `refs?prefix=claims/redrive-reason/<reason-id>` returning **1 ref / 267 bytes** costs **~790 ms**; `refs?prefix=claims/scheme` returning **1 ref / 152 bytes** costs **~790 ms**.
- The local mirror is the hot path — `served_from=mirror` **1515** against `served_from=origin` **19** in ten minutes — so this is local work, not a remote fetch and not the network.
- The consequence is a throughput ceiling: the live task-event consumer pays two listings per event and runs at **~1–2.5 events/s**, tens of thousands of events behind.

⚠️ **This spec addresses the residual cost only.** The per-ref `git cat-file -p` fan-out is a *different* defect and is fixed and shipped — `specs/completed/026-batch-claim-ref-commit-reads.md`, PR #79, **v0.44.1** — and that release is now **deployed to sun** (2026-11-13, `bborbe/sun#154`). That deploy moved the large-prefix cost by more than an order of magnitude but left the floor in place, which is exactly what this spec targets.

⚠️ **The numbers above are single-sample and were taken across the deploy.** They establish the order of magnitude and the shape (a one-ref answer costs the same as a 6,400-ref answer), not a precise baseline. Re-take the baseline with ≥3 samples on a quiet cluster and record the method before the post-deploy comparison — do not cite a single sample as the target.

## Problem

A lookup has been implemented as a full scan. `listClaimRefsFromMirror` runs `git for-each-ref` over the whole claim namespace and only then filters the result by the caller's prefix, so a request that wants one ref pays for all seven thousand. Because the callers re-list on a fixed cadence, the cost is sustained rather than bursty, and it lands on the same process that must also keep up with a Kafka partition.

The sibling spec fixed what a listing costs **per matched ref**. This one fixes what it costs when it matches almost nothing — the floor, not the slope.

## Goal

A claim-ref listing costs what its **answer** costs, not what the namespace costs. A prefix matching one ref and a prefix matching six thousand must be distinguishable by their response times, and the small one must not pay for the large one's existence.

## Non-goals

- The per-ref commit-message read. Fixed in v0.44.1; `specs/completed/026-batch-claim-ref-commit-reads.md`. The remaining `origin.ReadCommitMessage` call site is that spec's deliberate live-path fallback and must stay.
- Deploying v0.44.1 to sun — **done**, `bborbe/sun#154`. Not part of this change.
- Pruning, expiring or compacting refs under `claims/success/`. Retaining them is correct; listing them must simply be cheap.
- Changing the claim-ref storage model, key shapes, lease semantics or the `[]ClaimRef` response shape (documented in `docs/api.md`).
- Changing any caller's deadline or cadence.
- The executor's two-listings-per-event shape. That is a client-side change and removes a factor of two, not the ceiling.
- Adding pagination, a new endpoint, or a write-side change.

## Assumptions

- The mirror path is the hot path in production: measured `served_from=mirror` 1515 against `served_from=origin` 19 over ten minutes on sun. The live-origin path is colder but must be fixed too, or the defect returns the moment the mirror goes stale.
- The namespace is dominated by `claims/success/` (~6,400 refs). It grows over time, so a fix that merely makes today's walk faster will regress.
- **Scoping the invocation may be sufficient on its own.** Claim refs are written continuously by lease traffic, so they are expected to be loose refs rather than entries in `packed-refs`; if so, `git for-each-ref refs/claim-mirror/<prefix>` iterates a directory subtree rather than the whole namespace. This is the first thing to measure, and it decides whether the index below is needed at all.
- `go.mod` carries **no** embedded key-value store today (no bbolt, badger or sqlite). An index would therefore be a new dependency, which is why it is the fallback rather than the default.

## Acceptance Criteria

- [ ] **The `for-each-ref` invocation is prefix-scoped, or the namespace is not walked per request.** Evidence: the `PATH` shim in `hack/claim-ref-listing-probe.sh` already appends full argv to a counter file; the test asserts the recorded `for-each-ref` line names a **prefix-scoped** ref pattern (e.g. `refs/claim-mirror/claims/target/`) and **not** the bare namespace root. ⚠️ A process-*count* assertion does **not** satisfy this and must not be substituted: the unfixed full walk is also exactly one process, so a count cannot distinguish the two.
- [ ] **Scoping is measured before an index is built.** Evidence: `bash hack/claim-ref-listing-probe.sh` reports, for a fixture of **7,000** refs under `claims/success/` and a small synthetic fixture prefix (see Assumptions), a small-prefix listing under **100 ms**. The probe script seeds only `claims/success/` today and exits non-zero unless `refs_returned == PROBE_REFS`, so **prompt 1 must extend it** to seed and list the fixture prefix and to print the recorded `for-each-ref` argv line. `claims/target/` is a **synthetic fixture prefix** — it does not exist in production, so do not go looking for it there. If the 100 ms figure holds, the index behaviours below are **not** implemented, the index sections are **deleted into a follow-up spec**, and this spec is complete at this AC.
- [ ] **A one-ref listing answers in under 100 ms on sun.** `**Post-Deploy (Rung-3):**`
    - `deploy_check:` `kubectlsun -n agent rollout status deploy/git-rest-openclaw`
    - `deploy_target:` sun / `agent` / `git-rest-openclaw`
    - Evidence: take a **reason id** from `refs?prefix=claims%2Fredrive-reason%2F` — refs there are keyed `<reason-id>/<phase>/<n>`, and per-id counts run 2–5, never a fixed number, so **name the count you measured rather than asserting three**. ⚠️ Do **not** derive the id from `claims%2Fredrive%2F`; those are task ids in a different namespace and the lookup returns an empty `[]` (2 bytes), which would time an empty listing and call it a small one. Time `refs?prefix=claims%2Fredrive-reason%2F<reason-id>` **five times**; every sample under 100 ms. A single sample does not satisfy this.
- [ ] **`refs?prefix=claims%2Fsuccess%2F` answers in under 1000 ms.** `**Post-Deploy (Rung-3):**`
    - `deploy_check:` `kubectlsun -n agent rollout status deploy/git-rest-openclaw`
    - `deploy_target:` sun / `agent` / `git-rest-openclaw`
    - Evidence: five consecutive samples under **1000 ms** (pre-fix: **39,111 ms**), **and** `kubectlsun -n agent logs deploy/agent-task-executor-openclaw --since=30m | grep -c 'context deadline exceeded'` **reads 0** over a window that begins after the rollout. ⚠️ Assert on the printed count being `0`, never on `grep`'s exit status — `grep -c` exits 1 precisely when the count is zero, so an exit-status assertion fails exactly when the criterion passes. ⚠️ Count only deadline errors raised by **git-rest** calls; the executor also logs `guard=claim_unprobeable … http://vault-api…: context deadline exceeded`, which is a different service and must not be counted against this change.
- [ ] **Both listing paths are covered.** Evidence: the argv assertion runs twice — once with a fresh claim mirror configured (mirror path) and once with `claimMirrorFreshAt` unset (live-origin path). Per-path process totals are 026's invariant and are **not** re-asserted here.
- [ ] **The listing returns identical values in identical order to the pre-change implementation.** Evidence: the **existing** `pkg/git/testdata/claim_refs_golden.json`, deep-equalled by `TestListClaimRefsGolden`, continues to pass, and `TestClaimMirrorPrefixSemantics` continues to pass. No second golden file is created — it would pin the same invariant twice.
- [ ] **If, and only if, the index is built: it cannot serve a stale answer.** Evidence: a test writes a new claim ref, lists its prefix, and asserts the new ref is present; it then deletes a ref, lists again, and asserts the ref is absent. A second test asserts that when the ref set changes through a path the index does not observe, the listing returns the live set — the index must fail toward the origin, never toward a stale result.
- [ ] `make precommit` exits 0.
- [ ] **`CHANGELOG.md` gains a `## Unreleased` section carrying the entry.** `origin/master` carries **no** `## Unreleased` heading — the release bot renames it and does not re-add it — so the heading must be created, not merely appended to. Evidence: `awk '/^## Unreleased/{f=1;next} /^## v/{f=0} f && /claim ref/' CHANGELOG.md` returns ≥ 1 line.

## Verification

Rung ladder per `docs/verifying-specs.md`. Rung 1 is mandatory. ⚠️ **Rung 3 is taken by judgment, not by table sanction:** the selection table's "pure code path under `pkg/git/`" row marks rung 3 as *not required*, and this change is that row (response shape, query param and error mapping all unchanged). It is taken anyway because the ladder's rationale names throughput-sensitive paths and because the defect is only measurable at production scale. Rung 2 is *optional* for this row and is skipped because dev has no `claims/success/` of the relevant size.

### Rung 1 — container-executable

- `make precommit`
- `go test ./pkg/git -run 'TestClaimMirror|TestListClaimRefs'`
- `bash hack/claim-ref-listing-probe.sh` — exits 0 and its printed argv line names a prefix-scoped pattern.

### Rung 2 — dev

Skipped (optional for this row; dev lacks the prefix size that triggers the defect).

### Rung 3 — prod (sun)

- Roll out the release to sun, then run the two Post-Deploy ACs above.

## Desired Behavior

1. **A listing's cost is a function of its answer.** A prefix matching k refs costs what k refs cost. The number of refs under other prefixes is not a term in that cost.
2. **The prefix is applied before the work, not after it.** The `for-each-ref` invocation names the prefix, so git walks that subtree instead of the namespace. If measurement shows that is not enough, a prefix-keyed index replaces the walk — but the scoped invocation is tried first and its numbers recorded.
3. **The answer is unchanged.** The same `[]ClaimRef` values, in the same sorted order, with the same per-item fallback and the same context-cancellation semantics as today.
4. **Freshness is fail-safe, if an index exists.** Any doubt about the index degrades to the current live behaviour rather than serving a stale answer.
5. **The write path updates the index, if an index exists.** `PutClaimRef` and `DeleteClaimRef` update it in the same request that changes the ref, so a caller that writes then lists sees its own write.
6. **Both listing paths honour the contract.** The mirror path and the live-origin fallback each satisfy behaviour 1.
7. **The log says which path answered.** The existing `served_from=` line gains an index value if and only if an index exists; it must not be repurposed otherwise.

## Constraints

- The `[]ClaimRef` response shape, the `prefix` query parameter and the error mapping are unchanged. Prefix semantics are whole-path-component matching, already documented in `docs/api.md` — this spec must not restate or change it.
- `TestClaimMirrorPrefixSemantics` is the regression lock for prefix semantics; it must keep passing unchanged.
- No new runtime dependency unless the scoped invocation is measured insufficient. An in-memory index rebuilt at startup satisfies the index ACs without one.
- The write path (`PutClaimRef`, `DeleteClaimRef`) must not gain a new failure mode: an index update failure must not fail the ref write.
- The live-origin path must not be deleted as a simplification — it is the fallback when the mirror is stale, absent or missing an object.
- Logging follows the dark-factory rule at `~/.claude/plugins/marketplaces/dark-factory/docs/rules/logging-conventions.md` (absolute path — this repo has no `docs/rules/`).
- Tests are plain `testing.T`, matching the existing `pkg/git` suite; counterfeiter mocks must be regenerated if an interface changes.

## Failure Modes

| Trigger | Expected behaviour | Recovery |
|---|---|---|
| Scoped invocation still walks a large `packed-refs` | Listing stays slow after the change | Probe reports the small-prefix time above 100 ms; build the index, which is why it is kept as the fallback |
| Index misses a ref written through an unobserved path | Listing returns the live set, not the stale index | `served_from=origin` appears in the log for the next listing |
| Index update fails after a successful ref write | Ref write still returns success; index marked dirty | Next listing rebuilds from the live source; `served_from=origin` shows it |
| Mirror rewritten by the puller while a listing reads the index | The listing returns either the pre-pull or post-pull set, never a mix | The next listing shows `served_from=mirror` with a fresh `age_s`; a persistent mismatch logs at WARN |
| Memory grows with the namespace (index only) | Bounded by the ref count, not by request volume | After a restart the log shows `served_from=mirror` and the listing returns the full ref set — the index was rebuilt |
| Context cancelled mid-listing | Wrapped `context.Canceled`, no partial result | Unchanged from today; the existing cancellation test covers it |

## Security / Abuse Cases

- `prefix` is already validated by `validateRefPrefix`; the change must not widen what a prefix can reach.
- If an index exists, it must not let one prefix read another's refs — the prefix filter stays the authority, the index is only an accelerator.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Scope the `for-each-ref` invocation to the prefix on the mirror path; extend `hack/claim-ref-listing-probe.sh` to seed and list a small fixture prefix and to print the recorded `for-each-ref` argv | 1, 2, 6, 7 | 1, 2, 5, 6, 9, 10 | — |
| 2 | Cover the live-origin path with the same contract | 1, 2, 6 | 5 | 1 |
| 3 | **Only if prompt 1's measurement misses 100 ms:** build the prefix-keyed index and wire it to the write path | 2, 4, 5, 7 | 7 | 1, 2 |

Prompt 3 is conditional. If prompt 1's probe shows a scoped listing under 100 ms, prompts 1–2 are the whole change; the index Desired Behaviors, AC and Failure Modes rows are then **removed into a follow-up spec**, not left as outstanding work.

## Do-Nothing Option

Every claim-ref listing keeps paying a several-hundred-millisecond floor regardless of its answer. The live task-event consumer stays capped at a couple of events per second and stays tens of thousands of events behind, so spawn decisions read a stale view; and the gap widens as `claims/success/` grows, because the walk is over the whole namespace. The cost of doing nothing is not a constant — it is a cost that grows with every claim the fleet writes.

## Expected vs Actual

**Expected**, per `docs/api.md`: a claim-ref listing returns the refs under the requested prefix, and its cost tracks the number of refs returned.

**Actual**, measured 2026-11-13 on sun: the listing returns the correct refs, but its cost tracks the size of the whole namespace. A one-ref, 152-byte answer and a 6,400-ref, 1.59 MB answer are within the same order of magnitude, against a 62 ms control on the same service.

## Reproduction

Rung 1 — local. The probe's argv counter is an internal shell variable written into a `mktemp -d` that is removed by an `EXIT` trap, so it is not reachable after the run; **prompt 1 extends the script to print the recorded `for-each-ref` argv line**, and this step reads that output.

```bash
# 1. Seed a namespace-shaped fixture and list a small prefix.
#    PROBE_REFS controls the claims/success/ count.
PROBE_REFS=7000 bash hack/claim-ref-listing-probe.sh

# 2. The script prints the argv it captured for the listing.
#    Pre-fix output names the bare namespace root, not the requested prefix:
#      for-each-ref argv: for-each-ref --format=%(objectname) %(refname) refs/claim-mirror/
```

Rung 3 — sun, 2026-11-13, deployment `git-rest-openclaw` at **v0.44.1** (after the deploy):

```bash
# control — same service, same proxy
kubectlsun -n agent get --raw "/api/v1/namespaces/agent/services/git-rest-openclaw:9090/proxy/healthz"
#   → 62 ms

# a one-ref answer
kubectlsun -n agent get --raw "/api/v1/namespaces/agent/services/git-rest-openclaw:9090/proxy/api/v1/refs?prefix=claims%2Fscheme"
#   → ~790 ms, 1 ref, 152 bytes

# an empty answer — 2 bytes, the same order of cost
kubectlsun -n agent get --raw "/api/v1/namespaces/agent/services/git-rest-openclaw:9090/proxy/api/v1/refs?prefix=claims%2Fredrive-reason%2F<task-id-from-claims%2Fredrive%2F>"
#   → ~790 ms, 0 refs, 2 bytes
```

Same probe, same cluster, same proxy: `/healthz` 62 ms against `refs` ~790 ms for a one-ref answer. The control rules out the network, the proxy and the payload. ⚠️ Single samples — re-take with ≥3 samples before using any of these as a comparison baseline.
