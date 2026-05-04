---
status: prompted
tags:
    - dark-factory
    - spec
    - bug
approved: "2026-05-04T08:57:56Z"
generating: "2026-05-04T09:01:24Z"
prompted: "2026-05-04T09:07:03Z"
branch: dark-factory/bug-readiness-blocks-on-pull-mutex
---

## Summary

- Readiness probe returns 503 with body `context canceled` whenever a background `git pull` stalls (e.g. SSH timeout to remote).
- The probe never actually runs `git status` — it blocks on the same mutex the pull holds, then the Kubernetes 1s probe timeout cancels the request context.
- A transient SSH timeout (~75s) flips the pod NotReady on every PullInterval cycle, disrupting traffic routing.
- Error message misreports cause: operators cannot distinguish repo corruption, network outage, or probe timeout.
- Fix shape: bound pull duration AND decouple readiness from the pull mutex (defence-in-depth — neither alone is sufficient).

## Problem

Readiness is supposed to signal "this pod can serve writes." Today it is coupled to a long-running background `git pull` via a shared mutex on the git wrapper. When the remote is unreachable, every readiness probe in the pull window returns 503 with a misleading `context canceled` message, the pod is marked NotReady, and traffic flaps. The probe response also carries no information about the actual failure (the SSH timeout that triggered it), so operators cannot triage from the probe alone.

## Goal

`/readiness` reflects cached service health and answers within the Kubernetes probe timeout (default 1s) regardless of whether a background pull is in flight or stalled. When unhealthy, the response body describes the real cause (e.g. last pull error, or "no successful pull yet"). A pull that hangs on the network never marks the pod NotReady on its own — only a sustained absence of recent successful state does.

## Desired Behavior

1. `/readiness` returns within Kubernetes probe `timeoutSeconds` regardless of pull state — the handler never invokes a git subprocess and never blocks on the pull mutex.
2. `/readiness` 200 iff a successful pull occurred within the freshness threshold (see Assumptions) AND no later pull has marked the working tree as unrecoverable.
3. `/readiness` 503 body identifies the underlying cause in plain text (e.g. `last pull failed: ssh timeout`, `no successful pull yet`, `last successful pull stale (Nm Ms ago)`); body MUST NOT contain the substring `context canceled`.
4. Pull subprocess duration is bounded by a per-pull timeout (configurable; default chosen to be well under SSH `ConnectTimeout`). On timeout, the subprocess is aborted and the puller records the failure in cached state.
5. Cached state has a freshness threshold beyond which readiness flips 503 even if no subsequent pull has explicitly failed — prolonged silence is unhealthy.
6. Before the first successful pull (cold start), `/readiness` returns 503 with body `no successful pull yet` (not 200). The startup pull at `main.go:228` is the first opportunity to flip Ready.

## Assumptions

- Kubernetes probe `timeoutSeconds` defaults to 1s; service must answer within that bound.
- Default `PullInterval` is 30s (existing flag).
- SSH default `ConnectTimeout` is ~75s; per-pull timeout MUST be substantially less than this to be useful.
- HTTP contract for `/readiness` (URL, 200/503 status codes) is frozen per `docs/api.md:20`.
- **Cache-freshness threshold**: `3 × PullInterval` (default 90s). After this without a successful pull, readiness flips 503 regardless of whether the last attempt explicitly failed.
- **Per-pull timeout**: configurable via a new optional flag (default 60s — well under SSH 75s ConnectTimeout, well over normal pull duration). Following the existing `PullInterval` flag pattern.
- **Cold-start state**: 503 with `no successful pull yet` until the startup pull (`main.go:228`) or the first ticker-driven pull succeeds, whichever comes first.

## Workaround

Operators hitting this in prod before the fix lands can mitigate by:
- Raising the readiness probe `timeoutSeconds` (e.g. to 5s) — reduces flapping but does not fix misleading error bodies.
- Increasing `failureThreshold` (e.g. to 5–10) — tolerates transient stalls without flipping NotReady.
- Pointing `GitRemoteURL` at a reachable remote, or removing the remote temporarily if the pod only needs read-side functionality.

These are stopgaps; none address the root cause (mutex coupling + unbounded pull).

## Reproduction

**Build / environment:**
- Pod: `vault-obsidian-openclaw-0`, `kubectlquant -n dev`
- Build: `v0.18.0-1-ga62b5f8-dirty`, observed 2026-05-02 / 2026-05-03

**Recipe (deterministic):**
1. Deploy git-rest with `GitRemoteURL` pointing to a host whose SSH port is black-holed (e.g. `iptables -A OUTPUT -p tcp --dport 22 -d github.com -j DROP`, or use an unreachable host). Default `PullInterval=30s`.
2. Wait for the next pull cycle (≤30s after startup).
3. Hit `/readiness` repeatedly with curl during the pull window.

**Observed evidence (verbatim from prod logs, 2026-05-03 18:23:49):**

```
puller.go:47 WARN: git pull failed error="exit status 1
git [pull]: ssh: connect to host github.com port 22: Operation timed out
fatal: Could not read from remote repository."
```

Followed immediately by a cluster of 21 readiness errors at 18:23:49.193*:

```
http_json-error-handler.go:71] handle GET request to /readiness failed
with status 503 and code INTERNAL_ERROR:
git status: git status --porcelain: git [status --porcelain]: : context canceled
```

The cluster repeats at 18:25:05.979* on the next PullInterval cycle.

## Expected vs Actual

