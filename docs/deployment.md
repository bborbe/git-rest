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

Reference deployment: `trading-agent-trade-analysis/vault/obsidian-trading/k8s/` (see [`vault-obsidian-trading-sts.yaml`](https://github.com/bborbe/trading/tree/master/vault/obsidian-trading/k8s)).

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

- New image tag → rolling restart re-pulls the image; existing PVC data is reused, so no re-clone is needed.
- Major config changes (e.g. new remote URL) → wipe the PVC or move the pod to a fresh PVC so bootstrap re-clones.

### Monitoring

Scrape `/metrics` with Prometheus:

```yaml
annotations:
  prometheus.io/path: /metrics
  prometheus.io/port: "9090"
  prometheus.io/scrape: "true"
```

Key metrics: request count + latency histogram, git operation durations, build info.

## Operational notes

- **Auto-commit noise**: every write produces a commit. For high-write workloads, upstream consumers should accept this or batch through a higher-level API.
- **Conflict handling**: git-rest has no rebase/merge-conflict strategy — if a periodic pull encounters divergence, the push will fail and the pod will go unready until resolved manually.
- **Backups**: since data lives in the remote, the PVC is effectively a cache. Losing it triggers a re-clone on next bootstrap.
