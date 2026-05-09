---
status: prompted
approved: "2026-05-09T23:08:38Z"
generating: "2026-05-09T23:08:39Z"
prompted: "2026-05-09T23:10:08Z"
branch: dark-factory/bug-pid1-no-init-leaks-grandchild-zombies
---

# bug: /main runs as PID 1 with no reaper, leaks git grandchild zombies

## Summary

- The `git-rest` container's `ENTRYPOINT ["/main"]` makes the Go binary PID 1
- `cmd.Run()` correctly reaps the **direct** `git` child it spawned via `os/exec`
- BUT `git` itself spawns helpers (`ssh`, `git-remote-https`, `git-fetch-pack`, credential helpers); when those become orphans (parent `git` exits or is killed mid-operation) they get reparented to PID 1 = `/main`
- Go's runtime does NOT install a generic `SIGCHLD` reaper — it only `wait()`s on processes started via `os/exec.Cmd`; reparented orphans accumulate as `<defunct>` zombies forever
- Over multi-day pod uptime with `PULL_INTERVAL=30s`, zombies grow without bound and eventually break node-level operations (PID-table scan in `k3s-killall.sh` times out)

## Reproduction

git-rest version: `v0.19.4` (running in prod cluster on `nuke-k3s-prod-0`, observed 2026-05-09)

Pod: `vault-obsidian-trading-0` (StatefulSet `vault-obsidian-trading`, namespace `prod`).
Uptime at observation: 3 days, 02h 49m. `PULL_INTERVAL=30s`.

```bash
ssh nuke-k3s-prod-0 "ps -eo pid,ppid,state,comm | awk '\$3==\"Z\"{print \$2}' | sort | uniq -c | sort -rn | head"
   9075 4165837
   8992 4165829
```

Both PPIDs are `/main -v=2` instances under k3s pod sandboxes:

```
ps -p 4165837,4165829 -o pid,ppid,user,etime,cmd
    PID    PPID USER         ELAPSED CMD
4165829 4165709 root      3-02:49:28 /main -v=2
4165837 4165758 root      3-02:49:28 /main -v=2
```

Process environment confirms:

```
HOSTNAME=vault-obsidian-trading-0
GIT_USER_NAME=vault-obsidian-trading
```

Sample of accumulated zombies (truncated — there are ~18,000 total across the two `/main` processes, growing at ~1 every ~15s):

```
1504200 ?        Zs     0:00 [git] <defunct>
1504572 ?        Zs     0:00 [git] <defunct>
...
4194004 ?        Zs     0:00 [git] <defunct>
```

Operational impact observed the same day:

```
~/Documents/workspaces/scripts/remote-k3s-shutdown-nuke.sh
[5] 00:35:37 [FAILURE] nuke-k3s-dev-0.hm.benjamin-borbe.de  Timed out, Killed by signal 9
[6] 00:35:37 [FAILURE] nuke-k3s-prod-0.hm.benjamin-borbe.de Timed out, Killed by signal 9
```

`k3s-killall.sh` walks the full process tree (`ps -e -o ppid= -o pid=` + repeated `grep`/`sed` per node); with hundreds of thousands of zombies the scan exceeds `pssh -t 300`. Because the pssh block has `set -o errexit`, the failure aborted the script before kafka/master nodes were even attempted.

### Minimal repro against any fresh `git-rest:v0.19.4` pod

```bash
# Deploy a single git-rest pod with PULL_INTERVAL=30s and a real GIT_REMOTE_URL
# (any reachable repo over SSH).
kubectl exec -n <ns> <pod> -- sh -c \
  'while true; do ps -eo state,comm | awk "\$1~/Z/ && \$2==\"git\"" | wc -l; sleep 60; done'
```

Zombie count grows monotonically. Within ~1h, expect double-digit zombies. After 24h, hundreds. After a week, thousands. There is no reclamation mechanism.

## Expected vs Actual

| | Expected | Actual |
|---|---|---|
| After 24h with `PULL_INTERVAL=30s` | 0 `[git] <defunct>` under `/main` | hundreds |
| After 3 days | 0 | ~9,000 per pod |
| `k3s-killall.sh` on host | completes in seconds | times out after 300s |
| Pod memory / PID slot consumption | bounded | grows unboundedly until cgroup PID limit or kernel `kernel.pid_max` |

## Why this is a bug

1. **Containers running as PID 1 must reap orphans.** This is documented Linux/container hygiene: when a process is PID 1 it inherits all orphaned children of every process in its PID namespace and is the only process that can `wait()` on them. Without a reaper, orphans become permanent zombies. Standard mitigations are `tini`, `dumb-init`, or `shareProcessNamespace: true`.

2. **Go's `os/exec` only reaps what it started.** The Go runtime does not install a generic `SIGCHLD` handler. `cmd.Wait()` reaps the direct child `git`; it cannot and does not touch grandchildren that get reparented from elsewhere. This is well-known Go-on-PID-1 behaviour.

3. **Indefinite resource leak.** Each zombie holds a PID slot and a small kernel `task_struct`. With `PULL_INTERVAL=30s` and `git pull` over SSH (which routinely spawns helpers), the leak is steady and unbounded. The pod will eventually hit cgroup PID limits or the host's `kernel.pid_max`.

