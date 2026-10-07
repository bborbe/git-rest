---
status: prompted
approved: "2026-10-07T18:39:40Z"
generating: "2026-10-07T18:42:44Z"
prompted: "2026-10-07T19:24:03Z"
branch: dark-factory/bug-pull-wedges-on-modify-delete-conflict
---

## Summary

- A `git merge` that produces `CONFLICT (modify/delete):` is invisible to the conflict-path parser, which matches only the content-conflict form. The conflict list comes back empty, the resolver is never reached, and the pull returns an error **without running `git merge --abort`** — the repository is left mid-merge.
- The quarantine flow creates exactly this shape: it `git rm`s the original path, so a quarantined file's canonical is *deleted in HEAD*, and any later upstream edit to that canonical produces a modify/delete conflict on the next pull.
- A second defect compounds it: the nesting guard aborts the merge for *any* conflicted path under `_conflicts/`, including a `_conflicts/` entry the upstream side has drained (deleted). Such a path never reaches the resolver.
- Observed on the dev vault replica on 2026-10-07: `vault-obsidian-personal-0` sat `0/1 NotReady` with `git_rest_quarantined_backlog 3`, `git_rest_resolver_failures_total{category="nested_source"} 219`, and 22 unpushed commits that could not reach `origin/master`. It was unwedged by hand.
- The fix makes a modify/delete conflict resolve on its merits (the upstream version wins when HEAD deleted the path), accepts an upstream drain of a `_conflicts/` entry, and guarantees that after any pull returns the repository is never left mid-merge.

## Problem

`parseMergeConflictPaths` (`pkg/git/git.go:91-108`) extracts conflicted paths by scanning git merge output for the literal substring `"Merge conflict in "`. Git emits that substring for `CONFLICT (content):` lines only. A `CONFLICT (modify/delete):` line — `CONFLICT (modify/delete): <path> deleted in HEAD and modified in origin/master.  Version origin/master of <path> left in tree.` — contains no such substring, so the parser returns an empty slice.

`resolveConflictMerge` (`pkg/git/git.go:776-794`) then takes its `len(conflictPaths) == 0` branch (`git.go:783-792`) and returns a wrapped error **without ever running `git merge --abort`**. The repository is left mid-merge with unmerged paths in the index. Every later pull repeats the same failure, so `/readiness` never returns to ready, Kubernetes marks the pod NotReady, and every write through the pod's HTTP API fails with `Committing is not possible because you have unmerged files`.

The shape is not exotic — the quarantine flow manufactures it. When a conflicted file is quarantined, the original path is removed from the tree (`git rm`, per the comment at `pkg/git/git.go:769`; `git mv` is unusable on conflicted files), so that path is *deleted in HEAD*. If the upstream side later modifies the canonical, the next pull sees exactly the modify/delete conflict the parser cannot read.

`validateConflictPathsNotNested` (`pkg/git/git.go:449-473`) is the second defect. It rejects any conflicted path already under `_conflicts/` and aborts the merge. It runs at `git.go:818`, before `ensureConflictsDir` (`git.go:821`) and before any per-file resolution, so the path never reaches the resolver. It cannot distinguish a genuine re-quarantine (the path would be moved under `_conflicts/` a second time, deepening the tree) from a `_conflicts/` entry the upstream side has drained by deleting it — and it aborts both.

The two defects compound: the quarantine flow creates the modify/delete shape, and the parser cannot handle it. Fixing only the guard does not clear the wedge — the parser gap is load-bearing.

## Goal

A `git-rest` instance whose pull meets a modify/delete conflict, or a conflict on a `_conflicts/` path that the upstream side has drained, completes the pull instead of wedging. A modify/delete conflict in which HEAD deleted the path and upstream modified it is resolved by taking the upstream version; an upstream drain of a `_conflicts/` entry is accepted; the merge is committed and pushed; readiness returns to ready within one pull interval. After every pull — resolved or aborted — the repository is never left mid-merge. No operator action is required, and no content is discarded.

## Non-goals

- **Deterministic quarantine filenames** (a blob hash instead of a wall-clock timestamp). Two replicas naming the same logical conflict differently is harmless once an upstream drain is accepted, because the drain removes the entry regardless of which replica's name it carries. A separate decision owns that change.
- **New metrics or alerts.** The existing `git_rest_quarantined_backlog` gauge and `git_rest_resolver_failures_total{category="nested_source"}` counter carry the evidence. No new metric, threshold, or alert rule.
- **The `status: next` vs `in_progress` write race** that produced the quarantined files in the first place.
- **A drain mechanism of git-rest's own.** Draining `_conflicts/` remains an operator act; this change only accepts a drain that has already happened upstream.
- **Auto-deleting quarantined content outside an operator drain.** Spec 013's rule stands unchanged.

## Assumptions

