---
status: completed
spec: [001-git-rest-server]
summary: Created pkg/handler package with filesGet/Post/Delete/List/Healthz/Readiness handlers, shared JSON helpers, and pkg/factory with Create* factory functions, all with Ginkgo tests at 93.5% coverage
container: git-rest-002-spec-001-http-handlers
dark-factory-version: v0.108.0-dirty
created: "2026-04-11T19:30:00Z"
queued: "2026-04-11T19:37:18Z"
started: "2026-04-11T19:47:23Z"
completed: "2026-04-11T19:54:23Z"
branch: dark-factory/git-rest-server
---

<summary>
- HTTP handler for `GET /api/v1/files/{path}` returns file content with status 200, or 404 if not found
- HTTP handler for `POST /api/v1/files/{path}` accepts raw body, writes file to git, returns 200 on success
- HTTP handler for `DELETE /api/v1/files/{path}` removes file from git, returns 200 or 404 if not found
- HTTP handler for `GET /api/v1/files/` with `?glob=pattern` query param returns a JSON array of matching paths
- Request body size is capped at 10 MB to prevent abuse
- Path traversal attempts (`..`, absolute paths) return 400 Bad Request with a JSON error body
- All error responses are JSON: `{"error": "message"}`
- Health handler at `/healthz` always returns 200 (liveness)
- Readiness handler at `/readiness` returns 200 only when the git repo is clean and has no unpushed commits
- All handlers are in `pkg/handler/` and instantiated via factory functions in `pkg/factory/`
</summary>

<objective>
Build all HTTP handlers that consume the `Git` interface from `pkg/git`. Each handler delegates to `Git` and maps domain errors to HTTP status codes. The postcondition is a tested handler package with Ginkgo suites using Counterfeiter mocks of `Git`, and factory functions that wire the handlers.
</objective>

<context>
Read `CLAUDE.md` and `docs/dod.md` for project conventions.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md` for interface/constructor/struct pattern.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` for Ginkgo/Gomega/Counterfeiter test conventions.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-http-handler-refactoring-guide.md` for handler structure.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-factory-pattern.md` for `Create*` factory conventions.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` for error wrapping rules.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-json-error-handler-guide.md` for JSON error response conventions.

Existing files:
- `pkg/git/git.go` — `Git` interface and `ErrNotFound` sentinel (created in prompt 1)
- `pkg/git/mocks/fakes.go` — Counterfeiter mock of `Git`
- `go.mod` — module `github.com/bborbe/git-rest`
</context>

<requirements>
1. Create `pkg/handler/files_get.go`:
   - Handler struct `filesGetHandler` with field `git git.Git`
   - Constructor: `func NewFilesGetHandler(g git.Git) http.Handler`
   - `ServeHTTP`:
     - Extract path from `r.URL.Path` after stripping `/api/v1/files/` prefix
     - Call `g.ReadFile(r.Context(), path)`
     - On `git.ErrNotFound`: write JSON error `{"error": "not found"}` with status 404
     - On path validation error (contains "traversal" or "absolute" or "empty"): write JSON error with status 400
     - On other errors: write JSON error with status 500
     - On success: write content bytes with status 200, `Content-Type: application/octet-stream`

2. Create `pkg/handler/files_post.go`:
   - Handler struct `filesPostHandler` with field `git git.Git`
   - Constructor: `func NewFilesPostHandler(g git.Git) http.Handler`
   - `ServeHTTP`:
     - Extract path from `r.URL.Path` after stripping `/api/v1/files/` prefix
     - Read body with `http.MaxBytesReader(w, r.Body, 10*1024*1024)` (10 MB limit)
     - Call `g.WriteFile(r.Context(), path, body)`
     - On path validation error: 400 JSON error
     - On body-too-large error: 413 JSON error `{"error": "request body too large"}`
     - On other errors: 500 JSON error
     - On success: write status 200 with JSON body `{"ok": true}`

3. Create `pkg/handler/files_delete.go`:
   - Handler struct `filesDeleteHandler` with field `git git.Git`
   - Constructor: `func NewFilesDeleteHandler(g git.Git) http.Handler`
   - `ServeHTTP`:
     - Extract path from `r.URL.Path` after stripping `/api/v1/files/` prefix
     - Call `g.DeleteFile(r.Context(), path)`
     - On `git.ErrNotFound`: 404 JSON error
     - On path validation error: 400 JSON error
     - On other errors: 500 JSON error
     - On success: 200 JSON `{"ok": true}`

