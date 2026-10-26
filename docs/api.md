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
| `POST` | `/api/v1/files/{path}` | Create or overwrite a file (written and staged; coalesced commit + push) |
| `DELETE` | `/api/v1/files/{path}` | Delete a file (auto-commits + pushes) |
| `GET` | `/api/v1/refs?prefix=claims/` | List claim refs from the local claim mirror (falls back to the origin when stale) |
| `PUT` | `/api/v1/refs/claims/{key}` | Create or renew a claim ref (compare-and-swap) |
| `DELETE` | `/api/v1/refs/claims/{key}?expected_sha=<sha>` | Delete a claim ref (compare-and-swap) |
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

Create or overwrite a file. On success git-rest writes the content and stages it; a background flusher commits and pushes the accumulated writes within the coalescing window (`--write-coalesce-delay`, default `30s`, or `--write-coalesce-max-delay`, default `120s`). Set `--write-coalesce=false` to restore the previous synchronous write-commit-push behaviour. In local-only mode (no `--git-remote-url`) the commit stays on disk and no push is attempted. A write whose bytes match the file already on disk stages nothing and produces no commit.

```bash
curl -X POST \
  -H 'Content-Type: application/octet-stream' \
  --data-binary @local-file.md \
  http://localhost:9090/api/v1/files/30%20Analysis/my-note.md
```

Response: `{"ok":true}` on success.

| Status | Meaning |
|--------|---------|
| `200` | File written and staged |
| `400` | Invalid path (e.g. path traversal) |
| `409` | `create_only=1` and the path is already occupied |
| `413` | Request body exceeds 10 MB limit |
| `422` | Overwrite would drop a task file's frontmatter |

Overwriting an existing file whose parent directory is `25 Tasks` or `tasks` is refused with `422` when the request body carries no `---` frontmatter block; the existing file is left untouched. New files in those directories and overwrites that keep frontmatter are written normally.

A task file's top-level `phase:` line is normalized on every write: the documented alias `phase: in_progress` is stored as `phase: execution`, matching vault-cli's `NormalizeTaskPhase`. No other phase value and no `status:` line is rewritten.

Body limit: `10 * 1024 * 1024` bytes (10 MiB).

Commit message format: `git-rest: create <path>` or `git-rest: update <path>`. A coalesced batch spanning more than one path uses `git-rest: update <n> files`.

## Delete File

Remove a file synchronously: pending coalesced writes are flushed first, then the deletion is committed and pushed.

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

## Claim Refs

Claim refs are lightweight, content-free refs under `refs/claims/` that record
which cluster and task hold a claim until when. Reads are served from a local
claim mirror (falling back to the live origin when it is stale) and writes use
an origin-enforced compare-and-swap, so two callers can never both believe they
won.

Only refs under `refs/claims/` are reachable through these endpoints. The
`{key}` is the full path below `refs/`, e.g. `claims/task/t-42`. Any key that
does not start with `claims/`, contains `.` or `..` components, an empty
component, a refspec metacharacter or an invalid ref name is rejected with
`400` before any origin operation. The served branch is never touched.

A 409 must not be retried: another caller won the race or the claim already moved.
On a `500` the caller may retry on its own schedule, but must first list the ref
to learn the true state. git-rest itself never retries a push.

Every claim-ref request is counted in
`git_rest_ref_requests_total{op,result}`, with `op` in `get`, `put`, `delete`
and `result` equal to the HTTP status code.

### List Claim Refs

Request: `GET /api/v1/refs?prefix=claims/<sub-prefix>`. `prefix` is required
and must start with `claims/`.

```bash
curl 'http://localhost:9090/api/v1/refs?prefix=claims/task'
```

The read is served from a locally maintained claim mirror under
`refs/claim-mirror/*`, which the puller's periodic fetch refreshes. While the
mirror is fresh the request path makes no network call. A read **falls back**
to the live origin when the mirror is older than the staleness bound — twice the pull interval with a minimum of 60 seconds — or was never configured. A
claim written by another cluster is visible after at most one pull cycle. Each
read logs one INFO line naming its source: `served_from=mirror` with an
`age_s=` field, or `served_from=origin`.

