# git-rest

Lightweight REST API server for reading and writing files in a git repository. Auto-commits and pushes on writes, periodic pull keeps the local clone in sync.

## Use Cases

- Expose a git repo to non-git clients (HTTP-only consumers, agents, K8s pods)
- Serve a vault/wiki/notes repo over HTTP without a database or web app
- Multi-instance deploys: one git-rest per repo, sharing nothing but the remote

## Features

- File CRUD over HTTP — `GET`, `POST`, `DELETE` on `/api/v1/files/{path}`
- Glob-based file listing
- Auto-clone on startup if `--git-remote-url` is set and `--repo` is empty
- Auto-init local-only repos when no remote URL is configured
- Periodic `git pull` to stay in sync with the remote
- Auto `git add + commit + push` on every write/delete
- Optional SSH key + git author config
- Health and readiness endpoints (readiness reflects git status)
- Prometheus metrics on `/metrics`

## Quick Start

```bash
# local repo, no remote (file CRUD only)
git-rest --repo /tmp/my-repo --listen :8080

# remote repo, auto-clone over SSH
git-rest \
  --repo /data \
  --listen :8080 \
  --git-remote-url git@github.com:owner/repo.git \
  --git-ssh-key /ssh/id_ed25519 \
  --git-user-name git-rest \
  --git-user-email git-rest@example.com \
  --pull-interval 30s
```

## Configuration

All flags can be set as environment variables.

| Flag | Env Var | Required | Default | Description |
|------|---------|----------|---------|-------------|
| `--listen` | `LISTEN` | yes | `:8080` | HTTP listen address |
| `--repo` | `REPO` | yes | — | Path to git repository on disk |
| `--pull-interval` | `PULL_INTERVAL` | no | `30s` | Periodic `git pull` interval |
| `--git-remote-url` | `GIT_REMOTE_URL` | no | — | Remote URL to clone on startup (omit for local-only) |
| `--git-ssh-key` | `GIT_SSH_KEY` | no | — | Path to SSH private key for git operations |
| `--git-user-name` | `GIT_USER_NAME` | no | — | Git author name for commits |
| `--git-user-email` | `GIT_USER_EMAIL` | no | — | Git author email for commits |
| `--sentry-dsn` | `SENTRY_DSN` | no | — | Sentry DSN for error reporting |
| `--sentry-proxy` | `SENTRY_PROXY` | no | — | HTTP proxy for Sentry |

Build metadata (set via build flags):

| Flag | Env Var | Default |
|------|---------|---------|
| `--build-git-version` | `BUILD_GIT_VERSION` | `dev` |
| `--build-git-commit` | `BUILD_GIT_COMMIT` | `none` |
| `--build-date` | `BUILD_DATE` | — |

## Bootstrap Modes

git-rest decides what to do at startup based on `--git-remote-url` and the state of `--repo`:

| Remote URL | `--repo/.git` exists | Action |
|-----------|---------------------|--------|
| set | no | `git clone` into `--repo` |
| set | yes | use existing repo, periodic pull |
| empty | no | `git init` empty repo, no push/pull |
| empty | yes | use existing repo, no push/pull |

Local-only mode (no remote) skips all push/pull operations — useful for testing or single-node deployments.

## Documentation

- [API Reference](docs/api.md) — full endpoint reference with curl examples
- [Deployment Guide](docs/deployment.md) — Kubernetes + standalone setup
- [Definition of Done](docs/dod.md) — internal release criteria

## License

BSD-2-Clause
