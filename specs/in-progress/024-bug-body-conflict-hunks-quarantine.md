---
status: prompted
approved: "2026-11-02T17:42:59Z"
generating: "2026-11-02T17:56:16Z"
prompted: "2026-11-02T18:09:39Z"
branch: dark-factory/bug-body-conflict-hunks-quarantine
---

## Summary

- A merge conflict whose regions lie entirely below a markdown file's closing `---` is resolved in place by the existing `combineBodies` rule instead of being quarantined.
- The rule already ships and is called immediately before the write; it is unreachable for body-only regions because the side extractor rejects an empty side and prepends `---\n` to a body region, so the frontmatter split then finds no closing delimiter.
- The resolved file is the original with only the conflict regions replaced — the frontmatter block and every line outside a region stay byte-identical.
- Anything outside that shape keeps today's quarantine path unchanged.
- Each auto-resolved path increments a new `git_rest_body_merged_total` counter, pre-initialised to zero.

## Problem

git-rest's resolver treats a body conflict as an unparseable file rather than as a merge it already knows how to perform. Three personal-vault files quarantined on 2026-11-02 were exactly this shape — the Mac's `git-ai-sync` racing the dev replica: `60 Periodic Notes/Daily/2026-11-02.md` (ours 0 lines, theirs 98), `24 Goals/The Weekly Trading Rebuild Runs on the Cluster Instead of the Build Host.md` (3 vs 4), and `25 Tasks/Build the Monorepo BuildKit Job Builder and Fan-Out Launcher.md` (0 vs 3, EOF append). All three returned `quarantined — body conflict outside the frontmatter` from vault-api, which is its designed policy; the YAML resolver is the per-path fallback and is the component that must handle them.

The concatenation rule already exists as `combineBodies` — empty-ours → theirs, empty-theirs → ours, equal → ours, both non-empty and different → `ours`, a blank line, then `theirs` — and the resolver calls it immediately before writing. The defect is reachability, through two independent gates:

- The side extractor (`splitConflictSides`) returns not-ok whenever either side has zero lines, so the two empty-side regions fail before any merge is attempted.
- For a both-non-empty body region it prepends a `---` line, so the frontmatter split finds no closing delimiter and the resolver reports "no frontmatter delimiter pair".

## Reproduction

The shape is reproduced at unit level and committed on this branch as `69008a1` — four specs under `Describe("Resolve body-only conflict regions")` in `pkg/git/yaml_merge_resolver_test.go`. Three fail against master `f60784b`:

```
$ go test ./pkg/git/ -ginkgo.focus "body-only conflict regions"
Ran 4 of 214 Specs in 0.906 seconds
FAIL! -- 1 Passed | 3 Failed

[FAIL] AC-bodyonly-ours-empty: theirs body replaces the region verbatim
[FAIL] AC-bodyonly-theirs-empty: ours body kept verbatim
[FAIL] AC-bodyonly-both-differ: ours + blank line + theirs
```

Gate attribution, counted per distinct log line (ginkgo prints each failure twice in captured output):

```
2 specs  resolve conflict "note.md": no conflict region: conflict resolution failed
1 spec   resolve conflict "note.md": no frontmatter delimiter pair: conflict resolution failed
```

The fourth spec, `AC-bodyonly-nonmarkdown`, passes on master by design — it is the fail-closed guard. These committed specs are the acceptance tests for prompts 1 and 3; the fix must turn the three red ones green without editing their assertions.

End-to-end reproduction at the puller (rung 1, see `## Verification`): a temp repo whose `tasks/probe.md` has its conflict markers written entirely below the closing `---` is left quarantined — `_conflicts/` holds 1 file, the merge commit subject names `quarantined=[tasks/probe.md]`, and the `no_frontmatter` failure counter increments by 1.

## Expected vs Actual

**Expected.** A conflict confined to the body of a markdown file is merged deterministically, with no model call: each region is replaced by the concatenation rule, the frontmatter and every byte outside the regions are preserved, the file is written and staged, and the pull commits a clean merge. No `_conflicts/` entry is created.

