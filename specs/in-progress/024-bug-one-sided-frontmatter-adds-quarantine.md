---
status: verifying
approved: "2026-11-02T15:22:52Z"
generating: "2026-11-02T19:16:56Z"
prompted: "2026-11-02T19:22:23Z"
verifying: "2026-11-02T19:34:07Z"
branch: dark-factory/bug-one-sided-frontmatter-adds-quarantine
---

## Summary

- A merge conflict in which each side adds a **different** frontmatter key at the same anchor is quarantined instead of resolved.
- Both sides are one-sided additions relative to the merge base; neither writer deleted anything, and the union of the two keys is what both writers intended.
- The region-level resolver requires the two sides of a conflict region to carry the identical key set, so it rejects the pair and the file lands in `_conflicts/`.
- Spec 016 excluded one-sided keys deliberately. This spec **supersedes that exclusion for the add case only**, on the operator's 2026-11-02 decision to keep the Mac as a permanent writer.

## Problem

On 2026-11-02 at 13:00:21Z the prod replica quarantined a task file after the Mac's sync daemon and git-rest each added a different frontmatter key at the same position. Both additions were legitimate and neither writer removed anything, so the union is the only answer that loses no data — yet the region-level path cannot express a union, so a two-key disagreement becomes a human intervention. Because the Mac stays a writer by operator decision, this class recurs on every collision, and each recurrence leaves a `_conflicts/` entry that nothing drains.

## Goal

A conflict region inside the frontmatter whose sides add keys the merge base does not carry resolves to the union of both sides, with no conflict markers and no quarantine. Every region whose correct answer needs a human — a delete that raced an edit, a side with no merge base to consult, an unparsable hunk, a non-markdown file — still quarantines exactly as today.

## Non-goals

- Body conflicts — a body both sides changed differently keeps quarantining; `bborbe/vault-api` owns that policy and is unchanged here.
- One-sided **deletes**: a key the base carries and one side removed is never resurrected.
- Conflict regions outside the frontmatter, and non-markdown files.
- Draining or rewriting existing `_conflicts/` entries.
- Any change to `bborbe/vault-api`, including its `ReasonBodyConflict` rule.

## Assumptions

- The conflict arises from a **merge**, not a rebase: `MERGE_HEAD` exists and index stage 1 is readable during resolution.
- A one-sided key is genuinely ambiguous only in the absence of the base; with the base readable, add and delete are distinguishable.
- The pod that must exercise this path is `vault-obsidian-openclaw` on **prod**, which runs `VAULT_WRITE_MODE=true` with no `VAULT_API_URL`. Dev `vault-obsidian-openclaw` carries no `VAULT_WRITE_MODE` and therefore runs `MarkerResolver`; dev `vault-obsidian-personal` runs the vault-api resolver first, so neither dev pod reaches the YAML resolver for this shape.
- The Mac sync daemon remains a permanent writer (operator decision, 2026-11-02).

## Acceptance Criteria

