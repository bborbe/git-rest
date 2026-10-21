---
status: completed
spec: [018-leased-claim-ref-operations]
summary: 'Documented the claim-ref endpoints in README and docs/api.md, added the deployment metric row, and added the feat changelog bullet under a new ## Unreleased section.'
execution_id: git-rest-claim-refs-exec-071-spec-018-ref-docs-changelog
dark-factory-version: v0.198.7
created: "2026-10-21T18:06:52Z"
queued: "2026-10-21T18:17:00Z"
started: "2026-10-21T18:33:43Z"
completed: "2026-10-21T18:35:55Z"
branch: dark-factory/leased-claim-ref-operations
---

# Document the claim-ref endpoints and add the release note

<summary>
- The README feature list advertises the claim-ref endpoints and the compare-and-swap lease.
- The API reference documents listing, create/renew and delete with request and response bodies.
- The API reference carries the full `200`/`400`/`409`/`500` status table and states that a `409` must not be retried.
- The API reference explains the `refs/claims/`-only namespace and the prefix's whole-path-component matching.
- The API reference states that the list response never contains a nonce.
- The changelog gains an `## Unreleased` section with one `feat:` bullet above the current release.
- No Go file, test, route, metric or configuration changes in this prompt.
</summary>

<objective>
Close the documentation loop for the claim-ref endpoints: document the three routes, their bodies, their status contract and the no-retry rule in the API reference, advertise them in the README, and add the release note under a new `## Unreleased` heading.
</objective>

<context>
There is no repo-root `CLAUDE.md` in this worktree — do not look for one.

Read these coding-plugin guides before editing (paths inside the YOLO container):

- `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md` — doc voice, structure, and when to add a section rather than a bullet
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — `## Unreleased` rules, `feat:` prefixes, one bullet per logical change

Files to read in full before editing:

- `README.md` — the `## Features` list is the only section this prompt changes
- `docs/api.md` — the endpoint table, the per-endpoint section style, the status-table style and the error-response section
- `CHANGELOG.md` — the top of the file. The current first release heading is `## v0.31.0`; the new `## Unreleased` section goes directly above it, between the SemVer preamble and that heading.
- `pkg/handler/refs_get.go`, `pkg/handler/refs_put.go`, `pkg/handler/refs_delete.go` — the source of truth for the request fields, response bodies and status codes you document
- `pkg/git/claim_refs.go` — the source of truth for the namespace guard, the metadata fields, the nonce rule and the prefix matching

**Verified facts — do not re-derive, do not contradict:**

- The three routes are `GET /api/v1/refs?prefix=claims/…`, `PUT /api/v1/refs/claims/{key}` and `DELETE /api/v1/refs/claims/{key}?expected_sha=…`.
- The list response is a JSON array of objects with exactly `ref`, `sha`, `cluster`, `task_id`, `lease_until`; it never contains `nonce`.
- The PUT body is `{"expected_sha":…,"cluster":…,"task_id":…,"lease_until":…}`; an empty `expected_sha` means the ref must not exist. Success is `{"ref":"refs/claims/<key>","sha":"<new sha>"}`.
- DELETE requires `expected_sha`; success is `{"ok":true}`.
- Statuses: `200` success; `400` invalid name, body or query; `409` lease mismatch or absent ref; `500` no origin remote, transport/auth failure, hook decline or timeout.
- The metric is `git_rest_ref_requests_total{op,result}` with `op` in `get`, `put`, `delete`.
- The body limit is 64 KiB (`413` above it).
- The prefix matches whole path components: `claims/task/1` never matches `claims/task/10`; a prefix ending in `/` matches everything below it.
- Only `refs/claims/` is reachable through these endpoints; any other name is rejected with `400` before anything is pushed.

**Scope:** this prompt is documentation and changelog only. Do NOT edit any Go file, any test, `helm/`, or `specs/`.
</context>

<requirements>

## 1. Advertise the endpoints in `README.md`

In the `## Features` list, add one bullet near the file-CRUD bullet:

```markdown
- Claim refs under `refs/claims/` over HTTP — `GET /api/v1/refs`, `PUT` and `DELETE` on `/api/v1/refs/claims/{key}` with an origin-enforced compare-and-swap lease (a lost race is `409` and must not be retried)
```

Do not change any other README section, the configuration table, or the quick-start examples.

## 2. Document the endpoints in `docs/api.md`

Extend the endpoint table with three rows, in the order list → create/renew → delete:

```markdown
| `GET` | `/api/v1/refs?prefix=claims/` | List claim refs live from the origin |
| `PUT` | `/api/v1/refs/claims/{key}` | Create or renew a claim ref (compare-and-swap) |
| `DELETE` | `/api/v1/refs/claims/{key}?expected_sha=<sha>` | Delete a claim ref (compare-and-swap) |
```

Then add a `## Claim Refs` section (before `## Healthz`) with three subsections. Use the existing document's voice and table style.

**Namespace rule (state it once, near the top of the section):** only refs under `refs/claims/` are reachable. The `{key}` is the full path below `/refs/`, e.g. `claims/task/t-42`. Any key that does not start with `claims/`, contains `.` or `..` components, an empty component, a refspec metacharacter or an invalid ref name is rejected with `400` before any origin operation. The served branch is never touched.

**`### List Claim Refs`:**

