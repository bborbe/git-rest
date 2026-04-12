---
status: completed
spec: [002-auto-clone-and-git-config]
summary: Added SSHKeyPath type to pkg/git, refactored runCmd/runCmdOutput to struct methods that set GIT_SSH_COMMAND when sshKeyPath is configured, wired the new parameter through factory.CreateGitClient and main.go application struct with fast-fail validation, and added SSH key test cases.
container: git-rest-024-spec-002-ssh-key-setup
dark-factory-version: v0.108.0-dirty
created: "2026-04-12T15:59:22Z"
queued: "2026-04-12T16:21:38Z"
started: "2026-04-12T16:21:39Z"
completed: "2026-04-12T16:27:12Z"
branch: dark-factory/auto-clone-and-git-config
---

<summary>
- Git operations now support SSH key authentication via a configurable key path
- The SSH command is set per git process so it applies to clone, pull, push, and commit
- Missing SSH key file at startup produces a clear error and prevents the service from starting
- Services without the SSH key flag behave identically to the current version
- The SSH command disables strict host key checking for automated container deployments
</summary>

<objective>
Add SSH key support to the git package so all git operations (clone, pull, push) use a configured SSH key when provided. The key path is passed from the application struct through the factory to the git client. Missing key file fails startup fast.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Read coding guides before implementing (in `~/.claude/plugins/marketplaces/coding/docs/`):
- `go-error-wrapping-guide.md`: error wrapping rules
- `go-factory-pattern.md`: factory wiring rules

Files to read before making changes:
- `main.go` — application struct and createGitClient method
- `pkg/git/git.go` — git struct, New constructor, runCmd and runCmdOutput functions (~lines 100-127)
- `pkg/factory/factory.go` — CreateGitClient function
</context>

<requirements>

## 1. Add SSH key path type

Create a new type in `pkg/git/git.go` (or a new file `pkg/ssh-key-path.go`):
```go
type SSHKeyPath string
```

## 2. Add SSHKeyPath to git struct and constructor

In `pkg/git/git.go`, add `sshKeyPath SSHKeyPath` to the `git` struct (~line 63). Update the `New` constructor (~line 51) to accept `sshKeyPath SSHKeyPath` as a parameter.

## 3. Set GIT_SSH_COMMAND on all git commands

In `runCmd` and `runCmdOutput` (~lines 102-127), these are currently package-level functions that don't have access to the struct. Refactor them to methods on the `git` struct so they can access `sshKeyPath`.

When `g.sshKeyPath != ""`, set the environment on the command:
```go
if g.sshKeyPath != "" {
    cmd.Env = append(os.Environ(), fmt.Sprintf("GIT_SSH_COMMAND=ssh -i %s -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no", string(g.sshKeyPath)))
}
```

Update all callers of `runCmd` and `runCmdOutput` in git.go to use `g.runCmd` and `g.runCmdOutput`.

## 4. Add CLI flag to application struct

In `main.go`, add to the `application` struct:
```go
GitSSHKey git.SSHKeyPath `required:"false" arg:"git-ssh-key" env:"GIT_SSH_KEY" usage:"Path to SSH private key for git operations"`
```

## 5. Validate SSH key file exists on startup

In `main.go` `createGitClient` method (~line 58), before creating the git client, validate:
```go
if a.GitSSHKey != "" {
    if _, err := os.Stat(string(a.GitSSHKey)); err != nil {
        return nil, errors.Wrapf(ctx, err, "ssh key file %s", a.GitSSHKey)
    }
}
```

## 6. Wire SSHKeyPath through factory

Update `pkg/factory/factory.go` `CreateGitClient` to accept `sshKeyPath git.SSHKeyPath` and pass it to `git.New`.

Update the call in `main.go` `createGitClient` to pass `a.GitSSHKey`.

## 7. Update tests

Update all tests that call `git.New` or `factory.CreateGitClient` to pass the new `sshKeyPath` parameter (empty string `""` for existing tests).

Add test cases:
- SSH key path set → `cmd.Env` contains `GIT_SSH_COMMAND`
- SSH key path empty → `cmd.Env` not modified
- SSH key file missing → startup returns error

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- All new logic runs before the HTTP server starts
- SSH key file must exist if specified (fail fast with clear error)
- No new external dependencies
- Use `errors.Wrap`/`errors.Wrapf` from `github.com/bborbe/errors`
</constraints>

<verification>
make precommit
</verification>
