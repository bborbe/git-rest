---
status: completed
summary: Replaced http.NewServeMux with gorilla/mux.NewRouter().SkipClean(true) in main.go so path traversal attempts reach validatePath and return 400 Bad Request; promoted gorilla/mux to direct dependency in go.mod
container: git-rest-023-switch-to-gorilla-mux
dark-factory-version: v0.108.0-dirty
created: "2026-04-12T15:20:06Z"
queued: "2026-04-12T15:30:21Z"
started: "2026-04-12T15:30:23Z"
completed: "2026-04-12T15:35:23Z"
---

<summary>
- HTTP router switched from stdlib http.NewServeMux to gorilla/mux for consistency with all other services
- Path traversal attempts using ../ now return 400 Bad Request instead of being silently cleaned by the mux
- gorilla/mux with SkipClean preserves raw URL paths for handler-level validation
- All existing routes behave identically for valid requests
- Metrics middleware and all endpoint registrations updated for gorilla/mux API
</summary>

<objective>
Replace http.NewServeMux with gorilla/mux.NewRouter().SkipClean(true) so path traversal attempts reach the handler's validatePath and return 400 Bad Request. All other services use gorilla/mux — git-rest should follow the same pattern.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Read these files before making changes:
- `main.go` — current http.NewServeMux routing (~line 83)
- `pkg/handler/files_get.go` — path extraction via TrimPrefix
- `pkg/handler/files_post.go` — path extraction via TrimPrefix
- `pkg/handler/files_delete.go` — path extraction via TrimPrefix
- `pkg/handler/files_dispatch.go` — dispatch handler for GET (list vs read)
- `pkg/handler/metrics_middleware.go` — wraps the router
- `pkg/factory/factory.go` — handler creation functions
- `pkg/git/git.go` — validatePath function (~line 71), ErrInvalidPath sentinel

Read `go.mod` to confirm `github.com/gorilla/mux` is available (currently indirect dep).

Read `~/.claude/plugins/marketplaces/coding/docs/go-http-handler-refactoring-guide.md` for handler patterns.
</context>

<requirements>
## 1. Switch router in main.go

Replace in `createHTTPServer` (~line 83):

```go
// Before:
mux := http.NewServeMux()
mux.Handle("GET /api/v1/files/", factory.CreateFilesDispatchHandler(getH, listH))
mux.Handle("POST /api/v1/files/", postH)
mux.Handle("DELETE /api/v1/files/", deleteH)
mux.Handle("/healthz", healthzH)
mux.Handle("/readiness", readinessH)
mux.Handle("/metrics", promhttp.Handler())

// After:
router := gorillamux.NewRouter().SkipClean(true)
router.Handle("/api/v1/files/{path:.*}", factory.CreateFilesDispatchHandler(getH, listH)).Methods(http.MethodGet)
router.Handle("/api/v1/files/{path:.*}", postH).Methods(http.MethodPost)
router.Handle("/api/v1/files/{path:.*}", deleteH).Methods(http.MethodDelete)
router.Handle("/healthz", healthzH)
router.Handle("/readiness", readinessH)
router.Handle("/metrics", promhttp.Handler())
```

Import alias: `gorillamux "github.com/gorilla/mux"` to avoid conflict with local variable names.

Pass `router` instead of `mux` to `factory.CreateMetricsMiddleware`.

## 2. Update path extraction in handlers

All three file handlers currently extract the path with:
```go
path := strings.TrimPrefix(req.URL.Path, "/api/v1/files/")
```

This still works with gorilla/mux + SkipClean(true) because req.URL.Path retains the raw path. Keep TrimPrefix — do NOT switch to mux.Vars(req)["path"] because gorilla normalizes vars but we want the raw path to reach validatePath.

Verify that all three handlers (files_get.go, files_post.go, files_delete.go) continue to use TrimPrefix.

## 3. Update files_dispatch.go

The dispatch handler that routes between list and get based on query params. Verify it still works with gorilla/mux routing. The `{path:.*}` pattern matches empty path too (for `GET /api/v1/files/?glob=...`).

## 4. Update metrics middleware

`factory.CreateMetricsMiddleware` accepts `http.Handler`. gorilla/mux.Router implements http.Handler, so no signature change needed. Just verify the middleware still wraps correctly.

## 5. Update handler tests

Handler tests use `httptest.NewRequest` which doesn't go through the router. The path traversal tests should still pass since they test the handler directly with a crafted request path.

Verify all existing tests pass without modification. If any test relies on stdlib mux behavior, update it.

## 6. Update main_test.go

The "Compiles" test uses `gexec.Build`. Verify it still compiles after the import change.

## 7. Promote gorilla/mux to direct dependency

Run `go mod tidy` to promote `github.com/gorilla/mux` from indirect to direct dependency in go.mod.


</requirements>

<constraints>
- Only change files in `.`
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Do NOT change handler path extraction to mux.Vars — keep TrimPrefix for raw path validation
- Use import alias `gorillamux` to avoid name conflicts
- Use `errors.Wrap`/`errors.Errorf` from `github.com/bborbe/errors` — never `fmt.Errorf` or bare `return err`
</constraints>

<verification>
make precommit
</verification>
