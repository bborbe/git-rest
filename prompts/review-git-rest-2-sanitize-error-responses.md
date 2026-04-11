---
status: draft
created: "2026-04-11T00:00:00Z"
---

<summary>
- All HTTP handlers currently return raw err.Error() strings to clients, exposing git stderr output, filesystem paths, and operational details
- This is an information disclosure vulnerability (OWASP A09)
- Internal errors (git command failures, filesystem errors) must be logged server-side and replaced with a generic "internal error" response body
- Validation errors (ErrInvalidPath, ErrNotFound) may return their category label but not the full error chain
- This prompt depends on review-git-rest-1-sentinel-invalid-path.md being applied first
</summary>

<objective>
Update all HTTP handlers to log internal errors with `slog.ErrorContext` and return a generic client message instead of the raw error string. Validation and not-found errors continue to return their category labels. This eliminates server-side path and git-stderr leakage to clients.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Files to read before making changes (read ALL first):
- `pkg/handler/files_get.go`: current error handling (after prompt 1 has been applied)
- `pkg/handler/files_post.go`: current error handling
- `pkg/handler/files_delete.go`: current error handling
- `pkg/handler/files_list.go`: current error handling
- `pkg/handler/readiness.go`: current error handling
- `pkg/handler/helpers_test.go`: helper patterns used in tests
</context>

<requirements>
1. In `pkg/handler/files_get.go`, update the internal-error branch to log before responding:
   ```go
   slog.ErrorContext(r.Context(), "read file failed", "error", err, "path", path)
   writeJSONError(w, http.StatusInternalServerError, "internal error")
   ```
   The `ErrNotFound` and `ErrInvalidPath` branches do NOT log (these are client errors).

2. In `pkg/handler/files_post.go`, update the `WriteFile` error branch:
   ```go
   slog.ErrorContext(r.Context(), "write file failed", "error", err, "path", path)
   writeJSONError(w, http.StatusInternalServerError, "internal error")
   ```
   Also update the `io.ReadAll` non-size-error branch (~line 36):
   ```go
   slog.ErrorContext(r.Context(), "read request body failed", "error", err)
   writeJSONError(w, http.StatusInternalServerError, "internal error")
   ```

3. In `pkg/handler/files_delete.go`, update the internal-error branch:
   ```go
   slog.ErrorContext(r.Context(), "delete file failed", "error", err, "path", path)
   writeJSONError(w, http.StatusInternalServerError, "internal error")
   ```

4. In `pkg/handler/files_list.go`, update the error branch:
   ```go
   slog.ErrorContext(r.Context(), "list files failed", "error", err)
   writeJSONError(w, http.StatusInternalServerError, "internal error")
   ```

5. In `pkg/handler/readiness.go`, update the error branch:
   ```go
   slog.ErrorContext(r.Context(), "readiness check failed", "error", err)
   writeJSONError(w, http.StatusInternalServerError, "internal error")
   ```

6. Add `"log/slog"` to the imports in each modified handler file.

7. Update handler tests to assert that internal-error responses return the string `"internal error"` (not the raw git or filesystem error message). Tests for 400/404 responses are unaffected.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- ErrNotFound responses return "not found" (unchanged)
- ErrInvalidPath responses return "invalid path" (unchanged from prompt 1)
- Only internal/unexpected errors get the slog.ErrorContext call and "internal error" body
- Use `log/slog` — not `fmt`, not `glog`
</constraints>

<verification>
make precommit
</verification>