**Actual.** The file is quarantined. The side extractor returns not-ok for an empty side, and for a non-empty body region the prepended `---` makes the frontmatter split fail; either way the resolver returns `ErrConflictResolutionFailed` with category `no_frontmatter`, and the puller writes a `_conflicts/` copy.

## Why this is a bug

The resolver's own doc comment states the contract: step 4 is "Combines bodies per the rules in the spec (theirs if non-empty, else ours; both differ → `ours\n\ntheirs`)", and `combineBodies` implements exactly that with a doc comment naming the spec. The behaviour therefore contradicts the code's stated contract, and `docs/deployment.md`'s conflict-handling section describes the same rule. A reader of either would conclude the case is handled; it is not.

The cost is silent-ish: the canonical is restored (v0.29.6), so no content is destroyed on the failure path, but a `_conflicts/` copy is created per event, `git_rest_quarantined_backlog` rises, and `VaultObsidianPersonalQuarantineBacklog` re-fires until a human drains the file. Measured rate before the fix: 11 file-quarantines over 10 merge commits in 14 days.

## Goal

When a merge produces one or more conflict regions that lie entirely below a markdown file's closing `---`, git-rest resolves each of them deterministically through the existing concatenation rule and commits the result, so the vault keeps one canonical file and no `_conflicts/` entry is created. Every other conflict shape behaves exactly as it does today.

## Non-goals

- Do NOT auto-resolve a region that crosses the frontmatter/body boundary, a non-markdown path, or a markdown file with no frontmatter block.
- Do NOT auto-drain, delete, or rewrite existing `_conflicts/` entries.
- Do NOT change the deep-merge path (theirs-wins on frontmatter keys), the scalar-frontmatter rule from spec 016, `MarkerResolver`, or the pull loop.
- Do NOT deduplicate a shared prefix or suffix between the two sides. The 3-vs-4 append case deliberately resolves to `ours`, a blank line, then `theirs`, which repeats the shared lines. This looks like duplication and is the specified behaviour.
- Do NOT add per-repository, per-path, or per-key configuration.
- Do NOT add a repeat-guard for two replicas resolving the same divergence (see the concurrency row in `## Failure Modes`).
- Supersedes spec 016's Non-goal "Do NOT auto-resolve body conflicts" for the body-only shape only. Spec 016 itself is unchanged.

## Assumptions

- The pull path is a **merge**, not a rebase: the puller runs `git merge --no-edit`, so `MERGE_HEAD` exists when the resolver runs. The rebase path is unused (`//nolint:unused`).
- In production the personal and openclaw pods run the vault-api resolver with the YAML resolver as its per-path fallback, and vault-api treats any non-`merged` result as a decline. That is why the fix belongs in the YAML resolver and why the rung-2/3 probes still exercise it.
- Affected vault markdown files carry a frontmatter block; a `.md` with no frontmatter block keeps quarantining.
- Unlike spec 016, this rule is timestamp-independent — no `MERGE_HEAD` timestamp lookup, no clock-skew exposure.
- The baseline is `f60784b` (v0.39.0).

## Acceptance Criteria