**Expected** (per `docs/api.md:20` — `/readiness` checks git status + pending pushes; per Kubernetes probe contract — must answer within `timeoutSeconds`):
- 200 within 1s when a recent successful pull is cached, even if a fresh pull is currently stalled.
- When 503, body identifies the real failure (e.g. `last pull failed: ssh timeout`, or `no successful pull yet`).

**Actual:**
- 503 on every probe in the pull window with body containing `git status --porcelain: : context canceled`.
- `git status` never executes — the goroutine blocks on the pull mutex, then the probe's 1s context is canceled by Kubernetes, and `exec.CommandContext` returns immediately without invoking git.
- Pod oscillates between Ready and NotReady on each PullInterval cycle.

## Why this is a bug

1. **Misreports cause.** The body says `context canceled` but no work was attempted. Operators cannot tell whether the repo is corrupt, the network is down, or the probe just timed out waiting for a lock.
2. **False NotReady.** A transient, recoverable SSH timeout (expected on flaky links) flips the pod out of service rotation, disrupting traffic routing for the whole pull duration.
3. **Violates probe semantics.** Kubernetes readiness is a fast-path signal. Gating it on a slow git subprocess that shares a mutex with a 75s-blocking pull breaks the contract documented in `docs/api.md:20` and the Kubernetes probe model.

## Root Cause (triaged)

Line numbers are pinned to build `v0.18.0-1-ga62b5f8` and may drift; function names are stable.

- `git` struct uses a single `sync.Mutex` for all operations (`pkg/git/git.go`, `git` struct, ~line 78).
- `git.Pull()` acquires `g.mu.Lock()` and holds it for the full subprocess duration (~line 334).
- `git.Status()` acquires the same `g.mu.Lock()` (~line 406). The acquisition is not context-aware, so the goroutine blocks until the pull releases regardless of the caller's deadline.
- `puller.run` invokes `git.Pull(ctx)` with the application's lifetime context (`pkg/puller/puller.go`, ~line 46). No per-pull timeout, so an SSH `ConnectTimeout` (~75s default) keeps the mutex held that long.
- `git.runCmdOutput` uses `exec.CommandContext(ctx, ...)` (~line 141). Once the readiness HTTP request context is canceled by the Kubernetes 1s probe timeout while still waiting on the mutex, `cmd.Run()` returns immediately with `context canceled` and never starts git.

## Constraints

- MUST NOT change the public HTTP contract: `/readiness` URL and status codes (200/503) per `docs/api.md:20` are frozen.
- MUST NOT require new flags to be functional. Sensible defaults; flags are optional for tuning.
- MUST remain compatible with the existing single-binary deploy and current Helm/manifest probe configuration.
- Fix must be defence-in-depth: bound pull duration AND decouple readiness from the pull mutex. Bounding pull alone still leaves a window of false-NotReady up to the bound; decoupling alone leaves an unbounded mutex hold elsewhere in the system.
- Must not regress existing tests (`make test`).

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---------|-------------------|----------|
| Remote SSH black-holed mid-pull | `/readiness` continues returning 200 while cached pull state is fresh; pull subprocess is bounded and aborted on timeout | Next PullInterval retries; readiness reflects cached state |
| Pull never succeeds since pod start | `/readiness` returns 503 with body explaining "no successful pull yet" (not `context canceled`) | Operator sees real cause, fixes remote/credentials |
| Last successful pull is older than freshness threshold | `/readiness` returns 503 with body naming the last pull error | Operator sees real cause |
| Concurrent readiness requests during a 30s+ stalled pull | Each request returns within probe timeout without contending on the pull mutex | n/a |
| Probe context canceled by Kubernetes | Body never contains `context canceled`; either responds before cancel, or the response was already cached | n/a |

## Acceptance Criteria

- [ ] **AC1:** With SSH to remote blocked (simulated stalled pull), `/readiness` returns 200 within 1s when a prior pull succeeded recently. Verified by integration test that injects a pull-stalling scenario and HTTP-pings `/readiness` under the probe timeout.
- [ ] **AC2:** `/readiness` 503 response body never contains the substring `context canceled`. When unhealthy, body contains a meaningful failure summary (last pull error, or "no successful pull yet").
- [ ] **AC3:** With a pull artificially held for 30s+, every `/readiness` request returns within 100ms. Verified by integration test that injects a stalling pull and measures p99 readiness latency over ≥100 concurrent requests.
- [ ] **AC4:** New unit tests cover (a) a pull whose subprocess exceeds the configured bound is aborted and the failure is recorded in cached state, (b) the readiness handler reads cached state without invoking any git subprocess, (c) readiness surfaces the cached last-pull error in the 503 body when unhealthy, (d) cold-start readiness returns 503 with `no successful pull yet` before any pull has succeeded.
- [ ] **AC5:** `make test` passes; existing readiness behavior under healthy conditions (200 when repo clean and pulls succeeding) is unchanged.
- [ ] **AC6 (verification):** Replay the original reproduction recipe (SSH black-holed against a running pod). Confirm `/readiness` returns 200 throughout the stall window, and 503 bodies (when seen) describe the SSH failure rather than `context canceled`.

## Verification

```
make test
```

Plus the runtime repro replay called out in AC6: deploy the patched binary against a remote with port 22 dropped; observe `/readiness` over a full PullInterval cycle and confirm both the latency bound and the body content.

## Open Questions

All resolved inline (see Assumptions and Desired Behavior). No outstanding questions block approval.
