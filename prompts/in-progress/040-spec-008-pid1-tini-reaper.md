---
status: committing
spec: [008-bug-pid1-no-init-leaks-grandchild-zombies]
summary: 'Added tini to Alpine runtime stage in Dockerfile (apk install line + ENTRYPOINT) and added CHANGELOG ## Unreleased entry documenting the zombie-reaping fix.'
container: git-rest-040-spec-008-pid1-tini-reaper
dark-factory-version: v0.156.1-1-g04f3863-dirty
created: "2026-05-10T00:00:00Z"
queued: "2026-05-09T23:19:21Z"
started: "2026-05-09T23:19:23Z"
branch: dark-factory/bug-pid1-no-init-leaks-grandchild-zombies
---

<summary>
- `/main` no longer runs as PID 1 — `tini` sits between the container runtime and `/main`, reaping any orphaned grandchild processes (git helpers, ssh, credential helpers)
- Zombie `[git] <defunct>` processes stop accumulating under long-lived pods — the leak that reached ~18,000 zombies per pod after 3 days at `PULL_INTERVAL=30s` is eliminated
- SIGTERM from kubelet is forwarded by tini to `/main`, so graceful shutdown (HTTP drain + puller cancel) is preserved
- Image size increase is ≤ 100 KB (tini is ~10 KB in Alpine)
- No CLI flags, environment variables, or StatefulSet manifests need to change — rolling restart via Keel picks up the new tag automatically
- `make precommit` passes; CHANGELOG updated
</summary>

<objective>
Install `tini` in the runtime stage of `Dockerfile` and chain it as the entrypoint before `/main`, so that `/main` is no longer PID 1 and any grandchild processes orphaned by `git` (ssh, git-remote-https, credential helpers) are reaped by tini rather than accumulating as zombies indefinitely.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

The entire fix is Dockerfile-only — no Go code changes. The bug is a container/PID-1 problem, not a Go runtime problem.

Files to read in full before implementing:
- `Dockerfile` — current build and runtime stages; you will modify the runtime stage only
- `CHANGELOG.md` — add `## Unreleased` section

Background (do NOT change these files, just understand the topology):
- `main.go` — the Go binary; receives SIGTERM for graceful shutdown via `run.CancelOnFirstError`
- `pkg/puller/` — periodic git pull loop; each pull spawns a `git` subprocess that itself may spawn helpers (`ssh`, `git-remote-https`, etc.) as grandchildren

Why tini works here:
- When `/main` is PID 1 it only reaps processes it started via `os/exec.Cmd.Wait()`. Grandchildren of `git` that get orphaned (e.g. when a pull-timeout context cancel kills `git` before the helper exits) are reparented to PID 1 = `/main`, but Go never calls `wait()` on them — they become permanent zombies.
- `tini` is a minimal init that installs a SIGCHLD handler and calls `waitpid(-1, ...)` for any child, including reparented grandchildren. It also forwards signals (SIGTERM → `/main`) so graceful shutdown is unaffected.
- `tini` in Alpine's default repo is ~10 KB; Alpine 3.23 ships it in the `community` repository (enabled by default in `alpine:3.23`).
</context>

<requirements>

## 1. Add `tini` to the runtime stage in `Dockerfile`

In the runtime (`FROM alpine:3.23`) stage, add `tini` to the existing `apk --no-cache add` line. Keep all existing packages; only add `tini`:

Before:
```dockerfile
RUN apk --no-cache add \
    ca-certificates \
    git \
    gnupg \
    openssh-client \
    tzdata \
    && rm -rf /var/cache/apk/*
```

After:
```dockerfile
RUN apk --no-cache add \
    ca-certificates \
    git \
    gnupg \
    openssh-client \
    tini \
    tzdata \
    && rm -rf /var/cache/apk/*
```

Keep packages in alphabetical order (tini falls between openssh-client and tzdata).

## 2. Update `ENTRYPOINT` in `Dockerfile`

