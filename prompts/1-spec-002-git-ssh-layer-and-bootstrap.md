---
status: created
spec: [002-auto-clone-and-git-config]
created: "2026-04-12T16:10:00Z"
branch: dark-factory/auto-clone-and-git-config
---

<summary>
- The git package gains SSH key support: all git commands (pull, push, commit, etc.) forward GIT_SSH_COMMAND when an SSH key path is configured
- runCmd and runCmdOutput become methods on the git struct so they can pick up the SSH env automatically
- A new package-level Clone function runs `git clone` with SSH env before the git struct exists
- A new package-level ConfigureUser function runs `git config user.name` / `git config user.email` in the repo
- A new `pkg/bootstrap/` package encapsulates all startup checks: SSH key file existence, parent directory creation, clone-if-needed, and git identity configuration
- factory.CreateGitClient accepts an sshKeyPath parameter and threads it through to git.New
- All new code is covered by tests
</summary>

<objective>
Add SSH key propagation to the git layer and create a bootstrap package that handles auto-clone and git identity configuration on startup. This is the foundation for the next prompt which wires CLI flags and invokes bootstrap from main.go.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Read coding guides before implementing (in `~/.claude/plugins/marketplaces/coding/docs/`):
- `go-patterns.md` — Interface→Constructor→Struct, error wrapping, counterfeiter
- `go-factory-pattern.md` — Create* prefix, zero logic factories
- `go-testing-guide.md` — Ginkgo/Gomega, counterfeiter mocks, coverage ≥80%
- `go-security-linting.md` — gosec rules for file permissions, #nosec with reasons

Files to read before making changes:
- `pkg/git/git.go` — current runCmd/runCmdOutput functions, git struct, New constructor
- `pkg/git/git_test.go` — existing test helpers (initRepo, runGit, noopMetrics)
- `pkg/git/git_suite_test.go` — test suite bootstrap
- `pkg/factory/factory.go` — CreateGitClient signature to update
</context>

<requirements>

## 1. Convert runCmd and runCmdOutput to methods on git struct

In `pkg/git/git.go`, change:
```go
func runCmd(ctx context.Context, dir string, args ...string) error
func runCmdOutput(ctx context.Context, dir string, args ...string) ([]byte, error)
```
to methods:
```go
func (g *git) runCmd(ctx context.Context, dir string, args ...string) error
func (g *git) runCmdOutput(ctx context.Context, dir string, args ...string) ([]byte, error)
```

In each method, inject `g.sshEnv` into the command environment:
```go
cmd.Env = append(os.Environ(), g.sshEnv...)
```

Add `sshEnv []string` field to the `git` struct.

Update all call sites inside the struct methods from `runCmd(ctx, g.repoPath, ...)` to `g.runCmd(ctx, g.repoPath, ...)` and similarly for `runCmdOutput`.

Keep the `#nosec G204` comment on each method (binary is still hardcoded to "git").

## 2. Update New constructor to accept sshKeyPath

Change `New` constructor signature:
```go
func New(
    repoPath string,
    sshKeyPath string,
    m metrics.Metrics,
    currentDateTimeGetter libtime.CurrentDateTimeGetter,
) Git
```

In the constructor body, build sshEnv when sshKeyPath is non-empty:
```go
var sshEnv []string
if sshKeyPath != "" {
    sshEnv = []string{
        "GIT_SSH_COMMAND=ssh -i " + sshKeyPath + " -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no",
    }
}
```

Set `sshEnv` on the returned `git` struct.

## 3. Add package-level Clone function

Add to `pkg/git/git.go`:
```go
// Clone clones remoteURL into repoPath using sshKeyPath for authentication.
// If sshKeyPath is empty, no SSH configuration is applied.
func Clone(ctx context.Context, repoPath string, remoteURL string, sshKeyPath string) error
```

Implementation:
- Build cmd: `exec.CommandContext(ctx, "git", "clone", remoteURL, repoPath)`
- If sshKeyPath is non-empty, set `cmd.Env = append(os.Environ(), "GIT_SSH_COMMAND=ssh -i "+sshKeyPath+" -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no")`
- Capture stdout+stderr into a buffer; on error wrap with `errors.Wrapf(ctx, err, "git clone %s: %s", remoteURL, buf.String())`
- Add `// #nosec G204 -- binary is hardcoded to "git"; remoteURL is operator-supplied config, not user input`

## 4. Add package-level ConfigureUser function

Add to `pkg/git/git.go`:
```go
// ConfigureUser sets git user.name and user.email in repoPath's local config.
// Empty strings are skipped.
func ConfigureUser(ctx context.Context, repoPath string, name string, email string) error
```

Implementation:
- Use the existing package-level `runCmd` pattern but implemented inline (since `runCmd` is now a method):
  - For each non-empty value, run `git config user.name <name>` or `git config user.email <email>` using a local helper that mirrors the old `runCmd` body (no SSH env needed for config commands)
- Alternatively, implement a small local helper `runGitCmd(ctx context.Context, dir string, args ...string) error` that is package-level (for use by Clone and ConfigureUser only, not the git struct methods).

