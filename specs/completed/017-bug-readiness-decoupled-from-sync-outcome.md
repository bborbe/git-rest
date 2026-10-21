---
status: completed
approved: "2026-10-21T15:40:05Z"
generating: "2026-10-21T15:43:34Z"
prompted: "2026-10-21T15:51:56Z"
verifying: "2026-10-21T15:58:41Z"
completed: "2026-10-21T16:14:26Z"
branch: dark-factory/bug-readiness-decoupled-from-sync-outcome
---

## Summary

- `/readiness` is derived from the last **pull** outcome, and a rejected **push** is recorded as a pull failure — so a push race flips the pod NotReady.
- On nuke prod, `vault-obsidian-openclaw-0` sat `0/1` for 31h with **0 restarts**: the process was healthy and its clone was current; only the push lost a race against another writer.
- The Service carried no endpoint, so every in-cluster reader was cut off — cluster-assigner hit `connection refused` for a full working day.
- The pod flaps rather than recovering: `Ready` at `14:01:48Z`, `Ready=False` again at `14:17:58Z`; ~84% of push attempts rejected in a 25-minute window.
- Fix shape: readiness must answer "can this pod serve?", not "did the last sync win?" — sync failures stay visible through the existing error metric and alert.

## Problem

git-rest exists to serve a vault over HTTP and to keep that vault's clone in sync with its remote. Those are two capabilities with independent failure modes, but `/readiness` reports only the second. When two git-rest instances push to the same branch — the standing topology for the OpenClaw vault, one pod per cluster — one of them loses every race, its `git push` is rejected, the puller records that rejection as a pull failure, and within 90 seconds the pod is removed from its Service. Reads, which never touch the remote, worked the entire time. The outage is therefore self-inflicted: a pod that could serve traffic is deliberately taken out of rotation because it could not write.

## Goal

`/readiness` answers 200 whenever the process can serve its API — the HTTP server is listening and the served repository path is readable — regardless of whether the most recent sync attempt succeeded. A rejected push, a failed fetch, or a merge conflict no longer removes the pod from its Service. Sync failures remain visible through the puller's warning logs and through `git_rest_git_operation_errors_total{operation=...}`, which `helm/templates/alerts.yaml:16` already alerts on.

## Assumptions

- Bootstrap aborts startup on failure (`main.go:65`), so a process that is serving HTTP has a cloned or initialised repo on disk.
- Only the kubelet consumes `/readiness` (`helm/templates/statefulset.yaml:99-114`); no alert expression reads it.
- `/readiness` is unauthenticated by design (`main.go:479`), so its body must not disclose host paths.
- The repo is served from a single replica per vault; multi-writer behaviour at `replicas > 1` stays undefined, as spec 006 established.

## Reproduction

Deterministic, rung 1: a locally built binary against a temp repo whose remote rejects every push. No SSH, no network, no cluster.

```bash
set -e
# Build the tree that holds the fix — never the main checkout, which replays
# the unfixed binary and fails every assertion falsely.
REPO_UNDER_TEST="${REPO_UNDER_TEST:-$(git rev-parse --show-toplevel)}"
BASE=/tmp/git-rest-pushfail-repro && rm -rf "$BASE" && mkdir -p "$BASE" && cd "$BASE"

# Bare remote built by clone, so no push is needed to seed it.
git init -q -b main seed && cd seed
git config user.email repro@local && git config user.name repro
printf 'line one\n' > note.md
git add -A && git commit -q -m init
cd "$BASE"
git clone -q --bare seed origin.git

# Reject every push. core.hooksPath is required: a global setting would
# otherwise make git ignore the per-repo hooks directory and the hook no-op.
mkdir -p origin.git/hooks
printf '#!/bin/sh\nexit 1\n' > origin.git/hooks/pre-receive
chmod +x origin.git/hooks/pre-receive
git -C origin.git config core.hooksPath hooks

git clone -q origin.git work && cd work
git config user.email repro@local && git config user.name repro

# Boot against the clean, level clone, then make the served clone locally ahead
# so the puller attempts a push on its next tick.
cd "${REPO_UNDER_TEST}" && go build -o /tmp/git-rest-pushfail-bin .
/tmp/git-rest-pushfail-bin -listen=:18449 -repo="$BASE/work" -pull-interval=3s -v=1 > "$BASE/run.log" 2>&1 &
sleep 3
cd "$BASE/work" && git commit -q --allow-empty -m "local ahead of remote"
sleep 15

curl -s -w '\nHTTP %{http_code}\n' http://localhost:18449/readiness
curl -s -o /dev/null -w 'GET note.md HTTP %{http_code}\n' http://localhost:18449/api/v1/files/note.md

# The process is left RUNNING: AC1, AC2, AC3 and AC7 read it. Teardown is step 7,
# a separate snippet below, and must not be part of this block.
```