Replace the existing `ENTRYPOINT` line so tini wraps `/main`:

Before:
```dockerfile
ENTRYPOINT ["/main"]
```

After:
```dockerfile
ENTRYPOINT ["/sbin/tini", "--", "/main"]
```

The `--` separates tini's own flags from the command it launches. `/sbin/tini` is the canonical path for tini installed from Alpine's package.

The `ARG`/`ENV` build-metadata lines between the `COPY` and `ENTRYPOINT` remain unchanged.

## 3. Add CHANGELOG entry

In `CHANGELOG.md`, insert `## Unreleased` immediately before the first versioned heading (currently `## v0.19.5` on line 5). If `## Unreleased` already exists, append to it:

```markdown
## Unreleased

- fix: Install `tini` as PID 1 init reaper in Docker image to prevent zombie `[git] <defunct>` accumulation. With `/main` running as PID 1, grandchild processes spawned by `git` (ssh helpers, credential helpers) were reparented to `/main` on exit but never reaped — growing to ~18,000 zombies per pod after 3 days at `PULL_INTERVAL=30s` and blocking `k3s-killall.sh` node shutdown. `tini` reaps all orphaned children and forwards SIGTERM to `/main` for graceful shutdown (prod incident 2026-05-09, `vault-obsidian-trading-0`).
```

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- The fix MUST NOT change any CLI argument behaviour: `/main -v=2 --some-flag=...` must still work after the entrypoint chain
- SIGTERM from kubelet MUST be forwarded to `/main` — `tini --` (default mode) does this; do NOT use `-s` (signal-only mode) or any flag that disables signal forwarding
- Image size increase MUST be ≤ 100 KB — tini is ~10 KB, well within budget
- The fix MUST work with the existing `apk --no-cache add` line — do NOT add a separate `RUN apk add tini` layer
- `tini` package name in Alpine is `tini`; the installed binary path is `/sbin/tini` — use this exact path in ENTRYPOINT
- No changes to `main.go`, any `pkg/` file, `go.mod`, `go.sum`, or `vendor/` — the fix is Dockerfile-only
- No changes to any consuming StatefulSet manifests — the fix is image-only; Keel rolls out the new tag automatically
- Existing `Makefile` targets (`make precommit`, `make build`, `make upload`, `make buca`) MUST continue to work unchanged
- Existing tests must still pass
</constraints>

<verification>
Confirm tini is added to the apk install line:
```bash
grep -n "tini" /workspace/Dockerfile
```
Expected: one match on the `tini \` line inside the `apk --no-cache add` block, and one match on the `ENTRYPOINT` line.

Confirm ENTRYPOINT references tini:
```bash
grep -n "ENTRYPOINT" /workspace/Dockerfile
```
Expected: `ENTRYPOINT ["/sbin/tini", "--", "/main"]`

Confirm no Go files were modified:
```bash
git -C /workspace diff --name-only | grep -v "Dockerfile\|CHANGELOG.md"
```
Expected: no output (only Dockerfile and CHANGELOG.md changed).

Final validation:
```bash
cd /workspace && make precommit
```
Must pass with exit code 0.

Note for operator (post-build, NOT exercised by this prompt — these are runtime-only checks per bug-workflow.md and will be validated during `dark-factory:verify-spec 008`):
- Image size delta ≤ 100 KB: `docker images --format '{{.Repository}}:{{.Tag}} {{.Size}}' | grep git-rest` — compare new tag against `v0.19.5`
- Signal forwarding: `docker run -d --name t <new-image> --help; docker stop t` — must exit within the default 10s grace period
- Zero zombies in deployed pod after ≥1h with `PULL_INTERVAL=30s`: `kubectl exec -n <ns> <pod> -- ps -eo state,comm | awk '$1 ~ /Z/ && $2 == "git"' | wc -l` — must return `0` on every poll across at least 60 pull cycles
</verification>
