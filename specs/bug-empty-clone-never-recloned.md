---
status: draft
kind: bug
created: 2026-11-03
---

## Summary

- A repository directory that contains a `.git` but no commits is treated as "already cloned", so git-rest never clones it and never recovers.
- Every pull then fails at upstream resolution, forever, across restarts — the state lives on the volume, not in the container.
- The pod reports ready while serving nothing: from `v0.31.0` readiness only checks that the repo path is readable, so an empty clone looks healthy to the probe.
- The failed clone that produced the empty `.git` is never surfaced, so the operator sees a pull error instead of a clone error.
- The recovery path returns silence when it cannot read the repository state, so the one place that could explain the wedge reports nothing.

## Problem

`cloneIfNeeded` decides whether to clone by testing whether the `.git` directory exists. A clone that failed partway — a refused SSH key, a killed process, a network drop — can leave that directory behind with no refs and no commits. From then on the early return is taken on every start, the clone is never retried, and the puller fails one step later at `@{u}`, where the error reads like a repository-state problem rather than the missing-clone problem it is. The condition is self-perpetuating: no code path re-clones, and no restart changes anything.

This cost a dev vault replica nine days of downtime (`vault-obsidian-agent-0`, GKE `seibert-k8s` dev cluster, namespace `agent`, 2026-10-25 → 2026-11-03), during which every write to that vault failed and a second workload (`agent-task-controller-agent`) crash-looped on the resulting unready service. The same failure is already recorded in this repo's own deployment notes, measured 2026-11-01 on `bborbe/obsidian-openclaw` (`docs/deployment.md:167`): a partial `.git` accepted by the next start's `cloneIfNeeded`, leaving the pod at `1/1 Ready` while serving an **empty working tree**, permanently.

## Goal

A git-rest instance whose repository directory holds a `.git` with no resolvable HEAD clones from its configured remote on startup, logs what it discarded in doing so, and leaves no state in which a failed clone is mistaken for a usable one.

## Non-goals

- No re-clone retry loop, no backoff, no startup health signal.
- No change to the readiness contract, the pull state machine, the dirty-tree rescue, the quarantine paths or the conflict resolver.
- No new configuration field or flag.

## Reproduction

**Affected versions:** the defect is present through `v0.40.2` (HEAD of `master` at filing). Readiness semantics changed in `v0.31.0` (`CHANGELOG.md:108-110`), which is why the same defect looks different before and after that release.

Observed on the dev agent vault, 2026-11-03, image `git-rest:v0.29.2`, `REPO=/data`, `GIT_REMOTE_URL=ssh://git@forgejo.seibert.tools:7999/data/obsidian-agent.git`:

```
$ kubectldev -n agent exec vault-obsidian-agent-0 -- git -C /data status
No commits yet
nothing to commit (create/copy files and use "git add" to track)

$ kubectldev -n agent exec vault-obsidian-agent-0 -- git -C /data rev-parse HEAD
fatal: ambiguous argument 'HEAD': unknown revision or path not in the working tree.

$ kubectldev -n agent exec vault-obsidian-agent-0 -- git -C /data branch -a -v
fatal: failed to resolve HEAD as a valid ref

$ kubectldev -n agent exec vault-obsidian-agent-0 -- git -C /data rev-list --count HEAD --not --remotes
fatal: ambiguous argument 'HEAD': unknown revision or path not in the working tree.
```

The puller repeats, every `PULL_INTERVAL` (30s), for the container's whole life:

```
puller.go WARN git pull failed error="exit status 128
  git [rev-parse --abbrev-ref --symbolic-full-name @{u}]: fatal: HEAD does not point to a branch
  ... (*git).Pull ... no upstream configured ..."
```

⚠️ That wording is part of the misdirection: `@{u}` prints the detached-HEAD message for an **unborn** HEAD too, so the log points at `recoverRepoState` rather than at the missing clone.

Minimal local reproduction, no cluster needed, and the form the fix must replay:

```
mkdir -p /tmp/empty-clone/repo && git -C /tmp/empty-clone/repo init   # .git exists, zero commits
git init --bare /tmp/empty-clone/remote.git
<git-rest binary> --repo /tmp/empty-clone/repo --git-remote-url /tmp/empty-clone/remote.git
git -C /tmp/empty-clone/repo rev-parse HEAD      # pre-fix: exit 128; post-fix: exit 0
```

## Expected vs Actual

**Expected** — `README.md:29` documents the contract: "Auto-clone on startup if `--git-remote-url` is set and `--repo` is empty". A directory that cannot resolve `HEAD` is not an empty `--repo` in any usable sense — `git status` itself reports `No commits yet` — so it is cloned. If cloning fails, the failure is returned with the remote URL in the message.

**Actual** — the early return keys on `os.Stat(gitDir)` alone (`cloneIfNeeded`, `main.go:346-349`). The failed clone's residue is indistinguishable from a healthy clone, the clone is never retried, and the failure surfaces downstream as an untyped `no upstream configured`, which names neither the clone nor the remote.

