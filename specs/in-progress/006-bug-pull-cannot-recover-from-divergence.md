---
status: prompted
tags:
    - dark-factory
    - spec
    - bug
approved: "2026-05-04T19:55:21Z"
generating: "2026-05-04T19:55:22Z"
prompted: "2026-05-04T20:05:39Z"
branch: dark-factory/bug-pull-cannot-recover-from-divergence
---

## Summary

- git-rest's pull loop fails forever when the local repo and remote have diverged (both sides have new commits).
- The failure is the cryptic git hint `Need to specify how to reconcile divergent branches`, not a real conflict — git is refusing to choose a strategy.
- Once stuck, every subsequent pull tick fails identically and readiness reports 503 indefinitely; only manual operator intervention recovers the pod.
- A service whose entire purpose is "automate git" must self-heal divergence automatically. Only true content conflicts should require humans.
- Fix is a deterministic 4-state pull state machine (fast-forward, push, rebase+push, no-op) with explicit conflict signalling.

## Problem

git-rest is a single-writer vault sync service for Obsidian-style StatefulSets. Its pull loop invokes plain `git pull` with no strategy configured. When the local repo has unpushed commits AND the remote has new commits at the same time (e.g. dark-factory rewrote a task file locally while an operator pushed from their laptop), git refuses to act and emits a config hint instead of an error. The pull loop logs the hint forever, readiness stays 503 forever, and the only recovery is exec'ing into the pod and running git commands by hand. This contradicts the service contract — keep `/data` in sync with the remote — for the most common transient sync failure on Earth.

## Goal

Pull recovers automatically from divergence without operator intervention, and reports a precise, actionable error only when a real content conflict requires a human. The cryptic git hint never appears in logs or 503 bodies.

## Reproduction

Observed in production on 2026-05-04 around 19:30 UTC.

- Pod: `vault-obsidian-openclaw-0`
- Cluster/namespace: `kubectlquant -n prod`
- Image: `docker.quant.benjamin-borbe.de:443/bborbe/git-rest:v0.19.1` (also reproduces on `v0.18.0` with a different surface symptom — pod NotReady stuck behind pull mutex)
- State: `/data` had 40 local-only commits (dark-factory task status churn rewriting `tasks/61fc8314-6e39-57b8-a886-bac5193d3f82.md`) AND remote had new commits — real divergence.

Every PullInterval tick (30s) for over 24 hours emitted:

```
puller.go:74 WARN git pull failed error="exit status 128
git [pull]: ...
fatal: Need to specify how to reconcile divergent branches.
hint: You can do so by running one of the following commands sometime before
hint:   git config pull.rebase false  # merge
hint:   git config pull.rebase true   # rebase
hint:   git config pull.ff only       # fast-forward only"
```

