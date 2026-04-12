---
status: draft
---

## Summary

- git-rest can initialize a local git repository when no remote URL is configured
- Enables standalone operation for development, testing, and ephemeral containers
- Parent directories are created automatically if they do not exist
- Push and pull operations gracefully handle the absence of a remote
- Existing deployments with pre-cloned repos or remote URLs work identically

## Problem

git-rest currently requires either a pre-cloned repository or a remote URL (`specs/in-progress/002-auto-clone-and-git-config.md`). For local development, integration testing, and ephemeral container workloads that only need file versioning without a remote, users must manually run `git init` before starting the service. This adds unnecessary setup friction for the simplest use case.

## Goal

When the configured repo path does not exist and no remote URL is provided, git-rest initializes a new local git repository. Files can be created, updated, and deleted with full git history. Pull silently succeeds (no remote to pull from). Push is not attempted.

## Assumptions

- Spec 002 (`specs/in-progress/002-auto-clone-and-git-config.md`) is already implemented
- The bootstrap method in main.go already handles remote clone logic
- Git binary is available on PATH

## Non-Goals

- Adding a remote to an initialized repo at runtime
- Bare repository support
- Multiple branch support

## Desired Behavior

1. **Auto-init on startup**: When `--git-remote-url` is NOT set and `--repo` path has no `.git` directory, git-rest initializes a new git repository at the repo path before starting the HTTP server.
2. **Directory creation**: If `--repo` path does not exist, git-rest creates the directory (and parents) before init.
3. **Pull without remote**: The puller must not fail when the repo has no remote configured. It should detect "no remote" and skip the pull silently (or log at debug level).
4. **Push without remote**: Write and delete operations must not fail when there is no remote. The `git push` step should be skipped when no remote is configured.
5. **Readiness without remote**: The readiness endpoint should return 200 when the working tree is clean, even when there is no remote to compare push status against.
6. **Backward compatible**: Repos with a remote (cloned or pre-existing) continue to push and pull normally.

## Constraints

- All new logic runs before the HTTP server starts (bootstrap phase)
- No new external dependencies
- Must not break spec 002 (`specs/in-progress/002-auto-clone-and-git-config.md`) behavior (remote clone still works)
- Push skip must be determined dynamically based on whether the repo has a remote, not configured once at startup

## Failure Modes

| Trigger | Expected Behavior | Recovery |
|---------|-------------------|----------|
| Repo dir parent doesn't exist, no remote URL | Create parent dirs, then git init | N/A |
| git init fails (permissions) | Fail startup with clear error | Fix permissions |
| Push after write in repo without remote | Skip push silently, commit is local-only | Add remote later if needed |
| Pull in repo without remote | Skip pull silently | Add remote later if needed |
| Repo path exists as a file (not directory) | Fail startup with clear error | Remove file or choose different path |

## Do-Nothing Option

Users run `git init` manually before starting git-rest. Works but adds a setup step to every local development session and every integration test that uses git-rest. The agent-trade-analysis test suite alone would need init scripts in at least 4 scenarios. For ephemeral containers (CI, one-shot jobs) the manual init means adding an entrypoint wrapper or init container for what should be the simplest deployment mode.

## Security / Abuse

- No additional attack surface — local init creates an empty repo with no network access
- Path validation (`specs/in-progress/001-git-rest-server.md`) still applies to all file operations

## Acceptance Criteria

- [ ] Starting with non-existent `--repo` dir and no `--git-remote-url` initializes a local repo
- [ ] File CRUD operations work in local-only repo (commit succeeds, push skipped)
- [ ] Puller does not error when repo has no remote
- [ ] Readiness returns 200 for clean local-only repo
- [ ] Starting with existing repo + remote still pushes and pulls normally
- [ ] `make precommit` passes

## Verification

```bash
make precommit
```