**Step 7 — teardown** (run after AC1, AC2, AC3 and AC7 have been read):

```bash
kill "$(pgrep -f /tmp/git-rest-pushfail-bin)" 2>/dev/null || true
rm -rf /tmp/git-rest-pushfail-bin
```

**Observed on v0.30.1 (2026-10-21), verbatim:**

```
no successful pull yet
HTTP 503
GET note.md HTTP 200
```

```
I1021 15:21:04.094603 puller.go:74] WARN git pull failed error="exit status 1
git [push]: To /tmp/git-rest-pushfail-repro/origin.git
 ! [remote rejected] main -> main (pre-receive hook declined)
...
push failed
```

Readiness is 503 while the read API answers 200 on the same process, one second apart.

**Production form of the same shape** (nuke prod, `vault-obsidian-openclaw-0`):

- Pod `0/1 Running`, **0 restarts**, from pod start `2026-10-20T06:58:51Z` until `Ready` at `2026-10-21T14:01:48Z` — 31 hours.
- `kubectlnukeprod -n prod get endpoints vault-obsidian-openclaw` returned **no endpoints**; cluster-assigner v0.1.1 failed with `connection refused` at `vault-obsidian-openclaw:9090`.
- Prod logged **25 `merged diverged history` against 21 `git pull failed`** in a 25-minute window (~84% of push attempts rejected, latest `14:24:06Z`).
- The `14:01:48Z` recovery was a single push that won the race; `Ready=False` returned at `14:17:58Z`.

## Expected vs Actual

**Expected** — readiness reports whether the pod can serve. `docs/api.md:118` says the probe exists so a pod leaves rotation when it cannot handle traffic. A pod whose clone is current, whose HTTP server is up, and whose read path answers 200 in microseconds can handle traffic.

**Actual** — readiness reports whether the last sync attempt succeeded. A push-only failure — a lost race on a remote the pod does not control — removes a fully serving pod from its Service, and holds it out for as long as the race is lost.

## Why this is a bug

Three reasons, in increasing severity:

1. **Wrong signal.** The probe answers a question nobody asked. Operators reading a NotReady pod conclude the process is broken; the process is fine, and the correct response (fix the writer race) is invisible from the probe.
2. **The remedy is worse than the disease.** A failing push costs *some* writes. Leaving the Service costs *all* reads, for every consumer. The current behaviour converts a partial degradation into a total outage.
3. **It is unbounded.** The failure is sticky for as long as the race persists — 31 hours in prod — and it self-reinforces: the loser never stops trying, so it never stops failing.

## Root Cause (triaged)

Pinned to `bborbe/git-rest@d20a4ae` (v0.30.1). Function names are stable; line numbers drift.

- `pkg/git/git.go` `syncWithUpstream` — the `remoteSHA == baseSHA` branch calls `git push` and returns `errors.Wrap(ctx, err, "push failed")` on rejection. `pullMergeAndPush` does the same after a clean merge (`push after clean merge failed`).
- `pkg/puller/puller.go` `runOnce` — the returned error goes to `p.state.RecordPull(err)` with no distinction between a read-side failure and a write-side one.
- `pkg/puller/pull_state.go` `ReadinessStatus` — 503 when `lastConflict` is set, when `lastSuccessAt` is zero, or when `now - lastSuccessAt > freshnessThreshold`.
- `main.go` — `freshnessThreshold` is `3 × PullInterval` (default `30s` → 90s).

Net: a push rejection is indistinguishable from a pull failure at every layer above `pkg/git`, and readiness is a pure function of pull success. Nothing in the read path is consulted.

## Desired Behavior

1. `/readiness` returns **200** when the process is serving and the served repo path is readable — independent of the last sync outcome.
2. `/readiness` returns **503** only when the repo path is missing or unreadable.
3. A push rejection, a failed fetch, or a merge conflict leaves `/readiness` at 200 and leaves `GET /api/v1/files/...` working.
4. The 503 body is a short reason that names the failure without disclosing a host path (`repo path unreadable`) and never a sync error such as `push failed` or `no successful pull yet`.
5. The sync outcome stays observable: `pkg/git` continues to increment `git_rest_git_operation_errors_total{operation="push"|"pull"|"fetch"|"rebase"}` and the puller continues to log `git pull failed`.
6. Cold start is ready: a process whose HTTP server is listening and whose repo path is readable answers 200 even before its first pull tick completes.
7. `/healthz` behaviour is unchanged: 200 while the process is alive.