- The vault's remote accepts the puller's push on this path. The pod already pushes to the same remote after a resolved merge and after a clean merge, so this adds no credential or ruleset surface.
- The modify/delete shape is produced by the quarantine flow itself (`git rm` on the original path), not by an operator hand-deleting a file. The canonical is therefore *absent from HEAD's tree* while still present upstream.
- The divergence is reproducible deterministically from a bare origin plus two clones, and the puller's ticker has no immediate first tick, so booting the binary against a **clean, level clone** and forming the divergence within one pull interval is what reaches the merge path. Booting with the divergence already present lets the boot path's raw `git pull` consume it and the merge path is never exercised. This ordering is a hard requirement of the recipes below, not an incidental detail.
- Deterministic quarantine filenames (a blob hash instead of a wall-clock timestamp) are **not** required by this spec. Two replicas quarantining the same logical conflict under different names is harmless once an upstream drain is accepted, because the drain removes the entry regardless of which replica's name it carries. A separate decision owns that change.
- The nuke vault pod's image may be rolled by the cluster's image-policy controller rather than by `make apply`, and the deploy pin in `~/Documents/workspaces/nuke/git-rest/Makefile` can lag the tag the pods actually run. The Post-Deploy freshness anchor is therefore the build commit compiled into the binary, not the image tag.
- `git_rest_build_info{commit,date,version}` is exposed on the vault pods' `:9090` metrics endpoint, as it is on the agent vault: the same binary and the same `Makefile` build-arg path produce it.

## Reproduction

Two fixtures, from a bare origin and two clones each. **Ordering is load-bearing in both**: the binary boots against a clean, level clone and the divergence is formed only afterwards, within one pull interval. The ticker has no immediate first tick, so the first pull happens one interval after boot — forming the divergence faster than that is what makes the merge path run.

### A — modify/delete conflict on a canonical (the load-bearing defect)

```bash
set -e
# Run this recipe FROM THE WORKTREE THAT HOLDS THE FIX. Step 3 builds
# "$REPO_UNDER_TEST", and the default resolves to the tree you invoke this from —
# building the main checkout on master would replay the UNFIXED binary and fail
# every AC falsely.
REPO_UNDER_TEST="${REPO_UNDER_TEST:-$(git rev-parse --show-toplevel)}"
BASE=/tmp/git-rest-md-repro && rm -rf "$BASE" && mkdir -p "$BASE" && cd "$BASE"

# 1. Bare origin. tasks/doomed.md is tracked HERE, at the merge-base, so the local
#    deletion in step 5 is a plain deletion of an existing path and the remote edit
#    in step 4 is a plain modification of the same path.
git init -q --bare -b test/repro origin.git
git clone -q origin.git seed && cd seed
git config user.email repro@local && git config user.name repro
mkdir -p tasks && printf 'line one\n' > tasks/doomed.md
git add -A && git commit -q -m init
BRANCH=$(git rev-parse --abbrev-ref HEAD) && git push -q origin "$BRANCH"
cd "$BASE"

# 2. Work clone — the repo the puller serves. Its remote is LEVEL at this point.
git clone -q origin.git work && cd work
git config user.email repro@local && git config user.name repro

# 3. Boot the puller FIRST, against the clean, level clone.
cd "$REPO_UNDER_TEST" && go build -o /tmp/git-rest-md-repro-bin .
/tmp/git-rest-md-repro-bin -listen=:18445 -repo="$BASE/work" -pull-interval=10s -v=1 > "$BASE/run.log" 2>&1 &
sleep 3

# 4. NOW advance the remote: the upstream side MODIFIES tasks/doomed.md.
cd "$BASE/seed" && printf 'line one\nUPSTREAM EDIT\n' > tasks/doomed.md
git add -A && git commit -q -m "remote modifies doomed" && git push -q origin "$BRANCH"

# 5. NOW delete it locally — the shape the quarantine flow produces (git rm on the
#    original path). A commit is required: HEAD must differ from the merge-base or
#    the puller takes the fast-forward path and never merges. Steps 4 and 5 must both
#    finish inside one pull interval, or a tick fast-forwards between them.
cd "$BASE/work" && git rm -q tasks/doomed.md && git commit -q -m "local deletes doomed"
echo "mergebase: $(git merge-base HEAD "origin/$BRANCH" | cut -c1-8)  HEAD: $(git rev-parse --short HEAD)"
git status --porcelain

# 6. Wait one pull interval, then read the merge state AND readiness.
sleep 12
echo "MERGE_HEAD: $(test -e "$BASE/work/.git/MERGE_HEAD" && echo present || echo absent)"
git -C "$BASE/work" status --porcelain
curl -s -o /dev/null -w 'readiness HTTP %{http_code}\n' http://localhost:18445/readiness

# 7. Teardown. Every AC below re-runs this recipe on the same port, so each run must
#    start from a clean process — a survivor keeps answering :18445 with stale evidence.
kill "$(pgrep -f /tmp/git-rest-md-repro-bin)" 2>/dev/null || true
```

Observed against v0.29.1 (commit `87a3688`), verbatim from `vault-obsidian-personal-0`:

```
WARN git pull failed error="exit status 1\nmerge origin/master: CONFLICT (modify/delete): 25 Tasks/The Parity Harness Compares Generated UUIDs and Timestamps Raw.md deleted in HEAD and modified in origin/master.  Version origin/master of 25 Tasks/The Parity Harness Compares Generated UUIDs and Timestamps Raw.md left in tree.\nCONFLICT (modify/delete): 25 Tasks/Vault UI Task List Reflects a Write Within 100ms.md deleted in HEAD and modified in origin/master.  Version origin/master of 25 Tasks/Vault UI Task List Reflects a Write Within 100ms.md left in tree.\nAutomatic merge failed; fix conflicts and then commit the result.\n"
```