Implementation details for the helper:
```go
func runGitCmd(ctx context.Context, dir string, args ...string) error {
    // #nosec G204 -- binary is hardcoded to "git"
    cmd := exec.CommandContext(ctx, "git", args...)
    cmd.Dir = dir
    var buf bytes.Buffer
    cmd.Stdout = &buf
    cmd.Stderr = &buf
    if err := cmd.Run(); err != nil {
        return errors.Wrapf(ctx, err, "git %v: %s", args, buf.String())
    }
    return nil
}
```

Then `Clone` and `ConfigureUser` use `runGitCmd`. The struct methods use `g.runCmd`/`g.runCmdOutput`.

## 5. Create pkg/bootstrap/ package

Create `pkg/bootstrap/bootstrap.go`:

```go
// Bootstrapper prepares a git repository before the HTTP server starts.
//
//counterfeiter:generate -o ../../mocks/bootstrapper.go --fake-name FakeBootstrapper . Bootstrapper
type Bootstrapper interface {
    Bootstrap(ctx context.Context) error
}
```

Constructor:
```go
// New returns a Bootstrapper that ensures the repository at repoPath is ready.
func New(
    repoPath string,
    remoteURL string,
    sshKeyPath string,
    userName string,
    userEmail string,
) Bootstrapper
```

Private struct:
```go
type bootstrapper struct {
    repoPath   string
    remoteURL  string
    sshKeyPath string
    userName   string
    userEmail  string
}
```

`Bootstrap` implementation:
1. If `sshKeyPath` is non-empty: check that the file exists using `os.Stat`. If it does not exist, return `errors.Errorf(ctx, "ssh key file not found: %s", sshKeyPath)`.
2. If `remoteURL` is non-empty:
   a. Check if `repoPath/.git` exists via `os.Stat`. If it exists, skip clone.
   b. If `.git` does not exist: create parent directory with `os.MkdirAll(repoPath, 0750)`. Then call `git.Clone(ctx, repoPath, remoteURL, sshKeyPath)`.
3. If `userName != ""`: call `git.ConfigureUser(ctx, repoPath, userName, "")`.
4. If `userEmail != ""`: call `git.ConfigureUser(ctx, repoPath, "", userEmail)`.

Wait — ConfigureUser should handle empty strings by skipping. So one call is fine:
3. Call `git.ConfigureUser(ctx, repoPath, userName, userEmail)` — the function skips empty values internally.

Return nil on success.

## 6. Create pkg/bootstrap/bootstrap_suite_test.go

```go
package bootstrap_test

import (
    "testing"
    "time"

    "github.com/onsi/ginkgo/v2"
    "github.com/onsi/gomega"
    "github.com/onsi/gomega/format"
)

func TestBootstrap(t *testing.T) {
    format.TruncatedDiff = false
    time.Local = time.UTC
    gomega.RegisterFailHandler(ginkgo.Fail)
    ginkgo.RunSpecs(t, "Bootstrap Suite", ginkgo.Label())
}
```

## 7. Create pkg/bootstrap/bootstrap_test.go

Write tests covering:
- Bootstrap with no flags set: no error (no-op)
- Bootstrap with sshKeyPath that doesn't exist: returns error containing "ssh key file not found"
- Bootstrap with sshKeyPath that exists: no error from file check
- Bootstrap with remoteURL but `.git` already exists: skips clone (no clone call made)
- Bootstrap with remoteURL and no `.git`: calls Clone (use a temp local bare repo as remote)
- Bootstrap with userName and userEmail: calls ConfigureUser and sets identity in repo

Use a real temp directory for integration-style tests (following the pattern in `pkg/git/git_test.go` with `os.MkdirTemp`). Create a local bare git repo to serve as remote for clone tests.

## 8. Update factory.CreateGitClient

In `pkg/factory/factory.go`, update `CreateGitClient` signature:
```go
func CreateGitClient(
    repoPath string,
    sshKeyPath string,
    m metrics.Metrics,
    currentDateTimeGetter libtime.CurrentDateTimeGetter,
) git.Git {
    return git.New(repoPath, sshKeyPath, m, currentDateTimeGetter)
}
```

The existing call site in `main.go` passes `""` for sshKeyPath (the next prompt wires the real value).

## 9. Update main.go call site

In `main.go`, update the `factory.CreateGitClient` call in `createGitClient` to pass an empty string for `sshKeyPath` for now:
```go
return factory.CreateGitClient(a.Repo, "", metrics.NewMetrics(), libtime.NewCurrentDateTime()), nil
```

This keeps `main.go` compiling while the next prompt adds the real flag.

## 10. Regenerate counterfeiter mocks

Run:
```bash
cd /workspace && go generate ./pkg/bootstrap/...
```

If counterfeiter is not available via `go generate`, manually create `mocks/bootstrapper.go` following the pattern of `mocks/git.go`. Check `mocks/git.go` first to understand the structure.

</requirements>

<constraints>
- Only change files in `/workspace`
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- Use `errors.Wrap`/`errors.Wrapf`/`errors.Errorf` from `github.com/bborbe/errors` — never `fmt.Errorf` or bare `return err`
- No `context.Background()` in `pkg/` — only in `main.go`
- Bootstrap must have ≥80% statement coverage
- SSH key path is a CLI flag (operator-supplied config) — no path traversal risk; document with `#nosec` if gosec flags it
- `StrictHostKeyChecking=no` is intentional for container deployments — add a comment explaining why
</constraints>

<verification>
Run `make test` after implementing each step to catch issues early.

```bash
cd /workspace && make precommit
```
</verification>
