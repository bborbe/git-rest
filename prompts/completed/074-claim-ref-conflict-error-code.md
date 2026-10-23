---
status: completed
summary: Claim-ref PUT/DELETE lease conflicts now return JSON error code CLAIM_CONFLICT with the existing 409 status; tests, API docs, and CHANGELOG updated.
execution_id: git-rest-claim-conflict-exec-074-claim-ref-conflict-error-code
dark-factory-version: v0.198.7
created: "2026-10-22T10:30:00Z"
queued: "2026-10-22T06:32:16Z"
started: "2026-10-23T15:39:41Z"
completed: "2026-10-23T15:41:43Z"
---

<summary>
- A claim-ref `PUT` or `DELETE` that loses its lease race returns a conflict-specific machine-readable error code, `CLAIM_CONFLICT`, instead of `INTERNAL_ERROR`
- The HTTP status for a lost lease race stays `409`, so clients that branch on status see no change
- Every git push output classified as a lease conflict today still produces the `409` and now carries the new code
- An invalid claim ref name still returns `400`, and any other failure still returns `500` with `INTERNAL_ERROR`
- The file-write `409` path (files API) is not touched
- The API docs describe the new code and the real error body shape
- A changelog entry under `## Unreleased` records the response-code change
</summary>

<objective>
Make the claim-ref write handlers (`PUT` and `DELETE` on `/api/v1/refs/claims/{key}`) return the JSON error code `CLAIM_CONFLICT` with HTTP 409 when the origin rejects the compare-and-swap lease, instead of the library default `INTERNAL_ERROR`. Status mapping and lease-conflict classification stay exactly as they are.
</objective>

<context>
Read CLAUDE.md for project conventions.
Read `pkg/handler/refs_put.go` — find `mapRefWriteError`; it is the single point both `NewRefsPutHandler` and `NewRefsDeleteHandler` (`pkg/handler/refs_delete.go`) use to translate a claim-ref write error into an HTTP status. Today it calls `libhttp.WrapWithStatusCode(err, http.StatusConflict)` for `git.ErrRefConflict`.
Read `pkg/git/claim_refs.go` — find `ErrRefConflict` and `isLeaseConflict`; `isLeaseConflict` lists the git push output markers classified as a lost lease (`stale info`, `remote ref does not exist`, `cannot lock ref`, and `[remote rejected]` lines). Do not change this function.
Read `pkg/factory/factory.go` — the refs handlers are wrapped in `libhttp.NewJSONErrorHandler`, which writes `{"error":{"code":...,"message":...}}` and uses the error's code when it implements `libhttp.ErrorWithCode`, defaulting to `INTERNAL_ERROR` otherwise.
Read the `github.com/bborbe/http` module source in the Go module cache (`go env GOMODCACHE`, version from `go.mod`) — `http_error-handler.go` (`WrapWithCode(err error, code string, statusCode int) error`) and `http_json-error-handler.go`.
Read `pkg/handler/refs_test.go` and `pkg/git/claim_refs_internal_test.go` for the existing test style (Ginkgo/Gomega, counterfeiter mocks in `mocks/`).
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-json-error-handler-guide.md` and `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`.
</context>

<requirements>
1. In `pkg/handler/refs_put.go`, add an exported constant next to `mapRefWriteError`:
   ```go
   // ErrorCodeClaimConflict is the JSON error code returned when a claim ref
   // write loses its compare-and-swap lease race.
   const ErrorCodeClaimConflict = "CLAIM_CONFLICT"
   ```
2. In `mapRefWriteError`, replace the `git.ErrRefConflict` branch:
   - old: `return libhttp.WrapWithStatusCode(err, http.StatusConflict)`
   - new: `return libhttp.WrapWithCode(err, ErrorCodeClaimConflict, http.StatusConflict)`
   Leave the `git.ErrInvalidRefName` (400) branch and the default branch unchanged. Do not touch `pkg/handler/files_post.go`.
3. In `pkg/handler/refs_test.go`, the existing "error mapping" context already drives PUT and DELETE (mocks `PutClaimRefReturns` / `DeleteClaimRefReturns`) to 409 / 400 / 500 through `libhttp.NewJSONErrorHandler`. Add body-code assertions to those existing table cases (decode the JSON body):
   - PUT and DELETE with an error wrapping `git.ErrRefConflict` → status `409` AND `error.code == "CLAIM_CONFLICT"`
   - PUT with an error wrapping `git.ErrInvalidRefName` → status `400` (unchanged)
   - PUT with a plain error → status `500` AND `error.code == "INTERNAL_ERROR"`
   Add a case only where one of these is missing; do not duplicate existing cases.
4. `pkg/git/claim_refs_internal_test.go` already tests `isLeaseConflict` for every marker (`stale info`, `remote ref does not exist`, `cannot lock ref`, `[remote rejected]`). Verify only — do not change it or `isLeaseConflict`.
5. In `docs/api.md`, change the PUT and DELETE claim-ref `409` rows to state that the body `error.code` is `CLAIM_CONFLICT`, and update the "Error Responses" example body to the actual shape `{"error":{"code":"...","message":"..."}}`.
6. Add to `CHANGELOG.md` a `## Unreleased` section directly above the newest `## vX.Y.Z` heading (reuse it if one already exists) with exactly this bullet:
   ```
   - fix: Return error code `CLAIM_CONFLICT` instead of `INTERNAL_ERROR` when a claim ref `PUT` or `DELETE` loses its lease race. The HTTP status stays `409`.
   ```
7. Self-check: re-run the `<verification>` commands and confirm each passes; walk every bullet in `<summary>` against the change.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- HTTP status codes for every path stay as they are today
- Do not change `isLeaseConflict`, `ErrRefConflict`, or any other error code in the API
- Do not touch `pkg/handler/files_post.go`
- Use `github.com/bborbe/errors` for any new error wrapping, never `fmt.Errorf`
</constraints>

<verification>
Run `ROOTDIR=/workspace make precommit` — must pass.
Run `grep -n 'WrapWithCode(err, ErrorCodeClaimConflict, http.StatusConflict)' pkg/handler/refs_put.go` — must print exactly one line.
Run `grep -cE 'CLAIM_CONFLICT|ErrorCodeClaimConflict' pkg/handler/refs_test.go` — must print a number of at least 2.
Run `grep -c 'CLAIM_CONFLICT' docs/api.md` — must print a number of at least 2.
Run `! grep -q 'CLAIM_CONFLICT\|ErrorCodeClaimConflict' pkg/handler/files_post.go && echo files_post-untouched` — must print `files_post-untouched`.
Run `awk '/^## /{sec=$0} /CLAIM_CONFLICT/{print sec}' CHANGELOG.md` — must print `## Unreleased`.
</verification>
