---
status: prompted
approved: "2026-10-22T10:18:01Z"
generating: "2026-10-22T10:34:33Z"
prompted: "2026-10-22T10:40:58Z"
branch: dark-factory/commit-coalescing
---

## Summary

- Every HTTP write to git-rest today produces one commit and one synchronous push. On the OpenClaw vault that is 12,490 commits in 24 hours — one every ~8 seconds — against a GitHub `main` ref lock held for a few hundred milliseconds per push.
- Three writers contend for that lock (nuke dev, nuke prod, and the host sync daemon). At the current rate a writer with a slow uplink loses `cannot lock ref` on every attempt; a fourth writer deployed on 2026-10-21 was reverted the next day for exactly this.
- Most of that volume is not content change. One task file is rewritten several times within seconds, so a burst of seven commits in under two minutes is one logical edit.
- A write returns as soon as its content is safely on disk, and a background flusher turns a burst into one commit and one push. That also takes the push off the request path, which is what makes intake commands stop expiring.

## Problem

A single HTTP write today runs write → stage → commit → push to completion before responding, so both the commit rate and the response time are set by the push. On `bborbe/obsidian-openclaw` that produced **12,490 `git-rest:` commits in the trailing 24 hours** (measured 2026-10-21, fresh clone at HEAD `48de6b2374`, 14,111 commits total). Attribution over the pinned window `2026-10-20 23:45` → `2026-10-21 23:45`: PR Review 5,380 · Update Go 2,766 · Release bborbe 2,188 · Dark Factory 1,172 · hypothesis 793 · everything else 189 · touched no task file 2 — the four big families are 92.1% of the volume.

The per-task sequences show why the volume exceeds the work. `bborbe-sun#30` took **7 commits in 114 seconds**: body, `planning→execution`, `execution→planning` five seconds later, `planning→ai_review`, `ai_review→planning` three seconds later, `planning→done`, body. `bborbe-agent#104` flipped `planning→execution` then `execution→planning` **two seconds later**. The dominant write reason is phase-field oscillation, not content change.

The same coupling sets latency: `WriteResult` measured **p50 2.71 s / p90 3.66 s / p99 7.18 s** (peer measurement, v0.15.1, 1,294 writes), and 20 intake commands expired in a 31-minute burst against a 60-minute deadline (`bborbe/agent-task-controller` `pkg/factory/factory.go:33`, whose comment names "a slow git-rest round-trip" as a cause it was sized to absorb).

Batching the commits cuts the rate; taking the push off the request path cuts the latency. Either alone leaves half the problem.

**The hazard that makes this non-trivial.** Between a write and its flush the working tree is dirty, and the 30 s puller runs against it — the failure recorded in [[git-rest Dirty Clone Blocks Pulls]]. Spec 019 (in `specs/in-progress/`, prompted but not yet verified) widens the rescue to every pull path. This spec must not re-create the wedge that rescue exists to clear.

## Goal

A write returns once its content is on disk and staged, and a background flusher turns a burst of writes into one commit and one push. A pull cycle never merges against a tree this process dirtied. A write that is staged but not yet pushed survives a pod restart — flushed on SIGTERM, or recovered on boot after a crash. Claim refs keep their synchronous push and their 200/409 meaning. Coalescing can be turned off without a redeploy, restoring today's behaviour exactly.

## Assumptions

- **A staged path is the durable record of a pending write.** Staging writes the blob and the index entry to disk, so a staged-but-uncommitted change survives a crash. No sidecar file is needed to know what was pending.
- **Staging alone does not preserve the no-op property.** Today "unchanged content" is detected by the *commit attempt* — `git commit` fails with `nothing to commit` (`pkg/git/git.go:746-755`). Under coalescing no commit runs inside the request, and the index may still hold an earlier pending write, so a commit would succeed. The short-circuit must therefore move to the staging step and compare the **new bytes to the file on disk**, not the index to `HEAD`: after the first write of a burst the index already differs from `HEAD`, so an index-vs-HEAD check would report "changed" for every later identical write and the no-op property would be silently lost.
- **A claim-ref push is independent of the working tree.** A ref push does not touch the index, so a synchronous claim push is legal while file writes are pending.
- **Every git operation is serialised by one mutex in `pkg/git`.** This is the package's existing design (see `pkg/git/git.go` and specs 005 and 019); the flusher and the puller therefore never run concurrently, and a shared pending set needs no second lock.
- **The 30 s pull interval is a natural flush boundary but must not be the only one.** A pull-only flush would satisfy every rate and durability criterion while leaving a vault with a long pull interval flushing late; the timer is what bounds staleness independently of the pull schedule.
- **The off-switch exists for two named consumers**: it is AC2's comparator (proving the reduction comes from coalescing and not from an unrelated change), and it is the rollback lever for a change that weakens `200` from "committed and pushed" to "written and staged" — revertable by flag, without a redeploy.

