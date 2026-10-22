---
status: prompted
approved: "2026-10-22T07:01:54Z"
generating: "2026-10-22T07:07:30Z"
prompted: "2026-10-22T07:24:32Z"
branch: dark-factory/bug-dirty-tree-rescue-all-pull-paths
---

## Summary

- A vault pod's working tree can go dirty at any time; the sync must recover from it wherever it is encountered, not only on one of four paths.
- The dirty-tree rescue shipped in spec 014 is wired into the fast-forward path alone. A dirty tree on the diverged merge, on the push retry, or at startup has no rescue and wedges the pod indefinitely.
- The rescue that does exist can be defeated by a stale git lock file: it captures and publishes the changes correctly, then fails its own cleanup step and leaves the tree dirty, so the next cycle repeats the failure.
- Lock cleanup exists but runs only once, at process start, so a lock created later is never cleared.
- Observed live on 2026-10-22: a dev vault pod served 500 on every write for ~35 minutes, and only a restart cleared it.

## Problem

`rescueDirtyTree` is reachable from exactly one call site — `fastForwardOrRescue` (`pkg/git/git.go:1714`) — which `syncWithUpstream` reaches only in the `localSHA == baseSHA` case. A dirty working tree in any other state has no rescue:

- **Diverged** (`localSHA != baseSHA` and `remoteSHA != baseSHA`): `syncWithUpstream` calls `pullMergeAndPush`, whose `git merge --no-edit` aborts with `Your local changes … would be overwritten by merge`. `parseMergeConflictPaths` looks for `CONFLICT (content):` markers, finds none, and the resolver aborts. Spec 014's Failure Modes table names this shape as *"Not covered by this spec"*.
- **Push retry**: `retryPush` (`pkg/git/git.go:356`) takes the same merge path after a non-fast-forward rejection, with the same missing rescue.
- **Startup**: `syncOnStartup` runs a raw `git pull` (`main.go:233`) before the puller starts. It fails non-fatally (`main.go:234` logs `startup git pull failed (puller will retry)`), but on a diverged repo git refuses with `fatal: Need to specify how to reconcile divergent branches` and the divergence survives into the puller's first cycle.

Separately, the rescue that *is* wired can be defeated by a lock. On 2026-10-22 the dev pod's rescue ran, pushed its branch, and then died at `reset --hard origin/main` (`pkg/git/git.go:1846`) on `fatal: Unable to create '/data/.git/index.lock': File exists`. Because the reset never completed, the tree stayed dirty and the next cycle re-entered the same path — 55 times in 30 minutes. `cleanupStaleLocks` (`main.go:114`) removes every `*.lock` under `.git/`, but only during bootstrap, so a lock that appears after boot is permanent for the life of the pod.

Both defects have the same user-visible consequence: a vault pod that stops syncing and serves 500 on every write, indefinitely, until an operator restarts it. The 2026-10-22 incident cost ~35 minutes of dev write availability and was reported independently by two sessions before it was picked up.

## Goal

A `git-rest` instance whose working tree is dirty recovers on whichever sync path it reaches — fast-forward, diverged merge, push retry, or startup — and a stale `.git/index.lock` never outlives the pull cycle that finds it. Recovery requires no operator action, no local commit and no working-tree change is discarded, and the recovery is observable through `git_rest_pull_rescues_total` and a log line naming the captured paths.

## Assumptions

- **Boot ordering is load-bearing in the reproduction, not in production.** A binary that boots against an already-dirty tree may have that state consumed by `recoverUntracked` (`main.go:158-186`) or `syncOnStartup` (`main.go:233`) before the puller's first cycle. `recoverUntracked` only acts when `hasUntracked` (`main.go:191-199`) is true, so a tracked-file-only modification survives boot — which is what makes the startup recipe viable.
- **`Pull` holds `g.mu` for its whole body** (`pkg/git/git.go:1622-1623`), and `sync.Mutex` is not reentrant. Any re-evaluation after a rescue must be inline, never a recursive `Pull`.
- **A lock is stale if and only if no process holds it.** The pod is a single-replica StatefulSet that serialises its own git operations under `g.mu`, so a lock present at a pull cycle was left by a process that is no longer running — unless an external process holds it, which is why the removal must check liveness rather than delete unconditionally.
- **The vault's remote accepts new branch pushes from the pod's existing credentials.** Spec 014 established this; a `rescue/*` branch is a new ref name, not a new permission.

