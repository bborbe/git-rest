---
status: completed
spec: [001-git-rest-server]
summary: Created pkg/git package with Git interface, serialized shell operations via sync.Mutex, path validation, WriteFile/DeleteFile/ReadFile/ListFiles/Pull/Status implementations, Counterfeiter mock in pkg/git/mocks/fakes.go, and comprehensive Ginkgo tests with 84.1% coverage
container: git-rest-001-spec-001-git-layer
dark-factory-version: v0.108.0-dirty
created: "2026-04-11T19:30:00Z"
queued: "2026-04-11T19:37:17Z"
started: "2026-04-11T19:37:19Z"
completed: "2026-04-11T19:47:21Z"
branch: dark-factory/git-rest-server
---

<summary>
- A `Git` interface abstracts all git shell operations (add, commit, push, pull, status, glob)
- All git operations are serialized via a mutex — no concurrent git invocations are possible
- File path validation rejects `..` components, absolute paths, and empty paths before any operation
- Each mutation operation produces a commit with a deterministic message: `git-rest: create {path}`, `git-rest: update {path}`, `git-rest: delete {path}`
- The git layer detects whether a file is new (create) or existing (update) to pick the right commit message
- A `ListFiles(pattern string)` operation returns matching relative paths using `git ls-files` filtered by a glob pattern
- `Status()` returns whether the working tree is clean and has no unpushed commits (used by the readiness probe)
- The git layer uses `os/exec` to invoke the system `git` binary — no embedded library
- Counterfeiter-generated mock is produced for the `Git` interface for use in handler tests
- All git errors are wrapped with `github.com/bborbe/errors`
</summary>

<objective>
Build the serialized git operations layer that all HTTP handlers will delegate to. This layer owns path safety validation and all shell-out-to-git logic, ensuring no concurrent git commands can race. The postcondition of this prompt is a tested `pkg/git` package with a `Git` interface + implementation, plus a generated Counterfeiter mock.
</objective>

<context>
Read `CLAUDE.md` and `docs/dod.md` for project conventions.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md` for interface/constructor/struct pattern.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` for Ginkgo/Gomega/Counterfeiter conventions.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` for error wrapping rules.
Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-security-linting.md` for gosec file-permission rules.

Existing files to be aware of:
- `main.go` — skeleton only, will be wired in a later prompt
- `go.mod` — module is `github.com/bborbe/git-rest`
</context>

<requirements>
1. Create `pkg/git/git.go` with the following:
   - A `//go:generate` counterfeiter annotation targeting the `Git` interface
   - A `Git` interface (exported) with methods:
     ```go
     type Git interface {
         WriteFile(ctx context.Context, path string, content []byte) error
         DeleteFile(ctx context.Context, path string) error
         ReadFile(ctx context.Context, path string) ([]byte, error)
         ListFiles(ctx context.Context, pattern string) ([]string, error)
         Pull(ctx context.Context) error
         Status(ctx context.Context) (Status, error)
     }
     ```
   - A `Status` struct:
     ```go
     type Status struct {
         Clean       bool // working tree has no uncommitted changes
         NoPushPending bool // no commits ahead of remote
     }
     ```
   - A `New` constructor:
     ```go
     func New(repoPath string) Git
     ```
     returning `*git` (private struct implementing `Git`).
   - The private `git` struct holds:
     - `repoPath string`
     - `mu sync.Mutex` — all exported methods lock this before running any git command

2. Implement path validation as a package-private function `validatePath(path string) error`:
   - Reject empty string → `errors.New(ctx, "path must not be empty")`
   - Reject paths containing `..` anywhere → `errors.New(ctx, "path traversal not allowed")`
   - Reject absolute paths (starts with `/`) → `errors.New(ctx, "absolute paths not allowed")`
   - Use `filepath.Clean` to normalize, then re-check the result does not escape via `..`
   - All methods call `validatePath` before acquiring the mutex

3. Implement `WriteFile(ctx, path, content)`:
   - Calls `validatePath(path)`
   - Acquires `mu`
   - Determines if the file already exists (using `os.Stat` on `filepath.Join(repoPath, path)`)
   - Writes `content` to `filepath.Join(repoPath, path)`, creating intermediate directories with `os.MkdirAll` (perm `0750`)
   - Writes file with `os.WriteFile` (perm `0600`)
   - Runs `git add <path>` via `exec.CommandContext`
   - Determines commit message: `"git-rest: create <path>"` if file was new, `"git-rest: update <path>"` if file existed
   - Runs `git commit -m "<message>"`
   - Runs `git push`
   - Each command's working directory is set to `repoPath`
   - On any command error, combine stdout+stderr into the wrapped error message
   - Wrap all errors with `errors.Wrapf(ctx, err, "...")`