- [ ] **Rung-1:** a fresh build, run with `-vault-write=true`, resolves the same conflict shape through a real pull and the file is merged, not quarantined. Evidence: `git -C "$BASE/work" log -1 --format=%s` matches `^merge: resolved=\[<path>\] quarantined=\[\]$` and `find "$BASE/work/_conflicts" -type f 2>/dev/null | wc -l` prints `0`. The fixture is built per the rung-1 recipe in `docs/verifying-specs.md` — a bare origin plus a clean-boot work clone where the **remote** advances with `previous_assignee` while the **local** commits `pr_state`. It is not the mid-merge repository from `## Reproduction`: `recoverAbandonedMerge` (`pkg/git/git.go:1927`) aborts any `MERGE_HEAD` on the first pull, so booting the puller there would discard the remote side and never reach the resolver.
- [ ] A region whose sides carry **disjoint** scalar keys, with a merge base carrying neither, resolves to both keys. Evidence: `grep -c '^pr_state: open$' <file>` = 1, `grep -c '^previous_assignee: ' <file>` = 1, `grep -c '^<<<<<<<' <file>` = 0.
- [ ] The same case fails against the pre-fix code. Evidence: `git checkout f60784b -- pkg/git/yaml_merge_resolver.go` — the pinned pre-fix master tip (release `v0.39.0`, the state before this spec), **not** `origin/master`, which carries the fix by the time the ladder runs on an auto-release repo, and not `HEAD^`, since the two-prompt decomposition puts base-read plumbing in an earlier commit that also touches this file — then run the case and observe `ErrConflictResolutionFailed` with failure category `no_frontmatter`; then `git checkout HEAD -- pkg/git/yaml_merge_resolver.go` and the same case passes.
- [ ] A one-sided key is kept **only** when the merge base is readable and does not carry it. Evidence: a case with a real merge base where `pr_state` is absent from base and added by ours resolves and keeps `pr_state`; a case where `pr_state` is in base, deleted by ours and edited by theirs still returns `ErrConflictResolutionFailed` (`errors.Is`) and `git diff` on the file is empty.
- [ ] Overlapping keys keep their existing per-key rules. Evidence: `trigger_count: 3` vs `trigger_count: 0` in a region that also carries a one-sided add writes `grep -c '^trigger_count: 3$'` = 1 **and** keeps the added key; a differing `assignee` with a controlled newer committer timestamp resolves to the newer side.
- [ ] The union path counts as an auto-resolution, not a failure. Evidence: `FakeMetrics.IncAutoresolvedCallCount()` = 1 and `IncResolverFailureCallCount()` = 0 for the disjoint case; an unparseable region still returns `ErrConflictResolutionFailed`, increments `no_frontmatter`, and leaves the file byte-identical.
- [ ] **Post-Deploy (Rung-2):** the dev pods run the released tag. Rung 2 is an image-freshness check only — dev `vault-obsidian-openclaw` runs `MarkerResolver`, so no resolver behaviour can be asserted there.
  - `deploy_check:` `kubectlnukedev -n dev get statefulset vault-obsidian-openclaw -o jsonpath='{.spec.template.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(git describe --tags --abbrev=0 origin/master)`
- [ ] **Post-Deploy (Rung-3):** the **released image** resolves the pair. Rung 3 is included because the incident was observed on prod, and prod `vault-obsidian-openclaw` is the only pod that reaches the YAML resolver. Evidence: running the released image against a copy of the rung-1 fixture writes both `pr_state` and `previous_assignee` with zero `<<<<<<<` markers, and the prod pod carries the same tag:

  The copy is taken **before** the rung-1 pull runs. Copying afterwards would copy a repository that is already level and clean, so the image would find no conflict and the zero-marker grep would pass trivially.

  ```bash
  cp -a "$BASE" "$BASE-run"
  docker run -d --name gitrest-verify -v "$BASE-run":/work \
    -e GIT_CONFIG_COUNT=1 -e GIT_CONFIG_KEY_0=safe.directory -e GIT_CONFIG_VALUE_0='*' \
    <released-image> -repo=/work/work -listen=:19090 -vault-write=true -pull-interval=2s
  sleep 10
  docker logs gitrest-verify | grep -E 'resolved|quarantined'
  grep -c '^<<<<<<<' "$BASE-run/work/<path>"    # must print 0
  docker stop gitrest-verify && docker rm gitrest-verify
  ```

  The mount is the whole `$BASE-run` so the fixture's bare origin stays reachable; the container is detached because the binary serves until stopped; the `safe.directory` override clears git's dubious-ownership check on a host-mounted repo.
  - `deploy_check:` `kubectlnukeprod -n prod get statefulset vault-obsidian-openclaw -o jsonpath='{.spec.template.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(git describe --tags --abbrev=0 origin/master)`
