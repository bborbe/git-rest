# Deployment Guide

How to deploy git-rest. Two common shapes: standalone binary and Kubernetes StatefulSet.

## Standalone Binary

### Local-only mode

No remote, no SSH — git-rest runs a local `git init` and keeps commits on disk only. Useful for testing and single-node deployments.

```bash
git-rest --repo /data --listen :8080
```

On startup:

1. Create `/data` if it doesn't exist.
2. If `/data/.git` is missing, run `git init`.
3. Serve the HTTP API. Writes commit locally, no push.

### Remote-backed mode

Pass a remote URL + SSH key. git-rest clones on startup and pushes coalesced write bursts (see `### Write coalescing` below), or immediately on a delete.

```bash
git-rest \
  --repo /data \
  --listen :8080 \
  --git-remote-url git@github.com:owner/repo.git \
  --git-ssh-key /ssh/id_ed25519 \
  --git-user-name git-rest \
  --git-user-email git-rest@example.com \
  --pull-interval 30s
```

Prerequisites:

- The SSH key must have write access to the remote (GitHub "Deploy key" with write, or a PAT-authorised key).
- `known_hosts` is handled by the embedded SSH client; no manual config needed.
- `/data` must be writable by the process user.

### Write coalescing

A write returns once its content is on disk and staged — no commit or push happens inside the request. A background flusher commits and pushes the accumulated writes once per window, and a pull cycle flushes pending writes before merging. Set `--write-coalesce=false` to restore the previous synchronous write-commit-push behaviour; that is the rollback lever.

| Flag | Env | Default | Meaning |
|------|-----|---------|---------|
| `--write-coalesce` | `WRITE_COALESCE` | `true` | Coalesce file writes into one commit and one push per window. `false` restores the synchronous write-commit-push path on every request. |
| `--write-coalesce-delay` | `WRITE_COALESCE_DELAY` | `30s` | Flush after the pending set has been quiet for this long. |
| `--write-coalesce-max-delay` | `WRITE_COALESCE_MAX_DELAY` | `120s` | Flush when the oldest pending write is this old, even under a continuous write stream. |

On SIGTERM the process flushes and pushes pending writes before exiting, bounded by a 10 s deadline; a flush cut short logs `git-rest: flush deadline exceeded` and leaves the paths staged. A pull cycle bounds its pre-merge flush to one third of the remaining pull budget (`--pull-timeout`, or the 60 s startup ceiling), so a contended push cannot consume the whole deadline. A flush cut short by that bound logs `git-rest: flush deadline exceeded before pull`, leaves the paths staged for the next cycle, and the cycle still merges and runs the dirty-tree rescue on the remaining budget. Every pull cycle logs `git-rest: pull budget pull_start=… pull_deadline=… flush_deadline=…`; `flush_deadline=none` when the context carries no deadline. On boot, paths left staged by a crash are committed and pushed before the HTTP server starts listening, logging `git-rest: recovered pending writes` with the path count. WaitDelay bounds every git subprocess once its context is cancelled: a cancelled command returns within 2 s instead of waiting for an orphaned child to release its output pipe, and a command whose process succeeded before the grace expired is still reported as a success, so a push that landed is not retried.

## Kubernetes

Pattern: **one StatefulSet per repo**. Each vault/repo becomes a named service (e.g. `vault-obsidian-trading`), with a dedicated PVC and a secret holding the SSH key.

