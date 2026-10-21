# API Reference

HTTP endpoints exposed by git-rest. All file operations are under `/api/v1/files/`.

## Base URL

`http://<host>:<listen-port>`

Default port: `9090` (set via `--listen`).

## Endpoints

| Method | Path | Purpose |
|--------|------|---------|
| `GET` | `/api/v1/files/{path}` | Read file contents |
| `GET` | `/api/v1/files/{path}?glob=<pattern>` | List files matching glob |
| `POST` | `/api/v1/files/{path}` | Create or overwrite a file (auto-commits + pushes) |
| `DELETE` | `/api/v1/files/{path}` | Delete a file (auto-commits + pushes) |
| `GET` | `/healthz` | Liveness probe |
| `GET` | `/readiness` | Readiness probe (serves while the repository path is readable) |
| `GET` | `/metrics` | Prometheus metrics |

`{path}` is a relative path within the repo. Leading `/api/v1/files/` is stripped and the remainder is used as-is.

## Get File

Read a file from the repo.

```bash
curl http://localhost:9090/api/v1/files/README.md
```

Response: raw file bytes with `Content-Type: application/octet-stream`.

| Status | Meaning |
|--------|---------|
| `200` | File contents in body |
| `404` | File not found |
| `400` | Invalid path (e.g. path traversal) |

## List Files

Pass `?glob=<pattern>` on a `GET` to list files instead of reading one. The path component of the URL is ignored — only the query param matters.

```bash
# list all markdown files at repo root
curl 'http://localhost:9090/api/v1/files/?glob=*.md'

# single-level subdirectory match
curl 'http://localhost:9090/api/v1/files/?glob=30+Analysis/*.md'
```

Response: JSON array of paths, e.g. `["README.md","CHANGELOG.md"]`.

Glob semantics: Go's `filepath.Match` — single-level wildcard only. `**` (doublestar) is **not** supported.

## Write File

Create or overwrite a file. On success git-rest stages the change, commits, and pushes (if `--git-remote-url` is set).

```bash
curl -X POST \
  -H 'Content-Type: application/octet-stream' \
  --data-binary @local-file.md \
  http://localhost:9090/api/v1/files/30%20Analysis/my-note.md
```

Response: `{"ok":true}` on success.

| Status | Meaning |
|--------|---------|
| `200` | File written + committed |
| `400` | Invalid path (e.g. path traversal) |
| `413` | Request body exceeds 10 MB limit |

Body limit: `10 * 1024 * 1024` bytes (10 MiB).

Commit message format: `git-rest: create <path>` or `git-rest: update <path>`.

## Delete File

Remove a file. Auto-commits + pushes.

```bash
curl -X DELETE http://localhost:9090/api/v1/files/30%20Analysis/my-note.md
```

Response: `{"ok":true}` on success.

| Status | Meaning |
|--------|---------|
| `200` | File deleted + committed |
| `404` | File not found |
| `400` | Invalid path |

Commit message format: `git-rest: delete <path>`.

## Healthz

```bash
curl http://localhost:9090/healthz
```

Always returns `200 OK` with body `ok` if the process is alive. Used for Kubernetes liveness probes.

## Readiness

```bash
curl http://localhost:9090/readiness
```

Returns `200 OK` with body `ok` when the process can serve its API: the HTTP
server is listening and the served repository path is readable.

Returns `503 Service Unavailable` with body `repo path unreadable` when the
served repository path is missing or unreadable. Used for Kubernetes readiness
probes.

Readiness does not depend on the outcome of the most recent sync. A rejected
push, a failed fetch, or a merge conflict leaves the probe at `200`; those
failures are reported through the pod log (`git pull failed`) and through
`git_rest_git_operation_errors_total{operation=...}`.

## Metrics

```bash
curl http://localhost:9090/metrics
```

Prometheus text format. Includes:

- Build info (version, commit)
- HTTP request metrics (count, duration, status)
- Git operation metrics (clone, pull, push, commit durations)

## Path Handling

- `{path}` is taken verbatim after the prefix — URL-encode spaces (`%20`) and other reserved chars.
- Paths are normalised and rejected if they escape the repo (path traversal → `ErrInvalidPath` → `400`).
- Nested directories are auto-created on write.

## Error Responses

Errors are returned as JSON with an `error` field, e.g.:

```json
{"error":"file not found"}
```

The HTTP status code carries the primary signal; the body is advisory.