## Why this is a bug

`cloneIfNeeded` exists to guarantee the repository is present before the puller runs. A directory that cannot resolve `HEAD` has no repository in any usable sense, yet it satisfies the only test the function applies, so the guarantee it is named for is not delivered. From `v0.31.0` the readiness probe no longer compensates: it checks only that the repo path is readable (`pkg/handler/repo_readiness.go`, `docs/deployment.md:153`), so a replica serving nothing reports `1/1`. The state is unreachable by any restart and reachable from an ordinary transient failure.

## Acceptance Criteria

- [ ] **An empty repository is cloned on startup.** Starting from a directory that contains `.git` and zero commits, the clone runs and the resulting repository resolves `HEAD` to the remote's default branch. Evidence: a spec that initialises an empty repo, invokes the startup clone against a local bare remote, asserts `git rev-parse --abbrev-ref HEAD` exits 0 and prints the default branch name; and that a failing remote returns an error whose message contains the remote URL. (Second clause is a regression-lock — the wrap already exists in `cloneIfNeeded`.)
- [ ] **A healthy clone is not re-cloned (negative).** Given a repository that already resolves `HEAD`, the startup clone leaves it untouched. Evidence: the HEAD sha recorded before and after is identical, and `git -C <repo> status --short` returns empty.
- [ ] **The re-clone does not silently discard local-only content.** When a HEAD-less directory is re-cloned, any path present locally but absent from the remote is named in a WARN log line before it is removed — and paths that **are** on the remote are not named. Evidence: a spec seeding a remote-present file plus one local-only file in a HEAD-less repo, then asserting the WARN line lists the local-only path, does **not** list the remote-present path, and that the remote's file is present afterwards.
- [ ] **An unresolvable HEAD is surfaced through `Pull`, not swallowed.** `Pull` returns an error wrapping `ErrRepoUnrecoverable` when the repository's HEAD cannot be resolved, instead of an untyped upstream error. Evidence: a spec that removes `.git/HEAD`, calls `Pull`, and asserts `err != nil` and `errors.Is(err, ErrRepoUnrecoverable)` — the existing pattern at `pkg/git/git_test.go:1947`.
- [ ] **`make precommit` exits 0** on the branch.
- [ ] **Post-Deploy (Rung-2):** dev serves a real repository, and the running binary carries this fix. Evidence, on `vault-obsidian-agent-0` after the deploy: `kubectldev -n agent exec vault-obsidian-agent-0 -- git -C /data rev-parse --abbrev-ref HEAD` exits 0 and prints `master`; `git -C /data log --oneline -1` returns a commit; and the pod logs an INFO line from the successful clone naming the remote.
  - `deploy_check:` `kubectldev -n agent exec vault-obsidian-agent-0 -- wget -qO- http://localhost:9090/metrics | grep -m1 '^git_rest_build_info' | sed -E 's/.*commit="([^"]+)".*/\1/' | grep -E '^[0-9a-f]{7,40}$'`
  - `deploy_target:` `$(git rev-parse --short HEAD)`
  - ⚠️ The image tag cannot be the gate here: the agent vault runs the floating `git-rest:dev` tag with `imagePullPolicy: Always` (`docs/verifying-specs.md:399,427`), so a tag comparison "would succeed no matter which build is deployed". The commit baked into the binary is the discriminating check.
  - ⚠️ `readyReplicas`/`/readiness` are deliberately **not** this AC's evidence: from `v0.31.0` readiness only checks that the repo path is readable, so an empty clone reports `1/1`.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — format, generate, test, lint clean
- **Replay the reproduction** — the mandatory bug check (`bug-workflow.md`): `git init` a directory, `git init --bare` a local remote, start the built binary against both, and assert `git -C <repo> rev-parse HEAD` exits 0 and the log carries the clone INFO line. Before the fix this leaves the repo HEAD-less; after it, HEAD resolves.
- `grep -n 'ErrRepoUnrecoverable' pkg/git/git.go` — the new return site exists

### Operator-executable (runs on the host after merge, spec verification ladder)

- `gh pr view <n> --json state,mergeCommit` — PR merged
- `git tag --contains <merge-sha>` — the release tag contains the merge
- Deploy to the agent vault per `docs/verifying-specs.md:380-401`: move the mirror pin in `seibert-data/agent`'s `git-rest/Dockerfile` to the released tag, rebuild the dev mirror, `BRANCH=dev make buca`, then `kubectldev -n agent rollout restart statefulset/vault-obsidian-agent`. ⚠️ This is **not** the nuke-hosted vault deployment (`docs/deployment.md:183`, `VERSION` in `~/Documents/workspaces/nuke/git-rest/Makefile`) — that pin governs `vault-obsidian-{openclaw,personal,trading}` and does not deploy this pod.
- The Rung-2 AC above, after that deploy.