- [ ] A body-only region with an empty `ours` side is resolved — evidence: the committed spec `AC-bodyonly-ours-empty` passes through `NewYAMLMergeResolver(dir, m).Resolve(ctx, []string{rel})`; the written file is byte-equal to `original[0:openLine] + theirsBody + original[closeLine:]`, `grep -c '^<<<<<<<' <file>` = 0, `grep -c '^=======' <file>` = 0, `grep -c '^>>>>>>>' <file>` = 0, and `fakeMetrics.IncResolverFailureCallCount()` = 0.
- [ ] A body-only region with an empty `theirs` side keeps ours verbatim — evidence: `AC-bodyonly-theirs-empty` passes; written file byte-equal to `original[0:openLine] + oursBody + original[closeLine:]`; `IncResolverFailureCallCount()` = 0.
- [ ] A body-only region where both sides are non-empty and differ resolves to the concatenation — evidence: `AC-bodyonly-both-differ` passes; written file byte-equal to `original[0:openLine] + oursBody + "\n\n" + theirsBody + original[closeLine:]`; `IncResolverFailureCallCount()` = 0.
- [ ] Two body-only regions in one file are resolved independently, not concatenated into one block — evidence: a Ginkgo test over a file with two separated body regions whose contents differ; `Resolve` succeeds, each region carries its own concatenation result, the separator text between them is preserved byte-identically, and no markers remain. A concatenating implementation deletes the separator and cannot pass this.
- [ ] A task-shaped file's frontmatter survives, with only the documented phase-alias normalisation applied — evidence: a Ginkgo test over a `25 Tasks/…` path whose frontmatter carries the alias `phase: in_progress`; the written file differs from the conflicted file only in the conflict-region lines and the normalised alias line (`phase: in_progress` → `phase: execution`), every other frontmatter line and both `---` delimiters are unchanged, and the body region carries the concatenation. A fixture on the canonical `phase: execution` would not discriminate, because preserving and normalising look identical there.
- [ ] A path or file that does not match the shape still quarantines — evidence: two Ginkgo cases, one over `note.txt` carrying a frontmatter block and one over `note.md` with no frontmatter block; each returns `ErrConflictResolutionFailed` via `errors.Is`, leaves the file byte-identical to its pre-call contents, and increments `IncResolverFailureCallCount()` once.
- [ ] A file with one body region and one boundary-crossing region quarantines whole (all-or-nothing) — evidence: a Ginkgo test asserting `ErrConflictResolutionFailed` and that the file is left unmodified.
- [ ] A resolved path is staged, so the merge can commit — evidence: in a repo left in a **real conflicted merge** (a `git merge` that produced unmerged entries, the shape `setupYAMLConflictRepo` builds), `git diff --name-only --diff-filter=U` is empty and `git status --porcelain` shows no `UU` entry after `Resolve`; the existing `AC-staged` spec is the shape to copy. The hand-written-conflict fixtures cannot carry this check — `setupHandWrittenConflict` runs its own `git add` on the conflicted bytes and leaves no merge in progress, so both clauses already pass before `Resolve` is called. Without a genuine-merge fixture, a resolver that writes the splice and returns nil without staging passes every other unit AC while the unmerged index entry keeps the merge from completing.
- [ ] `git_rest_body_merged_total` is present on `/metrics` at value `0` before the first event and increments once per auto-resolved path — evidence: a unit test gathering `prometheus.DefaultGatherer.Gather()` after `metrics.NewMetrics()` (reuse the existing pre-init helper), plus a Ginkgo `IncBodyMergedCallCount()` assertion of 1 per resolving case.
- [ ] A body resolution increments neither the scalar-rule counter nor any resolver-failure category — evidence: across the four resolving cases, `FakeMetrics.IncAutoresolvedCallCount()` = 0 and `IncResolverFailureCallCount()` = 0.
- [ ] A successful resolution that is *not* a body merge leaves the new counter at 0 — evidence: a Ginkgo case through the deep-merge path (both sides carry frontmatter) and one through the scalar-frontmatter rule both succeed while `FakeMetrics.IncBodyMergedCallCount()` = 0. Without this, incrementing the counter in the generic success path passes every other AC.
- [ ] The existing paths do not regress, and no test file outside the resolver's own is touched — evidence: `git diff --name-only 69008a1 -- 'pkg/git/*_test.go'` prints exactly `pkg/git/yaml_merge_resolver_test.go` and nothing else, so `conflict_resolver_test.go`, `git_test.go`, `yaml_merge_scalar_internal_test.go`, `frontmatter_internal_test.go`, `task_phase_test.go`, `resolve_conflict_merge_test.go` and `vault_api_merge_resolver_test.go` are all untouched. A ref-less `git diff --stat` is NOT usable: it compares working tree to index and is empty once the work is committed. **and** the only deletion anywhere in `pkg/git/*_test.go` is the obsolete `Entry("outside frontmatter", "---\ntitle: probe\n---\n<<<<<<< HEAD\nbody a\n=======\nbody b\n>>>>>>> theirs\n")` in `DescribeTable("AC4: rejection shapes keep quarantining")` — that fixture is exactly the body-only shape this change resolves, so spec 024 supersedes spec 016's body-conflict non-goal for it and the expectation is obsolete rather than a regression. Evidence: `git diff --numstat 69008a1 -- pkg/git/yaml_merge_resolver_test.go | awk '{s+=$2} END{print s+0}'` prints `2` (those two lines and nothing else), and the committed `Describe("Resolve body-only conflict regions")` block whose assertions are ACs 1-3's evidence stays byte-identical, as does every other `Entry` in that table. (`grep -c '^-'` is NOT usable either: `git diff` always emits the `--- a/<path>` header, so it counts 1 on an addition-only change.) `make test` exits 0 with the spec 016 scalar cases passing unedited. The `IncResolverFailureCallCount() = 0` clauses in ACs 1-3 are new `It` blocks in that same file — permitted additions, because the committed block asserts only file bytes and marker absence.
- [ ] `make precommit` exits 0 and `CHANGELOG.md` carries a `## Unreleased` bullet naming the body rule — evidence: exit code 0; `sed -n '/^## Unreleased/,/^## v/p' CHANGELOG.md | grep -c 'body conflict'` returns ≥ 1. (`awk '/^## /{s=$0} /body/{print s}'` is NOT usable — `/body/` already matches the released headings `## v0.36.1`, `## v0.19.3` and `## v0.6.1`.)
- [ ] `docs/deployment.md` documents the body rule beside the existing scalar-frontmatter bullet, in behavioural terms — evidence: `grep -n 'body conflict' docs/deployment.md` returns ≥1 line; `sed -n '/body conflict/,/^$/p' docs/deployment.md | grep -c 'blank line'` ≥ 1 and `sed -n '/body conflict/,/^$/p' docs/deployment.md | grep -c 'theirs'` ≥ 1 (the concatenation rule is described — `grep -c 'ours'` alone would also accept the wrong "ours wins" rule); `grep -n 'combineBodies' docs/deployment.md` returns 0 lines (no internal helper name in operator docs).
- [ ] **Post-Deploy (Rung-2):** on dev, a body-only probe conflict on the personal vault is auto-resolved — the merge commit subject is `merge: resolved=[00 Inbox/body-probe.md] quarantined=[]` (the rung-2/3 probe path in the personal vault; `tasks/probe.md` is the rung-1 temp-repo path and does not appear here), `find <clone>/_conflicts -type f | wc -l` shows a delta of 0, `git_rest_body_merged_total` increments by 1, and `git_rest_quarantined_backlog` does not increase from its pre-probe baseline.
  - `deploy_check:` `kubectlnukedev -n dev get --raw "/api/v1/namespaces/dev/pods/vault-obsidian-personal-0:9090/proxy/metrics" | grep -m1 '^git_rest_build_info' | sed -E 's/.*version="([^"]+)".*/\1/' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$'`
  - `deploy_target:` `$(git describe --tags --abbrev=0)`
- [ ] **Post-Deploy (Rung-3):** the same probe and the same evidence are reproduced on prod after promotion.
  - `deploy_check:` `kubectlnukeprod -n prod get --raw "/api/v1/namespaces/prod/pods/vault-obsidian-personal-0:9090/proxy/metrics" | grep -m1 '^git_rest_build_info' | sed -E 's/.*version="([^"]+)".*/\1/' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$'`
  - `deploy_target:` `$(git describe --tags --abbrev=0)`

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — format, generate, test, lint clean
- `make test` — Ginkgo suite passes, including the four `Resolve body-only conflict regions` specs committed in `69008a1`
- `go test ./pkg/git/ -ginkgo.focus "body-only conflict regions"` — 4 passed, 0 failed
- `grep -n 'git_rest_body_merged_total' pkg/metrics/metrics.go` — the counter is declared, registered, and pre-initialised