## Non-goals

- Not changing HTTP status codes, path-traversal validation, or the JSON error shape.
- Not changing claim-ref semantics, the lease check, or `ErrRefConflict` → 409.
- Not changing the conflict resolver, the dirty-tree rescue (spec 019), the quarantine layout, or readiness.
- Not reducing the number of agent writes — that is `bborbe/agent-task-controller` v0.16.0's job, and it is separable because no-op writes already produce no commits.
- Not serialising pushes across clusters. That is [[Vault API Resolves Cross-Cluster Frontmatter Merges]]; this spec lowers the rate that makes serialisation necessary and does not replace it.
- Not coalescing `DeleteFile`. Deletes stay synchronous; they gain only a flush-first ordering guarantee.

## Acceptance Criteria

Every AC runs against recipe binaries built from this branch. Ports and `$BASE` paths are per-recipe so runs never collide; see § Recipes.

- [ ] **AC1 — a burst to one path lands as one commit carrying the last content.** Recipe (a): `git -C "$BASE/work" log --format='%s' -- tasks/x.md | grep -c '^git-rest: update tasks/x.md$'` returns `1`; `git -C "$BASE/work" show HEAD:tasks/x.md` prints `C`; the last `GET /api/v1/files/tasks/x.md` returned body `C`. Evidence shape: stdout match + file content + HTTP response.
- [ ] **AC2 — negative: the reduction is the coalescing, not an unrelated change.** Recipe (a) run with `--write-coalesce=false`: the same `grep -c` returns `3`, content `C` still at HEAD. A build that stops committing fails AC1; a build that ignores the flag fails this. Evidence shape: stdout match.
- [ ] **AC3 — a burst spanning two paths lands as one commit touching both.** Recipe (b): `git -C "$BASE/work" log -1 --format='%s'` matches `^git-rest: update 2 files$`; `git -C "$BASE/work" show --name-only --format='' HEAD | sort` prints exactly `tasks/x.md` and `tasks/y.md`. Evidence shape: stdout match + file list.
- [ ] **AC4 — write latency leaves the request path.** Recipe (c) issues 200 sequential `POST`s to one path and prints the p50 of client-measured durations: it is **≤ 250 ms**. Evidence shape: numeric stdout, threshold asserted in the recipe. The comparator is the same recipe against `--write-coalesce=false`, whose p50 is **≥ 500 ms** — if it is not, the recipe is not exercising a push and proves nothing. ⚠️ **The comparator only means anything because recipe (c)'s remote is deliberately slow**: a local bare repo completes `add + commit + push` in ~220 ms, so without the `pre-receive` sleep the synchronous path would clear 250 ms on a *correct* implementation and this AC would be unsatisfiable. The hook is part of the recipe, not an optional refinement.
- [ ] **AC5 — every pull cycle starts clean, and a pending write reaches origin within one cycle of it.** Recipe (d): `git -C "$BASE/work" status --porcelain` returns 0 lines; `git -C "$BASE/work" log origin/$(git -C "$BASE/work" rev-parse --abbrev-ref HEAD) --format='%s' -- tasks/x.md | grep -c '^git-rest:'` returns ≥ 1; `grep -c 'git-rest: flushed pending writes' "$BASE/run.log"` returns ≥ 1; and `grep -c 'rescue branch pushed' "$BASE/run.log"` returns `0`. The last clause is the negative half: a design that leaves the tree dirty for the puller to trip over shows up as a rescue, which is the failure this AC exists to catch. Evidence shape: exit code + stdout + log line.
- [ ] **AC6 — a pending write survives SIGTERM.** Recipe (e), run as **one shell script** (the `T0` variable does not survive separate commands), issues one `POST`, waits 2 s, records `T0`, sends `SIGTERM`, and waits for exit. Then: `git -C "$BASE/work" log origin/$(git -C "$BASE/work" rev-parse --abbrev-ref HEAD) --format='%s' -- tasks/x.md | grep -c '^git-rest:'` returns ≥ 1, and `git -C "$BASE/work" show origin/$(git -C "$BASE/work" rev-parse --abbrev-ref HEAD):tasks/x.md` prints `SURVIVOR`. Evidence shape: exit code + stdout match + file content. **The deadline is not asserted here** — recipe (e) has no slow remote, so the flush completes in ~0.1 s and a build with no deadline would pass; the 10 s bound is AC14's job.
- [ ] **AC7 — a pending write survives SIGKILL and is recovered on boot.** Recipe (f) issues one `POST`, waits 2 s, `kill -9`s the process, then asserts the precondition — `git -C "$BASE/work" log origin/$(git -C "$BASE/work" rev-parse --abbrev-ref HEAD) -- tasks/x.md` carries no `SURVIVOR` commit **and** `git -C "$BASE/work" diff --cached --name-only` lists `tasks/x.md` — then reboots the same binary against the same repo and waits two pull intervals. Then `git -C "$BASE/work" show origin/$(git -C "$BASE/work" rev-parse --abbrev-ref HEAD):tasks/x.md` prints `SURVIVOR` and `grep -c 'git-rest: recovered pending writes' "$BASE/run.log"` returns ≥ 1. The precondition is load-bearing: without it the AC passes on a write flushed before the kill, and recovery is never exercised. Evidence shape: exit code + log line + file content.
- [ ] **AC8 — negative: claim refs stay synchronous while a file write is pending.** Recipe (g) issues one `POST` to a file, immediately issues `PUT /api/v1/refs/claims/{key}` — the route is `docs/api.md:20`, and the body carries `cluster`, `task_id` and `expected_sha` (**empty for a create** — `ClaimRefRequest` documents "ExpectedSHA empty means the ref must not exist", `pkg/git/claim_refs.go:51-52`, and a non-empty lease against a nonexistent ref yields `stale info` → `ErrRefConflict` → 409) — and asserts `git -C "$BASE/work" ls-remote origin 'refs/claims/*'` lists the ref **at the moment the PUT returned 200**, while `git -C "$BASE/work" log origin/$(git -C "$BASE/work" rev-parse --abbrev-ref HEAD) --format='%s' -- tasks/x.md | grep -c '^git-rest:'` still returns `0`. Routing claim refs through the buffer fails this. Evidence shape: stdout match + negative commit count.
- [ ] **AC9 — negative: unchanged content still produces no commit and no push.** Recipe (h) posts content `A` to `tasks/x.md` three times inside the window, then flushes: `git -C "$BASE/work" log --format='%s' -- tasks/x.md | grep -c '^git-rest: update tasks/x.md$'` returns `1` (the first write only) and `grep -c 'write file: no changes to commit (content unchanged)' "$BASE/run.log"` returns `2`. Evidence shape: stdout match + log line. The content is named `A` deliberately: posting the preamble's existing `v0` three times stages nothing at all, so the count would be `0` and the AC would fail against a correct implementation. The log string is quoted in full because it is the existing message at `pkg/git/git.go:751` and the short-circuit must keep emitting it.
- [ ] **AC10 — the flush is observable.** Recipe (a), immediately after the flush. `N1='([1-9]|[1-9][0-9]+)'` and `N3='([3-9]|[1-9][0-9]+)'`:
  - `curl -s localhost:18447/metrics | grep -cE "^git_rest_write_flushes_total\\{outcome=\"ok\"\\} $N1$"` returns `1`
  - `curl -s localhost:18447/metrics | grep -cE "^git_rest_write_coalesced_total $N3$"` returns `1` — three writes absorbed, so the floor is **3**, not 1; a build that increments by one per flush passes an `N1` bound and is caught only here
  - `curl -s localhost:18447/metrics | grep -cE '^git_rest_write_pending [0-9]+$'` returns `1`

  Both regexes use the `X|Y` alternation deliberately: a bare `[3-9]` stops matching once the value reaches 10, so the assertion would pass on a correct implementation and fail on a busier one. Recipe (d2) drives `outcome="error"` to ≥ 1. Evidence shape: metric value. Without this AC the laziest implementation ships the mechanism and no observability.