with the stack framing it as the empty-conflict-list branch:

```
github.com/bborbe/git-rest/pkg/git.(*git).resolveConflictMerge
	github.com/bborbe/git-rest/pkg/git/git.go:786
github.com/bborbe/git-rest/pkg/git.(*git).pullMergeAndPush
	github.com/bborbe/git-rest/pkg/git/git.go:765
```

The repo was left mid-merge (`MERGE_HEAD` present), `/readiness` answered non-200, and an unrelated API write failed with `write file: git commit: U 25 Tasks/The Parity Harness ...md` / `error: Committing is not possible because you have unmerged files.`

### B — upstream drain of a `_conflicts/` entry

```bash
set -e
# Same rule as fixture A: run from the worktree holding the fix.
REPO_UNDER_TEST="${REPO_UNDER_TEST:-$(git rev-parse --show-toplevel)}"
BASE=/tmp/git-rest-drain-repro && rm -rf "$BASE" && mkdir -p "$BASE" && cd "$BASE"

# 1. Bare origin carrying a quarantined file, committed at the merge-base.
git init -q --bare -b test/repro origin.git
git clone -q origin.git seed && cd seed
git config user.email repro@local && git config user.name repro
mkdir -p "_conflicts/25 Tasks"
printf -- '---\ntitle: a\n---\nbody\n' > "_conflicts/25 Tasks/Prev A.1791388434.md"
git add -A && git commit -q -m init
BRANCH=$(git rev-parse --abbrev-ref HEAD) && git push -q origin "$BRANCH"
cd "$BASE"

# 2. Work clone — level at this point.
git clone -q origin.git work && cd work
git config user.email repro@local && git config user.name repro

# 3. Boot the puller FIRST, against the clean, level clone.
cd "$REPO_UNDER_TEST" && go build -o /tmp/git-rest-drain-repro-bin .
/tmp/git-rest-drain-repro-bin -listen=:18446 -repo="$BASE/work" -pull-interval=10s -v=1 > "$BASE/run.log" 2>&1 &
sleep 3

# 4. NOW the operator drains origin: the upstream side DELETES the _conflicts/ entry.
cd "$BASE/seed" && git rm -q "_conflicts/25 Tasks/Prev A.1791388434.md"
git commit -q -m "operator drains the quarantine" && git push -q origin "$BRANCH"

# 5. NOW the replica's own local change to the same quarantined path, committed so
#    HEAD diverges from origin. Steps 4 and 5 must both finish inside one interval.
cd "$BASE/work"
printf -- '---\ntitle: a\n---\nLOCAL REPLICA EDIT\n' > "_conflicts/25 Tasks/Prev A.1791388434.md"
git commit -q -am "local replica touches the quarantine"
LOCAL_SHA=$(git rev-parse HEAD)
git rev-parse --short HEAD "origin/$BRANCH"

# 6. Wait one pull interval, then read readiness, the merge state and the counters.
sleep 12
curl -s -o /dev/null -w 'readiness HTTP %{http_code}\n' http://localhost:18446/readiness
curl -s http://localhost:18446/metrics | grep -E '^git_rest_(quarantined_backlog|resolver_failures_total\{category="nested_source"\})'
echo "MERGE_HEAD: $(test -e "$BASE/work/.git/MERGE_HEAD" && echo present || echo absent)"
git -C "$BASE/work" status --porcelain

# 7. Teardown.
kill "$(pgrep -f /tmp/git-rest-drain-repro-bin)" 2>/dev/null || true
```

Observed against v0.29.1: `/readiness` non-200; the log repeating

```
WARN git-rest: nested conflicted path already under _conflicts/ rejected; aborting merge path="_conflicts/25 Tasks/The Parity Harness Compares Generated UUIDs and Timestamps Raw.1791388434.md" reason="path already under _conflicts/; re-quarantining would nest the tree one level deeper"
```

then `git pull failed error="conflict resolution failed: nested conflict path already quarantined"` every pull interval; `git_rest_resolver_failures_total{category="nested_source"}` climbing (219 at the time of the report); `git_rest_quarantined_backlog` stuck at 3; 22 local commits that could not reach `origin/master`.

