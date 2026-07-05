# git-rest Helm chart

Deploys one [git-rest](https://github.com/bborbe/git-rest) instance per entry in
`vaults` — each syncs a git repository into the cluster and serves it over
REST/HTTP with a background puller (fetch + rebase) and optional write mode. Used
to host Obsidian vaults in-cluster as `vault-obsidian-<name>`, consumed by the
agent-task-controller and other services.

Per vault the chart renders:

- **StatefulSet** — git checkout on a `ReadWriteOnce` PVC (`/data`), the git-rest
  binary, liveness `/healthz` + readiness `/readiness`, ssh key mounted at `/ssh`.
- **Service** — `:9090` (http + admin).
- **Secret** *(optional)* — only when the vault inlines `secret:` and sets no
  `existingSecret`; keys `ssh-key`, `gateway-secret`, `sentry-dsn`.
- **Alert CRs** *(optional, `alerts.enabled`)* — quant `monitoring.benjamin-borbe.de/v1`
  Alerts: pull-failing, rebase-conflict, puller-silent.

## Install

```bash
helm install git-rest oci://registry-1.docker.io/bborbe/git-rest --version 0.1.0 \
  --namespace <ns> --values my-values.yaml
```

Minimal `my-values.yaml`:

```yaml
vaults:
  - name: openclaw
    repoUrl: git@github.com:bborbe/obsidian-openclaw.git
    writeMode: true                          # false/omitted = read-only (puller only)
    secret:                                  # generic cluster: chart creates the Secret
      sshKey: |-
        -----BEGIN OPENSSH PRIVATE KEY-----
        ...
      gatewaySecret: "change-me"
    storage:
      size: 1Gi
      storageClassName: standard
```

## Secrets — `existingSecret` vs inline

- **`existingSecret: <name>`** — reference a Secret you already manage (keys
  `ssh-key`, `gateway-secret`, `sentry-dsn`). Nothing is created. This is how
  **quant** consumes it (teamvault-rendered `vault-obsidian-<name>` Secrets).
- **inline `secret: {sshKey, gatewaySecret, sentryDsn?}`** — the chart creates the
  Secret. For generic clusters without an external secret manager.

## Values

| Key | Default | Notes |
|---|---|---|
| `image.registry` / `image.repository` / `image.tag` | `docker.io` / `bborbe/git-rest` / `""` (→ appVersion) | image ref |
| `image.pullPolicy` | `Always` | puller keeps mutable tags fresh; use `IfNotPresent` for pinned semver |
| `image.pullSecrets` | `[]` | private registry pull secrets |
| `logLevel` | `"2"` | glog `-v` level |
| `pullInterval` | `"30s"` | puller fetch+rebase interval |
| `sentry.proxy` | `""` | `SENTRY_PROXY` URL |
| `affinity` | `{}` | node affinity for every vault STS |
| `podSecurityContext` / `securityContext` | `{}` | overridable; empty by default |
| `resources` | 20m/50Mi → 500m/256Mi | per-vault container resources |
| `keel.enabled` | `false` | keel registry-poll auto-redeploy |
| `alerts.enabled` | `false` | quant Alert CRs (needs the CRD) |
| `vaults[]` | `[]` | `{name, repoUrl, writeMode?, existingSecret? \| secret{}, storage{size,storageClassName}}` |

## License

BSD — see the [LICENSE](../LICENSE) at the repo root.
