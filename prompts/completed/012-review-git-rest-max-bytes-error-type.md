---
status: completed
summary: Replaced fragile err.Error() string comparison with typed errors.As(err, &maxBytesErr) check using *http.MaxBytesError in files_post handler.
container: git-rest-012-review-git-rest-max-bytes-error-type
dark-factory-version: v0.108.0-dirty
created: "2026-04-11T00:00:00Z"
queued: "2026-04-11T21:05:41Z"
started: "2026-04-11T21:25:32Z"
completed: "2026-04-11T21:29:46Z"
---

<summary>
- files_post.go detects the "body too large" error by comparing err.Error() to a hardcoded internal Go stdlib string
- This string ("http: request body too large") is an undocumented implementation detail of http.MaxBytesReader
- Go 1.19 introduced *http.MaxBytesError as a typed error — the correct way to detect this case
- Using the typed error check makes the code robust to stdlib refactoring and is the idiomatic Go approach
</summary>

<objective>
Replace the fragile `err.Error() == "http: request body too large"` string comparison in `pkg/handler/files_post.go` with a typed `errors.As(err, &maxBytesErr)` check using `*http.MaxBytesError`.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Files to read before making changes (read ALL first):
- `pkg/handler/files_post.go` (~line 31-37): the string comparison to replace
- `pkg/handler/files_post_test.go`: existing test for the 413 response path
</context>

<requirements>
1. In `pkg/handler/files_post.go`, replace the body-size error check (~line 31-37):
   ```go
   // Before:
   if err.Error() == "http: request body too large" {
       writeJSONError(w, http.StatusRequestEntityTooLarge, "request body too large")
       return
   }

   // After:
   var maxBytesErr *http.MaxBytesError
   if errors.As(err, &maxBytesErr) {
       writeJSONError(w, http.StatusRequestEntityTooLarge, "request body too large")
       return
   }
   ```

2. Add `"errors"` to the import block if not already present. Remove the `"strings"` import if it becomes unused after this change (it may already be removed by prompt 1).

3. Verify the existing test for the 413 path in `pkg/handler/files_post_test.go` still passes — no test changes should be needed since the behavior is identical.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Use the standard library `errors.As` (not github.com/bborbe/errors for this check — the type assertion is against *http.MaxBytesError from net/http)
</constraints>

<verification>
make precommit
</verification>