Against the current binary this fixture reaches the merge but the conflict line is a modify/delete, so the parser returns an empty list and the repo is left mid-merge (fixture A's symptom). The drain shape therefore only reaches the nesting guard once the parser recognises modify/delete — which is why both defects are in scope and why fixing only the guard does not clear the wedge.

### C — genuine re-quarantine (the shape that must stay refused)

Same fixture as B, except step 4 **modifies** the `_conflicts/` path instead of deleting it, with invalid YAML on the remote side, and step 5 modifies it locally with valid frontmatter — a content conflict in which both sides changed the path and the resolver cannot merge the remote side. Run the puller with `-vault-write=true` — the deployed configuration, since `values-dev.yaml` sets `writeMode: true` for the personal and openclaw vaults, selecting the YAML merge resolver. This is the shape the guard exists for, and it is the shape observed live on 2026-10-07.

## Expected vs Actual

**Expected.** A merge conflict is either resolved or aborted, and the puller never returns while the repository is mid-merge. When HEAD deleted a path the upstream modified, the upstream version is authoritative — that is the operator's manual repair (`git checkout origin/master -- <path>`) performed automatically, and it is the same direction the existing marker resolver already stages (it `git add`s the version git left in the tree). When the upstream side deleted a `_conflicts/` entry, the deletion is the operator's recorded drain and is accepted. A re-quarantine that would nest the tree stays refused.

**Actual.** The conflict-path parser returns an empty list for a modify/delete line, and the empty-list branch returns an error without aborting, leaving the repository mid-merge and the pod NotReady indefinitely. Separately, the nesting guard aborts on any `_conflicts/` path, including one the upstream side drained, so the path never reaches the resolver and the drain never lands.

## Workaround

Operator-side only, and it needs cluster surgery: back up the unpushed commits, reset the index per path, remove the stale `_conflicts/` copies, then conclude the mid-merge git-rest left open by taking the upstream canonicals (`git checkout origin/master -- <paths> && git commit --no-edit`). Performed by hand on 2026-10-07 against `vault-obsidian-personal-0` (backup branch at `a1fd07000a`; quarantines drained in `7bf8717a21`; merge concluded in `967b8087a3`). The pod then logged `recovered from abandoned merge`, pushed, and returned to Ready. The manual procedure is not a fix.

## Why this is a bug

Spec 006 established that the puller recovers from divergence without operator intervention, and `recoverRepoState` already heals abandoned rebases, detached HEADs and leftover `MERGE_HEAD` — the codebase's stated posture is that recoverable git states are recovered. The contract of `ConflictResolver` is explicit (`pkg/git/conflict_resolver.go:16-19`): the resolver is called with the conflicted paths, and "on error the puller runs `git merge --abort` and returns `ErrConflictResolutionFailed`". The empty-conflict-list branch violates both halves: it does not call the resolver (there are no paths) and it does not abort. Returning while mid-merge is not a state any other branch of this code produces.

Spec 012 documents quarantine as moving a file to `_conflicts/<path>.<ts>.md` and spec 013 bounds it: the tree stays exactly one level deep, and quarantined content is never auto-deleted outside an operator drain. The guard implements the first half but aborts the second half's legitimate outcome — the operator's drain arriving as an upstream deletion. Rejecting a drained entry does not protect the tree; it wedges the replica on a state that is already correct on the remote.

## Acceptance Criteria

- [ ] **The parser recognises a modify/delete conflict.** A `pkg/git` internal test calls `parseMergeConflictPaths` with the verbatim incident line `CONFLICT (modify/delete): 25 Tasks/The Parity Harness Compares Generated UUIDs and Timestamps Raw.md deleted in HEAD and modified in origin/master.  Version origin/master of 25 Tasks/The Parity Harness Compares Generated UUIDs and Timestamps Raw.md left in tree.` and asserts the returned slice has length 1 and its only element equals `25 Tasks/The Parity Harness Compares Generated UUIDs and Timestamps Raw.md` — evidence: assertion on the returned slice's length and exact string value.
- [ ] **The other parser outcomes are unchanged.** The same test asserts `parseMergeConflictPaths` still returns `["a.md"]` for a `CONFLICT (content): Merge conflict in a.md` line, and returns a zero-length slice for output containing no conflict line (`fatal: refusing to merge unrelated histories`) — evidence: two assertions on the returned slice's length (positive and negative).
- [ ] **Negative — the repository is never left mid-merge.** For fixture A (resolved) and fixture C (aborted), after `Pull` returns: `test ! -e "$BASE/work/.git/MERGE_HEAD"` exits 0 AND `git -C "$BASE/work" status --porcelain` prints 0 lines. Both probes must hold for both fixtures — evidence: exit code 0 for the file-absence probe and 0 lines of stdout for the status probe. The same two probes hold for the empty-conflict-list branch specifically: a merge whose output contains no parseable conflict line leaves the repository clean.
- [ ] **A modify/delete conflict resolves by taking the upstream version.** Fixture A, after one pull interval: `git -C "$BASE/work" rev-parse --verify MERGE_HEAD` exits non-zero AND `git -C "$BASE/work" status --porcelain` prints 0 lines AND `git -C "$BASE/work" show HEAD:tasks/doomed.md` contains `UPSTREAM EDIT` AND `git -C "$BASE/work" ls-tree -r --name-only HEAD | grep -c 'tasks/doomed.md'` prints `1` AND `git -C "$BASE/work" log -1 --format=%s` matches `^merge: resolved=\[tasks/doomed\.md\] quarantined=\[\]$` AND `git -C "$BASE/work" rev-parse HEAD` equals `git -C "$BASE/work" rev-parse origin/"$BRANCH"` — evidence: exit code, empty stdout, file content, line count, stdout match, and SHA equality.
- [ ] **The restored path is never quarantined, under both resolver configurations.** Fixture A replayed twice — once with `-vault-write=false` (marker resolver) and once with `-vault-write=true` (YAML merge resolver). In both runs: `find "$BASE/work/_conflicts" -type f 2>/dev/null | wc -l` prints `0` AND `git -C "$BASE/work" show HEAD:tasks/doomed.md` contains `UPSTREAM EDIT` — evidence: file count and file content. The YAML-resolver run is the discriminating one: the version git leaves in the tree for a modify/delete conflict carries no conflict markers, so a resolution that merely delegates to the YAML merge resolver quarantines the path instead of restoring it.
- [ ] **An upstream drain of a `_conflicts/` entry is accepted.** Fixture B, after one pull interval: `test ! -e "$BASE/work/_conflicts/25 Tasks/Prev A.1791388434.md"` exits 0 AND `git -C "$BASE/work" ls-tree -r --name-only HEAD | grep -c 'Prev A.1791388434'` prints `0` AND `git -C "$BASE/work" status --porcelain` prints 0 lines AND `git -C "$BASE/work" rev-parse HEAD` equals `git -C "$BASE/work" rev-parse origin/"$BRANCH"` AND `curl -s -o /dev/null -w '%{http_code}' http://localhost:18446/readiness` prints `200` AND `curl -s http://localhost:18446/metrics | grep '^git_rest_quarantined_backlog'` prints `git_rest_quarantined_backlog 0` AND the pre-drain local content is still reachable — `git -C "$BASE/work" merge-base --is-ancestor "$LOCAL_SHA" HEAD` exits 0 — evidence: file absence, line count, empty stdout, SHA equality, HTTP status, metric value, and ancestry exit code. The last probe is the no-content-discarded assertion: the local commit is a parent of the merge, so its content survives in history.
- [ ] **The drain does not trip the nesting guard, and a genuine re-quarantine still does.** Fixture B: `git_rest_resolver_failures_total{category="nested_source"}` read after the pull equals the reading taken before the pull (delta 0) — evidence: metric delta. Fixture C, same process and same reads: `g.Pull(ctx)` returns a non-nil error satisfying `errors.Is(err, ErrConflictResolutionFailed)`, `git_rest_resolver_failures_total{category="nested_source"}` increases by exactly 1, `os.Stat(filepath.Join(repo, "_conflicts", "_conflicts"))` returns an error satisfying `os.IsNotExist`, and `git status --porcelain` prints 0 lines — evidence: error sentinel, metric delta, filesystem absence, empty stdout.
- [ ] **A resolved drain is visible to an operator.** Fixture B's `$BASE/run.log` contains one INFO record naming `_conflicts/25 Tasks/Prev A.1791388434.md` and containing the substring `upstream deletion` — evidence: log line (grep returns ≥1 match).
- [ ] **Spec 013 is reconciled with this amendment.** `specs/in-progress/013-quarantine-nesting-and-drain.md` states the operator-drain exception in prose next to its no-auto-delete rule and next to its `## Non-goals` abort rule — evidence: `grep -c 'operator drain' specs/in-progress/013-quarantine-nesting-and-drain.md` prints ≥1 AND `grep -c 'modify/delete' specs/in-progress/013-quarantine-nesting-and-drain.md` prints ≥1.
- [ ] **`make precommit` exits 0** from the repo root — evidence: exit code.
- [ ] **`CHANGELOG.md` carries the change under `## Unreleased`** — evidence: `awk '/^## /{sec=$0} /modify\/delete/{print sec}' CHANGELOG.md` prints `## Unreleased`.
- [ ] **Post-Deploy (Rung-2):** the dev vault pod runs the fixed build and is healthy in steady state — evidence: `deploy_check` output equals `deploy_target`, `kubectlnukedev -n dev get pod vault-obsidian-personal-0 -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}'` prints `True`, `kubectlnukedev -n dev exec vault-obsidian-personal-0 -- wget -qO- http://localhost:9090/metrics | grep '^git_rest_quarantined_backlog'` prints `git_rest_quarantined_backlog 0`, and `git_rest_resolver_failures_total{category="nested_source"}` read twice ten minutes apart is unchanged.
  - `deploy_check:` `kubectlnukedev -n dev exec vault-obsidian-personal-0 -- wget -qO- http://localhost:9090/metrics | grep -m1 '^git_rest_build_info' | sed -E 's/.*commit="([^"]+)".*/\1/' | grep -E '^[0-9a-f]{7,40}$'`
  - `deploy_target:` `$(git -C ~/Documents/workspaces/git-rest rev-parse --short HEAD)`

  **The comparison is short-SHA against short-SHA.** `Makefile` passes `--build-arg BUILD_GIT_COMMIT=$(git rev-parse --short HEAD)`, so the metric's `commit` label is a short SHA; a full 40-character `rev-parse HEAD` would never compare equal. The trailing `grep -E '^[0-9a-f]{7,40}$'` makes a missing or empty metric exit non-zero, so the gate fails rather than passing falsely.

  **The freshness anchor is the build commit, not the image tag.** The chart renders a floating tag, enables keel, and sets `pullPolicy: Always`, and `values-dev.yaml` pins `image.tag: v0.25.0` while the pods have been observed running v0.29.1 — so comparing the pod's image tag against any tag string succeeds no matter which build is deployed, a check that cannot fail. The commit baked into the binary discriminates.
- [ ] **Rung-1 (image):** the released image resolves the drain shape when replayed against fixture B outside the vault pod — evidence: `docker run --rm -v /tmp/git-rest-drain-repro/work:/data -v /tmp/git-rest-drain-repro/origin.git:/tmp/git-rest-drain-repro/origin.git docker.io/bborbe/git-rest:$(git -C ~/Documents/workspaces/git-rest describe --tags --abbrev=0) -repo=/data -pull-interval=3s`; after two intervals, inside `/tmp/git-rest-drain-repro/work`: `test ! -e .git/MERGE_HEAD` exits 0, `git status --porcelain` prints 0 lines, `ls-tree -r --name-only HEAD | grep -c 'Prev A.1791388434'` prints `0`, and the container log carries one INFO line containing `upstream deletion`. This AC carries no `**Post-Deploy**` marker: a `docker run` of the released image is not a deployed system — it is rung 1 against the released artifact, matching the `Rung 1 (image)` entry in `## Verification`.

  The replay is deliberately **not** run inside the dev vault pod: the fixture wedges its pod (see Desired Behavior 5), which would block the shared dev vault for its other consumers. Spec 013's rung-2 replay set the same precedent.

Scenario coverage — none. Every behavioral assertion above is reachable through in-process integration tests against the real `git` binary in a temp working tree, plus the fixture replays and cluster reads of the deploy rungs. No multi-service interaction is load-bearing, so a scenario would add ceremony without signal.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

```
make precommit
make test
grep -rn 'modify/delete' pkg/git/                    # >=1 match — the parser handles the new form
grep -rl 'parseMergeConflictPaths' pkg/git/*_test.go # >=1 file — the parser now has test coverage
```

All commands exit 0; each `grep` returns at least one match. Today `modify/delete` appears nowhere in the Go source, and `parseMergeConflictPaths` has no `_test.go` reference — note the second grep is scoped to `*_test.go` deliberately: unscoped (`pkg/git/`) it matches only the function's own definition in `pkg/git/git.go` and would pass before any change. The scoped pair is the in-container signal that the change landed.

No `git` command belongs in this rung: the daemon runs with `hideGit=true`, so a `git log` in-container dies with `fatal: not a git repository` and prints 0 lines — indistinguishable from the "0 lines" the branch-relative assertions below expect. Those are operator-only for exactly this reason.

### Operator-executable (runs on the host, spec verification ladder)

- **Rung 1**: `go build -o /tmp/git-rest-verify .` — a fresh binary from current source, never the installed one — then replay fixtures A, B and C from `## Reproduction` and assert the AC probes above.
- **Rung 1 (image)**: build the image (`VERSION=dev ALLOW_UNTAGGED_BUILD=1 make buca`) and replay fixture B against it with `docker run` as in the second Post-Deploy AC — this exercises the released artifact rather than the working tree.
- **Rung 2** (dev cluster, per `docs/verifying-specs.md` and `[[git-rest - Deploy New Version]]`): release via the repo's `autoRelease` flow (`/github-release-repo-trigger` after the merge — never a hand-bumped version), `make buca` from `~/Documents/workspaces/git-rest` to push `bborbe/git-rest:vX.Y.Z`, bump `VERSION` in `~/Documents/workspaces/nuke/git-rest/Makefile` through its own PR, then `cd ~/Documents/workspaces/nuke/git-rest && BRANCH=dev make apply`, then `kubectlnukedev -n dev rollout restart statefulset/vault-obsidian-personal`. Assert the Post-Deploy (Rung-2) AC, then the Rung-1 (image) AC.

  **Confirm which mechanism moves the image tag.** The chart enables keel (`keel.enabled: true` in `values-dev.yaml`) and sets `pullPolicy: Always`, so the pods re-pull on keel's own schedule; both the nuke pin (`VERSION ?= v0.26.0` in `~/Documents/workspaces/nuke/git-rest/Makefile`) and the chart's `image.tag: v0.25.0` lag the v0.29.1 the pods were observed running. Do not treat a successful `make apply` as proof the new build is live: read the running build commit (the `deploy_check`) and only then assert the rest.
- **Rung 3** (prod promotion): `cd ~/Documents/workspaces/nuke/git-rest && BRANCH=master make apply`, then `kubectlnukeprod -n prod get pod vault-obsidian-personal-0` shows `1/1 Running` with `git_rest_quarantined_backlog 0`. The prod replica carries the same pod name and the same incident history; promote immediately after rung 2 with no soak, per `docs/verifying-specs.md`.

## Desired Behavior

1. The conflict-path parser recognises both conflict forms git emits for a failed merge — the content form and the modify/delete form — and extracts the path from each. A merge failure whose output carries no conflict line of either form still yields no paths, so the resolver is never invoked spuriously.
2. Every parsed path — including a path that arrives from a modify/delete conflict — flows through the existing resolution pipeline unchanged: the safe-path pre-flight, the nesting guard, the per-path resolver, and the quarantine fallback. A modify/delete conflict is therefore resolved or aborted by the same rules as a content conflict, and no path is special-cased past the resolver.
3. A modify/delete conflict in which the path was deleted in HEAD and modified upstream is resolved by taking the upstream version: the path is restored with the upstream content, staged, and included in the merge commit, which is then pushed. The path is not quarantined and no `_conflicts/` entry is created for it. This is the automated form of the operator's manual repair and it must hold under both resolver configurations, because the version git leaves in the tree for this conflict carries no conflict markers.
4. A conflicted path under `_conflicts/` whose upstream change is a deletion — the operator's drain — is resolved by accepting the deletion: the path is removed from the tree, the merge is committed and pushed, the pull succeeds, and readiness returns to ready within one pull interval. The local content is not discarded: the local commit is a parent of the merge, so the pre-drain content stays reachable in the repository's history.
5. The nesting guard still refuses a genuine re-quarantine. A `_conflicts/` path that is conflicted **without** an upstream deletion is rejected exactly as today: the merge aborts, `git_rest_resolver_failures_total{category="nested_source"}` increments by one, no path is created under a second `_conflicts/` level, and the repository is left clean rather than mid-merge. The guard's existing ordering property is preserved — both pre-flights run before the quarantine directory is created, so neither abort path creates it as a side effect.
6. After `Pull` returns — on every path, resolved or aborted — the repository is never left mid-merge: no merge is in progress and the working tree is clean. The empty-conflict-list branch of the conflict-merge handler aborts the merge before returning its error, so the invariant holds even when the parser finds no paths.
7. Every accepted drain emits exactly one INFO log line naming the `_conflicts/` path and stating that the upstream deletion was accepted, so an operator can see the drain land rather than inferring it from a gauge dropping.

## Constraints

- **Amends spec 013.** 013's `## Non-goals` reads "Do NOT suppress the abort for an all-rejected merge." This spec adds a deliberate, narrow exception: a `_conflicts/` path whose upstream change is a deletion is resolved rather than aborted. The implementing prompt updates 013's text so a reader of 013 finds the amendment — the operator-drain exception stated in prose beside the no-auto-delete rule, and the abort non-goal reconciled with the new path.
- **Spec 013's no-auto-delete rule stays.** Quarantined content is never deleted automatically outside an operator drain. The drain is the operator's own recorded commit on the remote, and the local pre-drain content remains in git history; neither half of that changes.
- **No nesting, ever.** No path is quarantined twice. The tree never gains a second `_conflicts/` level. A path under `_conflicts/` is refused rather than re-quarantined.
- **Do not change the `ConflictResolver` interface** (`pkg/git/conflict_resolver.go`) or its counterfeiter directive, and do not change the `merge: resolved=[…] quarantined=[…]` commit-message format established by spec 012.
- **Do not change the public HTTP contract** in `docs/api.md` — no new endpoint, no changed status code, no changed response shape, and no change to the readiness semantics. Readiness returning to ready is the observable, not a redefined contract.
- **Preserve the repo's documented invariants**: errors wrapped with `github.com/bborbe/errors` (never `fmt.Errorf`), metrics under the `git_rest_` prefix, and no `context.Background()` in `pkg/`. Three more hold in the code: `log/slog` for logging, timestamps from the injected `libtime` getter rather than `time.Now()`, and counters pre-initialised in `init()`.
- **Adding a method to the `Metrics` interface** (`pkg/metrics/metrics.go`) requires regenerating `mocks/metrics.go` with counterfeiter (`make precommit` runs the generator); without it the build breaks. This spec adds no metric.
- **Parser tests are internal.** `parseMergeConflictPaths` is unexported, so its cases live in the internal `package git` test file and call the helper directly, matching the existing `pkg/git/resolve_conflict_merge_test.go`. The merge-level fixtures live in `pkg/git/git_test.go` (Ginkgo v2 + Gomega) beside the existing quarantine specs, which they extend rather than replace. The merge-level fixtures use real `git` via `os/exec` against a temp working tree with no network.
- **No new alert and no new metric.** The existing `nested_source` counter and the `git_rest_quarantined_backlog` gauge carry the evidence; the alerting redesign in `helm/templates/alerts.yaml` is out of scope.
- **Deterministic quarantine filenames are out of scope.** Making the quarantine destination a hash of the conflicting blobs instead of a wall-clock timestamp is a separate decision with its own adopt-or-reject verdict; this spec neither requires nor forbids it.
- **Do not change the `status` next-vs-`in_progress` race** that produced the original conflicts, and do not add any drain mechanism of git-rest's own — draining is the operator's act, arriving as an upstream deletion.
- **Build via `make precommit`** from the repo root.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection | Reversibility | Concurrency |
|---|---|---|---|---|---|
| Merge yields a modify/delete conflict (HEAD deleted, upstream modified) — the bug | Resolve by taking the upstream version: restore, stage, commit, push; readiness returns ready | None needed — self-healed | INFO log of the committed merge; `git_rest_merge_outcome_total{result="resolved"}` increments; readiness returns 200 | Reversible — both sides are in history | `Pull` holds `g.mu` for its whole body, so cycles cannot overlap |
| Upstream drains a `_conflicts/` entry (upstream deletion, local change) | Accept the deletion: remove the path, commit, push; readiness returns ready | None needed — self-healed | INFO log naming the path and the accepted upstream deletion; `git_rest_quarantined_backlog` drops by one | Reversible — the local content is a merge parent and stays in history | Single-threaded per pod |
| `_conflicts/` path conflicted with no upstream deletion (a genuine re-quarantine) | Refuse exactly as spec 013 does: abort the merge, `nested_source` +1, no second `_conflicts/` level, repository left clean | Operator repairs or deliberately drains the file on the remote; the next pull has no conflict on it | WARN naming the nested path; `nested_source` climbs once per interval; readiness stays not-ready | Reversible — nothing is moved or staged | Single-threaded; the abort leaves the worktree clean for the next attempt |
| Merge failure with no parseable conflict line (unrelated histories, an environmental failure) | Abort the merge, then return the merge's own wrapped error — the repository is clean, not mid-merge | As today; the operator addresses the underlying git failure | Existing `git pull failed` warn; `git_rest_merge_outcome_total{result="aborted"}` increments | Reversible | Single-threaded |
| `git merge --abort` itself fails on the abort path | `Pull` returns the abort error naming the failed abort; the merge state is left for inspection rather than silently swallowed | Operator runs `git -C /data merge --abort` in the pod, then confirms `git status --porcelain` is empty | Error log naming the failed abort; readiness stays not-ready | Reversible | Single-threaded |
| The resolver fails on the restored modify/delete path | The path takes the ordinary quarantine fallback (spec 012): it moves to `_conflicts/`, the merge commits with `quarantined=[…]`, and the pull succeeds | Operator inspects `_conflicts/`, repairs or drains | Quarantine WARN; `git_rest_quarantined_files_total` increments; `quarantined_backlog` rises | Reversible — content is in `_conflicts/` and in history | Single-threaded |
| Push fails after a resolved merge | The merge commit stays local; the pull returns the push error and readiness stays not-ready | None needed — the next pull's `remoteSHA == baseSHA` case pushes it | Error log; readiness not-ready for one interval | Reversible — the commit is local | Single-threaded |
| Pod is killed between the merge and the push | The merge commit is local; `recoverRepoState` finds no abandoned merge (the merge committed) and the next pull pushes | None needed | `remoteSHA == baseSHA` push on the next cycle | Reversible | Single-threaded |
| Pod is killed mid-merge | `recoverRepoState` aborts the abandoned merge at the next boot before `@{u}` resolution | None needed | INFO log `git-rest: recovered from abandoned merge` | Reversible — the abort restores the pre-merge state | Single-threaded |
| Two pull cycles overlap | Impossible — `Pull` holds `g.mu` for its whole body | — | — | — | — |
| Timestamp or clock skew on the quarantine filename | The filename is a label, not an ordering key; the drain is matched by path, and a mismatched replica name is removed by whichever drain names it | None needed | — | Reversible | N/A |

## Security / Abuse Cases

- **The drain path performs a deletion, and it is bounded.** It applies only to a path under `_conflicts/`, and only when the upstream side deleted that same path in an operator-visible commit. The path still passes the existing containment pre-flight, so a traversal path cannot reach it. The content is not destroyed: the local commit is a parent of the merge, so it stays reachable in the repository's history and on the remote.
- **The modify/delete resolution writes upstream content into the tree.** That content is already published on the remote; the resolution adds no new disclosure.
- **The parser consumes git's stdout.** No shell interpolation is added: git is invoked with `--` and paths taken from git's own output, as today. The change is which output line is recognised, not how a path reaches the shell.
- **The guard's refusal remains the backstop.** Removing or weakening the nesting guard would be the abusive change; this spec keeps it and only narrows when it fires, so a `_conflicts/` path can never be nested a second level deep.
- **No new HTTP surface and no new user input crosses a trust boundary.** The readiness endpoint's semantics are unchanged; the metric is scrape-only.

## Suggested Decomposition

Prompts should be generated in this order — each row is a single prompt with a clear scope.

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Parser recognises the modify/delete form; the empty-conflict-list branch aborts before returning; a modify/delete conflict resolves by taking the upstream version under both resolver configurations | 1, 2, 3, 6 | 1, 2, 3, 4, 5 | — |
| 2 | The nesting guard accepts an upstream deletion of a `_conflicts/` path while still refusing a re-quarantine; the accepted-drain INFO log | 4, 5, 7 | 6, 7, 8 | prompt 1 (the drain shape reaches the guard only once the parser recognises modify/delete) |
| 3 | Regression tests for both fixtures and the re-quarantine negative; spec 013 reconciled; `CHANGELOG.md` under `## Unreleased` | — | 3, 9, 10, 11 | prompts 1, 2 |
| 4 | Docs: `docs/verifying-specs.md` gains this shape's rung-1 recipe (both fixtures, with the boot-ordering constraint) beside spec 014's recipe | — | — | prompts 1, 2 |

Rationale: prompt 1 carries the load-bearing defect and the mid-merge invariant, and is independently shippable — it alone returns readiness to ready for the modify/delete shape. Prompt 2 must follow it, because a drained `_conflicts/` path reaches the guard only after modify/delete lines are parsed; shipping prompt 2 first would leave the guard's new acceptance path unreachable and untested. Prompt 3 locks the shapes that must not regress, including the re-quarantine refusal that keeps spec 013's contract. Prompt 4 leaves the next puller bug a replayable shape with its boot-ordering trap recorded. The Post-Deploy (Rung-2) AC (12) is operator-verified against the dev cluster after the PR merges, and the Rung-1 (image) AC (13) is operator-verified by `docker run` against the released artifact; neither has a prompt row by design; AC 9 (`make precommit`) is carried by every prompt that changes the tree.
