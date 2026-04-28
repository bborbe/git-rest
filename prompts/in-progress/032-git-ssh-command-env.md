---
status: committing
summary: Added GitSSHCommand field, resolveGitSSHCommand function, updated runGitCmd signature to accept sshCommand, threaded SSH command through bootstrap/recoverUntracked/syncOnStartup, exported ResolveGitSSHCommand for tests, added 4 unit test cases, and updated CHANGELOG.md.
container: git-rest-032-git-ssh-command-env
dark-factory-version: v0.135.19-1-gc08c946
created: "2026-04-28T21:25:00Z"
queued: "2026-04-28T21:11:40Z"
started: "2026-04-28T21:15:00Z"
---

<summary>
- The git-rest container exposes a new optional env var that controls how git authenticates over SSH
- When set, it is used as-is for every git network operation (pull, push) at startup
- When empty, it is derived from the existing SSH key path so today's deployments keep working unchanged
- Local-only deployments (no remote) are unaffected — no SSH command is needed there
- Closes the v0.15.0 bug where startup pull/push failed with "Host key verification failed" because the bootstrap helper did not set the SSH wrapper that the periodic puller already uses
- Operators can now override the SSH wrapper for diagnostics (e.g. `-vvv`, alternate key paths) without rebuilding the image
</summary>

<objective>
Add a new optional config field `GitSSHCommand` (env `GIT_SSH_COMMAND`, arg `git-ssh-command`) to the application. Resolve it at startup: if non-empty, use as-is; if empty AND `GitSSHKey` is set, derive `ssh -i <key> -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no`. Thread the resolved value into the existing `runGitCmd` helper so all main.go-level git invocations (`recoverUntracked`, `syncOnStartup`) authenticate correctly. Closes the v0.15.0 SSH host-key failure observed in dev today.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Read coding guides before implementing (in `~/.claude/plugins/marketplaces/coding/docs/`):
- `go-error-wrapping-guide.md`
- `go-logging-guide.md` (this repo uses `log/slog` only)
- `go-testing-guide.md`

Files to read:
- `main.go` — `application` struct (~line 34), `runGitCmd` helper, `bootstrap()` calls to `recoverUntracked` and `syncOnStartup`
- `pkg/git/git.go` — the existing SSH wrapper inside `runCmd` (search for `GIT_SSH_COMMAND`); copy the format string verbatim so behavior matches the periodic puller exactly
- `main_test.go` — Ginkgo suite at repo root; existing `Describe("CleanupStaleLocks", ...)`, `Describe("RecoverUntracked", ...)`, `Describe("SyncOnStartup", ...)` blocks are the test pattern
- `export_test.go` — exposes the helpers; you'll add a new export for the SSH-command resolver if needed
- `CHANGELOG.md` — versioned sections; latest is `## v0.15.0`. Add a `## Unreleased` section above it
</context>

<requirements>

## 1. Add `GitSSHCommand` to the application struct

In `main.go`, add directly below the existing `GitSSHKey` field:

```go
GitSSHCommand string `required:"false" arg:"git-ssh-command" env:"GIT_SSH_COMMAND" usage:"Full SSH command for git network ops (overrides default derived from --git-ssh-key). Empty = derive from --git-ssh-key."`
```

## 2. Add a package-level resolver

Place near `runGitCmd` in `main.go`:

```go
// resolveGitSSHCommand returns the SSH command git should use for network
// operations. Precedence:
//
//  1. Explicit override (gitSSHCommand non-empty)        — used as-is
//  2. Derived from sshKeyPath (sshKeyPath non-empty)     — `ssh -i <key>
//                                                          -o UserKnownHostsFile=/dev/null
//                                                          -o StrictHostKeyChecking=no`
//  3. Both empty (local-only deployment)                 — empty string;
//                                                          callers MUST NOT
//                                                          set GIT_SSH_COMMAND
//                                                          when this is empty
//
// The derived form matches the format used by pkg/git/git.go's runCmd so the
// boot-time path and the periodic puller authenticate identically.
func resolveGitSSHCommand(gitSSHCommand, sshKeyPath string) string {
    if gitSSHCommand != "" {
        return gitSSHCommand
    }
    if sshKeyPath != "" {
        return "ssh -i " + sshKeyPath + " -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no"
    }
    return ""
}
```

## 3. Modify `runGitCmd` to honor the SSH command

Add a parameter (NOT a global) so each call site can choose. Signature change:

```go
// before
func runGitCmd(ctx context.Context, repoDir string, args ...string) (string, error)