## Non-goals

- Not changing the rescue's capture contract. Spec 014 defines it — object-only commit on a temporary non-existent index, `rescue/<timestamp>` branch, no reset until the push succeeded, `git stash create` forbidden. This spec calls that contract from more places; it does not alter it.
- Not changing the conflict resolver, the `_conflicts/` layout, or `parseMergeConflictPaths`' marker matching.
- Not changing the public HTTP contract in `docs/api.md` or the readiness semantics — readiness is deliberately decoupled from sync outcome (spec 017, `docs/deployment.md:204`), and this spec does not re-couple them.
- Not adding an alert or a new metric. The rescue counter already exists and the log line is the detection surface.
- Not covering the unborn-HEAD wedge. Separate task.

## Acceptance Criteria

Each recipe below is an independent script with its own `$BASE`, its own binary and its own teardown, so every metric assertion is an absolute value on a fresh process.

- [ ] **A dirty tree in the diverged state is rescued, the pull completes, and the local commit survives.** Recipe (a), then: `git -C "$BASE/work" status --porcelain` returns 0 lines; `git -C "$BASE/work" log --oneline | grep -c 'local work'` returns 1; `git -C "$BASE/work" log --oneline | grep -c 'remote work'` returns 1. Evidence shape: exit code + stdout match.
- [ ] **The diverged-state rescue preserves the dirty content on the remote.** Recipe (a), then derive the ref from the authoritative remote and assert its content:
  ```bash
  REF=origin/$(git -C "$BASE/work" ls-remote --heads origin 'refs/heads/rescue/*' \
        | awk '{print $2}' | sed 's#refs/heads/##' | tail -1)
  test -n "$REF" && git -C "$BASE/work" show "${REF}:tasks/x.md" | grep -q 'LOCAL UNCOMMITTED EDIT'
  ```
  exits 0. Evidence shape: file content (grep). The brace form `${REF}` is required — `"$REF:tasks/x.md"` is mangled by zsh's `:t` modifier.
