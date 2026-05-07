---
status: completed
approved: "2026-05-06T15:14:55Z"
generating: "2026-05-06T15:21:46Z"
prompted: "2026-05-06T15:25:38Z"
verifying: "2026-05-06T15:32:19Z"
completed: "2026-05-06T19:55:45Z"
branch: dark-factory/bug-write-file-no-content-change-returns-500
---

# bug: WriteFile returns 500 when content is identical to HEAD

## Summary

- `POST /api/v1/files/<path>` writes the body verbatim, runs `git add` + `git commit`
- When the body is byte-identical to the file currently in HEAD, `git commit` exits non-zero with `nothing to commit, working tree clean`
- The handler treats that exit code as a real error and returns HTTP 500
- Idempotent re-writes (the common case for retry-on-failure clients) flood the logs with false errors and break upstream retry semantics
- Observed in production: `bborbe/agent` task-controller's CQRS retry loop reads the 500 as failure, retries the same content 5 times, all 4 retries return 500, controller logs "failed after 5 attempts" — but the file actually landed on attempt 1

## Reproduction

git-rest version: `v0.13.x` (commit observed in dev cluster on 2026-05-06)

Minimal repro against any git-rest instance:

```bash
# First write — file does not exist yet → succeeds (200)
curl -sf -X POST -d 'hello' http://<git-rest>/api/v1/files/test.md

# Second write — same content → 500 "nothing to commit"
curl -i -X POST -d 'hello' http://<git-rest>/api/v1/files/test.md
# HTTP/1.1 500 Internal Server Error
# {"code":"INTERNAL_ERROR","message":"... git commit ...: On branch main\nYour branch is up to date with 'origin/main'.\n\nnothing to commit, working tree clean\n: exit status 1"}
```

Production trace from dev cluster on 2026-05-06 (vault-obsidian-openclaw vault-server consumed by agent-task-controller):

```
15:01:48.860  POST /api/v1/files/tasks/710db30e-...md  → 500  "nothing to commit, working tree clean"
15:01:50.893  POST same path same body                  → 500  same error
15:01:54.915  POST same path same body                  → 500  same error
15:01:58.xxx  (attempt 4)                                → 500  same error
15:02:02.933  POST same path same body                  → 500  same error
```

But git log on the vault-server's repo:

```
3ccc884 git-rest: create tasks/710db30e-86e2-59a6-8ac1-9c7718122d4e.md
```

The first POST at 15:01:48 had already created the file and committed it (commit `3ccc884`). Attempts 2-5 returned 500 because there was nothing left to commit.

## Expected vs Actual

| | Expected | Actual |
|---|---|---|
| First write (new file) | 200 OK, commit created | 200 OK, commit created |
| Second write (same content) | 200 OK, no-op (file already at this content) | 500 `INTERNAL_ERROR` "nothing to commit, working tree clean" |
| Second write (different content) | 200 OK, commit created | 200 OK, commit created |

The HTTP contract should be idempotent on the body. Per common REST semantics: PUT/POST of the same content should be a no-op success. The current implementation conflates "no diff to commit" with a real failure.

## Why this is a bug

1. **Breaks upstream retry semantics** — clients with at-least-once delivery (CQRS, Kafka consumers, watchers) WILL retry the same write. Returning 500 on idempotent retry signals "try again" when the work is already done. Worst case: infinite retry loops.

2. **Masks real failures** — operators reading vault-server logs see hundreds of 500s per day (one per retry from each watcher). Real disk-full / merge-conflict / push-rejected errors are buried in noise.

3. **Violates the implementation's own intent** — `WriteFile` already has a `fileExists` branch that anticipates idempotent writes (chooses commit message `update` vs `create`). It just doesn't handle the no-diff sub-case.

## Non-goals

- HTTP method semantics stay as-is (POST creates/updates; no shift to PUT)
- No `If-Match` / `ETag` / conditional-request support
- No change to push semantics or remote interaction
- Not generalizing to a "diff aware" mode — the fix is local to the no-op-commit case

## Scope

This spec covers BOTH `WriteFile` and `DeleteFile` — same root cause, same fix shape:

- **`WriteFile`** (`pkg/git/git.go` ~line 247): `git commit` returns "nothing to commit" when the body is identical to HEAD → fix to no-op success
- **`DeleteFile`** (`pkg/git/git.go` ~line 288): on retry of an already-successful delete, `git rm <path>` itself fails (file gone) before reaching commit. Verify the actual error shape during implementation; apply analogous treatment so a re-delete is also a no-op success rather than 500

If `DeleteFile` turns out to have a different failure mode that doesn't fit a single PR, the implementer files a follow-up `bug-delete-file-...` spec; the WriteFile fix proceeds either way.

## Constraints

- The fix MUST distinguish "no changes to commit" (success) from real `git commit` errors (e.g. pre-commit hook fail, out-of-disk during commit object write)
- The fix MUST log the no-op case at INFO level so operators can tell idempotent writes from "writes that actually wrote"
- HTTP response on idempotent no-op MUST be 200, not 304 — the resource is in the state the client requested; that's a success
- The `git push` step's behavior on no-op MUST be preserved — pushing nothing is already a quiet no-op in vanilla git, no change needed there
- Existing tests for `WriteFile` and `DeleteFile` MUST still pass

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| First write, file did not exist | 200, commit `git-rest: create <path>` | none |
| Re-write with identical content | 200, no commit, INFO log "no changes" | none |
| Re-write with different content | 200, commit `git-rest: update <path>` | none |
| Pre-commit hook rejects | 500, error message includes hook output | operator fixes hook |
| Disk full during write | 500, `os.WriteFile` error | operator clears space |
| Push rejected (remote diverged) | 500, push error | operator resolves |

## Acceptance Criteria

- [ ] Unit test: `WriteFile` called twice with identical body returns nil both times (and the second call produces no new commit)
- [ ] Unit test: `WriteFile` called with new content after an identical-write produces exactly one commit (not two)
- [ ] Integration test: POST same body to same path twice → both responses are 200
- [ ] Integration test: POST different body to same path → both responses are 200, two commits in `git log`
- [ ] No regression: real commit failures (e.g. hook rejection) still return 500
- [ ] CHANGELOG entry under `## Unreleased`
- [ ] `DeleteFile` integration test: delete same path twice → both responses 200 (or follow-up bug spec filed if implementation reveals a different root cause)

## Verification

```bash
cd ~/Documents/workspaces/git-rest && make precommit
```

Plus the integration repro from `## Reproduction` against a built binary — second POST with identical body must return 200.