Response: a JSON array, sorted by `ref`, of objects with exactly `ref`, `sha`,
`cluster`, `task_id` and `lease_until`:

```json
[
  {
    "ref": "refs/claims/task/t-42",
    "sha": "4b825dc642cb6eb9a060e54bf8d69288fbee4904",
    "cluster": "cluster-a",
    "task_id": "t-42",
    "lease_until": "2026-10-21T12:00:00Z"
  }
]
```

An empty result is `[]`, never `null`.

Prefix matching is by whole path components: `prefix=claims/task/1` returns only
`refs/claims/task/1`; `prefix=claims/task` returns `refs/claims/task/1` and
`refs/claims/task/10` but never `refs/claims/tasks/1`. A prefix ending in `/`
matches everything below it.

`nonce` is never returned. A commit whose message is not JSON is still listed,
with empty `cluster`, `task_id` and `lease_until`, and a warning in the log.

A claim whose ref is deleted or moved between listing and fetching is dropped
from the result, and a warning is logged. If a claim commit cannot be fetched
while its ref still points at it, or the origin cannot be re-listed, the request
answers `500`.

| Status | Meaning |
|--------|---------|
| `200` | Claim refs listed (possibly empty) |
| `400` | Missing or invalid `prefix` |
| `500` | Origin unreachable, no origin configured, or a listed claim commit could not be fetched |

### Create or Renew a Claim Ref

```bash
curl -X PUT \
  -H 'Content-Type: application/json' \
  -d '{"expected_sha":"","cluster":"cluster-a","task_id":"t-42","lease_until":"2026-10-21T12:00:00Z"}' \
  http://localhost:9090/api/v1/refs/claims/task/t-42
```

Request body:

- `cluster` and `task_id` are required and must be non-empty.
- `lease_until` is RFC3339 and stored verbatim; it is never compared to a
  clock, so expiry is the caller's concern.
- `expected_sha` is empty (the ref must not exist) or a full-length lowercase
  hex object id (40 or 64 characters).
- A `nonce` supplied in the body is ignored; the server generates its own.

Each successful call writes a new parentless commit on the empty tree whose
message carries the metadata and a server-generated nonce, so a repeated
identical request does not reproduce the same sha.

Response: `{"ref":"refs/claims/<key>","sha":"<new sha>"}`.

Body limit: 64 KiB.

| Status | Meaning |
|--------|---------|
| `200` | Claim ref created or renewed |
| `400` | Invalid key, body or field |
| `409` | The ref did not match `expected_sha`; the body `error.code` is `CLAIM_CONFLICT` |
| `413` | Request body exceeds 64 KiB |
| `500` | No origin remote, transport/auth failure, hook decline or timeout |

### Delete a Claim Ref

Request: `DELETE /api/v1/refs/claims/{key}?expected_sha=<sha>`. `expected_sha`
is required and must be a full-length lowercase hex object id.

```bash
curl -X DELETE 'http://localhost:9090/api/v1/refs/claims/task/t-42?expected_sha=4b825dc642cb6eb9a060e54bf8d69288fbee4904'
```

Response: `{"ok":true}`.

| Status | Meaning |
|--------|---------|
| `200` | Claim ref deleted |
| `400` | Invalid key or missing/invalid `expected_sha` |
| `409` | The ref did not match `expected_sha` or was already absent; the body `error.code` is `CLAIM_CONFLICT` |
| `500` | Origin unreachable or no origin configured |

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
- Claim-ref request counter `git_rest_ref_requests_total{op,result}`

## Path Handling

- `{path}` is taken verbatim after the prefix — URL-encode spaces (`%20`) and other reserved chars.
- Paths are normalised and rejected if they escape the repo (path traversal → `ErrInvalidPath` → `400`).
- Nested directories are auto-created on write.

## Error Responses

Errors are returned as JSON with a structured `error` object carrying a machine-readable
`code` and a human-readable `message`, e.g.:

```json
{"error":{"code":"CLAIM_CONFLICT","message":"push refs/claims/task/t-42 rejected"}}
```

A claim ref `PUT` or `DELETE` that loses its compare-and-swap lease race returns
`409` with `error.code` set to `CLAIM_CONFLICT`. The HTTP status code carries the
primary signal; the body is advisory.