- [ ] **A dirty tree present at boot is rescued by the startup pull or the first pull cycle.** Recipe (b), then within two pull intervals `grep -c 'rescue branch pushed' "$BASE/run.log"` returns ≥1 AND `git -C "$BASE/work" status --porcelain` returns 0 lines. The startup failure line is deliberately **not** asserted: a startup rescue that re-runs the pull with an explicit merge strategy may succeed and emit none, so requiring it would fail a correct implementation. Evidence shape: log line + exit code.
- [ ] **A stale `index.lock` created after boot does not wedge the puller.** Recipe (c), then: `test ! -f "$BASE/work/.git/index.lock"` exits 0; `test "$(git -C "$BASE/work" rev-parse HEAD)" = "$(git -C "$BASE/work" rev-parse "origin/$BRANCH")"` exits 0; `grep -c 'stale lock removed during pull cycle' "$BASE/run.log"` returns ≥1. Evidence shape: file presence (negative) + exit code + log line. Booting with the lock present does **not** test this — `cleanupStaleLocks` deletes it at startup and the AC would pass without the fix.
- [ ] **Negative — a lock held by a live process is not removed.** Recipe (c), but replace the bare `touch` with a pure file-descriptor holder that sleeps — a single process with the file open and nothing else. Use `python3`, **not** `flock`: `flock` is util-linux and absent on the macOS host where Rung 1 runs, and more fundamentally an OS lock is the **wrong signal** — git creates `index.lock` with `O_CREAT|O_EXCL` and never takes an OS lock on it, so a `fcntl`/`flock` probe cannot see a live git process holding the lock.
```bash
python3 -c 'import time,sys; f=open(sys.argv[1],"w"); time.sleep(60)' \
  "$BASE/work/.git/index.lock" & HOLDER=$!
```
Then assert `kill -0 "$HOLDER"` exits 0 before the window opens. After two pull intervals: `grep -c 'stale lock removed during pull cycle' "$BASE/run.log"` returns 0 and `test -f "$BASE/work/.git/index.lock"` exits 0. Then `kill "$HOLDER" 2>/dev/null || true` — the holder is a single process with no child, so no `pkill` is needed. Evidence shape: log line (negative) + exit code. **The holder must be a pure fd holder, never a git command**: with the lock file already present, `git update-index --refresh` exits 128 immediately and the process exits, leaving nothing holding the lock during the window — which would make this AC fail against correct code while the unsafe unconditional `rm -f` passes it.
- [ ] **Negative — the per-cycle cleanup removes stale locks and nothing else.** After recipe (c): `test -f "$BASE/work/.git/index"` exits 0 and `git -C "$BASE/work" status` exits 0 — no non-lock file is touched. Separately, with the remote advanced and `touch "$BASE/work/.git/HEAD.lock"` in place of the index lock, `test ! -f "$BASE/work/.git/HEAD.lock"` exits 0: the per-cycle cleanup reuses the same `*.lock` walk as `cleanupStaleLocks`, so its scope is every `*.lock` under `.git/`, not only `index.lock`. Evidence shape: exit code.
- [ ] **Every rescue increments the counter exactly once per event.** In recipes (a) and (b) `test "$(curl -s localhost:18446/metrics | grep '^git_rest_pull_rescues_total' | awk '{print $2}')" = "1"` exits 0; in recipe (c) the same comparison against `"0"` exits 0 (a clean tree at lock-removal time is not a rescue). Each recipe runs a fresh process, so the assertion is the absolute value, not a delta. Evidence shape: metric value.
- [ ] **Post-Deploy (Rung-2):** on dev, `vault-obsidian-openclaw-0` runs the fixed build and clears a post-boot lock without an operator restart. Evidence: after `kubectlnukedev -n dev exec vault-obsidian-openclaw-0 -- touch /data/.git/index.lock` and one pull interval, `kubectlnukedev -n dev logs vault-obsidian-openclaw-0 --since=10m | grep -c 'stale lock removed during pull cycle'` returns ≥1 AND `kubectlnukedev -n dev exec vault-obsidian-openclaw-0 -- git -C /data status --porcelain` returns 0 lines.
  - `deploy_check:` `kubectlnukedev -n dev exec vault-obsidian-openclaw-0 -- wget -qO- http://localhost:9090/metrics | grep -m1 '^git_rest_build_info' | sed -E 's/.*commit="([^"]+)".*/\1/' | grep -E '^[0-9a-f]{7,40}$'`
  - `deploy_target:` `$(git rev-parse --short HEAD)`

  The comparison is short-SHA against short-SHA: `Makefile:156` bakes `--build-arg BUILD_GIT_COMMIT=$(git rev-parse --short HEAD)`, so the metric's `commit` label is a short SHA and a full 40-character `rev-parse HEAD` would never compare equal even on a correct deploy. The trailing `grep -E '^[0-9a-f]{7,40}$'` makes `deploy_check` exit non-zero when the metric is missing.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — format, generate, test and lint clean
