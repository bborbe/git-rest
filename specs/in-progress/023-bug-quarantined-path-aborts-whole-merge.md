---
status: prompted
approved: "2026-10-26T10:49:10Z"
generating: "2026-10-26T11:09:07Z"
prompted: "2026-10-26T11:16:06Z"
branch: dark-factory/bug-quarantined-path-aborts-whole-merge
---

## Summary

- A conflicted path that already lives under `_conflicts/` makes `git-rest` abort the **entire** merge. The path is one file; the casualty is every pull on that replica.
- The guard's intent — never nest the quarantine tree one level deeper — is right. Its remedy — aborting the merge — converts one unresolvable file into a permanently unmergeable vault.
- The two grounds the guard's spec gave for that remedy do not apply: skipping the path does not stage conflict markers, and it needs no commit-with-unmerged-entries path.
- Prod `vault-obsidian-agent` sat `0/1` for six days on exactly this shape while the executor's reconcile backstop made zero successful passes.
- The fix keeps the no-deeper-nesting guarantee and moves it to where nesting can actually happen — the quarantine write — instead of aborting the merge.

## Problem

`resolveConflictPaths` runs `validateConflictPathsNotNested` as a pre-flight. A conflicted path under `_conflicts/` that is not an accepted upstream drain causes `git merge --abort`, an `ErrConflictResolutionFailed` return, and an unchanged tree — so the next pull reproduces the identical conflict. Nothing in the system can make progress until an operator edits the remote by hand.

Observed on prod on 2026-10-26, after six days:

```
WARN git-rest: nested conflicted path already under _conflicts/ rejected; aborting merge
  path="_conflicts/tasks/Fix Build - golden-party-v1 prod.1792604425.md"
  reason="path already under _conflicts/; re-quarantining would nest the tree one level deeper"
WARN git pull failed error="conflict resolution failed / nested conflict path already quarantined"
```

`git_rest_merge_outcome_total{result="aborted"}` reached 13486 and `git_rest_resolver_failures_total{category="nested_source"}` reached the identical 13486 — every abort was this one guard, and no merge ever resolved. The pod was `0/1` from `2026-10-20T04:48:34Z`; `sum(increase(executor_reconcile_passes_total{result="ok"}[4d]))` was 0, so `agent-task-executor` could not reconcile the vault at all and every consumer gated on that readiness stopped.

## Reproduction

The fixture is unchanged from spec 013 — a quarantined path diverged on both sides, with invalid YAML on the remote side so the YAML resolver cannot absorb it.

```bash
cd ~/Documents/workspaces/git-rest
go build -o /tmp/git-rest-verify .

ORIGIN=/tmp/gr-origin.git && rm -rf "$ORIGIN" && git init -q --bare -b main "$ORIGIN"
SEED=/tmp/gr-seed && rm -rf "$SEED" && mkdir -p "$SEED/_conflicts/25 Tasks" && cd "$SEED"
git init -q -b main && git config user.email v@local && git config user.name v
printf -- '---\ntitle: a\n---\nbody\n' > "_conflicts/25 Tasks/Prev A.md"
git add -A && git commit -q -m init && git remote add origin "$ORIGIN" && git push -q origin main

WORK=/tmp/gr-work && rm -rf "$WORK" && git clone -q "$ORIGIN" "$WORK" && cd "$WORK"
git config user.email v@local && git config user.name v
printf -- '---\ntitle: a\n---\nLOCAL CHANGE\n' > "_conflicts/25 Tasks/Prev A.md"
git commit -q -am local
cd "$SEED"
printf -- '---\ntitle: [unclosed\n---\nREMOTE CHANGE\n' > "_conflicts/25 Tasks/Prev A.md"
git commit -q -am remote && git push -q origin main
cd "$WORK"

/tmp/git-rest-verify -listen=:18445 -repo="$WORK" -pull-interval=3s -vault-write=true &
sleep 8
curl -s -w 'HTTP %{http_code}\n' -o /dev/null http://localhost:18445/readiness
kill %1 2>/dev/null
find "$WORK/_conflicts" -type f | sed "s|$WORK/||"
git -C "$WORK" log --oneline -3
```

