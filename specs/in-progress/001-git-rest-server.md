---
status: prompted
approved: "2026-04-11T19:23:50Z"
generating: "2026-04-11T19:24:04Z"
prompted: "2026-04-11T19:26:54Z"
branch: dark-factory/git-rest-server
---

## Summary

- Lightweight HTTP server exposing file read/write/delete operations on a local git repository
- Automatic `git add`, `git commit`, and `git push` on every write/delete
- Periodic `git pull` to stay in sync with remote
- Single binary, no database, no web UI
- Designed for deployment as a K8s StatefulSet with a PVC-backed git repo

## Problem

Agents running as K8s Jobs need to read/write files in Obsidian vaults (git repos). Mounting vaults directly requires git-sync sidecars, PVCs, and complex coordination. A centralized REST API for vault file operations eliminates mount complexity and enables multiple consumers.

## Goal

A running `git-rest` server accepts HTTP requests to read, create, update, and delete files in a git repository. Every mutation is committed and pushed. The server periodically pulls to incorporate remote changes. Consumers interact via standard HTTP — no git knowledge required.

## Assumptions

- Git binary available on PATH
- Repo already cloned and configured with remote (server does not clone)
- Single branch operation (no branch switching)
- Network access to git remote for push/pull
- Text files only — binary file handling is not optimized but not blocked

## Non-Goals

- Full git server (clone, branches, merge) — this is file CRUD only
- Web UI or file browser
- Authentication/authorization (handled by K8s network policy)
- Conflict resolution (fail on conflict, log error)
- Kafka integration (future iteration)

## Desired Behavior

1. `GET /api/v1/files/{path}` returns file content with 200, or 404 if not found
2. `POST /api/v1/files/{path}` creates or updates a file (body is raw file content), runs `git add + commit + push`, returns 200
3. `DELETE /api/v1/files/{path}` removes a file, runs `git add + commit + push`, returns 200 or 404
4. `GET /api/v1/files/` with query param `?glob=pattern` lists matching file paths as JSON array
5. Server runs `git pull` on a configurable interval (default 30s)
6. `GET /healthz` returns 200 when server is ready
7. `GET /readiness` returns 200 only when the git repo working tree is clean and has no unpushed commits
8. `GET /metrics` exposes Prometheus metrics (request counts, git operation durations, errors)

## Constraints

- Git operations use the system `git` binary, not an embedded library
- Git operations must be serialized to prevent concurrent commit conflicts
- Commit messages: `git-rest: create {path}`, `git-rest: update {path}`, `git-rest: delete {path}`
- Error responses are JSON: `{"error": "message"}`
- Must follow project coding conventions (see `docs/dod.md`)

## Failure Modes

| Trigger | Expected Behavior | Recovery |
|---------|-------------------|----------|
| `git push` fails (conflict) | Return 500 with error, log warning, readiness returns non-200 | Next `git pull` attempts merge; if pull also conflicts, manual intervention required |
| `git pull` fails (network) | Log warning, continue serving | Automatic retry on next interval |
| File path traversal (`../`) | Return 400 Bad Request | Input validation |
| Repo directory missing | Fail startup with clear error | Fix mount / config |
| Concurrent write requests | Serialized via mutex, no data corruption | N/A |

## Do-Nothing Option

Agents need git-sync sidecars or PVC mounts per vault. Each new vault consumer adds deployment complexity (sidecar config, credentials, volume coordination). The trade-analysis agent is currently blocked — it cannot write analysis files to the trading vault without a mount. Manual workaround: SSH into the cluster and copy files, which is not sustainable.

## Security / Abuse

- Path traversal: validate all paths are relative and within repo root (no `..`, no absolute paths)
- No auth — relies on K8s network policy (ClusterIP service, no Ingress)
- Request body size limit (10MB default) to prevent abuse
- No shell injection: file paths passed as arguments to git, never interpolated into shell strings

## Acceptance Criteria

- [ ] `GET /api/v1/files/README.md` returns file content
- [ ] `POST /api/v1/files/test.md` with body creates file, commits, pushes
- [ ] `POST /api/v1/files/test.md` with different body updates file, commits, pushes
- [ ] `DELETE /api/v1/files/test.md` removes file, commits, pushes
- [ ] `GET /api/v1/files/?glob=*.md` returns JSON array of matching paths
- [ ] `GET /api/v1/files/../../../etc/passwd` returns 400
- [ ] Server pulls from remote on configured interval
- [ ] `/healthz` and `/readiness` return 200
- [ ] `/metrics` exposes Prometheus counters (request counts, git operation durations, errors)
- [ ] `make precommit` passes
- [ ] Concurrent POST requests are serialized (no git lock errors)

## Verification

```bash
make precommit
```