- `make test` — unit + integration suite passes
- a test asserting the rescue is reachable from each of the four sync paths — a call-graph assertion, not a name grep: a grep for `rescueDirtyTree` proves the symbol exists and says nothing about reachability. The startup path needs plumbing first: `syncOnStartup` lives in `package main` and `rescueDirtyTree` is unexported in `pkg/git`, so the implementer must either export a rescue entry point from `pkg/git` or move the startup sync behind that package. The assertion is on behaviour (a dirty repo at boot ends clean with a rescue branch on the remote), not on a symbol name
- a unit test that a dirty tree on `retryPush`'s merge path is rescued — the push-retry path is not covered by a reproduction recipe (forcing a non-fast-forward rejection deterministically is a race), so it is covered here instead
- a test asserting the per-cycle lock cleanup runs on a non-boot cycle: boot the puller, create a lock, run one cycle, assert the lock is gone; then hold a lock with a live process and assert it survives

### Operator-executable (runs on the host after PR merge, per `docs/verifying-specs.md`)

- **Rung 1**: build a fresh binary and replay recipes (a), (b) and (c).
- **Rung 2**: publish the release image, move `VERSION` in `~/Documents/workspaces/nuke-dev/git-rest/Makefile` to the new tag, `make mirror` first, then `BRANCH=dev make apply`, then `scale 0→1`. Assert the Post-Deploy AC.
- **Rung 3** is out of scope — promoting to prod and verifying the prod symptom is a separate task.

## Reproduction

Three independent scripts. Each starts its own `$BASE`, boots its own binary, and tears it down by pid, so the metric assertion in each is an absolute value on a fresh process. **Run them sequentially** — all three bind `:18446`, so a concurrent run collides and a `pgrep`-based teardown would kill the wrong instance. Build the binary once:

```bash
cd ~/Documents/workspaces/git-rest-dirty-tree-rescue && go build -o /tmp/git-rest-multipath-bin .
```

### Recipe (a) — diverged + dirty

```bash
set -e
BASE=/tmp/git-rest-a && rm -rf "$BASE" && mkdir -p "$BASE" && cd "$BASE"
git init -q --bare -b test/repro origin.git
git clone -q origin.git seed && cd seed
git config user.email repro@local && git config user.name repro
mkdir -p tasks && printf 'line one\n' > tasks/x.md && printf 'secrets.env\n' > .gitignore
git add -A && git commit -q -m init
BRANCH=$(git rev-parse --abbrev-ref HEAD) && git push -q origin "$BRANCH"
cd "$BASE"
git clone -q origin.git work && cd work
git config user.email repro@local && git config user.name repro

# Boot FIRST, against a clean, level clone.
/tmp/git-rest-multipath-bin -listen=:18446 -repo="$BASE/work" -pull-interval=10s > "$BASE/run.log" 2>&1 & PID=$!
sleep 3

# The local commit lands on its OWN file, so the post-rescue merge is clean and
# the recipe exercises the dirty-tree rescue, not the conflict resolver. The
# remote commit touches the file the working tree will dirty — that collision is
# what makes the merge abort.
cd "$BASE/work" && printf 'local only\n' > tasks/z.md
git add -A && git commit -q -m "local work"
cd "$BASE/seed" && printf 'line one\nREMOTE COMMITTED\n' > tasks/x.md
git add -A && git commit -q -m "remote work" && git push -q origin "$BRANCH"
cd "$BASE/work" && printf 'line one\nLOCAL UNCOMMITTED EDIT\n' > tasks/x.md
echo "mergebase: $(git merge-base HEAD "origin/$BRANCH" | cut -c1-8)  HEAD: $(git rev-parse --short HEAD)"

sleep 22
curl -s http://localhost:18446/metrics | grep '^git_rest_pull_rescues_total'
kill "$PID" 2>/dev/null || true
```

### Recipe (b) — dirty + diverged at boot