- Request: `GET /api/v1/refs?prefix=claims/<sub-prefix>`; `prefix` is required and must start with `claims/`.
- Response: JSON array, sorted by `ref`, of `{ref, sha, cluster, task_id, lease_until}`.
- Prefix matching is by whole path components: `prefix=claims/task/1` returns only `refs/claims/task/1`; `prefix=claims/task` returns `refs/claims/task/1` and `refs/claims/task/10` but never `refs/claims/tasks/1`.
- `nonce` is never returned. A commit whose message is not JSON is still listed with empty `cluster`, `task_id` and `lease_until` and a warning in the log.
- An empty result is `[]`, never `null`.
- Include a curl example.
- Status table: `200` listed; `400` missing or invalid `prefix`; `500` origin unreachable or no origin configured.

**`### Create or Renew a Claim Ref`:**

- Request body and field rules: `cluster` and `task_id` required non-empty; `lease_until` is RFC3339 and stored verbatim (never compared to a clock); `expected_sha` is empty (the ref must not exist) or a full-length lowercase hex object id. A `nonce` supplied in the body is ignored.
- Each successful call writes a new parentless commit on the empty tree whose message carries the metadata and a server-generated nonce, so a repeated identical request does not reproduce the same sha.
- Success response `{"ref":"refs/claims/<key>","sha":"<new sha>"}`.
- Include a curl example.
- Status table: `200` created or renewed; `400` invalid key, body or field; `409` the ref did not match `expected_sha`; `413` body over 64 KiB; `500` no origin remote, transport/auth failure, hook decline or timeout.

**`### Delete a Claim Ref`:**

- Request: `DELETE /api/v1/refs/claims/{key}?expected_sha=<sha>`; `expected_sha` is required and must be a full-length lowercase hex object id.
- Success response `{"ok":true}`.
- Include a curl example.
- Status table: `200` deleted; `400` invalid key or missing/invalid `expected_sha`; `409` the ref did not match `expected_sha` or was already absent; `500` origin unreachable or no origin configured.

**No-retry rule (state it explicitly, in the shared part of the section):** include this sentence verbatim: `A 409 must not be retried: another caller won the race or the claim already moved.` On a `500` the caller may retry on its own schedule, but must first list the ref to learn the true state. git-rest itself never retries a push.

**Metric line:** mention `git_rest_ref_requests_total{op,result}` with `op` in `get`, `put`, `delete` and `result` equal to the HTTP status. In `docs/api.md` `## Metrics`, add the line "- Claim-ref request counter `git_rest_ref_requests_total{op,result}`". In `docs/deployment.md`, add a `git_rest_ref_requests_total` row to the metric table (read the file first and match the existing row style); this is a required docs change alongside the files above.

The document must contain the sentence `A 409 must not be retried: another caller won the race or the claim already moved.` and at least three occurrences of `api/v1/refs`.

Do not change the existing file-endpoint sections, the health/readiness/metrics sections, or the error-response example.

## 3. Add the changelog entry in `CHANGELOG.md`

Insert a new section directly above `## v0.31.0`, between the SemVer preamble and that heading:

```markdown
## Unreleased

- feat: Expose claim refs under `refs/claims/` over HTTP. `GET /api/v1/refs` lists claim refs live from the origin; `PUT` and `DELETE` on `/api/v1/refs/claims/{key}` create, renew and delete them with an origin-enforced compare-and-swap lease. A lost race returns `409` and is never retried; every request is counted in `git_rest_ref_requests_total{op,result}`.
```

Do not create a second `## Unreleased` heading, do not modify any existing release section, and do not reword existing bullets. If an `## Unreleased` section already exists above `## v0.31.0`, append the bullet to it instead of creating a new heading.

## 4. Self-check before finishing

Run every command in `<verification>` from the repo root. Confirm the documented statuses and bodies match the handlers, the no-retry wording is present, and `make precommit` still exits 0. Walk AC13 and AC14 against the change.

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT edit any Go file, any test file, `helm/`, or anything under `specs/`.
- Do NOT document behaviour the code does not implement: there is no lease-expiry enforcement, no server-side retry, no single-ref `GET`, no executor client, and no namespace other than `refs/claims/`.
- Do NOT add a `nonce` field to the documented list response.
- Do NOT invent a configuration flag or environment variable for the endpoints or the namespace.
- Keep the `## Unreleased` CHANGELOG section singular: append or create in place, never duplicate.
- The exact status codes must match the handlers: `200`, `400`, `409`, `500`, and `413` for an oversized PUT body.
- `make precommit` from the repo root MUST exit 0.
- The container's `.git` is masked: do not run `git diff`, `git log` or `git status` against the project repo as verification.
</constraints>

<verification>
Run from the repo root.

Documentation presence:

```bash
grep -c 'api/v1/refs' README.md
test "$(grep -c 'api/v1/refs' docs/api.md)" -ge 3
grep -qi 'a 409 must not be retried' docs/api.md
grep -q 'git_rest_ref_requests_total' docs/api.md
grep -q 'expected_sha' docs/api.md
grep -q 'refs/claims/' README.md
```

The first command must print a number ≥ 1. The remaining commands must exit 0.

Changelog:

```bash
awk '/^## /{sec=$0} /api\/v1\/refs/{print sec; exit}' CHANGELOG.md
```

Must print exactly `## Unreleased`.

The existing release heading is still present and singular:

```bash
grep -c '^## Unreleased' CHANGELOG.md
grep -c '^## v0.31.0' CHANGELOG.md
```

Both must print `1`.

Final gate:

```bash
ROOTDIR=/workspace make precommit
```

Must exit 0.
</verification>