## Constraints

- MUST NOT change the `/readiness` URL or its 200/503 status-code contract (`docs/api.md:20,118` keep their shape; their prose must be corrected).
- MUST preserve spec 005's latency guarantee: the handler never invokes a git subprocess and never blocks on the pull mutex. A repo-path readability check is a `stat`, not a git call.
- MUST NOT weaken liveness. `/healthz` keeps its current semantics and implementation.
- MUST NOT change write-path semantics: a failed push still returns an error to the API caller. This spec changes what readiness *reports*, not what a write *does*.
- MUST keep the sync signal intact: the existing metric series and log lines are unchanged, so `helm/templates/alerts.yaml:16` (`VaultObsidian*PullFailing`) and `:33` (`VaultObsidian*RebaseConflict`) continue to fire. No new alert and no new metric are introduced here.
- **Pull-state retention is a judgement call for the implementation, bounded by the signal constraint.** If `PullStateCache` retains no consumer once readiness stops reading it, removing it is acceptable provided DB5's metric and log signal is untouched. It MUST NOT be kept on the promise of a future endpoint.
- **Supersedes spec 005's readiness contract, not only its cold-start clause.** `005-bug-readiness-blocks-on-pull-mutex.md` defines readiness as a function of pull success. Superseded there: **DB2** ("200 iff a successful pull occurred within the freshness threshold"), **DB3** (503 body names the last pull error or `no successful pull yet`), **DB5** (the freshness threshold as a readiness input), **AC2** and **AC4(c)** (the cached last-pull error surfaced in the 503 body), and **AC4(d)** (cold-start 503 with `no successful pull yet`; 005's DB6 states the same rule, and this spec's DB6 replaces both with 200). An implementer preserving 005's freshness behaviour would build the wrong handler, so this list is the constraint, not just AC4(d).
- **Existing assertions the change invalidates.** Rewrite them; do not delete them while `PullStateCache` survives:
  - `pkg/puller/pull_state_test.go` — `Describe("cold start (no pull yet)")` (~line 34), `Describe("after a failed pull (first ever)")` (~line 53), and **`Describe("rebase conflict (immediate 503)")` (~lines 99-195), which holds 9 of the file's 15 assertions and asserts exactly the behaviour DB3 reverses** — the largest invalidated block. The file carries 15 `ReadinessStatus` assertions in total; if the cache is removed under the retention clause below, the file goes with it and that is not a deletion of coverage — the behaviour it asserted no longer exists.
  - `pkg/handler/readiness_test.go:66` — `It("body contains 'no successful pull yet'")`, and the whole `Context("when ReadinessStatus returns not ready: stale cache")` block at ~lines 79-105, which asserts the old 503 body contract.
  - `export_test.go:45` and `main.go:464` — both call `factory.CreateReadinessHandler(pullState)`; the signature changes with the dependency, so both call sites move together.
- MUST NOT regress spec 006's conflict handling: a rebase or merge conflict still leaves the repo in its conflicted state and still returns an error from `Pull`; only its effect on readiness changes.
- **Doc scope is four files, not one.** Each asserts the old semantics and survives the change unless corrected:
  - `docs/api.md:20` — "checks git status + pending pushes"
  - `docs/api.md:118` — "a pod flips unready … if a push is stuck"
  - `docs/api.md:112-116` — the positive contract block: "Returns `200 OK` … if: `git status` succeeds / Working tree is clean (no uncommitted changes) / No local commits pending push". This is the largest stale claim and is not covered by the two line citations above; the whole block is rewritten to the DB1/DB2/DB4 contract.
  - `README.md:33` — "readiness reflects git status"
  - `docs/deployment.md:134` — "removes the pod from the Service while git operations are in-flight or pushes are pending"
  - `docs/deployment.md:184` — "readiness reports 503 with the conflict path (`last pull failed: rebase conflict at <path>`)" — contradicts DB3 (a conflict stays ready) and DB4 (no sync error in the body)
  - `docs/deployment.md:196` — "Readiness returns to ready within one pull interval after the push succeeds" — false after the change; readiness never needed the push
  - `helm/templates/alerts.yaml:31` — the rebase-conflict alert description both tells an operator to "check `/readiness` for conflict path" and claims "readiness 503". Both go: point the operator at the pod logs, and drop the readiness claim.
- **Known collateral, deliberately not edited here.** `docs/verifying-specs.md:168,307` use "`/readiness` answers non-200" as a diagnostic for the modify/delete and drain recipes. Those replays stop discriminating once readiness no longer tracks sync. That is a verification-doc correction, not part of this fix; note it in the PR body so it is not mistaken for a regression.