```bash
set -e
BASE=/tmp/git-rest-b && rm -rf "$BASE" && mkdir -p "$BASE" && cd "$BASE"
git init -q --bare -b test/repro origin.git
git clone -q origin.git seed && cd seed
git config user.email repro@local && git config user.name repro
mkdir -p tasks && printf 'line one\n' > tasks/x.md && printf 'secrets.env\n' > .gitignore
git add -A && git commit -q -m init
BRANCH=$(git rev-parse --abbrev-ref HEAD) && git push -q origin "$BRANCH"
cd "$BASE"
git clone -q origin.git work && cd work
git config user.email repro@local && git config user.name repro

# Create the dirty + diverged state BEFORE boot. Tracked-file-only, so
# recoverUntracked's hasUntracked guard is false and the state survives boot.
# The local commit lands on its own file so the post-rescue merge stays clean.
printf 'local only\n' > tasks/z.md
git add -A && git commit -q -m "local work"
cd "$BASE/seed" && printf 'line one\nREMOTE COMMITTED\n' > tasks/x.md
git add -A && git commit -q -m "remote work" && git push -q origin "$BRANCH"
cd "$BASE/work" && printf 'line one\nLOCAL UNCOMMITTED EDIT\n' > tasks/x.md

/tmp/git-rest-multipath-bin -listen=:18446 -repo="$BASE/work" -pull-interval=10s > "$BASE/run.log" 2>&1 & PID=$!
sleep 22
curl -s http://localhost:18446/metrics | grep '^git_rest_pull_rescues_total'
kill "$PID" 2>/dev/null || true
```

### Recipe (c) — stale lock created after boot

```bash
set -e
BASE=/tmp/git-rest-c && rm -rf "$BASE" && mkdir -p "$BASE" && cd "$BASE"
git init -q --bare -b test/repro origin.git
git clone -q origin.git seed && cd seed
git config user.email repro@local && git config user.name repro
mkdir -p tasks && printf 'line one\n' > tasks/x.md && printf 'secrets.env\n' > .gitignore
git add -A && git commit -q -m init
BRANCH=$(git rev-parse --abbrev-ref HEAD) && git push -q origin "$BRANCH"
cd "$BASE"
git clone -q origin.git work && cd work
git config user.email repro@local && git config user.name repro

/tmp/git-rest-multipath-bin -listen=:18446 -repo="$BASE/work" -pull-interval=10s > "$BASE/run.log" 2>&1 & PID=$!
sleep 3

# Advance the remote (the pull needs work to do), then drop a stale lock AFTER boot.
cd "$BASE/seed" && printf 'line one\nREMOTE AGAIN\n' > tasks/x.md
git add -A && git commit -q -m "remote again" && git push -q origin "$BRANCH"
touch "$BASE/work/.git/index.lock"

sleep 22
test ! -f "$BASE/work/.git/index.lock" && echo "lock cleared"
git -C "$BASE/work" rev-parse HEAD
git -C "$BASE/work" rev-parse "origin/$BRANCH"
curl -s http://localhost:18446/metrics | grep '^git_rest_pull_rescues_total'
kill "$PID" 2>/dev/null || true
```

## Desired Behavior