### Operator-executable (host, after merge; rung ladder)

**Run the recipe from the worktree that holds the fix.** It builds `$REPO_UNDER_TEST`, whose default is the tree you invoke it from — running it from the main checkout on master replays the unfixed binary and fails every assertion falsely.

Rung 1 — fresh binary against a temp repo (the discriminating rung):

```bash
set -e
REPO_UNDER_TEST="${REPO_UNDER_TEST:-$(git rev-parse --show-toplevel)}"
BASE=/tmp/git-rest-body-repro && rm -rf "$BASE" && mkdir -p "$BASE" && cd "$BASE"
git init -q --bare -b test/repro origin.git
git clone -q origin.git seed && cd seed
git config user.email repro@local && git config user.name repro
mkdir -p tasks
printf -- '---\npage_type: note\n---\n\n# Heading\n\nbase body\n' > tasks/probe.md
git add -A && git commit -q -m init && BRANCH=$(git rev-parse --abbrev-ref HEAD) && git push -q origin "$BRANCH"
cd "$BASE"
git clone -q origin.git work && cd work
git config user.email repro@local && git config user.name repro
cd "$REPO_UNDER_TEST" && go build -o /tmp/git-rest-body-bin .
/tmp/git-rest-body-bin -listen=:18448 -repo="$BASE/work" -pull-interval=10s -vault-write=true > "$BASE/run.log" 2>&1 &
sleep 3
# Remote: a 4-line body.
cd "$BASE/seed" && printf -- '---\npage_type: note\n---\n\n# Heading\n\na\nb\nc\nd\n' > tasks/probe.md
git add -A && git commit -q -m "remote rewrites the body" && git push -q origin "$BRANCH"
# Local: a 3-line body on the same region, committed so HEAD diverges.
cd "$BASE/work" && printf -- '---\npage_type: note\n---\n\n# Heading\n\na\nb\nc\n' > tasks/probe.md
git commit -q -am "local rewrites the body"
sleep 12
git -C "$BASE/work" show HEAD:tasks/probe.md
git -C "$BASE/work" log -1 --format=%s
find "$BASE/work/_conflicts" -type f 2>/dev/null | wc -l
curl -s localhost:18448/metrics | grep -E '^git_rest_(body_merged_total|autoresolved_total|quarantined_backlog|resolver_failures_total)'
kill "$(pgrep -f /tmp/git-rest-body-bin)" 2>/dev/null || true
```

