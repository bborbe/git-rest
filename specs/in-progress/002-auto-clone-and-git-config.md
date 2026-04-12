---
status: verifying
approved: "2026-04-12T15:57:08Z"
generating: "2026-04-12T15:57:08Z"
prompted: "2026-04-12T16:00:18Z"
verifying: "2026-04-12T16:38:28Z"
branch: dark-factory/auto-clone-and-git-config
---

## Summary

- git-rest requires a pre-cloned repo, forcing K8s init containers for cloning and SSH setup
- New optional flags let git-rest clone the repo and configure git identity on startup
- SSH key path configures all git operations (clone, pull, push) automatically
- Existing deployments without new flags work identically to current behavior
- Eliminates init container complexity for every git-rest deployment

## Problem

Deploying git-rest in Kubernetes requires init containers or entrypoint scripts to copy SSH keys, clone the repo, and configure git identity. This logic is duplicated in every deployment, fragile to maintain, and has already caused issues with the agent-trade-analysis deployment (three init containers just to bootstrap a single git repo). The service should be able to bootstrap itself from configuration alone.

## Goal

git-rest accepts optional configuration for SSH key, remote URL, and git author identity. On startup it ensures the repository exists (cloning if needed) and configures git user settings. Existing deployments with pre-cloned repos continue to work unchanged.

## Non-goals

- Managing multiple repositories (one git-rest instance = one repo)
- Supporting HTTPS authentication (SSH only for now)
- Rotating SSH keys at runtime
- Creating the SSH key itself (must be provided as a file)

## Desired Behavior

1. **New CLI flags**: git-rest accepts `--git-remote-url` (`GIT_REMOTE_URL`), `--git-ssh-key` (`GIT_SSH_KEY`), `--git-user-name` (`GIT_USER_NAME`), `--git-user-email` (`GIT_USER_EMAIL`). All are optional.
2. **Auto-clone on startup**: When `--git-remote-url` is set and `--repo` path has no `.git` directory, git-rest clones the remote URL into the repo path before starting the HTTP server. If `.git` already exists, it skips cloning.
3. **SSH key setup**: When `--git-ssh-key` is set, git-rest sets `GIT_SSH_COMMAND` environment variable to `ssh -i <key-path> -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no` before any git operation. This applies to clone, pull, push, and all other git commands.
4. **Git user configuration**: When `--git-user-name` and/or `--git-user-email` are set, git-rest runs `git config user.name` and `git config user.email` in the repo after clone or on startup.
5. **Repo directory creation**: If `--repo` path does not exist and `--git-remote-url` is set, git-rest creates the parent directories before cloning.
6. **Backward compatible**: When none of the new flags are set, behavior is identical to current version. Startup still fails if repo doesn't exist and no remote URL is configured.

## Constraints

- All new logic runs before the HTTP server starts
- SSH key file must exist if `--git-ssh-key` is specified (fail fast with clear error)
- `--git-remote-url` without `--git-ssh-key` is valid (repo may use HTTPS or local path)
- SSH configuration must apply to all git operations (clone, pull, push, commit)
- No new external dependencies
- Container image must provide `git` and `ssh` binaries

## Failure Modes

| Trigger | Expected Behavior | Recovery |
|---------|-------------------|----------|
| SSH key file does not exist | Fail startup with clear error message | Fix SSH key path or mount |
| Clone fails (wrong URL, auth error) | Fail startup with git error output | Fix URL or credentials, restart |
| Repo path exists but is not a git repo and no remote URL | Fail startup (current behavior) | Provide `--git-remote-url` or pre-clone |
| Repo path exists with .git and remote URL set | Skip clone, proceed normally | N/A |
| Repo dir parent doesn't exist, remote URL set | Create parent dirs, then clone | N/A |
| Repo dir parent doesn't exist, no remote URL | Fail startup (current behavior) | Create directory or provide remote URL |

## Do-Nothing Option

Keep using init containers in K8s. Works today but each git-rest deployment needs 2-3 init containers with duplicated shell scripts for SSH setup, cloning, and git config. The agent-trade-analysis deployment already hit this — adding git-rest for the trading vault required writing init container logic that git-rest itself should own. Every future vault deployment would duplicate the same pattern.

## Security / Abuse

- SSH key path is validated to exist — no path traversal risk since it's a CLI flag, not user input
- `StrictHostKeyChecking=no` is acceptable for automated deployments; the alternative (managing known_hosts) adds complexity without meaningful security benefit in a container environment
- SSH key file permissions are not changed by git-rest — the deployer (K8s secret mount) must set correct permissions

## Acceptance Criteria

- [ ] `--git-remote-url`, `--git-ssh-key`, `--git-user-name`, `--git-user-email` flags accepted
- [ ] Starting with empty `--repo` dir + `--git-remote-url` clones the repo
- [ ] Starting with existing repo + `--git-remote-url` skips clone
- [ ] Starting without new flags works identically to current behavior
- [ ] `--git-ssh-key` sets GIT_SSH_COMMAND for all git operations (clone, pull, push)
- [ ] `--git-user-name` and `--git-user-email` configure git identity in repo
- [ ] Missing SSH key file at startup produces clear error
- [ ] `make precommit` passes

## Verification

```bash
make precommit
```