1. `rescueDirtyTree` is invoked from every sync path that can fail on a dirty tree: `fastForwardOrRescue` (as today), `pullMergeAndPush`, `retryPush`'s merge, and `syncOnStartup`. On each, the working tree is inspected before the merge is attempted and a rescue runs before any merge error is returned, and a successful rescue re-evaluates the sync **inline** rather than recursing into `Pull` — `Pull` holds `g.mu` for its whole body and `sync.Mutex` is not reentrant. "Inline" means *never `Pull`*: the existing bounded one-level recursion into `syncWithUpstream` (`pkg/git/git.go:1677-1681`) is preserved.
2. Each path distinguishes a dirty tree from every other cause by inspecting the working tree **before** the merge is attempted (`git status --porcelain`), never by matching git's abort text — the abort wording is git-version-dependent, and `parseMergeConflictPaths` matches only `CONFLICT (content):`, so a dirty-tree abort yields no paths.
3. **The reset target depends on the path, and this is the load-bearing distinction.** On the fast-forward path HEAD is an ancestor of upstream and holds no unique commit, so `reset --hard <upstream tracking ref>` is correct. On the diverged, push-retry and startup paths HEAD carries local commits that must survive, so the reset targets **HEAD**: it returns the working tree to the last commit without moving HEAD, and the subsequent merge keeps the local commits. Resetting a diverged path to the upstream ref would silently discard the local commit — recoverable only from the rescue branch, and it is exactly what AC 1 forbids.
4. Stale-lock cleanup runs before each pull/rescue cycle, not only at boot, reusing the same `*.lock` walk as `cleanupStaleLocks` — so its scope is every `*.lock` under `.git/`, not only `index.lock`. A lock is removed only when no process holds it; a lock held by **any** live process is left alone and the cycle proceeds as it does today. Liveness means an open file descriptor on the path — `lsof -t <path>` on darwin, an inode match under `/proc/*/fd` on linux — because git creates the lock with `O_CREAT|O_EXCL` and takes no OS lock on it, so `flock`/`fcntl` is **not** a valid signal. When liveness cannot be determined (the probe is unavailable or errors), log and **do not delete**; never fall back to an unconditional `rm`. The removal logs one INFO line beginning `stale lock removed during pull cycle` naming the removed path — a distinguishable prefix, because the boot cleanup already logs `removed stale lock` (`main.go:137`) and a shared substring would make the positive and negative ACs match the same line.
5. Every rescue, on every path, increments `git_rest_pull_rescues_total` exactly once and logs one INFO line beginning `rescue branch pushed` naming the branch and the captured paths — the contract spec 014's DB6 defines, unchanged.

## Constraints

- **Reuses spec 014's capture contract verbatim.** Object-only commit on a temporary non-existent index path (`$(mktemp -u)` — `mktemp` creates a zero-byte file git rejects), HEAD unmoved by the *capture*, real index untouched, `git stash create` forbidden, no reset until the push succeeded. Do not write a second rescue. The reset target is DB3's, and is the one place this spec narrows 014's rule.
- **Amends spec 014.** 014's Failure Modes row "Dirty tree in the diverged state" is closed by this spec; the row is updated to point here **and** loses its two "readiness stays not-ready" claims, which spec 017 invalidated. 014 is still in `specs/in-progress/` at `status: verifying`: land this row amendment after 014 completes, or coordinate with whoever is running its verification, so the edit does not race the 014 verifier.
- **A merge abort must never leave `MERGE_HEAD` behind.** Every failure branch that ran a merge runs `git merge --abort` first. `retryPush` (`pkg/git/git.go:373-374`) and `resolveConflictMerge` (`pkg/git/git.go:1132-1140`) already do this; the new rescue branches must preserve it.
- **Lock staleness must be decided, not assumed, and the decision must work on both hosts.** An unconditional `rm -f .git/index.lock` on every cycle is a data-corruption bug: it deletes the lock of a concurrent `git` process. AC 5 fails it deliberately. The liveness probe runs on darwin at Rung 1 and in the `alpine:3.23` pod at Rung 2, which share no single tool: `lsof` exists on macOS but not in the image, and `/proc` exists in the pod but not on macOS. Implement both branches, and **fail closed** — if neither probe can run, log and skip the deletion rather than deleting. `flock`/`fcntl` is explicitly not a signal here: git never takes an OS lock on `*.lock`.
- **`reset --hard` never hardcodes a branch name** — the upstream tracking ref is derived from `@{u}`, as `Pull` already does.
- **`docs/deployment.md` must be updated in the same change, at two named places.** Line 200 says `reset --hard` targets "the upstream tracking ref" — wrong for the diverged, push-retry and startup paths, which reset to HEAD. And the § "Dirty working tree rescue" section describes the rescue as fast-forward-only; it must name all four paths. The metric table's rescue row stays accurate — DB5 leaves the counter contract unchanged — so the update is about path coverage and the reset target, not the counter.
- **Preserve the repo's invariants.** Errors wrapped with `github.com/bborbe/errors` (never `fmt.Errorf`), metrics under the `git_rest_` prefix, no `context.Background()` in `pkg/`, `log/slog` for logging, timestamps from the injected `libtime` getter.
- **No new metric and no `Metrics` interface change.** The rescue counter already exists; the log line is the detection surface for lock removal.
- **CHANGELOG entry under `## Unreleased`.**

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection | Reversibility |
|---|---|---|---|---|
| Dirty tree on the diverged merge path | Rescue to `rescue/<ts>`, reset to HEAD, merge, push; local commit survives | None needed — self-healed | `rescue branch pushed` INFO; `git_rest_pull_rescues_total` increments | Reversible — content on `rescue/<ts>` |
| Dirty tree at startup | Startup pull rescues, or the first pull cycle does | None needed | `startup git pull failed` warn followed by `rescue branch pushed` | Reversible |
| Stale `index.lock` at a pull cycle | Lock removed, logged with the cycle prefix; the cycle proceeds | None needed — self-healed | INFO `stale lock removed during pull cycle path=…` | Reversible — no git state is touched |
| Lock held by any live process | **Not** removed; the cycle fails as it does today | None — the holder is mid-operation and will release | No `stale lock removed during pull cycle` line | Reversible |
| Rescue push rejected | Do not reset, do not clean; return the error; repo left for inspection | Operator runs `git -C <repo> push origin rescue/<ts>` and confirms `git ls-remote origin 'refs/heads/rescue/*'` lists it | `rescue push failed, leaving repo for inspection` | Reversible |
| Two pull cycles overlap | Impossible — `Pull` holds `g.mu` for its whole body | — | — | — |
| Crash between rescue push and reset | Content already safe on `rescue/<ts>`; next cycle creates a second branch | None needed | Two `rescue/*` refs at adjacent timestamps | Reversible |
| Clock skew | Branch name is a label, not an ordering key; a skewed name still preserves the content | None | — | Reversible |
| Disk exhaustion while building the rescue commit | Commit fails; do not reset or clean; return the error | Operator frees space and confirms `git -C <repo> status --porcelain` is unchanged | Error log | Reversible — changes intact |