- [ ] `docs/deployment.md` describes the new behaviour. Evidence: `grep -n 'one-sided' docs/deployment.md` returns a line naming the base-confirmed union, and `grep -c 'one-sided keys, duplicate keys' docs/deployment.md` returns 0.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — format, generate, test and lint all clean
- `make test` — the Ginkgo suite passes, including the new region cases
- `grep -n 'pr_state' pkg/git/yaml_merge_resolver_test.go` — the reproduction case exists and names the shape

### Operator-executable (runs on the host after merge to master, spec verification ladder)

- rung 1 per `docs/verifying-specs.md` § Rung 1 — temp repo, real pull with `-vault-write=true`, assert no `_conflicts/` entry
- `make buca` — image built and pushed for the released tag
- `kubectlnukedev -n dev get statefulset vault-obsidian-openclaw -o jsonpath='{.spec.template.spec.containers[0].image}'` — dev carries the new tag (freshness only)
- `docker run --rm -v "$BASE/work":/work <released-image> -repo=/work -vault-write=true` — the released image resolves the fixture
- `kubectlnukeprod -n prod get statefulset vault-obsidian-openclaw -o jsonpath='{.spec.template.spec.containers[0].image}'` — prod carries the new tag, after the operator's own apply

The tag is cut by the maintainer releaser on merge to master (`.maintainer.yaml` sets `release.autoRelease: true`); no manual tag step exists in this ladder.

## Desired Behavior

1. A conflict region inside the frontmatter whose sides carry disjoint scalar keys, and whose base lacks those keys, resolves to the union of both sides' keys.
2. A one-sided key is kept only when the merge base is readable and does not carry it; when the base carries it, or cannot be read at all, the region quarantines.
3. Overlapping keys continue to follow their existing rules: counters take the larger integer, every other scalar takes the side with the newer committer timestamp.
4. The resolved region lists the ours-side keys in their original order, then the theirs-only keys in their order, each appearing exactly once.
5. A region outside the frontmatter, a non-markdown file, or a side that is not parseable as scalar entries is left to the existing quarantine path, and every decline still returns `ErrConflictResolutionFailed`.
6. A region resolved by the union rule increments `git_rest_autoresolved_total` once and no resolver-failure category.

## Constraints