- [ ] **AC11 — `DeleteFile` flushes pending writes before its own commit.** Recipe (i): `POST` to `tasks/x.md`, then `DELETE tasks/x.md` inside the window, then flush. `git -C "$BASE/work" log -2 --format='%s'` lists `git-rest: delete tasks/x.md` above a `git-rest:` write commit, and `git -C "$BASE/work" status --porcelain` returns 0 lines. A delete that jumps the queue leaves the path re-created by the later flush. Evidence shape: stdout match + exit code.
- [ ] **AC12 — the contract and the runbook are updated in the same change.** `grep -c 'coalesc' docs/api.md` returns ≥ 1; **`grep -c 'written and staged' docs/api.md` returns ≥ 1**; `grep -c 'File written + committed' docs/api.md` returns `0`; `grep -c 'stages the change, commits, and pushes' docs/api.md` returns `0`; `grep -c 'write-coalesce' docs/deployment.md` returns ≥ 1; `grep -c '12,490' docs/deployment.md` returns ≥ 1; `grep -c 'coalesc' CHANGELOG.md` returns ≥ 1; `awk '/^## /{s=$0} /coalesc/{print s; exit}' CHANGELOG.md` prints `## Unreleased`; `git diff origin/master --name-only` lists all three. The positive `written and staged` assertion is load-bearing alongside the two negatives: without it, deleting the stale sentence and the `200` row passes while the contract states nothing at all. Evidence shape: file content (positive and negative) + file list. The two negative greps are the point: `docs/api.md:75` (`200 | File written + committed`) and `:62` ("stages the change, commits, and pushes") both promise a synchronous push, and a change that adds a coalescing sentence beside them leaves the contract contradicting itself. `CHANGELOG.md` carries no `## Unreleased` heading today, so the implementer creates it; the `awk` assertion stops the entry landing under a released section. The `12,490` baseline lands in `docs/deployment.md` under Monitoring so the regression stays checkable after this spec is archived. This AC is constraint-derived (Constraints § docs) — no Desired Behavior describes documentation.
- [ ] **AC13 — the flusher is a timer, not the pull boundary.** Recipe (j) boots with `--pull-interval=10m` and `--write-coalesce-delay=5s`, posts once, and waits 12 s — **no pull can occur in that window**. Then `git -C "$BASE/work" log --format='%s' -- tasks/x.md | grep -c '^git-rest: update tasks/x.md$'` returns `1` and `git -C "$BASE/work" log origin/$(git -C "$BASE/work" rev-parse --abbrev-ref HEAD) --format='%s' -- tasks/x.md | grep -c '^git-rest:'` returns ≥ 1. Second clause, recipe (k): boots with **`--pull-interval=10m --write-coalesce-delay=300s --write-coalesce-max-delay=20s`**, posts **distinct content `k1`, `k2`, …** every 2 s for 60 s, then `git -C "$BASE/work" log --format='%s' -- tasks/x.md | grep -c '^git-rest: update tasks/x.md$'` returns **≥ 3**. All three producers are now discriminated: the pull is disabled by the 10-minute interval, the trailing timer cannot fire because the stream never goes quiet for 300 s, so only the 20 s ceiling can produce the flushes — and a build with no ceiling produces **0**, a trailing-only build produces **0**, and a correct build produces 3 (at ≈20 s, 40 s and 60 s). Distinct content is mandatory: identical bytes stage nothing after the first write, so a constant payload would yield 0 and fail a correct implementation. Evidence shape: stdout match + numeric stdout. Without this AC a build that flushes **only** at the pull boundary passes every other AC.
- [ ] **AC14 — the shutdown deadline is enforced, not aspirational.** Recipe (e2) installs a `pre-receive` that sleeps 60 s, posts one `POST`, waits 2 s, then runs the SIGTERM-and-wait as a single script block and prints `ELAPSED` in milliseconds via `perl -MTime::HiRes` (whole-second `date +%s` gives up to 1 s of slack at a 10 s bound). Then `ELAPSED` is **≤ 11000**; the process has exited (the `wait` returns); `git -C "$BASE/work" log origin/$(git -C "$BASE/work" rev-parse --abbrev-ref HEAD) --format='%s' -- tasks/x.md | grep -c '^git-rest:'` returns `0` (the push never completed); and `git -C "$BASE/work" diff --cached --name-only` still lists `tasks/x.md` (the work is staged for boot recovery). Evidence shape: numeric stdout + exit code + negative commit count + file list. Without the slow remote this AC passes on a build that simply blocks until the push finishes, which is the exact defect it exists to catch.
- [ ] **Post-Deploy (Rung-2):** AC15 — on dev, `vault-obsidian-openclaw-0` runs the coalescing build and a write burst collapses. The verifier produces the burst itself: 20 sequential `POST`s to a scratch path under `/api/v1/files/` through the pod, distinct content each. **Both sides are measured as deltas around the burst**, because `git_rest_http_requests_total` is a cumulative counter and a lifetime value compared against a 10-minute commit count is vacuous on any pod older than the window. Before the burst, record `POSTS_BEFORE` (`kubectlnukedev -n dev exec vault-obsidian-openclaw-0 -- wget -qO- http://localhost:9090/metrics | grep '^git_rest_http_requests_total{method="POST"' | grep 'status="200"' | awk '{s+=$NF} END {print s+0}'`) and `COMMITS_BEFORE` (`kubectlnukedev -n dev exec vault-obsidian-openclaw-0 -- git -C /data rev-list --count HEAD`); after the burst settles, record `POSTS_AFTER` and `COMMITS_AFTER` the same way. Evidence: `POSTS_AFTER - POSTS_BEFORE` is **≥ 20** (the burst landed), `COMMITS_AFTER - COMMITS_BEFORE` is **< 20** (it did not land as twenty commits), and `kubectlnukedev -n dev logs vault-obsidian-openclaw-0 --since=10m | grep -c 'git-rest: flushed pending writes'` returns ≥ 1. Every number is read from the pod, so the comparison needs no external tally. ⚠️ **The denominator must be the successful-write counter.** `grep -c 'write file:'` in the pod log is not a write count — that string appears only in the unchanged-content INFO message and in two error wraps, so a normal window reads 0 and "commits strictly below 0" would be unsatisfiable against a correct deploy.
  - `deploy_check:` `kubectlnukedev -n dev exec vault-obsidian-openclaw-0 -- wget -qO- http://localhost:9090/metrics | grep -m1 '^git_rest_build_info' | sed -E 's/.*commit="([^"]+)".*/\1/' | grep -E '^[0-9a-f]{7,40}$'`
  - `deploy_target:` `$(git rev-parse --short origin/master)`

  The Post-Deploy marker is the first token of this bullet by requirement — `spec-verifier` extracts it with a literal grep for the marker string, so a marker anywhere but the start of the body is not found and Phase 0.5 skips silently instead of refusing. (The string appears exactly once in this file, in the bullet above, so that grep has one target.) Short-SHA against short-SHA because `Makefile:156` bakes `--build-arg BUILD_GIT_COMMIT=$(git rev-parse --short HEAD)`, so the metric's `commit` label is a short SHA and a full 40-character `rev-parse HEAD` would never compare equal even on a correct deploy.