4. Create `pkg/handler/files_list.go`:
   - Handler struct `filesListHandler` with field `git git.Git`
   - Constructor: `func NewFilesListHandler(g git.Git) http.Handler`
   - `ServeHTTP`:
     - Extract `glob` query param from `r.URL.Query().Get("glob")`
     - Call `g.ListFiles(r.Context(), glob)`
     - On error: 500 JSON error
     - On success: marshal result as JSON array, write with `Content-Type: application/json`, status 200
     - If no files match, return `[]` (empty JSON array), not null

5. Create `pkg/handler/healthz.go`:
   - Function `NewHealthzHandler() http.Handler` returning a handler that always writes 200 with body `ok`

6. Create `pkg/handler/readiness.go`:
   - Handler struct `readinessHandler` with field `git git.Git`
   - Constructor: `func NewReadinessHandler(g git.Git) http.Handler`
   - `ServeHTTP`:
     - Call `g.Status(r.Context())`
     - If `status.Clean && status.NoPushPending`: write 200 with body `ok`
     - Otherwise: write 503 with JSON error `{"error": "not ready"}`
     - On error calling `Status`: write 503 with JSON error

7. Create a shared helper `pkg/handler/json_error.go`:
   - `func writeJSONError(w http.ResponseWriter, status int, msg string)` that sets `Content-Type: application/json`, writes status code, and writes `{"error": "<msg>"}` as body
   - `func writeJSONOK(w http.ResponseWriter)` that writes 200 with `{"ok": true}`

8. Create `pkg/factory/factory.go`:
   - Import `pkg/git` and `pkg/handler`
   - Factory function: `func CreateFilesGetHandler(g git.Git) http.Handler` — returns `handler.NewFilesGetHandler(g)`
   - Factory function: `func CreateFilesPostHandler(g git.Git) http.Handler`
   - Factory function: `func CreateFilesDeleteHandler(g git.Git) http.Handler`
   - Factory function: `func CreateFilesListHandler(g git.Git) http.Handler`
   - Factory function: `func CreateHealthzHandler() http.Handler`
   - Factory function: `func CreateReadinessHandler(g git.Git) http.Handler`
   - Factories must contain zero business logic — pure `New*` delegation

9. Write Ginkgo tests in `pkg/handler/files_get_test.go`, `pkg/handler/files_post_test.go`, `pkg/handler/files_delete_test.go`, `pkg/handler/files_list_test.go`, `pkg/handler/readiness_test.go` (package `handler_test`):
   - Use `pkg/git/mocks.FakeGit` (Counterfeiter fake) for all tests
   - Use `httptest.NewRecorder()` and `httptest.NewRequest()`
   - Test suite bootstrap file: `pkg/handler/handler_suite_test.go`
   - Test cases for each handler:
     - Happy path: correct status code, correct body
     - `ErrNotFound` maps to 404
     - Path traversal (`../etc/passwd`) maps to 400
     - Git error maps to 500
   - For POST: test body > 10 MB returns 413
   - For list: test empty result returns `[]`, non-empty returns array
   - For readiness: clean=true+noPushPending=true → 200; either false → 503; Status error → 503
   - Coverage ≥ 80% for `pkg/handler`
</requirements>

<constraints>
- All handlers must live in `pkg/handler/` — no inline handlers in `main.go`
- Factory functions must live in `pkg/factory/` and use the `Create*` prefix
- Factory functions must have zero business logic (no conditionals, no loops)
- Error responses must be JSON: `{"error": "message"}` — never plain text
- Request body size limit is 10 MB (`http.MaxBytesReader`)
- Path extraction strips the `/api/v1/files/` prefix — path traversal validation happens inside `git.Git`
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Errors must be wrapped with `github.com/bborbe/errors` — never `fmt.Errorf`
- `context.Background()` must NOT appear in `pkg/` — always propagate `r.Context()`
</constraints>

<verification>
```bash
make test
make precommit
```

Additional checks:
```bash
# Confirm coverage
go test -coverprofile=/tmp/cover.out -mod=vendor ./pkg/handler/... && go tool cover -func=/tmp/cover.out | grep total

# Confirm factory has no business logic
grep -n "if\|for\|switch" /workspace/pkg/factory/factory.go && echo "FAIL: logic in factory" || echo "OK"
```
</verification>