- `pkg/git/yaml_merge_resolver.go` keeps `github.com/golang/glog`; never introduce `log/slog` in that file.
- Errors are wrapped with `github.com/bborbe/errors`; never `fmt.Errorf`.
- No **new** `context.Background()` in non-test `pkg/` code; timestamps come from the injected `libtime` getter, never `time.Now()`.
- The existing counter rule (`trigger_count`, `retry_count`, integer `metrics_*` → max) and the newer-side rule must not regress.
- The resolver remains fail-closed: a region it cannot decide aborts resolution with `ErrConflictResolutionFailed`.
- **The `Entry("one-sided key", …)` case at `pkg/git/yaml_merge_resolver_test.go:625` keeps expecting `ErrConflictResolutionFailed`.** Its fixture (`a: 1`/`b: 2` vs `a: 1`) is hand-written — `setupHandWrittenConflict` writes the markers directly, with no `MERGE_HEAD` and no index stages. The region declines on the shared non-counter key `a` because the merge-parent timestamps are unavailable, which happens before any base is read; the one-sided key `b` would independently decline on the unreadable base. Add a sibling case with a real merge base for the add path; do not delete or weaken the existing entry.
- This spec supersedes `specs/in-progress/016-yaml-scalar-conflict-autoresolve.md` Non-goal 2 **for one-sided adds only**. That spec's AC pinning one-sided keys to quarantine (its line 46) is superseded for the base-confirmed add case and unchanged everywhere else. Record the supersession in 016's own file as a one-line note; do not rewrite its acceptance evidence.
- `docs/deployment.md` line 234 currently lists "one-sided keys" among the shapes that keep the quarantine path; it must describe the base-confirmed union instead.
- `CHANGELOG.md` gains a new `## Unreleased` section above `## v0.39.0` with a bullet describing the base-confirmed union. The repo is auto-release, so a merge with no `## Unreleased` bullet cuts no tag and the deploy ladder has nothing to pin.
- No change to `bborbe/vault-api`; its body-conflict policy is out of scope.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| Merge base stage absent (add/add conflict) | Region is not unioned; `ErrConflictResolutionFailed`; file quarantined | Operator drains via the vault conflict runbook; `git_rest_quarantined_backlog` then reads 0 |
| One side's hunk is not parseable as scalar entries | Quarantine, no partial write; `no_frontmatter` increments | Same as above; `git diff` on the canonical is empty |
| Base carries the one-sided key (delete raced an edit) | Region quarantines; the deleted key is never resurrected | Operator picks the surviving side in the `_conflicts/` copy; backlog gauge returns to 0 |
| Merge-parent timestamps unavailable | Existing behaviour: the scalar rule declines rather than guessing; the log line names the path | Operator resolves by hand; the log line identifies the file |
| Disk write or `git add` fails after a successful merge | `ErrConflictResolutionFailed`; the puller aborts the merge | Next pull retries; `git_rest_merge_outcome_total{result="aborted"}` records the abort |
| Two replicas resolve the same path concurrently | Each resolves its own pull; the loser's push is rejected and its next pull re-merges the winner's commit | No action; confirm with `git_rest_push_retries_total` moving and `git status --porcelain` empty afterwards |
| Skewed clocks across the two hosts | The newer-side rule may pick the older edit for a shared key; the union itself is unaffected | The `_conflicts/` entry, if any, records both values; the operator picks the survivor and `git_rest_quarantined_backlog` returns to 0 |

## Security / Abuse

The resolver writes caller-independent content into a repository path reported by git's own merge output. Paths continue to be confined by `safePath`, which rejects absolute paths and anything resolving outside the repo root. Reading the merge base uses the same `git show :1:<path>` form the vault-api resolver already uses, with the path passed as a separate argv element.

## Reproduction

```bash
BASE=$(mktemp -d); cd "$BASE"
git init -q -b main .
printf -- '---\ncreated_date: "2026-11-02"\nphase: planning\n---\n\nbody line\n' > t.md
git add t.md && git -c user.email=a@b -c user.name=a commit -qm base
git checkout -q -b ours
printf -- '---\ncreated_date: "2026-11-02"\nphase: planning\npr_state: open\n---\n\nbody line\n' > t.md
git -c user.email=a@b -c user.name=a commit -qam ours
git checkout -q main && git checkout -q -b theirs
printf -- '---\ncreated_date: "2026-11-02"\nphase: planning\nprevious_assignee: someone\n---\n\nbody line\n' > t.md
git -c user.email=a@b -c user.name=a commit -qam theirs
git checkout -q ours && git merge theirs
```

The merge stops with a conflict whose markers sit between the last frontmatter key and the closing `---`:

    ---
    created_date: "2026-11-02"
    phase: planning
    <<<<<<< HEAD
    pr_state: open
    =======
    previous_assignee: someone
    >>>>>>> theirs
    ---

    body line

