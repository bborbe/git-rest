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

Pass a remote URL + SSH key. git-rest clones on startup and pushes on every write.

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
- `readinessProbe` → `/readiness`: removes the pod from the Service while git operations are in-flight or pushes are pending.

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

### Quarantine backlog

Quarantined files accumulate under `_conflicts/` in the served repo until an operator drains them. Two series make that visible:

| Series | Meaning |
|--------|---------|
| `git_rest_quarantined_files_total` | Monotonic counter over lifetime quarantine *events*. It never decreases, so it cannot express a backlog. |
| `git_rest_quarantined_backlog` | Gauge: regular files currently under `_conflicts/`, counted recursively. Refreshed once per pull cycle. A missing `_conflicts/` directory reports 0; an unreadable one reports 0 and logs one WARN, and never fails a pull. |

Every vault with `alerts.enabled=true` also gets an alert, `VaultObsidian<Realm>QuarantineBacklog`: `git_rest_quarantined_backlog{app="vault-obsidian-<name>"} > 0` for `1h`, severity `warning`. The one-hour window keeps a single transient quarantine event from paging an operator who is already handling it.

To drain: inspect `_conflicts/` inside the pod, repair the file or deliberately discard it, then remove it from the repo. The gauge follows the directory, so a removal is reflected on the next pull cycle and the alert clears once the count returns to zero. Quarantine surfaces and preserves; the drain is a signal, not an automatic repair.

## Operational notes

- **Auto-commit noise**: every write produces a commit. For high-write workloads, upstream consumers should accept this or batch through a higher-level API.
- **Conflict handling**: git-rest auto-recovers from divergence (local ahead AND remote ahead, no content conflict) by rebase + push within one PullInterval. Real content conflicts during rebase leave the repo in conflicted state, readiness reports 503 with the conflict path (`last pull failed: rebase conflict at <path>`), and require human inspection (`kubectl exec` + manual resolve, or PVC reset for recoverable churn).
- **Vault-write mode**: set `VAULT_WRITE_MODE=true` (or `--vault-write`) on pods that serve agent vault writes. The pod then uses `YAMLMergeResolver`: on a merge conflict in a markdown file with YAML frontmatter, the resolver deep-merges frontmatter keys (theirs wins on overlap) and combines bodies, producing a syntactically valid file. On YAML parse failure or missing frontmatter delimiters, the resolver fails for that file only: the puller moves the file to `_conflicts/<path>.<unix-ts>.md` and continues the merge, so one corrupt file no longer wedges the pod. The merge aborts only when every conflicted path fails both resolve and quarantine, or when a pre-flight guard rejects the merge — an unsafe path, a conflicted path that already lives under `_conflicts/`, or `_conflicts/` existing as a regular file. Leave `VAULT_WRITE_MODE` unset (or `false`) on pods serving human-touched repos — they continue using `MarkerResolver`. Watch `git_rest_resolver_failures_total{category}` on `/metrics` to distinguish failure modes (`yaml_parse_failed`, `no_frontmatter`, `write_failed`, `git_add_failed`, `unsafe_path`, `quarantine_io_failed`, `nested_source`).
- **Backups**: since data lives in the remote, the PVC is effectively a cache. Losing it triggers a re-clone on next bootstrap.

### Dirty working tree rescue

**Trigger.** The served repo's working tree holds uncommitted changes to tracked files while the remote has moved. `git merge --ff-only` refuses rather than clobbering the working tree, so the pull aborts. Before this rescue existed, that wedged the pod indefinitely — readiness reported not-ready on every cycle, and every consumer of the vault (including the agent-task-executor reconcile loop) stalled behind it. It now self-heals within one pull interval.

**What the puller does.** On that failure it captures the whole working-tree state — staged, unstaged, deletions and untracked alike — as a commit on a `rescue/<timestamp>` branch (`rescue/20060102T150405Z`, ISO-8601 basic UTC), pushes that branch to the vault's own remote, and only after the push succeeds returns the working tree to the remote state (`git reset --hard` on the upstream tracking ref, then `git clean -fd`). `.gitignore`d paths are excluded by construction, so secrets and keys never leave the pod. The rescue branch is never force-pushed and never deleted.

**Why a rescue branch and not a stash.** A stash is local-only: invisible to anyone but the operator holding the volume, and gone with the PVC. A rescue branch is recoverable from any clone of the vault's remote and survives the pod being replaced. An operator recovering a stuck pod by hand may still choose `git stash` or `git commit -am rescue`; the service now prefers the branch.

**A rejected rescue push.** If the push is rejected (ruleset, auth, network), nothing is reset and nothing is cleaned — HEAD stays put, the tree stays dirty — and the pod log carries `rescue push failed, leaving repo for inspection`. The local `rescue/<ts>` ref is created *before* the push precisely so the operator can finish the job by hand: `git -C <repo> push origin rescue/<ts>`, then confirm with `git ls-remote origin 'refs/heads/rescue/*'`. Readiness returns to ready within one pull interval after the push succeeds.

**Observing the rescue.** Each successful rescue logs exactly one INFO line beginning `rescue branch pushed`, naming the branch, the file count and the captured paths — e.g. `rescue branch pushed branch=rescue/20260925T120000Z files=3 paths=tasks/x.md,tasks/scratch.md,tasks/doomed.md`. Paths are logged, not only a count, so an unexpected publication is visible in the pod log and not only on the remote.

| Series | Meaning |
|--------|---------|
| `git_rest_pull_rescues_total` | Monotonic counter over lifetime rescue *events*, incremented once per successful rescue. Registered and pre-initialised to zero at process start, so the series is visible on `/metrics` before the first rescue. |