On v0.19.1, readiness returned 503 with body `no successful pull yet` indefinitely (per spec 005's stuck-pull contract — correct behavior, but service still wedged).

Recovery required an operator to exec into the pod and run:

```
git fetch origin && git rebase origin/main && git push
```

That command itself failed with a content conflict at the first commit, so recovery escalated to:

```
git rebase --abort && git reset --hard origin/main
```

— which dropped the 40 local commits.

## Workaround

Until the fix lands, operators recovering a stuck pod can run:

```bash
kubectlquant -n <ns> exec <pod> -- sh -c \
  'export GIT_SSH_COMMAND="ssh -i /ssh/id_ed25519 -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no" && \
   cd /data && git fetch origin && git rebase origin/<branch> && git push'
```

If the rebase reports a content conflict, escalate to:

```bash
kubectlquant -n <ns> exec <pod> -- sh -c \
  'cd /data && git rebase --abort && git reset --hard origin/<branch>'
```

This drops local-only commits — only safe when local work is reproducible (e.g. dark-factory task churn that re-asserts state on next status update). Audit `git log origin/<branch>..HEAD --oneline` first.

To prevent recurrence on a recovered pod (until the fix lands):

```bash
kubectlquant -n <ns> exec <pod> -- sh -c 'cd /data && git config pull.ff only'
```

This makes future pulls fast-fail on divergence with a clearer error rather than the cryptic hint, but does not provide auto-recovery — that requires the code fix.

### Deterministic recipe (for verification)

1. Deploy git-rest with default config; let one successful clone+pull complete.
2. From outside the pod (e.g. operator laptop), push a new commit to `origin/<branch>`.
3. Inside the pod (or via the WriteFile API), commit a non-conflicting change locally without pushing.
4. Wait one PullInterval — pull tick fails with `Need to specify how to reconcile divergent branches`.
5. Subsequent ticks fail identically forever; readiness 503 forever.

## Expected vs Actual

**Expected** (per `docs/api.md` readiness contract and the service's stated purpose of keeping `/data` in sync with remote):

- Pull detects divergence (local ahead AND remote ahead) and recovers automatically via rebase + push.
- Only true content conflicts require human intervention.
- When stuck, the 503 body names the underlying cause (e.g. `rebase conflict at <path>`), not a generic git config hint.
- The cryptic substring `Need to specify how to reconcile divergent branches` never appears in user-facing surfaces.

**Actual:**

- Pull fails forever on divergence with a `git config` hint masquerading as a fatal error.
- No recovery path. Pod stuck NotReady (v0.18.0 behind the spec-005 pull mutex) or stuck Ready=False (v0.19.1).
- Manual intervention required: SSH-equipped exec, `git fetch && git rebase && git push`, manual conflict resolution, set `git config pull.rebase` to prevent recurrence, restart pod (v0.18.0 only).

## Why this is a bug

git-rest exists to automate git. The current `Pull()` cannot complete the most common automation task — recovering from a concurrent push — without human help. Recovery currently requires five manual steps (exec, fetch+rebase+push, manual conflict resolution, config tweak, pod restart on v0.18.0). The service contract documented in `docs/api.md` promises `/data` stays in sync with remote; this contract is silently violated for hours or days at a time on the most common transient failure mode in a multi-writer setup. The single-writer StatefulSet assumption (`replicas: 1`) does not eliminate divergence — operators and external tools push to the same remote.

## Desired Behavior

1. Pull detects four distinct local/remote states and acts deterministically on each:
   - local clean, remote new → fast-forward.
   - local ahead, remote unchanged → push.
   - local ahead, remote ahead (divergence) → rebase onto remote, then push.
   - local clean, remote unchanged → no-op.
2. On a real content conflict during rebase, the repo is left in its conflicted state, the in-memory `lastErr` records the conflict path, and readiness returns 503 with a body naming that path.
3. The 503 body and log lines never contain the substring `Need to specify how to reconcile divergent branches`. The user-facing message is always either a conflict path or a transient network/auth error.
4. Recovery from divergence happens within one PullInterval — no operator intervention, no manual commands, no pod restart.
5. The pull strategy is encoded in the code path (explicit fetch + rebase + push), not in the repo's `git config`. The repo's config is not relied on for correctness.
6. Branch name is derived from `HEAD`'s upstream tracking ref, not hardcoded.
7. A new metric `git_operation_errors_total{operation="rebase",conflict="true"}` increments on rebase content conflicts. Existing pull/push metrics continue to behave as before.

## Constraints

- MUST NOT change the public HTTP contract documented in `docs/api.md` (URLs, status codes, response shapes).
- MUST NOT auto-clobber local commits. Rebase preserves local work; on conflict the repo is left for human inspection. `git reset --hard` and `git rebase --abort` are NEVER invoked automatically.
- MUST honor the single-writer assumption (`replicas: 1` in vault StatefulSets). Behavior under multi-writer (`replicas > 1`) is explicitly undefined and out of scope.
- MUST stay within the `--pull-timeout` budget from spec 005 (`005-bug-readiness-blocks-on-pull-mutex.md`). Fetch + rebase + push together must fit within the existing per-tick timeout.
- MUST preserve spec 005's healthy-path readiness behavior (cache flips Ready after first successful pull, 503 with named cause when stuck).
- 503 body MUST name the conflict path (filename) on rebase conflict.
- Branch name MUST be derived from `HEAD`'s upstream tracking ref. No hardcoded `main`/`master`.
- Linear history is preferred (rebase, not merge) — vault repos are append-mostly markdown, merge commits add noise to the automated stream.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---------|-------------------|----------|
| Local ahead AND remote ahead, no content conflict | Fetch, rebase onto remote tracking ref, push. Cache flips Ready within one PullInterval. | Automatic, single tick. |
| Local ahead AND remote ahead, content conflict during rebase | Repo left in conflicted state. `lastErr` records conflict path. 503 body: `last pull failed: rebase conflict at <path>`. Metric `git_operation_errors_total{operation="rebase",conflict="true"}` increments. | Human inspection required. Service does NOT auto-resolve. |
| Local ahead, remote unchanged (previous push failed transiently) | Push without re-fetching. Cache flips Ready on success. | Automatic. |
| Local clean, remote new (common case) | Fast-forward merge. | Automatic, no behavior change from today's healthy path. |
| Both clean | No-op. | N/A. |
| Network/auth failure during fetch or push | `lastErr` records the wrapped git error. 503 body names the operation that failed (`fetch failed`, `push failed`). | Retried on next tick. |
| `HEAD` has no upstream tracking ref | `lastErr` records `no upstream configured`. 503 body matches. Pull does not attempt fetch/rebase/push. | Operator must set upstream once; subsequent ticks recover. |

## Acceptance Criteria

- [ ] AC1: With local ahead AND remote ahead (real divergence, no content conflict), a single `Pull()` call rebases + pushes successfully and the readiness cache flips Ready within one PullInterval. Verified by an integration test that constructs a divergent state in a temp bare-repo fixture (no SSH, no network).
- [ ] AC2: With divergence AND content conflict, `Pull()` leaves the repo in a conflicted state and the readiness cache reports `503 last pull failed: rebase conflict at <path>`. The body NEVER contains the substring `Need to specify how to reconcile divergent branches`. Verified by integration test with a deterministic conflict fixture.
- [ ] AC3: With local ahead AND remote unchanged, `Pull()` recovers by pushing successfully within one PullInterval. Implementation may fetch-then-push or push-without-fetch; both are acceptable as long as no rebase is attempted (no remote work to integrate) and the push succeeds. Verified by integration test.
- [ ] AC4: With local clean AND remote new (the common case), `Pull()` fast-forwards. Existing healthy-path behavior preserved — spec 005's readiness flip on first success still works. Verified by existing integration tests continuing to pass plus a new fast-forward case.
- [ ] AC5: Unit/integration tests exist for each of the four pull states using a local bare-repo fixture. No SSH, no network. Tests are deterministic.
- [ ] AC6 (runtime verification, MANDATORY per bug-workflow.md): On a real pod (dev or prod), simulate divergence per the recipe in the Verification section. Observe automatic recovery within one PullInterval, no operator intervention. Required evidence: (a) `kubectlquant logs <pod> service` does NOT contain the substring `Need to specify how to reconcile divergent branches` (verify with `! grep -F 'Need to specify how to reconcile divergent branches' <captured-logs>`); (b) Ready condition transitions False → True within 30s of the divergence trigger (capture `kubectl get pod <pod> -o jsonpath='{.status.conditions[?(@.type=="Ready")].lastTransitionTime}'` before and after); (c) `git log --oneline -3` from inside the pod shows both the operator's external commit and the pod's local commit on the same linear history.
- [ ] AC7: New metric `git_operation_errors_total{operation="rebase",conflict="true"}` increments exactly once per rebase content conflict. Verified by metrics scrape after AC2 test runs.
- [ ] AC8: No regressions in existing spec 005 readiness behavior — pull mutex bounded by `--pull-timeout`, cache reports last pull error, `/healthz` always 200 while process alive.

**Scenario coverage:** this bug modifies the pull loop's interaction with the git CLI subprocess — a subprocess interface seam. Acceptance Criterion AC6 requires a runtime scenario on a real pod (not just unit/integration fixtures), satisfying the mandatory scenario-coverage rule for integration seams.

## Verification

```
make test
make precommit
```

Plus the runtime scenario from AC6:

1. Deploy the candidate image to a dev StatefulSet.
2. Wait for first successful pull (readiness Ready).
3. From an external workstation, push a new commit to `origin/<branch>`.
4. From the pod (or via `POST /api/v1/files/...`), create a non-conflicting local commit.
5. Wait one PullInterval (default 30s).
6. Confirm: pod Ready, `/data` HEAD includes both commits in linear history (`git log --oneline | head`), no `Need to specify how to reconcile divergent branches` in pod logs.
7. Repeat with a deliberately conflicting local edit. Confirm: pod Ready=False, 503 body names the conflict path, metric incremented.

Capture log evidence and Ready transitions for the verification report.