## Security / Abuse Cases

This change widens *where* the existing rescue runs; it does not widen *what* it publishes. The capture contract, the `.gitignore` exclusion, the "`rescue/*` is a new ref name, not a new permission" argument, and the never-deleted/never-force-pushed rule are all spec 014's and are unchanged. The new surface is lock deletion, which is bounded: it removes only `*.lock` files under `.git/`, only when no process holds them, and the removed path is logged.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Rescue wiring: `pullMergeAndPush`, `retryPush`, `syncOnStartup`; path-dependent reset target; inline re-evaluation; `merge --abort` preserved on every failure branch | 1, 2, 3 | 1, 2, 3 | — |
| 2 | Per-cycle stale-lock cleanup with a liveness check; the distinguishable INFO log | 4 | 4, 5, 6 | — |
| 3 | Regression tests (including the `retryPush` unit test); 014 Failure-Modes row amendment; `docs/deployment.md` four-path update; CHANGELOG | 5 | 7 | prompts 1, 2 |

AC 8 is the Post-Deploy rung-2 check — operator verification during spec verification, not a prompt deliverable.

Rationale: prompts 1 and 2 are independent — one adds call sites, the other adds a cycle-level guard — so they can run in parallel after approval. Prompt 3 locks the untouched paths and the two negative ACs that stop the laziest implementations.

## Do-Nothing Option

Dev `vault-obsidian-openclaw-0` re-wedges on the next post-boot lock, and prod inherits the same failure the first time its tree goes dirty on a diverged pull. Each occurrence costs an operator restart, and the vault serves 500 on every write until someone notices. The 2026-10-22 incident cost ~35 minutes of dev write availability and needed two independent sessions to report it before it was picked up.