Expected: the file keeps `---\npage_type: note\n---\n\n# Heading\n\n` and carries `a\nb\nc\n\na\nb\nc\nd` (the concatenation), the commit subject is `merge: resolved=[tasks/probe.md] quarantined=[]`, `_conflicts/` holds 0 files, `git_rest_body_merged_total 1`, `git_rest_autoresolved_total 0`, `git_rest_quarantined_backlog 0`.

On **rungs 2 and 3** — not on this rung-1 recipe — `git_rest_resolver_failures_total{category="vault_api_failed"}` increments on the same pull: vault-api declines a body conflict by policy and the fallback then succeeds. That is expected, not a regression. Rung 1 passes `-vault-write=true` with no vault-api URL, so it wires the bare YAML resolver and that category never moves.

Negative fixture — a region straddling the closing `---`, scripted:

```bash
set -e
BASE=/tmp/git-rest-body-neg && rm -rf "$BASE" && mkdir -p "$BASE" && cd "$BASE"
git init -q --bare -b test/repro origin.git
git clone -q origin.git seed && cd seed
git config user.email repro@local && git config user.name repro
mkdir -p tasks
printf -- '---\npage_type: note\ntags: [a]\n---\n\nbody\n' > tasks/probe.md
git add -A && git commit -q -m init && BRANCH=$(git rev-parse --abbrev-ref HEAD) && git push -q origin "$BRANCH"
cd "$BASE" && git clone -q origin.git work && cd work
git config user.email repro@local && git config user.name repro
/tmp/git-rest-body-bin -listen=:18449 -repo="$BASE/work" -pull-interval=10s -vault-write=true > "$BASE/run.log" 2>&1 &
sleep 3
# Remote edits the last frontmatter line AND the first body line in one hunk.
cd "$BASE/seed" && printf -- '---\npage_type: note\ntags: [b]\n---\n\nREMOTE body\n' > tasks/probe.md
git add -A && git commit -q -m "remote crosses the boundary" && git push -q origin "$BRANCH"
# Local edits the same two lines differently.
cd "$BASE/work" && printf -- '---\npage_type: note\ntags: [c]\n---\n\nLOCAL body\n' > tasks/probe.md
git commit -q -am "local crosses the boundary"
sleep 12
git -C "$BASE/work" log -1 --format=%s
find "$BASE/work/_conflicts" -type f 2>/dev/null | wc -l
curl -s localhost:18449/metrics | grep -E '^git_rest_(body_merged_total|resolver_failures_total)'
kill "$(pgrep -f /tmp/git-rest-body-bin)" 2>/dev/null || true
```