(The block is indented four spaces because the repo's global `pre-commit` hook rejects any staged file containing a conflict marker at column 0 — `^(<<<<<<< |>>>>>>> )`. The marker lines themselves are verbatim git output.)

Then run the puller over the conflicted repository with the YAML resolver selected — `-vault-write=true`. Without that flag `selectResolver` returns `MarkerResolver`, which commits the markers unchanged and makes the replay look like a failure of the fix.

Observed on 2026-11-02, prod replica, file `tasks/Dark Factory Implement github - bborbe-attention-controller - 166 - e3e3fa66 - df-probe-112920 - 76217c13.md`:

```
conflicted between Mac db43645a29 (12:59:14Z, +pr_state: open)
             and prod git-rest 0a6e3854fd (13:00:13Z, +previous_assignee: … at the same line)
git_rest_resolver_failures_total{category="no_frontmatter"} = 3
```

The resolver logs `has no frontmatter delimiter pair on one side and the scalar-frontmatter rule does not apply`.

Mechanism, confirmed by reading `pkg/git/yaml_merge_resolver.go`: `splitConflictSides` extracts the two sides and prepends `---`, but the region carries no closing delimiter, so `splitFrontmatter` rejects both sides and `resolveOne` falls into `applyScalarFrontmatterRule`. There `planScalarRegions` accepts the region (it lies inside the frontmatter), and `parseScalarSide` rejects it at the missing-key loop — `pr_state` is absent from the theirs map — which is what emits `no_frontmatter`.

## Expected vs Actual

**Expected:** the file is written with both `pr_state` and `previous_assignee` present, no conflict markers, `git_rest_autoresolved_total` increments, and no `_conflicts/` entry is created.

**Actual:** the resolver rejects the region because the two sides do not carry the identical key set, emits `no_frontmatter`, and the puller quarantines the file into `_conflicts/`.

## Why this is a bug

This is a deliberate contract change, not a defect against the current contract — and the record says so plainly, so the spec says it too.

`specs/in-progress/016-yaml-scalar-conflict-autoresolve.md` (status `verifying`) chose the exclusion on purpose. Its Summary line 15 reads *"Anything outside that shape (non-scalar hunks, hunks outside the frontmatter, one-sided keys, duplicate keys) falls back to today's quarantine path unchanged"*, and its Non-goal 2 reads *"Do NOT auto-resolve body conflicts, list items, block scalars, nested mappings, one-sided add/delete, or duplicate keys — those keep quarantining."* The guard in the code carries the same intent: `parseScalarSide`'s doc comment describes it as enforcing *"identical key sets"*.

That exclusion was correct when written: a one-sided key could be an add or a delete, and without consulting the merge base the resolver cannot tell them apart, so the safe answer was to quarantine both. What changed is the cost. On 2026-11-02 the operator decided the Mac stays a permanent writer, which makes Mac-vs-cluster frontmatter collisions routine rather than exceptional, and the backlog alert fires on each one. The exclusion's safety argument — that a one-sided key is ambiguous — stops holding once the base is consulted, which the resolver can do: it already reads `MERGE_HEAD` and the parent committer times, and the vault-api resolver reads index stage 1 by the same mechanism.

So this spec removes the ambiguity instead of removing the feature: a one-sided key is an add when the base lacks it (keep it) and a delete when the base carries it (quarantine). 016's exclusion is superseded for the add case and untouched everywhere else.

## Do-Nothing Option

Keeping 016's exclusion means every Mac-vs-cluster frontmatter collision keeps producing a `_conflicts/` entry and a firing `VaultObsidian<Realm>QuarantineBacklog`, with a human drain as the only recovery. The 2026-11-02 evidence shows this class is now routine rather than exceptional, so the cost grows with the Mac's write rate, and each occurrence risks the silent-data-loss shape the runbook warns about (the canonical is restored, but the `_conflicts/` copy is never drained). Doing nothing is defensible only if the Mac stops writing — which the operator has ruled out.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Base-read plumbing: read index stage 1 for the conflicted path, expose it to the region planner, decline when unreadable | 2 | — (behaviour-preserving; no AC closes here) | — |
| 2 | Union rule and ordering in `parseScalarSide` / `applyScalarFrontmatterRule`, plus the regression cases and the docs update | 1, 3, 4, 5, 6 | 2, 3, 5, 6, 9 | prompt 1 |

Rationale: prompt 1 is the seam that makes the rule safe and is independently testable (base readable / base carries / base unreadable); prompt 2 builds the union on it. ACs 1 and 7 are the verification ladder and produce no code.
