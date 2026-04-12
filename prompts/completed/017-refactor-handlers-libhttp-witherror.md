---
status: completed
summary: Refactored all HTTP handlers from http.Handler with manual JSON error writing to libhttp.WithError + libhttp.NewJSONErrorHandler pattern; replaced writeJSONError/writeJSONOK with WrapWithStatusCode and SendJSONResponse; replaced healthz handler with libhttp.NewPrintHandler; deleted json_error.go, healthz.go, and healthz_test.go; updated all handler tests to use the new WithError interface
container: git-rest-017-refactor-handlers-libhttp-witherror
dark-factory-version: v0.108.0-dirty
created: "2026-04-12T00:00:00Z"
queued: "2026-04-12T11:54:53Z"
started: "2026-04-12T11:54:54Z"
completed: "2026-04-12T12:06:27Z"
---

<summary>
- All HTTP handlers manually write JSON error responses instead of returning typed errors
- The standard pattern uses centralized error-to-JSON conversion but handlers bypass it entirely
- Each handler duplicates status code mapping logic instead of attaching status codes to errors
- The health check endpoint has a custom handler when a standard library utility already exists
- Factory functions pass handlers through without error formatting wrapping
- Refactoring removes all manual JSON error writing and reduces handler boilerplate significantly
</summary>

<objective>
Refactor all HTTP handlers from raw `http.Handler` with manual error writing to `libhttp.WithError` + `libhttp.NewJSONErrorHandler` pattern. Replace `writeJSONError`/`writeJSONOK` with error returns and `libhttp.WrapWithStatusCode`. Replace custom healthz handler with `libhttp.NewPrintHandler("OK")`.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Read coding guides before implementing (in `/home/node/.claude/plugins/marketplaces/coding/docs/`):
- `go-http-handler-refactoring-guide.md`: handler organization and factory patterns
- `go-json-error-handler-guide.md`: WithError + NewJSONErrorHandler pattern, WrapWithStatusCode, error codes
- `go-factory-pattern.md`: factory functions wrap handlers
- `go-testing-guide.md`: Ginkgo/Gomega test patterns for handler tests

Reference implementation in go-skeleton: `main.go` uses `libhttp.NewPrintHandler("OK")` for healthz.

Files to read before making changes (read ALL first):
- `pkg/handler/files_get.go`: current handler returning http.Handler
- `pkg/handler/files_post.go`: current handler with MaxBytesReader
- `pkg/handler/files_delete.go`: current handler
- `pkg/handler/files_list.go`: current handler returning JSON array
- `pkg/handler/readiness.go`: current handler
- `pkg/handler/healthz.go`: custom healthz handler to replace
- `pkg/handler/json_error.go`: writeJSONError/writeJSONOK helpers to delete
- `pkg/factory/factory.go`: factory functions to update
- `main.go`: route registration
- `pkg/handler/files_get_test.go`: tests to update
- `pkg/handler/files_post_test.go`: tests to update
- `pkg/handler/files_delete_test.go`: tests to update
- `pkg/handler/files_list_test.go`: tests to update
- `pkg/handler/readiness_test.go`: tests to update
- `pkg/handler/healthz_test.go`: tests to update
- `pkg/handler/helpers_test.go`: test helpers
</context>

<requirements>
1. Refactor `pkg/handler/files_get.go` to return `libhttp.WithError`:
   ```go
   func NewFilesGetHandler(g git.Git) libhttp.WithError {
       return libhttp.WithErrorFunc(func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
           path := strings.TrimPrefix(req.URL.Path, "/api/v1/files/")
           content, err := g.ReadFile(ctx, path)
           if err != nil {
               if errors.Is(err, git.ErrNotFound) {
                   return libhttp.WrapWithStatusCode(err, http.StatusNotFound)
               }
               if errors.Is(err, git.ErrInvalidPath) {
                   return libhttp.WrapWithStatusCode(err, http.StatusBadRequest)
               }
               return errors.Wrap(ctx, err, "read file")
           }
           resp.Header().Set("Content-Type", "application/octet-stream")
           _, _ = resp.Write(content)
           return nil
       })
   }
   ```
   Import `libhttp "github.com/bborbe/http"` and `"github.com/bborbe/errors"` (as `errors` — shadow stdlib). For `errors.Is`/`errors.As` calls, use `stderrors "errors"` alias for stdlib where both are needed in the same file. Follow the pattern already used in `pkg/git/git.go`.

2. Refactor `pkg/handler/files_post.go` to return `libhttp.WithError`:
   ```go
   func NewFilesPostHandler(g git.Git) libhttp.WithError {
       return libhttp.WithErrorFunc(func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
           path := strings.TrimPrefix(req.URL.Path, "/api/v1/files/")
           req.Body = http.MaxBytesReader(resp, req.Body, maxBodyBytes)
           body, err := io.ReadAll(req.Body)
           if err != nil {
               var maxBytesErr *http.MaxBytesError
               if errors.As(err, &maxBytesErr) {
                   return libhttp.WrapWithStatusCode(err, http.StatusRequestEntityTooLarge)
               }
               return errors.Wrap(ctx, err, "read request body")
           }
           if err := g.WriteFile(ctx, path, body); err != nil {
               if errors.Is(err, git.ErrInvalidPath) {
                   return libhttp.WrapWithStatusCode(err, http.StatusBadRequest)
               }
               return errors.Wrap(ctx, err, "write file")
           }
           return libhttp.SendJSONResponse(ctx, resp, map[string]bool{"ok": true}, http.StatusOK)
       })
   }
   ```