## Failure Modes

| Trigger | Expected behavior | Recovery | Concurrency |
|---------|-------------------|----------|-------------|
| Push rejected by the remote (two writers, one branch) | `/readiness` stays 200; reads served; `git_rest_git_operation_errors_total{operation="push"}` increments; `git pull failed` logged | Next tick retries; `VaultObsidian*PullFailing` (alerts.yaml:16) fires on sustained rejection | Two pods, one branch: the loser retries every tick and never succeeds until the race clears |
| Fetch fails (remote unreachable, auth broken) | `/readiness` stays 200; reads served from the local clone; `operation="fetch"` increments | Next tick retries; `VaultObsidian*PullFailing` fires | n/a |
| Merge/rebase conflict | Repo left conflicted; `Pull` returns `RebaseConflictError`; `/readiness` stays 200; reads of non-conflicted paths still work | Operator resolves; `VaultObsidian*RebaseConflict` (alerts.yaml:33) fires | n/a |
| Repo path deleted or unreadable at runtime | `/readiness` returns 503 with `repo path unreadable` | Operator restores the volume; pod restarts into bootstrap | n/a |
| Process shutting down | `/readiness` keeps answering 200 until the listener closes; Kubernetes removes the endpoint when the pod enters Terminating | Restart proceeds; no readiness behaviour depends on shutdown | n/a |

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Serving verdict: repo-path readability replaces the pull-outcome verdict, with the cold-start and sync-error unit specs rewritten | 1, 2, 4, 6 | 2, 4 | — |
| 2 | Handler, `ReadinessStateReader` interface, factory and `main.go` wiring; integration coverage of the 200-while-pushing and 503-while-unreadable cases | 1, 3, 5, 7 | 1, 3, 7 | prompt 1 |
| 3 | Prose correction across `docs/api.md`, `README.md`, `docs/deployment.md`, `helm/templates/alerts.yaml` | — | 6 | — |

Prompts 1 and 3 are independent and may be approved together; prompt 2 needs 1. Verification (rungs 1–3) is the operator ladder, not a prompt.

## Acceptance Criteria

- [ ] **AC1:** After running the `## Reproduction` recipe (which defines `$BASE` and port 18449), `/readiness` returns `200` while `GET /api/v1/files/note.md` returns `200`, and `/healthz` still returns `200` (DB7). Evidence: `curl -s -o /dev/null -w '%{http_code}' http://localhost:18449/readiness` prints `200`, the same for the file URL prints `200`, and `curl -s -o /dev/null -w '%{http_code}' http://localhost:18449/healthz` prints `200` — all read after ≥3 pull ticks.
- [ ] **AC2:** After the `## Reproduction` recipe, removing the served repo path at runtime makes `/readiness` return `503` with a body containing `repo path`. Evidence: `rm -rf "${BASE:?}/work"` while the process runs (`:?` refuses to expand in a shell where `BASE` is unset, instead of deleting `/work`), then `curl -s -w '\n%{http_code}' http://localhost:18449/readiness` prints a body containing `repo path` and status `503`. This is the discriminator that rules out a hardcoded `200`.
- [ ] **AC3:** No `/readiness` body, in either state, contains a sync-failure substring or a host path. Evidence: in the AC1 run, `curl -s http://localhost:18449/readiness | grep -Eic 'push|pull|sync|conflict|/tmp/'` prints `0`; in the AC2 run, the same grep against the 503 body prints `0`.
- [ ] **AC4:** Unit tests cover the verdict: (a) a repo whose path is readable reports ready even when the puller returned a sync error, (b) an unreadable repo path reports not-ready, (c) the verdict is unchanged when the puller returned a `RebaseConflictError` (DB3, and the case a hybrid implementation that keeps the conflict branch would fail), (d) the superseded cold-start and stale-cache specs are rewritten to assert the new contract. Evidence: `go test -v ./pkg/puller/... ./pkg/handler/... -args -ginkgo.v` exits 0 and its output names those four specs.
- [ ] **AC5:** `make precommit` exits 0.
- [ ] **AC6:** No artifact still claims readiness depends on sync state, **and** the corrected contract is present. Evidence, both required:
  - Negative: `grep -Eic 'pending pushes|push is stuck|readiness reflects git status|pushes are pending|check /readiness for conflict path|git status. succeeds|Working tree is clean|local commits pending push|readiness reports 503 with the conflict path|readiness 503, requires human resolution|returns to ready within one pull interval|flips unready|git operations are in-flight' docs/api.md README.md docs/deployment.md helm/templates/alerts.yaml` prints `0` for every file.
  - Positive: `grep -c '^## Readiness' docs/api.md` prints `1` **and** `sed -n '/^## Readiness/,/^## /p' docs/api.md | grep -Eic 'readable'` prints `≥1` — the block survives and now states the repo-readability contract. (The bare `grep 'readable'` form was satisfiable by a stray word elsewhere in the file.)