## Desired Behavior

1. On startup, a repository directory whose `.git` cannot resolve `HEAD` is treated as absent: it is cloned from `GIT_REMOTE_URL`.
2. If that clone fails, startup returns an error that names the remote URL; the process does not continue into the pull loop pretending the repository is present.
3. Before removing a HEAD-less directory's contents, the paths that exist locally but not on the remote are logged at WARN, naming each path; paths present on the remote are not named.
4. A successful startup re-clone logs at INFO the remote URL and the number of discarded paths — the operator's proof that the clone happened.
5. A repository that already resolves `HEAD` is left exactly as it is — no re-clone, no reset, no data loss.
6. `Pull` never returns an untyped upstream error on a path where the repository's HEAD could not be resolved; it returns an error wrapping `ErrRepoUnrecoverable`.
7. The existing pull state machine (no-op / fast-forward / push / merge), the dirty-tree rescue and the conflict resolver are unchanged.

## Assumptions

- The remote's default branch (`refs/remotes/origin/HEAD`) is the branch a fresh clone should land on — the same assumption `recoverRepoState` already makes.
- The vault volume is a cache of the remote (`docs/deployment.md:235`), so a HEAD-less directory may be discarded — but only after its local-only paths are named in the log.
- No caller branches on `ErrRepoUnrecoverable` today (only `pkg/git/git.go` and `pkg/git/git_test.go` reference it), so surfacing it from `Pull` is log-visible and otherwise inert.

## Constraints

- Non-test `pkg/git` code must not call `context.Background()`; contexts are passed in. (Tests may.)
- Errors are wrapped with `github.com/bborbe/errors`, never `fmt.Errorf`.
- Tests are Ginkgo v2 / Gomega, following the existing `main_test.go` and `pkg/git` patterns. Root tests are `package main_test`, so a test of the startup clone needs the hook exported through `export_test.go` (as `CleanupStaleLocks`, `RecoverUntracked` and `SyncOnStartup` already are).
- `git clone` refuses a non-empty destination (`fatal: destination path … already exists and is not an empty directory`, exit 128), so the fix must remove or relocate the residue rather than cloning over it — and must log what it removed (DB3, DB4).
- The 4-state sync, the dirty-tree rescue, the quarantine paths and the conflict resolver must not change behaviour.
- DB6 intentionally changes what `Pull` returns for one class of failure; that is the point, and it is not a violation of DB7.

## Security / Abuse Cases

- Removal is confined to the configured `--repo` directory. No path outside it is deleted, and no symlink is followed out of it — a symlinked entry inside the repo is unlinked, never traversed.
- The discard log names paths only, never file contents, so no vault content is copied into cluster logs.

## Failure Modes

| Trigger | Expected behavior | Recovery | Reversibility |
|---|---|---|---|
| Remote unreachable at startup | The clone returns an error naming the remote; startup fails visibly instead of entering the pull loop | Operator fixes auth/network and restarts; recovery is observable as `kubectldev -n agent logs vault-obsidian-agent-0 \| grep -i 'clone'` returning a clone line and `git -C /data rev-parse HEAD` exiting 0 | Irreversible only if the residue held local-only content — which DB3/DB4 name first |
| Clone interrupted mid-flight (process killed) | The residue has no resolvable HEAD, so the next start re-clones, logging any local-only paths it discards | Automatic on next start; the WARN line names what was discarded | Irreversible for discarded local-only content; the log is the recovery pointer |
| Disk full during re-clone | The clone fails with a non-zero exit; the error is returned naming the remote | Free space, restart; the next start re-clones | Reversible — nothing is removed until the discard step |
| Existing healthy repository | No clone, no reset; HEAD and working tree unchanged | n/a | n/a |
| `.git` exists but is corrupt in a way that still resolves HEAD | Pull path handles it as today (rescue/quarantine) | Unchanged from current behaviour | Unchanged |

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Startup clone treats a HEAD-less repository as not cloned; logs discarded local-only paths and the successful clone; failed clone returned with the remote URL | 1, 2, 3, 4, 5 | 1, 2, 3, 5 | — |
| 2 | `Pull` surfaces an unresolvable HEAD as `ErrRepoUnrecoverable` instead of an untyped upstream error | 6 | 4 | — |

Rationale: prompt 1 is the fix that actually unblocks the replica and is confined to `main.go`; prompt 2 is a separate file and a separate behaviour and can land independently. `make precommit` (AC5) applies to both rows. AC6 is operator-only and is intentionally uncovered by any prompt — it is the Rung-2 gate.

## Do-Nothing Option

The condition recurs on every transient clone failure, on every replica, with no self-healing path — nine days of a dead dev vault, a crash-looping consumer, and a prior occurrence already recorded in `docs/deployment.md:167` are the measured cost. Manual repair requires `kubectl exec` into the pod, which is operator-only and unavailable to any unattended path.