Reference deployment: `~/Documents/workspaces/nuke/git-rest/` (see the [`nuke` repository](https://github.com/bborbe/nuke)).

### Required manifests

| Manifest | Purpose |
|----------|---------|
| `StatefulSet` | Pod running git-rest, mounts PVC at `/data` and SSH key at `/ssh` |
| `Service` | ClusterIP exposing port `9090` |
| `Secret` | `ssh-key` entry with the deploy private key |

### StatefulSet essentials

```yaml
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: vault-<repo>
spec:
  replicas: 1
  serviceName: vault-<repo>
  template:
    spec:
      containers:
        - name: service
          image: bborbe/git-rest:v0.12.0
          env:
            - name: LISTEN
              value: ':9090'
            - name: REPO
              value: '/data'
            - name: GIT_REMOTE_URL
              value: 'git@github.com:owner/repo.git'
            - name: GIT_SSH_KEY
              value: '/ssh/id_ed25519'
            - name: GIT_USER_NAME
              value: 'vault-<repo>'
            - name: GIT_USER_EMAIL
              value: 'vault-<repo>@example.com'
            - name: PULL_INTERVAL
              value: '30s'
            - name: VAULT_WRITE_MODE
              value: 'true'  # set on vault-write pods; omit (or 'false') for human-touched repo pods
          ports:
            - containerPort: 9090
              name: http
          livenessProbe:
            httpGet: { path: /healthz, port: 9090 }
            initialDelaySeconds: 30
          readinessProbe:
            httpGet: { path: /readiness, port: 9090 }
            initialDelaySeconds: 15
          volumeMounts:
            - { name: datadir, mountPath: /data }
            - { name: ssh-key, mountPath: /ssh, readOnly: true }
      volumes:
        - name: ssh-key
          secret:
            secretName: vault-<repo>
            items:
              - { key: ssh-key, path: id_ed25519, mode: 0600 }
  volumeClaimTemplates:
    - metadata: { name: datadir }
      spec:
        accessModes: [ReadWriteOnce]
        resources:
          requests: { storage: 1Gi }
```

### Replicas must stay at 1

git-rest owns a local git working tree. Running multiple replicas against the same PVC will cause lock contention and conflicting commits. Use one pod per repo; scale horizontally by deploying separate StatefulSets for separate repos.

### PVC sizing

The PVC holds the full cloned repo. Size it to the repo's on-disk footprint plus headroom for growth. A vault of a few thousand markdown files fits comfortably in `1Gi`.

### SSH key provisioning

1. Generate a key: `ssh-keygen -t ed25519 -f deploy-key -N ''`.
2. Add the public key to the target repo as a **Deploy key with write access**.
3. Store the private key in your secret manager (e.g. TeamVault).
4. Inject it into the Kubernetes secret as the `ssh-key` entry.

The volume must mount with `mode: 0600` — OpenSSH rejects world-readable private keys.

### Health & readiness

- `livenessProbe` → `/healthz`: restarts the pod if the HTTP server hangs.
- `readinessProbe` → `/readiness`: removes the pod from the Service only when the served repository path is missing or unreadable. Sync failures — a rejected push, a failed fetch, a merge conflict — do not affect readiness; they stay visible in the pod log and in `git_rest_git_operation_errors_total`.

`initialDelaySeconds` on liveness should be generous on first boot to cover the initial `git clone` on large repos.

### Resources

Baseline for a small-to-medium vault:

```yaml
resources:
  limits:   { cpu: 500m, memory: 100Mi }
  requests: { cpu: 20m,  memory: 50Mi  }
```

Raise memory if the repo is large (>100 MB working tree) or if write volume is high.

### Upgrades

- New image tag → rolling restart re-pulls the image; existing PVC data is reused, so no re-clone is needed. The deployed tag is pinned by the `VERSION` variable in `~/Documents/workspaces/nuke/git-rest/Makefile`, which is the source of truth for what gets applied.
- Major config changes (e.g. new remote URL) → wipe the PVC or move the pod to a fresh PVC so bootstrap re-clones.

### Monitoring

Scrape `/metrics` with Prometheus:

```yaml
annotations:
  prometheus.io/path: /metrics
  prometheus.io/port: "9090"
  prometheus.io/scrape: "true"
```

Key metrics: request count + latency histogram, git operation durations, quarantine backlog, build info.

Write coalescing:

| Series | Meaning |
|--------|---------|
| `git_rest_write_flushes_total{outcome}` | Counter over coalesced flush attempts, `outcome` = `ok` or `error`. Pre-initialised to zero at process start. |
| `git_rest_write_coalesced_total` | Counter over writes absorbed into a coalesced batch. A three-write burst that lands as one commit increments it by 3. |
| `git_rest_write_pending` | Gauge: paths currently staged and waiting for a coalesced commit. Returns to 0 after a successful flush. |

Baseline: 12,490 `git-rest:` commits in the trailing 24 hours on `bborbe/obsidian-openclaw`, measured 2026-10-21. Coalescing is expected to reduce this by roughly the burst ratio; a return toward the baseline means the flusher is not firing.

### Claim refs

Claim refs are documented in the [API reference](api.md#claim-refs). One counter covers them:

| Series | Meaning |
|--------|---------|
| `git_rest_ref_requests_total` | Counter over claim-ref requests, labelled `op` (`get`, `put`, `delete`) and `result` (the HTTP status code). The common op/result pairs are pre-initialised to zero at process start. |

### Quarantine backlog

Quarantined files accumulate under `_conflicts/` in the served repo until an operator drains them. Two series make that visible:

| Series | Meaning |
|--------|---------|
| `git_rest_quarantined_files_total` | Monotonic counter over lifetime quarantine *events*. It never decreases, so it cannot express a backlog. |
| `git_rest_quarantined_backlog` | Gauge: regular files currently under `_conflicts/`, counted recursively. Refreshed once per pull cycle. A missing `_conflicts/` directory reports 0; an unreadable one reports 0 and logs one WARN, and never fails a pull. |

Every vault with `alerts.enabled=true` also gets an alert, `VaultObsidian<Realm>QuarantineBacklog`: `git_rest_quarantined_backlog{app="vault-obsidian-<name>"} > 0` for `1h`, severity `warning`. The one-hour window keeps a single transient quarantine event from paging an operator who is already handling it.

To drain: inspect `_conflicts/` inside the pod, repair the file or deliberately discard it, then remove it from the repo. The gauge follows the directory, so a removal is reflected on the next pull cycle and the alert clears once the count returns to zero. Quarantine surfaces and preserves; the drain is a signal, not an automatic repair.

## Operational notes

- **Auto-commit noise**: with coalescing enabled (the default) a burst of writes produces one commit and one push per window. With `--write-coalesce=false` every write produces a commit as before.
- **Conflict handling**: git-rest auto-recovers from divergence (local ahead AND remote ahead, no content conflict) by rebase + push within one PullInterval. Real content conflicts during rebase leave the repo in conflicted state and require human inspection (`kubectl exec` + manual resolve, or PVC reset for recoverable churn); `/readiness` stays `200` because the pod can still serve, and the conflict is visible in the pod log and through the `VaultObsidian<Realm>RebaseConflict` alert.
- **Vault-write mode**: set `VAULT_WRITE_MODE=true` (or `--vault-write`) on pods that serve agent vault writes. The pod then uses `YAMLMergeResolver`: on a merge conflict in a markdown file with YAML frontmatter, the resolver deep-merges frontmatter keys (theirs wins on overlap) and combines bodies, producing a syntactically valid file. A **scalar frontmatter** conflict — one whose hunks lie entirely inside the frontmatter and whose lines are all single-line `key: value` scalars — is auto-resolved in place instead of quarantined: allowlisted counters (`trigger_count`, `retry_count`, `metrics_*`) take the greater integer, every other scalar takes the newer merge parent's value (a tie goes to the local side), and `git_rest_autoresolved_total` increments once per resolved path. Every other shape — non-scalar hunks, hunks outside the frontmatter, one-sided keys, duplicate keys — keeps the quarantine path described below. On YAML parse failure or missing frontmatter delimiters, the resolver fails for that file only: the puller copies the conflicted bytes to `_conflicts/<path>.<unix-ts>.md`, restores the canonical from `HEAD`, and continues the merge, so one corrupt file no longer wedges the pod. The merge aborts only when every conflicted path fails both resolve and quarantine, or when a pre-flight guard rejects the merge — an unsafe path, a conflicted path that already lives under `_conflicts/`, or `_conflicts/` existing as a regular file. Leave `VAULT_WRITE_MODE` unset (or `false`) on pods serving human-touched repos — they continue using `MarkerResolver`. Watch `git_rest_resolver_failures_total{category}` on `/metrics` to distinguish failure modes (`yaml_parse_failed`, `no_frontmatter`, `write_failed`, `git_add_failed`, `unsafe_path`, `quarantine_io_failed`, `nested_source`).
- **Backups**: since data lives in the remote, the PVC is effectively a cache. Losing it triggers a re-clone on next bootstrap.

### Dirty working tree rescue

**Trigger.** The served repo's working tree holds uncommitted changes to tracked files while the remote has moved. Whichever sync path runs — fast-forward, the diverged merge, the push retry, or the startup sync — git refuses to clobber the working tree, so the sync aborts. Before this rescue existed, that wedged the pod indefinitely: the repository stopped advancing and consumers saw stale content until an operator restarted the pod. Readiness is decoupled from sync outcome, so the pod kept serving while its content went stale. It now self-heals within one pull interval.

**What the puller does.** On that failure it captures the whole working-tree state — staged, unstaged, deletions and untracked alike — as a commit on a `rescue/<timestamp>` branch (`rescue/20060102T150405Z`, ISO-8601 basic UTC), pushes that branch to the vault's own remote, and only after the push succeeds returns the working tree to a clean state. `git reset --hard` targets the upstream tracking ref on the fast-forward path, where HEAD holds no unique commit; on the diverged, push-retry and startup paths it targets `HEAD`, so the local commits those paths are about to merge survive. `git clean -fd` then removes untracked residue. `.gitignore`d paths are excluded by construction, so secrets and keys never leave the pod. The rescue branch is never force-pushed and never deleted.

**Why a rescue branch and not a stash.** A stash is local-only: invisible to anyone but the operator holding the volume, and gone with the PVC. A rescue branch is recoverable from any clone of the vault's remote and survives the pod being replaced. An operator recovering a stuck pod by hand may still choose `git stash` or `git commit -am rescue`; the service now prefers the branch.

**A rejected rescue push.** If the push is rejected (ruleset, auth, network), nothing is reset and nothing is cleaned — HEAD stays put, the tree stays dirty — and the pod log carries `rescue push failed, leaving repo for inspection`. The local `rescue/<ts>` ref is created *before* the push precisely so the operator can finish the job by hand: `git -C <repo> push origin rescue/<ts>`, then confirm with `git ls-remote origin 'refs/heads/rescue/*'`. The pod stays ready throughout — readiness tracks whether the served repository path is readable, not whether a push has succeeded — and the rescue branch appears on the remote once the push succeeds.

**Stale locks.** Before each pull cycle the puller removes stale `*.lock` files under `.git/` — every lock file, not only `index.lock` — but only when no live process holds an open file descriptor on it. The probe uses `lsof` on macOS and `/proc/<pid>/fd` on Linux; if neither can decide, the lock is left in place and a warning is logged. A removal logs `stale lock removed during pull cycle` with the path. The boot-time cleanup is unchanged and remains unconditional.

**Observing the rescue.** Each successful rescue logs exactly one INFO line beginning `rescue branch pushed`, naming the branch, the file count and the captured paths — e.g. `rescue branch pushed branch=rescue/20260925T120000Z files=3 paths=tasks/x.md,tasks/scratch.md,tasks/doomed.md`. Paths are logged, not only a count, so an unexpected publication is visible in the pod log and not only on the remote.

| Series | Meaning |
|--------|---------|
| `git_rest_pull_rescues_total` | Monotonic counter over lifetime rescue *events*, incremented once per successful rescue. Registered and pre-initialised to zero at process start, so the series is visible on `/metrics` before the first rescue. |
