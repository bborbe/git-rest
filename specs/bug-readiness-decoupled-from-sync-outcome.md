---
status: draft
kind: bug
created: "2026-10-21T15:21:18Z"
---

## Summary

- `/readiness` is derived from the last **pull** outcome, and a rejected **push** is recorded as a pull failure — so a push race flips the pod NotReady.
- On nuke prod, `vault-obsidian-openclaw-0` sat `0/1` for 31h with **0 restarts**: the process was healthy and its clone was current; only the push lost a race against another writer.
- The Service carried no endpoint, so every in-cluster reader was cut off — cluster-assigner hit `connection refused` for a full working day.
- The pod flaps rather than recovering: `Ready` at `14:01:48Z`, `Ready=False` again at `14:17:58Z`; ~84% of push attempts rejected in a 25-minute window.
- Fix shape: readiness must answer "can this pod serve?", not "did the last sync win?" — sync failures stay visible through the existing error metric and alert.

## Problem

git-rest exists to serve a vault over HTTP and to keep that vault's clone in sync with its remote. Those are two capabilities with independent failure modes, but `/readiness` reports only the second. When two git-rest instances push to the same branch — which is the standing topology for the OpenClaw vault, one pod per cluster — one of them loses every race, its `git push` is rejected, the puller records that rejection as a pull failure, and within 90 seconds the pod is removed from its Service. Reads, which never touch the remote, worked the entire time. The outage is therefore self-inflicted: a pod that could serve traffic is deliberately taken out of rotation because it could not write.

## Goal

`/readiness` answers 200 whenever the process can serve its API — the HTTP server is listening, the puller loop is running, and the served repository path is readable — regardless of whether the most recent sync attempt succeeded. A rejected push, a failed fetch, or a merge conflict no longer removes the pod from its Service. Sync failures remain visible through the puller's warning logs and through `git_rest_git_operation_errors_total{operation=...}`, which `helm/templates/alerts.yaml:16` already alerts on.

## Reproduction

Deterministic, rung 1: a locally built binary against a temp repo whose remote rejects every push. No SSH, no network, no cluster.