### Recipes

Build once: `go build -o /tmp/git-rest-coalesce-bin .` from this worktree. Run sequentially — every recipe binds `:18447`. Each recipe gets its own `$BASE` and its own binary.

**Shared preamble** (substitute the letter; `<EXTRA_FLAGS>` per the table):

```bash
BASE=/tmp/git-rest-<L> && rm -rf "$BASE" && mkdir -p "$BASE" && cd "$BASE"
git init -q --bare -b test/repro origin.git
git clone -q origin.git seed && cd seed
git config user.email repro@local && git config user.name repro
mkdir -p tasks && printf 'v0\n' > tasks/x.md && printf 'v0\n' > tasks/y.md
git add -A && git commit -q -m init && git push -q origin test/repro
cd "$BASE" && git clone -q origin.git work && cd work
git config user.email repro@local && git config user.name repro
/tmp/git-rest-coalesce-bin --listen=:18447 --repo="$BASE/work" --pull-interval=10s \
  <EXTRA_FLAGS> > "$BASE/run.log" 2>&1 & PID=$!
sleep 3
```

Teardown after each: `kill "$PID" 2>/dev/null || true`.

| Recipe | `<EXTRA_FLAGS>` | Steps after preamble | Observed by |
|---|---|---|---|
| (a) | — | `POST tasks/x.md` = `A`, `B`, `C` 1 s apart; `sleep 40`; `GET tasks/x.md` | AC1, AC2, AC10 |
| (b) | — | `POST tasks/x.md` = `A`, `POST tasks/y.md` = `B` 1 s apart; `sleep 40` | AC3 |
| (c) | — | **install the slow hook first** (below); 200 sequential `POST`s to `tasks/x.md` with **distinct content each** (`c1`, `c2`, …), timing each client-side; print p50 | AC4 |
| (d) | — | `POST tasks/x.md` = `D`; advance `seed` by writing **`tasks/z.md`** and pushing; `sleep 25` | AC5 |
| (d2) | — | as (d), but install a `pre-receive` that `exit 1`s; after the failed flush `sleep 25`, assert `outcome="error"` ≥ 1 and the commit is still local, then remove the hook and `sleep 25` | AC10 |
| (e) | — | `POST tasks/x.md` = `SURVIVOR`; `sleep 2`; `kill -TERM "$PID"` | AC6 |
| (e2) | — | install a `pre-receive` that `sleep 60`s; `POST tasks/x.md` = `E2`; `sleep 2`; SIGTERM-and-wait as one script block, print `ELAPSED` in ms | AC14 |
| (f) | — | `POST tasks/x.md` = `SURVIVOR`; `sleep 2`; `kill -9 "$PID"`; restart same binary | AC7 |
| (g) | `--write-coalesce-max-delay=300s` | `POST tasks/x.md` = `G`; immediately `PUT /api/v1/refs/claims/coalesc-probe` with body `{"cluster":"dev","task_id":"probe","expected_sha":""}` | AC8 |
| (h) | — | `POST tasks/x.md` = `A` ×3, 1 s apart; `sleep 40` | AC9 |
| (i) | — | `POST tasks/x.md` = `I`; `DELETE tasks/x.md` 1 s later; `sleep 40` | AC11 |
| (j) | `--pull-interval=10m --write-coalesce-delay=5s` | `POST tasks/x.md` = `J`; `sleep 12` | AC13 |
| (k) | `--pull-interval=10m --write-coalesce-delay=300s --write-coalesce-max-delay=20s` | `POST tasks/x.md` = `k1`, `k2`, … (**distinct** content) every 2 s for 60 s; `sleep 30` | AC13 |