- [ ] **AC7:** The sync signal is unchanged. Evidence: in the AC1 run, `curl -s http://localhost:18449/metrics | grep -E '^git_rest_git_operation_errors_total\{[^}]*operation="push"'` prints a value `> 0`, and `$BASE/run.log` contains ≥1 line matching `git pull failed`.
- [ ] **Post-Deploy (Rung-2):** (AC8) on dev, a pod serving a repo whose pushes are rejected stays `1/1` and Ready, while its logs keep logging the rejection. **Inducement:** the rejection must be produced and observed, not assumed — either a second writer pushes to the same branch, or the remote carries a `pre-receive` hook that declines (rung-1 recipe shape). On a clean single-writer pod with no rejection the log grep prints `0` and this AC is **inconclusive, not passing**; without an observed rejection it cannot discriminate the fix from the bug at all. Evidence: `kubectlnukedev -n dev get pod vault-obsidian-openclaw-0` prints `1/1` with `Ready=True`, read twice ≥10 minutes apart (two reads, not one continuous claim), and `kubectlnukedev -n dev logs vault-obsidian-openclaw-0 --since=10m | grep -c 'git pull failed'` prints ≥1.
  - `deploy_check:` `kubectlnukedev -n dev get --raw "/api/v1/namespaces/dev/pods/vault-obsidian-openclaw-0:9090/proxy/metrics" | grep -m1 '^git_rest_build_info' | sed -E 's/.*commit="([^"]+)".*/\1/' | grep -E '^[0-9a-f]{7,40}$'`
  - `deploy_target:` `$(git rev-parse --short "$(git describe --tags --abbrev=0 origin/master)")`
- [ ] **Post-Deploy (Rung-3):** (AC9) on nuke prod, `vault-obsidian-openclaw-0` reads `1/1` with a ready Service endpoint, read twice ≥10 minutes apart, while push rejections continue in the logs. **Inducement:** as AC8 — a rejection must be observed in the window, not assumed; if none occurs the AC is inconclusive. Evidence: `kubectlnukeprod -n prod get pod vault-obsidian-openclaw-0` prints `1/1` on both reads; `kubectlnukeprod -n prod get endpoints vault-obsidian-openclaw` lists a non-empty `ENDPOINTS` column on both; and `kubectlnukeprod -n prod logs vault-obsidian-openclaw-0 --since=10m | grep -c 'git pull failed'` prints ≥1 — the observation that makes the inducement checkable rather than asserted.
  - `deploy_check:` `kubectlnukeprod -n prod get --raw "/api/v1/namespaces/prod/pods/vault-obsidian-openclaw-0:9090/proxy/metrics" | grep -m1 '^git_rest_build_info' | sed -E 's/.*commit="([^"]+)".*/\1/' | grep -E '^[0-9a-f]{7,40}$'`
  - `deploy_target:` `$(git rev-parse --short "$(git describe --tags --abbrev=0 origin/master)")`

## Verification

### Container-executable (YOLO container at prompt time)

- `make precommit` — format, generate, test, lint
- `go test -v ./pkg/puller/... ./pkg/handler/... -args -ginkgo.v` — the rewritten verdict specs
- The AC6 negative grep, verbatim, across all four files — prints `0` per file. The AC6 positive checks (`## Readiness` survives and mentions `readable`) run in the same pass.

### Operator-executable (host, after PR merge)

- Rung 1: the `## Reproduction` recipe against a binary built from the worktree — `/readiness` `200`, read path `200`, plus the AC2 negative (`rm -rf` the repo → `503 repo path unreadable`).
- Rung 2: git-rest's cluster manifests live in the nuke repo, and `make buca` in this repo only builds and uploads — its `apply` target iterates subdirectory Makefiles and git-rest has none. So the dev deploy is: bump the `VERSION` pin in `~/Documents/workspaces/nuke/git-rest/Makefile`, then `cd ~/Documents/workspaces/nuke && git pull && cd git-rest && BRANCH=dev make apply`, then `kubectlnukedev -n dev rollout restart statefulset/vault-obsidian-openclaw`. Then AC8.
- Rung 3: the same apply with `BRANCH=master` for prod, then AC9.

## Open Questions

None — the readiness verdict, its negative control, the observability contract, and the superseded spec-005 behaviour are all fixed above.