Expected: the commit subject names `quarantined=[tasks/probe.md]`, `_conflicts/` holds 1 file, `git_rest_body_merged_total` stays `0`, and `git_rest_resolver_failures_total{category="no_frontmatter"}` increments by 1.

Rung 2 — dev: bump `VERSION` in `~/Documents/workspaces/nuke/git-rest/Makefile`, `BRANCH=dev make apply`, then replay the rung-1 probe against `00 Inbox/body-probe.md` in `bborbe/obsidian-personal`; read the counter from the pod's `/metrics` and the result from a local clone. Rung 3 — prod: the same after `BRANCH=master apply`. The task [[git-rest Merges Body-Conflict Hunks Instead of Quarantining Them]] owns the dev/prod probe evidence and the 24 h watch.

## Desired Behavior

1. Conflict regions are enumerated from the original file's line ranges, one region per range, and each is resolved and spliced independently. Regions are never concatenated into one block — concatenation is what makes a body region unparseable today and what would delete the text between two regions.
2. The rule applies only when every region lies entirely below the closing `---` of the file's frontmatter and the path's extension is `.md`. A file with no frontmatter block, or with any region crossing the closing delimiter, does not match.
3. Each matching region is resolved by the existing concatenation rule: `ours` empty → theirs verbatim; `theirs` empty → ours verbatim; equal → ours; both non-empty and different → `ours`, a blank line, then `theirs`. An empty side is a valid state here, not a parse failure.
4. The written file is the original with each matching region's lines replaced by the resolved lines. Every line outside the regions — including the whole frontmatter block and both `---` delimiters — is byte-identical to the original, with one documented exception: a task-shaped path whose frontmatter carries the `phase: in_progress` alias is normalised to `phase: execution` by the existing wrapper that every write path already applies. The file is never rebuilt from the regions alone.
5. Resolution is all-or-nothing per file: if any region does not match, or any write or `git add` fails, the resolver returns the existing `ErrConflictResolutionFailed` sentinel with the existing `no_frontmatter` category and leaves the file unmodified, so the puller quarantines exactly as today.
6. A path resolved by the body rule increments `git_rest_body_merged_total` once, after both the write and `git add` succeed, and increments neither `git_rest_autoresolved_total` nor any resolver-failure category. The deep-merge path, the scalar-frontmatter rule, `MarkerResolver`, and the quarantine path keep their current behaviour.

## Constraints