Recipe (a) rerun with `--write-coalesce=false` is the AC2 comparator; recipe (c) rerun the same way is AC4's comparator.

**Recipe (c)'s slow remote — inline, because the hook is load-bearing.** Do not use the shared preamble for (c); use this full block, so the hook cannot be missed:

```bash
BASE=/tmp/git-rest-c && rm -rf "$BASE" && mkdir -p "$BASE" && cd "$BASE"
git init -q --bare -b test/repro origin.git
printf '#!/bin/sh\nsleep 0.5\n' > "$BASE/origin.git/hooks/pre-receive"
chmod +x "$BASE/origin.git/hooks/pre-receive"
git clone -q origin.git seed && cd seed
git config user.email repro@local && git config user.name repro
mkdir -p tasks && printf 'v0\n' > tasks/x.md
git add -A && git commit -q -m init && git push -q origin test/repro
cd "$BASE" && git clone -q origin.git work && cd work
git config user.email repro@local && git config user.name repro
/tmp/git-rest-coalesce-bin --listen=:18447 --repo="$BASE/work" --pull-interval=10s \
  > "$BASE/run.log" 2>&1 & PID=$!
sleep 3
# 200 sequential POSTs to tasks/x.md, timing each client-side; print the p50.
```

Every push then costs ≥ 500 ms. The synchronous comparator lands at ~720 ms p50 (clearing AC4's ≥ 500 ms floor) and the coalesced path stays in the low tens of milliseconds, because its push happens on the flusher's tick rather than inside the request. That separation *is* the property under test. The 200-write comparator run takes ~2.5 min — expected, not a hang.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — format, generate, test and lint clean
- `make test` — unit + integration suite passes
- `go test ./pkg/git/... -run 'Coalesc'` — a table test that three staged writes inside the window yield one commit whose tree carries the last content, and that with coalescing disabled the same three yield three
- `go test ./pkg/git/... -run 'FlushBeforePull'` — a repo with a pending write is clean after `Pull`, asserted on behaviour rather than on a symbol name
- `go test ./pkg/git/... -run 'FlushTimerFires'` — with a long pull interval, a pending write is flushed by the timer alone
- `go test ./pkg/git/... -run 'FlushPushFailureKeepsPending'` — a failed push leaves the pending set intact and the next flush retries it
- `go test ./pkg/git/... -run 'DeleteFlushesPendingFirst'` — a delete of a path that is also pending orders correctly
- `go test ./pkg/git/... -run 'UnchangedWriteStagesNothing'` — a second write of identical bytes stages nothing, logs the unchanged message, and produces no commit
- `go test ./pkg/git/... -run 'RecoverPendingWrites'` — a staged path left by a crash is committed and pushed on boot

### Operator-executable (runs on the host after PR merge, per `docs/verifying-specs.md`)

- **Rung 1**: build a fresh binary and replay recipes (a)–(k). Run from the branch; the ACs read only the recipe's own `$BASE`.
- **Rung 2**: publish the release image, bump `VERSION` in `~/Documents/workspaces/nuke/git-rest/Makefile` (the pin, per `docs/deployment.md:152`), then in `~/Documents/workspaces/nuke-dev` run `git fetch && git merge --ff-only origin/master`, then `cd git-rest && BRANCH=dev make apply`. Assert AC15. `deploy_target` resolves `git rev-parse --short origin/master`, so the check is worktree-independent; a branch HEAD would otherwise compare a feature SHA against the released image's baked commit and read as a stale deploy. **The `VERSION` bump lives in `bborbe/nuke`, a different repo — it is an operator step and must never appear in a generated prompt for this spec.**
- **Rung 3** is out of scope — promoting to prod and verifying the prod symptom is a separate task.

## Desired Behavior

1. **A write returns once the content is on disk and staged.** No commit and no push happens inside the request. A write whose bytes match the file already on disk stages nothing, logs the existing unchanged message, and produces no commit.
2. **A flusher turns a burst into one commit and one push.** It fires when the pending set has been quiet for `--write-coalesce-delay` (default `30s`) or when the oldest pending write is `--write-coalesce-max-delay` old (default `120s`), whichever comes first. Under no contention the max-delay is the staleness ceiling; under contention the worst case is the max-delay plus the longest single git-operation hold (a pull bounded by `--pull-timeout`, default 60 s), because the flush and the pull serialise.
3. **The commit message keeps the `git-rest:` prefix.** A batch of exactly one path uses today's message verbatim — `git-rest: create <path>` or `git-rest: update <path>`. A batch of more than one path uses `git-rest: update <n> files`. Boot recovery follows the same rule — one recovered path uses the single-path form, more than one uses `git-rest: update <n> files`. The prefix is load-bearing: the rate measurement greps `^git-rest:`.
4. **Every pull cycle starts clean.** A pull flushes pending writes before it resolves its upstream or attempts a merge, so it never merges against a tree this process dirtied.
5. **A pending write survives a restart.** On `SIGTERM` the process flushes and pushes before exiting, bounded by a **10 s flush deadline**; when the deadline cuts the flush short, one INFO line beginning `git-rest: flush deadline exceeded` records it. On boot, if the index holds staged paths, they are committed and pushed before the HTTP server starts listening — the crash case, where the write reached the PVC but no flush ran — and that recovery logs one INFO line beginning `git-rest: recovered pending writes` naming the path count.
6. **Claim refs and deletes keep their synchronous semantics.** Claim-ref PUT/DELETE never enter the buffer; their 200 means the ref is on the origin. `DeleteFile` flushes pending writes first, then commits and pushes the deletion synchronously.
7. **The behaviour is observable and reversible.** Three metrics are exposed and pre-initialised: `git_rest_write_flushes_total{outcome}` (`ok` / `error`), `git_rest_write_coalesced_total`, and `git_rest_write_pending`. **`git_rest_write_coalesced_total` counts writes absorbed into a batch** — a three-write burst landing as one commit increments it by **3**, not by the 2 writes it merged away; `git_rest_write_pending` is the current pending-path count and returns to `0` after a successful flush. Each flush logs one INFO line beginning `git-rest: flushed pending writes` naming the path count and the push outcome. `--write-coalesce=false` restores today's synchronous write-commit-push on every request.

## Constraints

- **The buffer is scoped to the file-write path only.** `/api/v1/refs` PUT/DELETE under `refs/claims/*` must never enter it: a deferred claim push would break exactly-once claim scheduling, and `ErrRefConflict` → 409 (`pkg/handler/refs_put.go:48-50`) must mean the ref push really conflicted.
- **The unchanged-content short-circuit compares the new bytes to the file on disk, not the index to `HEAD`.** An index-vs-HEAD comparison reports "changed" for every write after the first in a burst, because the index still holds the earlier pending write — that silently loses the no-op property that keeps v0.16.0's write reduction effective. The existing log message `write file: no changes to commit (content unchanged)` (`pkg/git/git.go:750`) is kept.
- **A failed push must never drop the batch.** The pending set is cleared only after the push returns nil. A push or commit error leaves the set intact and the next tick retries.
- **The dirty-tree window is closed at every pull boundary.** DB4 is the mechanism; a design that lets the puller merge against a pending write fails AC5 by producing a rescue.
- **The timer is not optional and is not the pull boundary.** A pull-only flush passes every rate and durability criterion while leaving staleness tied to `--pull-interval`; DB2's timer is what bounds it independently. AC13 exists to catch exactly that build.
- **Flags follow the repo's `arg:` / `env:` convention and its sentinel convention.** `--write-coalesce` (bool, default `true`, `WRITE_COALESCE`), `--write-coalesce-delay` (`WRITE_COALESCE_DELAY`, default `30s`), `--write-coalesce-max-delay` (`WRITE_COALESCE_MAX_DELAY`, default `120s`). Disabling is a boolean, **not** a zero duration — `--pull-timeout` documents `0 = no timeout` (`main.go:46`), so a zero sentinel would read as "no ceiling" and silently invert the intent.
- **Preserve the repo's invariants.** Errors wrapped with `github.com/bborbe/errors` (never `fmt.Errorf`), metrics under the `git_rest_` prefix and pre-initialised in `init()`, no `context.Background()` in `pkg/`, `log/slog` for logging, timestamps from the injected `libtime` getter, factory functions as pure composition, interface → constructor → struct → method.
- **`docs/api.md` and `docs/deployment.md` must be updated in the same change.** `docs/api.md:62` and `:75` describe a synchronous commit+push and a `200` meaning "File written + committed"; both must state the coalescing window and that `200` means written and staged. `docs/deployment.md` gains the three flags, the three metrics, the boot-recovery step, the 10 s shutdown deadline, and the 2026-10-21 rate baseline (12,490 commits / 24 h) under Monitoring.
- **CHANGELOG entry under `## Unreleased`**, which does not exist yet and must be created.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection | Reversibility | Concurrency |
|---|---|---|---|---|---|
| Push rejected during flush | Commit stays local, pending set intact, error logged; next tick retries | None needed — the existing push retry already merges and retries | `git_rest_write_flushes_total{outcome="error"}` increments | Reversible — the commit is local until a push succeeds | The flush holds the package mutex, so no write is lost or reordered |
| Commit fails (index lock, disk full) | Pending set intact, error returned from the flush, writes continue to stage | Operator clears the lock or frees space; the next flush commits the accumulated paths | Error log naming the path count | Reversible — nothing left the host | The next flush serialises behind the failing one |
| Pod killed between write and flush | File on the PVC, staged in the index; boot recovery commits and pushes it | Automatic on boot | `git-rest: recovered pending writes` INFO | Reversible | Single replica — no second writer can flush the same index |
| Pod killed mid-push | Commit exists locally, remote does not have it; boot recovery pushes it | Automatic on boot | Boot push succeeds; `outcome="ok"` after restart | Reversible — the remote never saw the half-push | — |
| Continuous write stream never goes quiet | The max-delay ceiling forces a flush at 120 s | None needed | Flush interval in the log timestamps never exceeds the max-delay plus the longest git-operation hold | Reversible | The trailing timer resets per write; the ceiling is measured from the oldest pending write |
| Pull arrives with writes pending | Flush runs first, then the merge; tree is clean | None needed | `git-rest: flushed pending writes` immediately preceding the pull log line | Reversible | Both serialise on the package mutex |
| Flush exceeds its 10 s deadline on SIGTERM | Process exits anyway; the staged paths stay on disk | Boot recovery commits and pushes them on the next start | Shutdown log line naming the deadline | Reversible | — |
| Clock skew | Timers use monotonic time; a skewed wall clock changes no decision | None | — | Reversible | — |
| Coalescing disabled by flag | Every write commits and pushes synchronously, as today | None needed | Commit count returns to baseline | Reversible | — |
| Disk exhaustion while staging | Staging fails; the write returns 500 and nothing is pending | Operator frees space | 500 on the write | Reversible | — |

## Security / Abuse Cases

The write path already validates paths against traversal (`..`, absolute, empty) before the write executes, and this change adds no new input surface. The new surface is timing: a write returns 200 before its content is on the origin. That is bounded by the max-delay ceiling and by the pull-boundary flush, both observable, and the content is durable on disk at the moment 200 is returned — the guarantee weakens from "committed and pushed" to "written and staged", never to "accepted in memory". A client needing the stronger guarantee is the claim-ref path, deliberately excluded. The pending set is keyed on paths the API already accepted, so a flush cannot be induced to stage a file the API would reject.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Core coalescing: stage-not-commit in the write path, the on-disk no-op short-circuit, the pending set, the flusher timer with trailing + max-delay, the pull-boundary flush, the three flags, the three metrics, the flush log line | 1, 2, 3, 4, 7 | 1, 2, 3, 4, 5, 9, 10, 13 | — |
| 2 | Durability and ordering: SIGTERM flush with its 10 s deadline, boot recovery of staged paths, `DeleteFile` flush-first, claim-ref exclusion test, failed-push retry | 5, 6 | 6, 7, 8, 11, 14 | prompt 1 (uses the pending set and the flush) |
| 3 | Docs and release hygiene: `docs/api.md` write contract (both stale lines removed, `written and staged` stated), `docs/deployment.md` flags + metrics + boot recovery + shutdown deadline + rate baseline, CHANGELOG | — (constraint-derived) | 12 | prompts 1, 2 |
| — (operator) | Rung-2 verification on dev — no prompt delivers this | — | 15 | prompts 1–3 merged and released |

AC9 sits in prompt 1, not prompt 2: "unchanged content stages nothing" is DB1's behaviour and belongs with the write path that implements it. AC12 is constraint-derived — no Desired Behavior describes documentation — so prompt 3 carries no DB column entry.

**Deliberate non-split.** DB×AC is 7×15 = 105, above the guide's 50 threshold. The work is deliberately kept as one spec: it is a single mechanism whose parts are not independently shippable — durability has no meaning without the buffer, and the docs describe both — and the task it serves requires the flush and the debounce to land as separate *commits*, which the three-row decomposition already delivers without a second approval cycle. The threshold guards prompt-generator overload; the decomposition above removes that risk by fixing the split in advance. **`ls prompts/` must list three files before any is approved** — a short batch is not a retry state, and the spec reads `prompted` even when the generator under-produced.

## Do-Nothing Option

The rate keeps climbing — 5,464 commits in the 24 h before the measured window, 12,490 inside it. Every additional writer that cannot keep up either loses `cannot lock ref` continuously or gets reverted, as one did within a day of deploying. Meanwhile every intake write pays 2.7 s of push latency inside the request, and commands keep expiring against a 60-minute deadline that was sized assuming a fast round trip. The vault of record stays a store that no writer with a slow uplink can reliably push to.