4. **Breaks orderly node shutdown.** As demonstrated 2026-05-09: `k3s-killall.sh` cannot complete its process-tree walk within `pssh -t 300`, blocking the shutdown automation for the whole cluster.

5. **All `git-rest` consumers inherit the bug.** `vault-obsidian-trading`, `vault-obsidian-openclaw`, and any future deployment of this image are equally affected. Fixing it once in the image fixes every consumer.

## Workaround

Until the fix lands, operators can:

1. **Periodic pod restart** — Kubernetes `livenessProbe` or a CronJob `kubectl rollout restart sts/<name>` weekly. Crude, but bounds the leak.
2. **Pod-level `shareProcessNamespace: true`** — makes `pause` (PID 1 across containers) reap orphans. Changes pod semantics (containers see each other's processes) and must be set per-StatefulSet, so it doesn't fix the image for new consumers.
3. **Manual remediation when noticed**: `kubectl exec ... -- kill <main-pid>` is NOT safe (kills the service). `sudo kill <main-pid-on-host>` from the node has the same effect. The only safe in-situ remediation is pod restart.

None of these are a substitute for a proper PID 1 reaper in the image.

## Scope

Single-image fix in `git-rest`:

- **`Dockerfile`** — add a minimal init (`tini` is ~10 KB in Alpine, already-packaged) and chain it as the entrypoint
- **`CHANGELOG.md`** — entry under `## Unreleased` describing the fix and its operational impact
- **No Go code changes.** The leak is purely an init/PID-1 problem; `pkg/git/git.go` and `main.go` are correct as written

Out of scope for this spec:

- Changing `PULL_INTERVAL` defaults
- Tuning `git` invocations or replacing `os/exec` with something else
- Modifying any consuming StatefulSet (they bump the image tag via Keel; no manifest changes needed)

## Constraints

- The fix MUST NOT change CLI argument behaviour: `/main -v=2 --some-flag=...` must still work after the entrypoint chain
- The fix MUST forward signals (SIGTERM from kubelet) to `/main` so graceful shutdown is preserved — `tini -g` (kill process group) or default `tini` both do this; verify whichever is chosen
- Image size increase MUST be ≤ 100 KB (tini is ~10 KB; this is comfortable)
- The fix MUST work with the existing `apk --no-cache add` line (tini is in Alpine `community` repo, available by default in `alpine:3.23`)
- Existing `Makefile` build/buca targets MUST continue to work unchanged
- The fix MUST NOT depend on any change in StatefulSets — once the new image tag rolls out via Keel, zombies must stop accumulating with no manifest edits

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| Normal `git pull` cycle (success) | Direct `git` reaped by Go; any reparented helpers reaped by tini | none |
| `git pull` killed by pull-timeout context cancel | Go kills direct `git`, reaps it; tini reaps any orphan ssh/helpers | none |
| `/main` receives SIGTERM (pod shutdown) | tini forwards SIGTERM to `/main`; Go shuts down HTTP + puller; tini exits after `/main` exits | none |
| `/main` panics | tini exits with non-zero; container restarts per restartPolicy | kubelet restart |
| tini fails to start (binary missing) | container fails to start; clear error in pod events | image rebuild |
| Pre-existing zombies on a running pod (before fix rollout) | Stay zombies until pod restart; new image starts clean | rolling restart of the StatefulSet, which Keel does automatically on tag change |

## Acceptance Criteria

- [ ] `Dockerfile` installs an init reaper (tini) and chains it before `/main` as the entrypoint, so `/main` no longer runs as PID 1
- [ ] `make precommit` passes
- [ ] Built image runs locally: `docker run --rm <img> --help` succeeds and shows the same flags as before
- [ ] Built image forwards signals: `docker run -d <img> ...; docker stop <id>` exits within the default grace period (10s) — confirms the init propagates SIGTERM and `/main` shuts down
- [ ] Runtime verification: deploy the new tag to dev, watch a pod for ≥ 1 hour with `PULL_INTERVAL=30s`. The following command MUST return `0` on every poll across at least 60 pull cycles:
  ```bash
  kubectl exec -n <ns> <pod> -- ps -eo state,comm | awk '$1 ~ /Z/ && $2 == "git"' | wc -l
  ```
- [ ] Image size delta ≤ 100 KB compared to `v0.19.4`, measured with:
  ```bash
  docker images --format '{{.Repository}}:{{.Tag}} {{.Size}}' | grep git-rest
  ```
- [ ] CHANGELOG entry under `## Unreleased`

## Verification

```bash
cd ~/Documents/workspaces/git-rest && make precommit
```

Plus the runtime verification from `## Acceptance Criteria` against the deployed image — replay the original repro on a fresh pod for ≥ 1h and confirm zombie count stays at 0. Per [bug-workflow.md](../../../dark-factory/docs/bug-workflow.md), tests passing alone is NOT sufficient evidence to mark this complete; the bug is a runtime symptom and verification must exercise the runtime path.
