---
status: approved
created: "2026-04-11T00:00:00Z"
queued: "2026-04-11T21:05:37Z"
---

<summary>
- Handler error classification currently inspects error message strings for "traversal", "absolute", and "empty" substrings to decide HTTP status codes
- This coupling is fragile: renaming any validation error message silently degrades 400 Bad Request responses to 500 Internal Server Error
- A sentinel error ErrInvalidPath (similar to the existing ErrNotFound pattern) is the correct fix
- The validatePath function should also block access to .git/ directory components, which it currently allows
- All three write/delete/get handlers must be updated to use errors.Is instead of string matching
</summary>

<objective>
Add an `ErrInvalidPath` sentinel error to `pkg/git`, make `validatePath` wrap all validation failures with it, add a check that rejects `.git` path components, and update all three affected handlers to use `errors.Is(err, git.ErrInvalidPath)` for 400 vs 500 routing. Run this prompt before `review-git-rest-2-sanitize-error-responses.md`.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Files to read before making changes (read ALL first):
- `pkg/git/git.go` (~line 25-82): `ErrNotFound` sentinel, `validatePath` function
- `pkg/handler/files_get.go` (~line 27-39): string-based error classification
- `pkg/handler/files_post.go` (~line 40-48): string-based error classification
- `pkg/handler/files_delete.go` (~line 26-37): string-based error classification
- `pkg/handler/files_get_test.go`: existing test coverage to understand what tests already exist
- `pkg/handler/files_post_test.go`: existing test coverage
- `pkg/handler/files_delete_test.go`: existing test coverage
- `pkg/git/git_test.go`: existing validation path tests
</context>

<requirements>
1. In `pkg/git/git.go` (~line 26), add a new sentinel error directly below `ErrNotFound`:
   ```go
   // ErrInvalidPath is returned when the requested path fails validation.
   var ErrInvalidPath = stderrors.New("invalid path")
   ```

2. In `pkg/git/git.go`, update `validatePath` (~line 59-82) to:
   - Wrap all returned errors with `ErrInvalidPath` using `errors.Wrapf(ctx, ErrInvalidPath, "<message>")` instead of `errors.New(ctx, "<message>")`
   - Add a new check after the existing `..` checks that rejects `.git` path components:
     ```go
     for _, part := range strings.Split(path, "/") {
         if part == ".git" {
             return errors.Wrapf(ctx, ErrInvalidPath, ".git directory access not allowed")
         }
     }
     ```
   - Keep the existing empty, absolute, and traversal checks but change them to use `errors.Wrapf(ctx, ErrInvalidPath, ...)` format

3. In `pkg/handler/files_get.go` (~line 27-39), replace the string-matching block:
   ```go
   // Before:
   msg := err.Error()
   if strings.Contains(msg, "traversal") || strings.Contains(msg, "absolute") ||
       strings.Contains(msg, "empty") {
       writeJSONError(w, http.StatusBadRequest, msg)
       return
   }
   writeJSONError(w, http.StatusInternalServerError, msg)

   // After:
   if errors.Is(err, git.ErrInvalidPath) {
       writeJSONError(w, http.StatusBadRequest, "invalid path")
       return
   }
   writeJSONError(w, http.StatusInternalServerError, "internal error")
   ```
   Remove the `"strings"` import if no longer used.

4. Apply the same replacement in `pkg/handler/files_post.go` (~line 40-48) for the `WriteFile` error block. Remove the `"strings"` import if no longer used.

5. Apply the same replacement in `pkg/handler/files_delete.go` (~line 31-37). Remove the `"strings"` import if no longer used.

6. Add or update tests in `pkg/git/git_test.go` to verify:
   - A path containing `.git` (e.g., `.git/config`) returns an error wrapping `ErrInvalidPath`
   - `errors.Is(err, git.ErrInvalidPath)` returns true for all validation failures

8. Add or update tests in `pkg/handler/files_get_test.go`, `files_post_test.go`, and `files_delete_test.go` to verify:
   - An invalid path returns HTTP 400 (not 500)
   - A `.git/config` path returns HTTP 400
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Use `errors.Wrapf(ctx, ErrInvalidPath, "message")` — never `fmt.Errorf` or bare `return err`
- The `ErrInvalidPath` sentinel must use `stderrors "errors"` alias (already imported in git.go)
- Run this prompt BEFORE `review-git-rest-2-sanitize-error-responses.md`
</constraints>

<verification>
make precommit
</verification>