```bash
set -e
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
cd ~/Documents/workspaces/git-rest && go build -o /tmp/git-rest-pushfail-bin .
/tmp/git-rest-pushfail-bin -listen=:18447 -repo="$BASE/work" -pull-interval=3s -v=1 > "$BASE/run.log" 2>&1 &
sleep 3
cd "$BASE/work" && git commit -q --allow-empty -m "local ahead of remote"
sleep 15

curl -s -w '\nHTTP %{http_code}\n' http://localhost:18447/readiness
curl -s -o /dev/null -w 'GET note.md HTTP %{http_code}\n' http://localhost:18447/api/v1/files/note.md

kill "$(pgrep -f /tmp/git-rest-pushfail-bin)" 2>/dev/null || true
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

1. `/readiness` returns **200** when the process is serving, the puller loop is running, and the served repo path is readable — independent of the last sync outcome.
2. `/readiness` returns **503** only when the service cannot serve at all: the puller loop has not started, the repo path is missing or unreadable, or the process is shutting down.
3. A push rejection, a failed fetch, or a merge conflict leaves `/readiness` at 200 and leaves `GET /api/v1/files/...` working.
4. A 503 body names the concrete serving failure (`repo path unreadable: <path>`, `puller not started`) and never a sync error such as `push failed` or `no successful pull yet`.
5. The sync outcome stays observable: `RecordPull` continues to record every attempt, `pkg/git` continues to increment `git_rest_git_operation_errors_total{operation="push"|"pull"|"fetch"|"rebase"}`, and the puller continues to log `git pull failed`.
6. Cold start is ready: a process whose HTTP server is listening and whose repo path is readable answers 200 even before its first pull tick completes. Bootstrap clones or initialises the repo before the server starts, so a serving process has a repo.
7. `/healthz` behaviour is unchanged: 200 while the process is alive.

## Constraints

- MUST NOT change the `/readiness` URL or its 200/503 status-code contract (`docs/api.md:20,118` are frozen in shape, not in meaning — the prose must be corrected).
- MUST preserve spec 005's latency guarantee: the handler never invokes a git subprocess and never blocks on the pull mutex. A repo-path readability check is a `stat`, not a git call.
- MUST NOT weaken liveness. `/healthz` keeps its current semantics and its current implementation.
- MUST NOT change write-path semantics: a failed push still returns an error to the API caller. This spec changes what readiness *reports*, not what a write *does*.
- MUST keep the sync signal intact: the existing metric series and log lines are unchanged, so `helm/templates/alerts.yaml:16` continues to fire on sync failures. No new alert is introduced here.
- MUST NOT delete the `PullStateCache` write path. The recorded state keeps its consumers (metric, logs, and a future `/sync-status`); only its use as the readiness verdict is removed.
- MUST update `docs/api.md:20` ("checks git status + pending pushes") and `docs/api.md:118` ("a pod flips unready … if a push is stuck") to describe the new semantics.
- MUST NOT regress spec 006's conflict handling: a rebase or merge conflict still leaves the repo in its conflicted state and still returns an error from `Pull`; only its effect on readiness changes.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---------|-------------------|----------|
| Push rejected by the remote (writer race) | `/readiness` stays 200; reads served; `git_rest_git_operation_errors_total{operation="push"}` increments; `git pull failed` logged | Next tick retries; alert fires on sustained rejection |
| Fetch fails (remote unreachable, auth broken) | `/readiness` stays 200; reads served from the local clone; `operation="fetch"` increments | Next tick retries; alert fires |
| Merge/rebase conflict | Repo left conflicted; `Pull` returns `RebaseConflictError`; `/readiness` stays 200; reads of non-conflicted paths still work | Operator resolves; alert fires on `conflict="true"` |
| Repo path deleted or unreadable at runtime | `/readiness` returns 503 with a body naming the path | Operator restores the volume; pod restarts into bootstrap |
| Puller goroutine died before serving | `/readiness` returns 503 with `puller not started` | Pod restart |
| Process shutting down | `/readiness` returns 503 | Kubernetes removes the endpoint; restart proceeds |

## Acceptance Criteria

- [ ] **AC1:** With the Reproduction fixture (every push rejected), `/readiness` returns `200` while `GET /api/v1/files/note.md` returns `200`. Evidence: `curl -s -o /dev/null -w '%{http_code}' http://localhost:18447/readiness` prints `200`, and the same for the file URL prints `200`, both after ≥3 pull ticks (≥10s at `-pull-interval=3s`).
- [ ] **AC2:** With the served repo path removed at runtime, `/readiness` returns `503` and the body contains the substring `repo path`. Evidence: `rm -rf "$BASE/work"` while the process runs, then `curl -s -w '\n%{http_code}' http://localhost:18447/readiness` prints a body containing `repo path` and status `503`. This is the discriminator that rules out a hardcoded `200`.
- [ ] **AC3:** The `/readiness` body never contains a sync-failure substring. Evidence: in the AC1 run, `curl -s http://localhost:18447/readiness | grep -Eic 'push|pull|sync|conflict'` prints `0`.
- [ ] **AC4:** Unit tests cover the new verdict: (a) a fresh cache with no recorded pull reports ready, (b) a cache with a recorded push error still reports ready, (c) an unreadable repo path reports not-ready naming the path. Evidence: `go test ./pkg/puller/... ./pkg/handler/...` exits 0 and the three cases appear as named specs in the output.
- [ ] **AC5:** `make precommit` exits 0.
- [ ] **AC6:** `docs/api.md` no longer claims readiness depends on git status, pending pushes, or a stuck push. Evidence: `grep -Eic 'pending pushes|push is stuck' docs/api.md` prints `0`, and `grep -c 'readiness' docs/api.md` still prints ≥2 (the endpoint is still documented).
- [ ] **AC7:** The sync signal is unchanged. Evidence: in the AC1 run, `curl -s http://localhost:18447/metrics | grep -E '^git_rest_git_operation_errors_total\{operation="push"'` prints a value `> 0`, and `$BASE/run.log` contains ≥1 line matching `git pull failed`.
- [ ] **AC8 (Post-Deploy Rung-2):** on dev, a pod serving a repo whose pushes are rejected stays `1/1` and Ready, while its logs keep logging the rejection. Evidence: after `BRANCH=dev make buca` and a rollout, `kubectlnukedev -n dev get pod vault-obsidian-openclaw-0` prints `1/1` with `Ready=True` continuously across ≥2 pull intervals, and `kubectlnukedev -n dev logs vault-obsidian-openclaw-0 --since=10m | grep -c 'git pull failed'` prints ≥1.
  - `deploy_check:` `kubectlnukedev -n dev get --raw "/api/v1/namespaces/dev/pods/vault-obsidian-openclaw-0:9090/proxy/metrics" | grep -m1 '^git_rest_build_info' | sed -E 's/.*commit="([^"]+)".*/\1/'`
  - `deploy_target:` `$(git rev-parse --short HEAD)`
- [ ] **AC9 (Post-Deploy Rung-3):** on nuke prod, `vault-obsidian-openclaw-0` reads `1/1` with a ready Service endpoint, read twice ≥10 minutes apart, while push rejections continue in the logs. Evidence: `kubectlnukeprod -n prod get pod vault-obsidian-openclaw-0` prints `1/1` on both reads, and `kubectlnukeprod -n prod get endpoints vault-obsidian-openclaw` lists a non-empty `ENDPOINTS` column on both.
  - `deploy_check:` `kubectlnukeprod -n prod get statefulset vault-obsidian-openclaw -o jsonpath='{.spec.template.spec.containers[0].image}'`
  - `deploy_target:` `docker.prod.nuke.benjamin-borbe.de:443/bborbe/git-rest:<released-tag>`

## Verification

### Container-executable (YOLO container at prompt time)

- `make precommit` — format, generate, test, lint
- `make test` — unit + integration suite
- `grep -Eic 'pending pushes|push is stuck' docs/api.md` — prints `0`

### Operator-executable (host, after PR merge)

- Rung 1: the `## Reproduction` recipe against a freshly built binary — `/readiness` `200`, read path `200`, and the AC2 negative (`rm -rf` the repo → `503`).
- Rung 2: `BRANCH=dev make buca` + rollout on dev, then AC8.
- Rung 3: prod promotion, then AC9.

## Open Questions

None — the readiness verdict, its negative controls, and the observability contract are all fixed above.