3. Refactor `pkg/handler/files_delete.go` to return `libhttp.WithError`:
   ```go
   func NewFilesDeleteHandler(g git.Git) libhttp.WithError {
       return libhttp.WithErrorFunc(func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
           path := strings.TrimPrefix(req.URL.Path, "/api/v1/files/")
           if err := g.DeleteFile(ctx, path); err != nil {
               if errors.Is(err, git.ErrNotFound) {
                   return libhttp.WrapWithStatusCode(err, http.StatusNotFound)
               }
               if errors.Is(err, git.ErrInvalidPath) {
                   return libhttp.WrapWithStatusCode(err, http.StatusBadRequest)
               }
               return errors.Wrap(ctx, err, "delete file")
           }
           return libhttp.SendJSONResponse(ctx, resp, map[string]bool{"ok": true}, http.StatusOK)
       })
   }
   ```

4. Refactor `pkg/handler/files_list.go` to return `libhttp.WithError`:
   ```go
   func NewFilesListHandler(g git.Git) libhttp.WithError {
       return libhttp.WithErrorFunc(func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
           glob := req.URL.Query().Get("glob")
           files, err := g.ListFiles(ctx, glob)
           if err != nil {
               return errors.Wrap(ctx, err, "list files")
           }
           if files == nil {
               files = []string{}
           }
           return libhttp.SendJSONResponse(ctx, resp, files, http.StatusOK)
       })
   }
   ```

5. Refactor `pkg/handler/readiness.go` to return `libhttp.WithError`:
   ```go
   func NewReadinessHandler(g git.Git) libhttp.WithError {
       return libhttp.WithErrorFunc(func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
           status, err := g.Status(ctx)
           if err != nil {
               return libhttp.WrapWithStatusCode(
                   errors.Wrap(ctx, err, "git status"),
                   http.StatusServiceUnavailable,
               )
           }
           if !status.Clean || !status.NoPushPending {
               return libhttp.WrapWithStatusCode(
                   errors.New(ctx, "not ready"),
                   http.StatusServiceUnavailable,
               )
           }
           _, _ = resp.Write([]byte("ok"))
           return nil
       })
   }
   ```

6. Delete `pkg/handler/healthz.go` entirely. The factory will use `libhttp.NewPrintHandler("OK")` directly.

7. Delete `pkg/handler/json_error.go` entirely — `writeJSONError` and `writeJSONOK` are no longer needed.

8. Update `pkg/factory/factory.go` — factory functions now wrap `WithError` handlers with `NewJSONErrorHandler`:
   ```go
   import libhttp "github.com/bborbe/http"

   func CreateFilesGetHandler(g git.Git) http.Handler {
       return libhttp.NewJSONErrorHandler(handler.NewFilesGetHandler(g))
   }

   func CreateFilesPostHandler(g git.Git) http.Handler {
       return libhttp.NewJSONErrorHandler(handler.NewFilesPostHandler(g))
   }

   func CreateFilesDeleteHandler(g git.Git) http.Handler {
       return libhttp.NewJSONErrorHandler(handler.NewFilesDeleteHandler(g))
   }

   func CreateFilesListHandler(g git.Git) http.Handler {
       return libhttp.NewJSONErrorHandler(handler.NewFilesListHandler(g))
   }

   func CreateReadinessHandler(g git.Git) http.Handler {
       return libhttp.NewJSONErrorHandler(handler.NewReadinessHandler(g))
   }

   func CreateHealthzHandler() http.Handler {
       return libhttp.NewPrintHandler("OK")
   }
   ```

9. Delete `pkg/handler/healthz_test.go` — healthz is now a one-liner in factory using libhttp.

10. Update handler tests to work with `libhttp.WithError` return type. Tests should call the handler's `ServeHTTP(ctx, rec, req)` method directly and assert the returned error for error cases:
    - Success cases: assert `err` is nil, check response body/headers on the recorder
    - Error cases with status code: assert `err` is non-nil, use `errors.As(err, &errorWithStatusCode)` to verify the status code
    - Internal error cases: assert `err` is non-nil (no status code check needed — defaults to 500)
    - Create `ctx := context.Background()` in test setup for passing to `ServeHTTP`

11. Delete `pkg/handler/helpers_test.go` if `errWithMessage` is no longer used by any test after the refactor. If still referenced, keep it.

12. No changes needed to `pkg/handler/files_dispatch.go` or `pkg/handler/files_dispatch_test.go` — the dispatch handler receives factory-wrapped `http.Handler` instances and is unaffected.

13. Remove unused imports from all modified files. Add `libhttp "github.com/bborbe/http"` and `"context"` where needed.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Use `errors.Wrap`/`errors.New` from `github.com/bborbe/errors` — never `fmt.Errorf`
- Factory functions contain zero business logic — only `libhttp.NewJSONErrorHandler(handler.NewXxxHandler(deps...))`
- Handler functions return `libhttp.WithError`, not `http.Handler`
- Use `libhttp.SendJSONResponse` for success JSON responses — do not manually encode JSON
- Use `libhttp.WrapWithStatusCode` for non-500 error responses
- Unhandled errors default to 500 via NewJSONErrorHandler — do not explicitly set 500
- Delete json_error.go and healthz.go — do not leave dead code
</constraints>

<verification>
make precommit
</verification>