- Extend `pkg/git/yaml_merge_resolver.go`; the `ConflictResolver` interface and its counterfeiter mock stay byte-identical.
- Reuse `combineBodies` as-is. A second concatenation helper beside it is the defect this spec exists to prevent.
- Do not reuse `splitConflictSides` for extraction — it concatenates every region into one block. Reuse `enumerateConflictRegions` (which already scans the original lines and fails closed on unbalanced or nested markers) and `frontmatterBounds` (which already locates the closing delimiter), then follow the `planScalarRegions` / `spliceScalarRegions` precedent for the splice. The symbols are the constraint; the offsets move. A duplicate region enumerator is the same defect this spec forbids for concatenation.
- `git_rest_body_merged_total` is a separate series rather than a `rule` label on `git_rest_autoresolved_total`, because that counter's help text scopes it to the scalar rule and adding a label would break the existing unlabelled series.
- The scalar-frontmatter rule (spec 016) is unchanged and still runs first.
- The deep-merge path, `MarkerResolver`, the quarantine path, and the pull loop must not regress — their tests pass unedited.
- The conflict-failure sentinel remains the only failure error; no new exported error types.
- Errors via `github.com/bborbe/errors` — no `fmt.Errorf`, no bare `return err`.
- Keep `glog` in `pkg/git/yaml_merge_resolver.go`; do not introduce `slog` there and do not mix loggers.
- Tests use Ginkgo v2 / Gomega and Counterfeiter mocks; `make precommit` is the gate.
- No new dependency.
- `docs/deployment.md`'s conflict-handling section is the durable home for the rule and is updated in the same change, in behavioural terms — no internal helper names.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection | Reversibility | Concurrency |
|---|---|---|---|---|---|
| A region crosses the closing `---` | Rule does not apply; `no_frontmatter`; quarantine | Operator drains the `_conflicts/` entry as today | `git_rest_resolver_failures_total{category="no_frontmatter"}` rises; pod log names the path | Reversible by draining | N/A — the resolver runs under the pull mutex |
| The path is not `.md`, or the file has no frontmatter block | Rule does not apply; quarantine | Same | Same | Reversible by draining | N/A |
| A region's side fails to extract (no markers, unbalanced markers) | Rule does not apply; quarantine | Same | Same | Reversible by draining | N/A |
| Disk write or `git add` fails after a successful merge | Existing sentinel; file left as the puller found it | Next pull retries from a clean state | `git_rest_resolver_failures_total{category="write_failed"}` or `git_add_failed` | Reversible | N/A — one resolver pass per pull |
| The file's frontmatter was altered by the write | Cannot occur by construction — the write is a splice of the original lines; a full byte-equality test guards it | Revert the merge commit | Ginkgo byte-equality test fails | Reversible | N/A |
| Two replicas resolve the same divergence independently | **Non-idempotent by design.** Each produces the concatenation in its own order (`A\n\nB` vs `B\n\nA`); the next pull re-conflicts and yields `A\n\nB\n\nB\n\nA`, so the body grows across pulls | Operator inspects the file and removes the duplicated block; the repeat-guard is out of scope (see Non-goals) | `git_rest_body_merged_total` climbs once per event on the same path; the file grows on successive pulls | Partially reversible — content is duplicated, never lost | Two independent writers; no cross-process lock, no last-writer-wins reconciliation |
| vault-api declines the path first | Expected on every body resolution on rungs 2-3: `vault_api_failed` increments, then the fallback resolves | None needed | `git_rest_resolver_failures_total{category="vault_api_failed"}` rises alongside `git_rest_body_merged_total` | None — this is the normal path | N/A |

## Security / Abuse Cases

- The paths resolved come from `git merge`'s own conflict list, not from HTTP input; the existing `safePath` guard is unchanged and still rejects traversal and absolute paths.
- No new filesystem write is introduced: the splice writes the same conflicted path the resolver already writes, and the file is staged with the same `git add -- <path>`.
- The rule only ever emits bytes the two merge sides already contain (the theirs body, the ours body, or both joined by a blank line). It cannot introduce content the merge did not already carry, and it cannot reach a path outside the repo.

## Suggested Decomposition

Prompts should be generated in this order — each row is a single prompt with a clear scope.

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Per-region enumeration via the existing enumerator, the all-or-nothing gate, the in-place splice, staging, and making the four committed specs pass | 1, 2, 3, 4, 5 | 1-8 | — |
| 2 | `git_rest_body_merged_total`: declaration, registration, pre-init, `IncBodyMerged`, and the positive *and* negative FakeMetrics assertions | 6 | 9, 10, 11 | prompt 1 |
| 3 | Regression lock plus the durable docs: the untouched-suite and committed-reproduction diffs, `docs/deployment.md`, and the CHANGELOG bullet | 6 | 12, 13, 14 | prompt 1 |
| 4 | Deploy verification — no code; the operator rung ladder against dev then prod | — | 15, 16 | prompt 3, plus release and deploy |

Rationale: prompt 1 carries all the behavioural risk and is the only one that can fail the adversarial-laziness test; prompts 2 and 3 are independent once it lands; prompt 4 is evidence gathering and blocks only on the release. Verify the prompt count after generation — `prompted` means the generator finished, not that it produced every row.

## Do-Nothing Option

The three shapes keep quarantining at roughly the measured rate (11 file-quarantines over 10 merge commits in 14 days, with the Sentry-writer share already addressed elsewhere). Each event creates a `_conflicts/` copy that a human must drain, keeps `git_rest_quarantined_backlog` above zero, and re-fires `VaultObsidianPersonalQuarantineBacklog` — an alert this task's parent goal exists to silence. The cost is one small, well-understood rule in a resolver that already implements it.