4. Implement `DeleteFile(ctx, path)`:
   - Calls `validatePath(path)`
   - Acquires `mu`
   - Checks if file exists; if not, returns `ErrNotFound` (define `var ErrNotFound = stderrors.New("file not found")` using `stderrors "errors"` alias)
   - Runs `git rm <path>`
   - Runs `git commit -m "git-rest: delete <path>"`
   - Runs `git push`

5. Implement `ReadFile(ctx, path)`:
   - Calls `validatePath(path)`
   - Acquires `mu`
   - Reads `filepath.Join(repoPath, path)` with `os.ReadFile`
   - Returns `ErrNotFound` if `os.IsNotExist(err)` is true
   - Wraps other errors

6. Implement `ListFiles(ctx, pattern)`:
   - Acquires `mu` (no path validation needed — pattern is a glob, not a file path)
   - Runs `git ls-files` in `repoPath`
   - Filters results client-side using `filepath.Match(pattern, filename)` if pattern is non-empty; if pattern is empty, returns all files
   - Returns `[]string` of matching relative paths

7. Implement `Pull(ctx)`:
   - Acquires `mu`
   - Runs `git pull` in `repoPath`
   - Wraps errors

8. Implement `Status(ctx)`:
   - Acquires `mu`
   - Runs `git status --porcelain` — if output is non-empty, `Clean = false`
   - Runs `git log @{u}..HEAD --oneline` — if output is non-empty, `NoPushPending = false`; if the upstream tracking branch doesn't exist yet, treat as clean (no error)
   - Returns populated `Status{}`

9. Create `pkg/git/mocks/mocks.go` by running:
   ```
   cd /workspace && go generate ./pkg/git/...
   ```
   The `//go:generate` directive must be:
   ```go
   //go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -o mocks/fakes.go . Git
   ```

10. Create `pkg/git/git_test.go` (package `git_test`) with Ginkgo/Gomega tests:
    - Test suite bootstrap with `RunSpecs`
    - `validatePath` is not exported — test it indirectly through `ReadFile`/`WriteFile`/`DeleteFile` calls on a real temp-dir git repo initialized with `git init && git commit --allow-empty -m "init"`
    - Test cases:
      - `ReadFile` with empty path → error containing "must not be empty"
      - `ReadFile` with `../../../etc/passwd` → error containing "traversal"
      - `ReadFile` with `/absolute` → error containing "absolute"
      - `ReadFile` on non-existent file → `ErrNotFound`
      - `WriteFile` + `ReadFile` round-trip on a real git repo (use `git init` in a temp dir; skip push step by stubbing — or test up to commit if no remote configured, handling push error gracefully in tests)
      - `DeleteFile` on non-existent file → `ErrNotFound`
      - `ListFiles` with empty pattern returns all files
      - `ListFiles` with `*.md` only returns `.md` files
    - Coverage ≥ 80% for `pkg/git`
</requirements>

<constraints>
- Git operations must use `os/exec` + the system `git` binary — do NOT use `go-git` or any embedded library
- All git operations (per-method) must be serialized via `sync.Mutex` — no two git commands may run concurrently
- Commit messages must be exactly: `git-rest: create {path}`, `git-rest: update {path}`, `git-rest: delete {path}`
- All errors must be wrapped with `github.com/bborbe/errors` — never `fmt.Errorf`, never bare `return err`
- `context.Background()` must NOT appear in `pkg/` — always propagate the incoming `ctx`
- File permissions: directories `0750`, files `0600` (gosec requirement)
- Path validation must reject `..`, absolute paths, and empty strings
- Do NOT commit — dark-factory handles git
- Existing tests in `main_test.go` must still pass
</constraints>

<verification>
Run `make test` after implementing to check tests pass, then run `make precommit` as final validation.

Additional checks:
```bash
# Confirm path traversal is rejected
cd /workspace && go test ./pkg/git/... -run "traversal" -v

# Confirm coverage
go test -coverprofile=/tmp/cover.out -mod=vendor ./pkg/git/... && go tool cover -func=/tmp/cover.out | grep total
```
</verification>