// after
func runGitCmd(ctx context.Context, repoDir, sshCommand string, args ...string) (string, error)
```

Inside the function, after constructing `cmd := exec.CommandContext(...)`:

```go
if sshCommand != "" {
    cmd.Env = append(os.Environ(), "GIT_SSH_COMMAND="+sshCommand)
}
```

(Do NOT set `cmd.Env` when `sshCommand` is empty — that would erase the parent env. Only set when non-empty.)

## 4. Thread the resolved value through bootstrap

In `main.go`, in `(*application).bootstrap()`:

```go
func (a *application) bootstrap(ctx context.Context) error {
    sshCommand := resolveGitSSHCommand(a.GitSSHCommand, string(a.GitSSHKey))

    if err := cleanupStaleLocks(ctx, a.Repo); err != nil {
        return errors.Wrap(ctx, err, "cleanup stale locks")
    }
    if err := a.initIfNeeded(ctx); err != nil {
        return errors.Wrap(ctx, err, "init if needed")
    }
    if err := a.cloneIfNeeded(ctx); err != nil {
        return errors.Wrap(ctx, err, "clone if needed")
    }
    if err := a.configureUserIfSet(ctx); err != nil {
        return errors.Wrap(ctx, err, "configure user if set")
    }
    if err := recoverUntracked(ctx, a.Repo, sshCommand); err != nil {
        return errors.Wrap(ctx, err, "recover untracked")
    }
    if err := syncOnStartup(ctx, a.Repo, sshCommand); err != nil {
        return errors.Wrap(ctx, err, "sync on startup")
    }
    return nil
}
```

Update `recoverUntracked` and `syncOnStartup` signatures to accept `sshCommand string`. Pass the resolved value through to EVERY internal `runGitCmd` call uniformly — including `recoverUntracked`'s `git status`/`git add`/`git commit`. The env var is harmless on non-network commands and uniform call sites prevent future drift if any of those commands ever grows a network operation.

## 5. Update tests

`recoverUntracked` and `syncOnStartup` tests already exist. Update each to pass an empty `sshCommand` argument (`""`) — these tests use local file URLs / no remote / a local bare repo, none of which need SSH. The signatures must still match.

Add a focused unit test for `resolveGitSSHCommand` (it's exported via `export_test.go`):

```go
// in export_test.go
var ResolveGitSSHCommand = resolveGitSSHCommand
```

Test cases (in `main_test.go`, new `Describe("ResolveGitSSHCommand", ...)` block):

1. **Both empty** → returns empty string
2. **Only sshKeyPath set** → returns the derived format with the key path interpolated
3. **Only gitSSHCommand set** → returns it verbatim
4. **Both set** → returns gitSSHCommand verbatim (override wins)

## 6. Update CHANGELOG

`CHANGELOG.md` uses versioned sections; latest is `## v0.15.0`. Add `## Unreleased` directly after the preamble, above `## v0.15.0`:

```markdown
## Unreleased

- fix: `runGitCmd` now sets `GIT_SSH_COMMAND` for git network ops at startup, fixing the v0.15.0 bug where `syncOnStartup`'s pull/push failed with "Host key verification failed" because the SSH wrapper used by the periodic puller was not applied to the bootstrap path.
- feat: New `GIT_SSH_COMMAND` env var (and `--git-ssh-command` arg) for explicit SSH wrapper override. When unset, derives from `GIT_SSH_KEY` using the same format `pkg/git` already uses (`ssh -i <key> -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no`). Existing deployments need no config change.

## v0.15.0
```

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass (the prior `Describe` blocks for `CleanupStaleLocks`/`RecoverUntracked`/`SyncOnStartup` continue to work after the signature change — they pass `""` for `sshCommand`)
- Use `errors.Wrap`/`errors.Wrapf` from `github.com/bborbe/errors` — never `fmt.Errorf`
- Use `log/slog` — do NOT add `github.com/golang/glog`
- No new external dependencies
- Match the SSH command format from `pkg/git/git.go` exactly (do not change the option order; the deployed pods rely on it)
- `runGitCmd` must NOT set `cmd.Env` when `sshCommand` is empty — that would erase the inherited parent env
- `resolveGitSSHCommand` is a package-level function (not a method) so it is testable in isolation
- The new `GitSSHCommand` field is `required: false` — no existing deployment is forced to set it
- Do NOT modify `pkg/git/git.go` — that is a separate refactor; this prompt is scoped to `main.go` callers only
</constraints>

<verification>
`make precommit` — must pass.

Manual probe (after deploy):
```bash
# Default-derived path: GIT_SSH_COMMAND env unset, GIT_SSH_KEY=/ssh/id_ed25519
kubectlquant -n dev delete pod vault-obsidian-trading-0
kubectlquant -n dev logs vault-obsidian-trading-0 | grep -iE "startup git|recover"
# Expected:
#   "recovered untracked files into a commit"  (only if a recovery commit is pending)
#   "startup git pull succeeded"
#   "startup git push succeeded"
# No "Host key verification failed" anywhere.

# Verify the unpushed recovery commit landed upstream
kubectlquant -n dev exec vault-obsidian-trading-0 -- sh -c "cd /data && git log @{u}..HEAD --oneline"
# Expected: empty (recovery commit was pushed)
```
</verification>