Observed on prod on 2026-10-26 (the log line above), and asserted by spec 013's own AC at `pkg/git/git_test.go:2096` — `"AC2: Pull fails with ErrConflictResolutionFailed and leaves the file one level deep"`, inside `Describe("Pull nested quarantine guard")` at `:2085`, which builds exactly this fixture: the pull aborts, `_conflicts/` holds exactly one file, and `git log` shows no new commit. That spec is the one this change inverts in place.

## Expected vs Actual

**Expected.** A conflicted path that is already quarantined is resolved in place and the merge completes. The file stays at exactly one `_conflicts/` level, `_conflicts/_conflicts/` is never created, and the other conflicted paths in the same merge are handled normally.

**Actual.** The merge aborts, the tree is restored, and every subsequent pull hits the same wall. `git-rest`'s documented one-level quarantine contract (`_conflicts/<path>.<ts>.md`) is preserved by refusing to make progress at all, which is a worse outcome than the nesting it prevents.

## Why this is a bug

`specs/in-progress/013-quarantine-nesting-and-drain.md` chose the abort deliberately. Its non-goal reads: *"The abort rule stands for every rejected merge, including a `_conflicts/` path both sides changed."*

This spec supersedes exactly these units of spec 013, and nothing else:

| Superseded unit | Superseded claim |
|---|---|
| **013** non-goal bullet 1 (`:41`) | The clause "The abort rule stands for every rejected merge, including a `_conflicts/` path both sides changed." Such a path is now resolved in place, not aborted — and the bullet's own first clause ("Do NOT suppress the abort for an all-rejected merge") is unreachable for this shape, because the path is resolved rather than suppressed. |
| **013** Desired Behavior 2 | "…and then rejects the merge" |
| **013** Desired Behavior 3 | "A merge containing a nested conflicted path therefore **aborts**" (entire behavior) |
| **013** AC at `:122` | "…when it is the only conflicted path the merge aborts — `errors.Is(err, ErrConflictResolutionFailed)` is true" |
| **013** AC at `:123` | "A merge containing a nested conflicted path aborts even when another path in the same merge resolves" |
| **013** Failure Modes rows 1 and 7 | "Pre-flight guard rejects the merge … readiness 503" / "`Pull` returns `ErrConflictResolutionFailed`" |
| **013** Summary bullet 3, Goal, Expected vs Actual, Reproduction closing paragraph | The sentences that state the guard "rejects" the path and aborts |
| **015** Desired Behavior 5 | "The nesting guard still refuses a genuine re-quarantine. A `_conflicts/` path that is conflicted **without** an upstream deletion is rejected exactly as today: the merge aborts…" — the no-second-`_conflicts/`-level and clean-repository guarantees stay; the abort and the `ErrConflictResolutionFailed` return do not. The paragraph's ordering claim (both pre-flights run before `_conflicts/` is created) becomes moot because there is no longer a second pre-flight. |
| **015** AC "The drain does not trip the nesting guard, and a genuine re-quarantine still does" | Its **second** clause — Fixture C returning `ErrConflictResolutionFailed` with `nested_source` +1. The first clause (the drain's `nested_source` delta of 0) stays in force. |
| **015** Failure Modes row "`_conflicts/` path conflicted with no upstream deletion" | "Refuse exactly as spec 013 does: abort the merge, `nested_source` +1 … readiness stays not-ready" |

Everything else in spec 013 stays in force: the one-level `quarantineDestPath` contract (DB1), the `nested_source` category (DB4), the backlog gauge (DB5–DB6), and the alerts (DB7–DB8). A banner at the top of spec 013 mirrors this table so a verifier does not fail its AC2/AC3 on a correct implementation.

`specs/in-progress/015-bug-pull-wedges-on-modify-delete-conflict.md` asserts the same rule in its Desired Behavior 5, in the second clause of one AC, and in one Failure Modes row — and it carries a **live test** for it. That spec's drain behaviour is untouched and stays in force; only its "a genuine re-quarantine still aborts" claim is superseded. It gets the same banner.

**Three live tests assert the abort and all three must be inverted in place.** `pkg/git/git_test.go:2096` (`"AC2: Pull fails with ErrConflictResolutionFailed and leaves the file one level deep"`, in `Describe("Pull nested quarantine guard")` at `:2085`, from spec 013); `pkg/git/git_test.go:2188` (`"AC3: the whole merge aborts, the worktree is restored and no commit is created"`, in `Context("a merge contains a nested path and an ordinary resolvable path")` at `:2182`, spec 013's other AC); and `pkg/git/git_test.go:2916` (`"AC8: is still refused by the nesting guard"`, in `Context("a genuine re-quarantine")` inside `Describe("Upstream drain of a quarantined path (spec 015)")` at `:2810`). All three build this spec's fixture and all three assert `ErrConflictResolutionFailed` and a restored worktree — so on a correct implementation they fail, and `make precommit` with them cannot pass. Missing any one makes the change unshippable.

Two other `ErrConflictResolutionFailed` specs must **not** be touched, because they cover aborts this spec keeps: `pkg/git/git_test.go:1695` (`_conflicts/` existing as a regular file) and the unsafe-path spec.

`docs/verifying-specs.md:301-313` carries the same contradiction in operator form: its **Fixture C — "the shape that must stay refused"** is this spec's fixture, and it asserts readiness non-200, `nested_source` climbing per interval, an abort-restored worktree, and closes with *"A recipe run that 'fixes' fixture C is a **regression, not a pass**."* That paragraph is superseded too and must be inverted, or an operator following the ladder marks a correct fix as a regression. Fixture B (`:298`, the drain's `nested_source` delta 0) stays valid and unchanged.

The two technical grounds spec 013 gave for the abort are void here. It warned that treating a rejected nested file as handled "would either stage a conflicted file with its markers intact … or require a new commit-with-unmerged-entries path that git refuses." Neither applies: this fix stages the **clean local version** via `git checkout --ours` followed by `git add`, which leaves the working tree byte-identical to HEAD and converts the unmerged index entry to stage 0. There are no markers and no unmerged entries.

The remaining ground was a policy trade — accept a readiness 503 "within one pull interval" rather than deepen the tree. Six days of prod downtime, with the only covering alert silenced, is the evidence that the trade is wrong: the failure was neither visible nor self-healing, and the operator's only exit was a hand edit of the remote.

## Goal

One quarantined file can never again stop a vault replica from pulling. The no-deeper-nesting guarantee survives, enforced at the quarantine write rather than by refusing the merge.

## Do-Nothing Option

Cost of leaving it: the six days of prod downtime already measured in `## Problem` are the expected outcome of every recurrence. The class is not self-healing — the same file re-conflicts on every pull interval — and the trigger (any replica that quarantines a file another replica later edits) is produced by normal operation.

## Non-goals

- Do NOT change the `ConflictResolver` interface, `MarkerResolver`, or `YAMLMergeResolver` internals.
- Do NOT change the `merge: resolved=[…] quarantined=[…]` commit-message format.
- Do NOT change `quarantineDestPath`'s one-level contract, or spec 013's backlog gauge and alerts.
- Do NOT auto-delete, auto-restore, or auto-repair quarantined content. Skipping a path resolves the merge; it does not clean the quarantine.
- Do NOT drain the existing `_conflicts/` residue in any vault from code. That stays a one-time operator action.
- Do NOT add a retry, backoff, or cap around the pull loop.

## Acceptance Criteria

- [ ] A conflicted path already under `_conflicts/` no longer aborts the merge — evidence: **all three** live specs are inverted in place and green. `pkg/git/git_test.go:2096` (today `"AC2: Pull fails with ErrConflictResolutionFailed and leaves the file one level deep"`), `:2188` (today `"AC3: the whole merge aborts, the worktree is restored and no commit is created"`) and `:2916` (today `"AC8: is still refused by the nesting guard"`) each run `g.Pull(ctx)` on the `_conflicts/25 Tasks/Prev A.md` fixture and assert `err` is nil, the file's content and mode are byte-identical to their pre-pull state, `os.Stat(filepath.Join(repo, "_conflicts", "_conflicts"))` satisfies `os.IsNotExist`, `.git/MERGE_HEAD` does not exist, `git status --porcelain` is empty, `git log -1 --format=%s` starts with `merge: resolved=[_conflicts/25 Tasks/Prev A.md]`, the captured `slog` output holds one WARN naming the path, and `git_rest_resolver_failures_total{category="nested_source"}` increases by exactly 1 relative to the pre-pull reading. No spec anywhere may still assert the abort for this fixture — `grep -c 'AC2: Pull fails with ErrConflictResolutionFailed' pkg/git/git_test.go`, `grep -c 'the whole merge aborts' pkg/git/git_test.go` and `grep -c 'is still refused by the nesting guard' pkg/git/git_test.go` each return 0 (they read 1 today).
- [ ] The inverted spec is genuinely RED on unfixed code — evidence: run against `origin/master` before the fix, the same spec fails with an error satisfying `errors.Is(err, ErrConflictResolutionFailed)`; the failure output is captured in `# Results`.
- [ ] A nested path and an ordinary conflicting path in the same merge both complete — evidence: Ginkgo fixture with two diverged files, one under `_conflicts/` and one ordinary; `g.Pull(ctx)` returns nil, the ordinary path appears at `_conflicts/<path>.<ts>.md` with exactly one `_conflicts/` segment, and `git log -1 --format=%s` starts with `merge: resolved=[`.
- [ ] A **first-time** conflict is still quarantined at depth 1, so an implementation that keeps the local version for *every* conflict cannot pass — evidence: a second fixture whose only conflict is an ordinary path with invalid YAML on the remote side; after `g.Pull(ctx)` the file exists at `_conflicts/<path>.<ts>.md`, `git_rest_quarantined_files_total` increases by exactly 1, and no `_conflicts/_conflicts` directory exists.
- [ ] An `ours`-deleted `_conflicts/` path resolves by staging the upstream version instead of aborting — evidence: fixture with the path deleted locally and modified upstream; `g.Pull(ctx)` returns nil and `git status --porcelain` is empty afterwards.
- [ ] Skipping a quarantined path records no abort — evidence: `git_rest_merge_outcome_total{result="aborted"}` has a delta of exactly 0 across the AC1 pull, measured on the process-global registry so `metrics.NewMetrics()` backs the spec.
- [ ] No code path can create a nested quarantine directory — evidence: for each of the four fixtures, `find <repo> -type d -name _conflicts` prints exactly one line (the top-level directory) and `git -C <repo> log --format=%s | grep -c '_conflicts/_conflicts'` returns 0.
- [ ] `make precommit` exits 0, `CHANGELOG.md` carries the change under `## Unreleased`, and `docs/deployment.md` no longer documents the abort — evidence: exit code 0; `grep -c '^## Unreleased' CHANGELOG.md` returns 1 and `awk '/^## Unreleased/{f=1;next} /^## /{f=0} f && /^- /' CHANGELOG.md | grep -c .` returns ≥1 (a wording-independent check that the new section exists and holds at least one bullet — a pattern match on the word `nested` is unusable, because `CHANGELOG.md` already contains it under `## v0.29.2` and `## v0.26.0`); `grep -c 'an unsafe path, a conflicted path that already lives under' docs/deployment.md` returns 0 (that item is gone from the pre-flight abort list at `:215`); and `grep -c 'skipped and kept local' docs/deployment.md` returns ≥1. The `## Unreleased` section does **not** exist today (the newest heading is `## v0.36.2` at line 11), so the prompt must create it above the newest version heading rather than appending to a released section.
- [ ] The operator ladder no longer calls the fix a regression — evidence: `grep -c 'regression, not a pass' docs/verifying-specs.md` returns 0; `grep -c 'must stay refused' docs/verifying-specs.md` returns 0 (the Fixture C heading at `:301` is renamed — leaving it while asserting readiness `200` is a self-contradiction); the Fixture C assertions at `:307-311` now read readiness `200`, `git_rest_resolver_failures_total{category="nested_source"}` delta exactly 1 across one pull, no `_conflicts/_conflicts` path, and a `git status --porcelain` note whose parenthetical says the merge committed rather than "the abort restored the worktree"; and Fixture B's drain assertion at `:298` still reads `nested_source` delta 0 (`grep -c 'delta 0' docs/verifying-specs.md` returns ≥1).
- [ ] **Post-Deploy (Rung-3):** the prod agent vault pulls cleanly on the fixed image — evidence: `kubectlprod -n agent logs vault-obsidian-agent-0 --since=30m | grep -c 'already under _conflicts/ rejected'` returns 0 (scoped to this abort — a bare `aborting merge` would fail on any unrelated unsafe-path or `_conflicts/`-as-file abort in the window) and `kubectlprod -n agent get pod vault-obsidian-agent-0` reports `1/1`, while `git_rest_merge_outcome_total{result="aborted"}` is unchanged between two reads 5 minutes apart: `kubectlprod -n agent exec vault-obsidian-agent-0 -- wget -qO- http://localhost:9090/metrics | grep '^git_rest_merge_outcome_total{result="aborted"}'`.
  - `deploy_check:` `kubectlprod -n agent exec vault-obsidian-agent-0 -- wget -qO- http://localhost:9090/metrics | grep -m1 '^git_rest_build_info' | sed -E 's/.*commit="([^"]+)".*/\1/' | grep -E '^[0-9a-f]{7,40}$'`
  - `deploy_target:` `$(git rev-parse --short HEAD)`

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

```
make precommit
make test
grep -c 'nested conflict path already quarantined' pkg/git/git.go
grep -c 'already under _conflicts/ rejected' pkg/git/git.go
```

`make precommit` and `make test` exit 0. Both `grep`s return **0** — they read 1 each on unfixed master, so neither can pass before the change, and together they prove both the abort's error text and its WARN text are gone. Both patterns are scoped to *this* abort: a bare `grep -c 'aborting merge'` would be unsatisfiable, because four unrelated abort WARNs (`unsafe conflicted path`, `_conflicts/ exists as a regular file`, `stat _conflicts/ failed`, `failed to create _conflicts/`) must stay. No grep asserts a command spelling or the new log wording: the observable behaviour is pinned by AC1, not by source formatting.

### Operator-executable (runs on the host, spec verification ladder)

- `go build -o /tmp/git-rest-verify .` — fresh binary from current source, then replay `## Reproduction` against the temp fixture. Expect: `/readiness` returns `HTTP 200`, `find "$WORK/_conflicts" -type f` lists exactly one file, `git -C "$WORK" log --oneline -1` shows a `merge: resolved=[_conflicts/25 Tasks/Prev A.md]` commit, and no `_conflicts/_conflicts` directory exists.
- RED check against unfixed code: `git checkout origin/master -- pkg/git/git.go && make test` must fail the inverted spec, then `git checkout HEAD -- pkg/git/git.go` to restore.
- `git log --oneline origin/master..HEAD -- pkg/git/conflict_resolver.go mocks/conflict_resolver.go` — must print 0 lines. Host-only because the daemon runs with `hideGit=true` (the operator's launch override; the repo's `.dark-factory.yaml` does not set it), so in-container the same command prints 0 lines for the wrong reason.
- Deploy to the agent vault: follow `docs/verifying-specs.md` § *Rung 2 recipe: the GKE agent vault* (`:373-426`) — bump the `FROM` pin in `git-rest/Dockerfile` of `Seibert-Data/agent` (`~/Documents/workspaces/sm-octopus/agent`) to the released tag, `BRANCH=master make buca` from `~/Documents/workspaces/sm-octopus/agent/git-rest`, then `kubectlprod -n agent rollout restart statefulset/vault-obsidian-agent`. Two traps that doc records and this spec inherits: `BRANCH=prod` is invalid (`Makefile.env` maps only `master`/`staging`/`dev`), and the `core/` chart in that repo deploys the executor and controller, **not** the vault StatefulSet, so `make apply` there does not move the vault image.
- `kubectlprod -n agent rollout status statefulset/vault-obsidian-agent --timeout=180s` — forces the `Always` pull to take the new digest.

**No Rung-2 (dev) AC.** The dev agent vault is `0/1` today for an unrelated defect — `git [rev-parse --abbrev-ref --symbolic-full-name @{u}]: fatal: HEAD does not point to a branch` (detached HEAD) — so a dev AC cannot be satisfied until that is fixed, and it would prove no-regression only. The prod symptom is the real proof and is covered by the Rung-3 AC.

**No scenario.** The four-condition test fails at the first condition: the Ginkgo integration specs drive the real `git` binary against a temp working tree, and Rung 1 replays the identical fixture, so a scenario would add cost without signal.

## Desired Behavior

1. A conflicted path whose name is `_conflicts` or begins `_conflicts/` no longer aborts the merge, and is never passed to `quarantineOne`. It is resolved in place by keeping the local side: `git checkout --ours -- <path>` followed by `git add -- <path>`. The working tree stays byte-identical to HEAD, the index entry becomes stage 0, and the merge can commit. Enforcing the no-nesting rule here — at the skip — means `_conflicts/_conflicts/` cannot be created even if a future caller reintroduces the path into the resolve loop.
   **Ordering is load-bearing:** the skip must run *inside* `resolveEachPath`, after the existing `resolveModifyDelete` dispatch, and must not claim a path whose kind is `conflictKindOursDeleted`. `dropAcceptedDrains` removes only `conflictKindTheirsDeleted` before the guard runs, so an `ours`-deleted quarantined path reaches this point, and `checkout --ours` would fail on it (no stage 2 exists). Dispatching modify/delete first is what lets DB3's `ours`-deleted arm resolve it instead.
2. The configured resolver is deliberately **not** tried first for such a path. `_conflicts/` is operator-owned: a resolver that rewrote a quarantined file would mutate the artefact the operator is meant to inspect, and would do so silently on every pull. Keeping the local side is the minimal, predictable action, and the upstream edit stays reachable in the merge's second parent.
3. Modify/delete conflicts under `_conflicts/` keep spec 015's semantics. The `theirs`-deleted drain already runs today and is unchanged — `dropAcceptedDrains` removes that path before the guard sees it. The `ours`-deleted arm now actually runs: today the guard aborts it before `resolveEachPath` can dispatch to `resolveModifyDelete`.
4. `git_rest_resolver_failures_total{category="nested_source"}` increments exactly once per skipped path, and `git_rest_merge_outcome_total{result="aborted"}` is not incremented from this path. The category keeps its meaning as "an already-quarantined path was encountered"; only the consequence changes. The WARN that records the skip names the path and **must not contain the substring `aborting merge`** — that string is what the Rung-3 probe greps for absence of, and the old message carries it. Wording such as `git-rest: nested conflicted path already under _conflicts/ skipped; keeping the local version` satisfies both.
5. First-time conflicts are unchanged: the resolver runs first, and a resolver failure quarantines the path to `_conflicts/<path>.<unix-ts>.md` at exactly one level. A merge whose only conflicted path was an already-quarantined skip commits and pushes, listing the path in the `resolved=[…]` half of the commit message, so the merge is never abandoned while a conflicted path exists.

## Constraints

- The guard and its helpers are unexported. Pure-function cases live in the internal `package git` test file `pkg/git/resolve_conflict_merge_test.go`; the merge-level specs live in `pkg/git/git_test.go` (Ginkgo v2 + Gomega) beside spec 012/013's quarantine specs, which the inverted spec replaces rather than duplicates.
- All three existing specs must be edited **in place** — `pkg/git/git_test.go:2096`, `:2188` and `:2916`. Adding a new spec that asserts the new behaviour while any old one still asserts the abort is a contradiction the suite cannot hold, and `make precommit` will fail on it.
- The `nested_source` and `aborted`-delta ACs assert against the process-global registry (`prometheus.DefaultGatherer`), so the merge-level spec constructs the real `metrics.NewMetrics()` rather than `mocks.FakeMetrics` — the same precedent specs 012 and 013 set.
- `docs/deployment.md:215` currently states that the merge aborts on "a conflicted path that already lives under `_conflicts/`". That sentence was mandated by spec 013 and must be rewritten to say such a path is skipped and kept local. It is not optional: leaving it makes the repo document the fixed bug as the contract.
- `docs/verifying-specs.md:301-313` ("Fixture C — the shape that must stay refused") is the operator-facing form of the same superseded rule and must be inverted in the same change: readiness `200`, `nested_source` delta exactly 1, no `_conflicts/_conflicts` path, and the closing "regression, not a pass" sentence removed. Fixture B's drain assertions at `:290-299` stay valid and must not be touched.
- `specs/in-progress/015-bug-pull-wedges-on-modify-delete-conflict.md` gets a supersession banner alongside the one in spec 013, naming its Desired Behavior 5, the second clause of its nesting-guard AC, and its "genuine re-quarantine" Failure Modes row — while stating that its drain behaviour stays in force.
- `github.com/bborbe/errors` only, no `fmt.Errorf`; `slog` in new code; an injected `libtime` getter, never `time.Now()`; no `context.Background()` in non-test `pkg/` code (the Ginkgo suites legitimately use it for `ctx`).
- Precommit budgets are funlen 80, gocognit 20, nestif 4, golines 100 — extract a per-path helper rather than deepening the resolve loop.
- `.dark-factory.yaml` sets `workflow: direct`, `pr: false`, `autoRelease: false`, so the prompt commits to its branch directly and no PR or auto-release is expected.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection |
|---|---|---|---|
| Both sides edited the same `_conflicts/` path | Merge completes keeping the local content; upstream's version stays reachable in the merge's second parent | None needed — the next pull merges cleanly because the merge base now carries the kept content | `nested_source` increments once per event |
| A quarantined path is deleted upstream | `resolveModifyDelete` accepts the deletion (spec 015, unchanged) | None needed | `merge: … quarantined=[]` commit line |
| A quarantined path is deleted locally, modified upstream | `stageUpstreamVersion` takes the upstream version | None needed | `git status --porcelain` empty after pull |
| A **new** conflicting path with no `_conflicts/` prefix | Resolver first, quarantine at depth 1 on failure (unchanged) | Operator inspects `_conflicts/` | `git_rest_quarantined_files_total` + backlog gauge |
| `git checkout --ours` fails for a reason other than an `ours`-deleted path (e.g. the path is absent from the index) | The path joins neither the resolved nor the quarantined list. If it was the **only** conflicted path, the pre-existing "all conflicted paths failed resolve and quarantine" branch returns `ErrConflictResolutionFailed` and records the aborted outcome. If any sibling path resolved, the index entry stays unmerged, `git commit` refuses, and `commitAndPushMerge` aborts the merge and returns a **wrapped commit error** — and it does **not** call `IncMergeOutcome("aborted")` | Operator drains the path on the remote: `git rm` the `_conflicts/` entry, commit and push, after which the next pull accepts it as an upstream deletion | Single-path case: `ErrConflictResolutionFailed` plus a `git_rest_merge_outcome_total{result="aborted"}` delta of 1. Multi-path case: `git_rest_git_operation_errors_total{operation="merge"}` delta of 1, no new commit, `git status --porcelain` clean — the pull error carries only `commit resolved merge: …` with no path, so the metric is the discriminator. The arm is unreachable in practice: git reports no conflict for a path absent from the index |
| The merge commits but the push fails (remote moved mid-cycle) | The merge commit is retained locally and the pull returns a push error; the next cycle pushes it | None needed — the next pull retries the push | `git pull failed` WARN with a push error, and `git log origin/master..HEAD` lists the retained merge commit |
| Crash between `checkout --ours` and `git add` | Repository left mid-merge, the pre-existing recovery path handles it | Existing entry-state recovery | `git status` shows an unmerged path |

## Security / Abuse Cases

Not applicable — the change removes a merge abort and adds no input surface. `validateConflictPathsSafe` continues to reject traversal and unsafe paths before any of this runs.

## Suggested Decomposition

Single-layer change in `pkg/git` plus its tests, so one prompt is expected.

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Invert the nesting guard into a skip-and-keep-local resolution; invert **all three** existing specs (`git_test.go:2096`, `:2188`, `:2916`) in place; add the companion fixtures; update `docs/deployment.md`, `docs/verifying-specs.md`, and the two spec banners; add the CHANGELOG entry | 1-5 | 1-9 | — |

Rationale: every desired behavior lives in one resolve path and one guard, and the four fixtures share one helper. Splitting would duplicate the fixture setup without buying isolation. The Post-Deploy AC is verified by the operator ladder, not by a prompt.
