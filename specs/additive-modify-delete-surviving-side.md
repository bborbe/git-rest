---
status: draft
---

## Summary

- A vault file that one side deletes and the other modifies is quarantined as `no_frontmatter` even when the local modification only added content.
- Under the YAML merge resolver (`VAULT_WRITE_MODE=true`, what the vault pods run), this change keeps the surviving side when the deletion would lose nothing — every line of the merge base is present in the survivor — and resolves the conflict instead of quarantining it.
- Where the survivor dropped content the merge base had, the conflict still falls through to the YAML resolver, which quarantines.
- The `_conflicts/` drain and the ours-deleted arm are unchanged.
- Under the default `MarkerResolver` the arm is a no-op: its `Resolve` is a bare `git add` that already stages the survivor, so this change targets the vault-write configuration where the quarantine actually occurs.

## Problem

Observed on prod at `266dee14cf` (2026-11-13T07:57:23Z): `merge: resolved=[] quarantined=[tasks/Update Go bborbe-bolttkv bbfbd7c.md]`. The parents are `a24e276c` (ours, modified) and `7dfe2368` (theirs, `go-sync:`, removed) — a modify/delete, not a two-sided edit.

`resolveModifyDelete` (`pkg/git/git.go:1590`) runs before the configured resolver. `conflictKindOursDeleted` takes the upstream version; `conflictKindTheirsDeleted` **under `_conflicts/`** accepts the deletion; `conflictKindTheirsDeleted` **outside `_conflicts/` returns false** (`:1633`) by design, documented in its own comment (`:1589`) as "exactly as before", citing spec 015 Desired Behavior 4. The path then reaches the configured resolver. Under the YAML merge resolver (`VAULT_WRITE_MODE=true`, what the vault pods run), `splitConflictSides` returns `ok=false` on an empty side (`pkg/git/yaml_merge_resolver.go:381–383`), `resolveOne` increments `IncResolverFailure(resolverFailureNoFront)` at `:101`, and the path quarantines.

A modify/delete carries no conflict markers, so this is exactly the path that produces the quarantine. The quarantine backlog then re-arms on a class the YAML resolver was never meant to answer, and `VaultObsidianOpenclawQuarantineBacklog` keeps firing.

## Goal

Under the YAML merge resolver, a modify/delete conflict in which the upstream side deleted the path, the local side modified it, and the modification is additive-only relative to the merge base resolves to the surviving side with no `_conflicts/` entry. A modification that dropped content the merge base had still quarantines. Under the default `MarkerResolver` the survivor already wins (its `Resolve` is a bare `git add`), so the arm is a behavioural no-op there; the lossy case also resolves there today and continues to. The `_conflicts/` drain and the ours-deleted arm behave exactly as today in both configurations.

## Do-Nothing Option

Keeping today's behaviour is not acceptable: every additive local edit that races an upstream `go-sync:` deletion lands in `_conflicts/` and increments `git_rest_resolver_failures_total{category="no_frontmatter"}`, so `VaultObsidianOpenclawQuarantineBacklog` fires on conflicts that lost no content. The only steady-state alternatives are a permanently firing alert or an operator silencing it — and a silenced backlog hides the lossy quarantines that genuinely need review. The cost of the change is one line-multiset comparison on a path that already failed to resolve.

Alternatives considered and rejected: **blob-hash equality** (too strict — any additive edit changes the hash, so it resolves nothing); **always keep the survivor** (resurrects a file the upstream deliberately deleted even when the local edit dropped content — the data-loss case the fall-through guards); **quarantine-only** (today's behaviour, rejected above).

## Non-goals

- **The `_conflicts/` drain arm** (spec 015 Desired Behavior 4) — unchanged.
- **The ours-deleted arm** (`stageUpstreamVersion`) — unchanged.
- **The `MarkerResolver` configuration.** It already resolves this shape (lossy or not) via its bare `git add`; this change does not alter or document that.
- **A new metric or alert.** The existing `git_rest_resolver_failures_total{category="no_frontmatter"}` and `git_rest_quarantined_backlog` carry the evidence; one INFO log marks the resolution.
- **Deterministic quarantine filenames** — a separate decision.
- **Changing the `merge: resolved=[…] quarantined=[…]` commit-message format** (spec 012).

## Assumptions

- The merge-base blob (stage 1) is the "deleted side's last version": it is the last content the deleted side carried before the deletion. Comparing it against the survivor (stage 2) is the additive test.
- A genuine modify/delete populates stages 1 and 2. `classifyConflicts` also yields `conflictKindTheirsDeleted` for a stage-2-only add/delete shape; a missing stage 1 or 2 falls through as non-additive.
- The vault pods run `VAULT_WRITE_MODE=true` (the YAML merge resolver), which is why the quarantine occurs there and not under the default `MarkerResolver`.

## Desired Behavior

1. For `conflictKindTheirsDeleted` outside `_conflicts/`, when every line of the merge base (stage 1) is present in the surviving side (stage 2) as a multiset — for each line, the base count must not exceed the survivor count — take the surviving side (`git checkout --ours -- <path>` then `git add -- <path>`), log one INFO record naming the path, and resolve.
2. Otherwise return false unchanged, so `resolveEachPath` hands the path to the configured resolver: under the YAML resolver it quarantines; under `MarkerResolver` it stages the survivor and resolves.
3. When reading either blob fails, log a WARN naming the path and the error and return false; never panic.
4. `conflictKindOursDeleted` and the `_conflicts/` arm are unchanged.
5. The `resolveModifyDelete` doc comment keeps the spec 015 Desired Behavior 4 citation on the `_conflicts/` arm and replaces the outside-`_conflicts/` sentence with the additive arm and the remaining non-additive fall-through.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection | Reversibility | Concurrency |
|---|---|---|---|---|---|
| YAML resolver: upstream deleted a path, local modified it additively, outside `_conflicts/` | Keep the surviving side; resolve; no quarantine | None needed | INFO log naming the path; `no_frontmatter` does not increment | Reversible — both sides in history | Single-threaded per pull (`Pull` holds `g.mu`) |
| YAML resolver: upstream deleted a path, local modified it lossily (survivor dropped a base line) | Fall through to the YAML resolver; quarantine | Operator runs `find _conflicts -name '<basename>*'` (≥1 hit), diffs against `git show :1:<path>`, and confirms `no_frontmatter` incremented by exactly 1 | `quarantined=[…]` in the merge message; `no_frontmatter` +1 | Reversible — content in `_conflicts/` and history | Single-threaded |
| Marker resolver: any modify/delete outside `_conflicts/` | Survivor staged and resolved (today and after — the arm is a no-op) | None needed | INFO/merge message | Reversible | Single-threaded |
| Blob read fails (missing stage 1 or 2) | WARN, return false, fall through | Next pull retries | WARN naming the path and error | Reversible | Single-threaded |
| The `checkout --ours` or `add` fails | WARN, return false, fall through to the resolver | Next pull retries | WARN naming the path and error | Reversible | Single-threaded |
| Upstream deleted a path under `_conflicts/` | Accept the deletion (unchanged) | None needed | INFO `upstream deletion` | Reversible | Single-threaded |
| HEAD deleted a path the upstream modified | Take the upstream version (unchanged) | None needed | INFO | Reversible | Single-threaded |
| Upstream deleted a task an agent had appended to (deletion-intent conflict) | The additive arm keeps the survivor, so the file returns upstream | Operator deletes it again — the next merge is a clean delete because the replica no longer differs from base | INFO log naming the path | Reversible | Single-threaded |

## Constraints

- Errors wrapped with `github.com/bborbe/errors`; no `fmt.Errorf`.
- `log/slog` in `pkg/git/git.go`; `pkg/git/yaml_merge_resolver.go` keeps `glog` — do not touch it.
- Timestamps from the injected libtime getter, never `time.Now()` in new code.
- No new `context.Background()` in non-test `pkg/` code; thread the caller's `ctx`.
- Interface → Constructor → Struct → Method; factory functions stay pure.
- No new metric and no new alert.
- **Amends spec 015's outside-`_conflicts/` behaviour**, not its `_conflicts/` arm: 015 DB3 covers ours-deleted and DB4 the `_conflicts/` drain; this adds the theirs-deleted-outside-`_conflicts/` additive arm, which supersedes 015 DB2's "no path is special-cased past the resolver" for this one shape.
- **Records the trade-off**: an additive local edit now beats an upstream deletion. That is deliberate — the deletion loses no content — and one INFO log is the only signal. Deletion intent is subordinate to a local additive edit; the operator deletes again if that is wrong.
- **`docs/deployment.md`'s resolver paragraph gains one sentence on the additive arm**, so the operator-facing arm catalogue stays complete.
- The existing `TestResolveModifyDeleteTheirsDeleted` case (`pkg/git/resolve_conflict_merge_test.go:894–897`) asserts the outside-`_conflicts/` path "must not be resolved here"; it is updated so that assertion is no longer vacuous — either removed (AC #1 owns the behaviour) or rebuilt on a real unmerged index. The `_conflicts/tracked.md` acceptance block at `:899–905` is a different case and stays.

## Acceptance Criteria

- [ ] **Additive theirs-deleted outside `_conflicts/` resolves to the surviving side.** A `pkg/git` internal test drives `resolveEachPath` with the paths marked `conflictKindTheirsDeleted`, against a genuine unmerged index (`git ls-files -u` shows stages 1 and 2), with a resolver that **fails** for these paths (`failingResolver`, not `fakeResolver`, whose `Resolve` returns nil). Evidence: the additive path leaves no unmerged entry (`git ls-files -u` no longer lists it) and `git show :<path>` returns the surviving content; the non-additive path (survivor dropped a merge-base line) is quarantined — `quarantined` contains exactly it and `_conflicts/<basename>.*.md` exists. Pairing the two in one call proves the blob comparison ran rather than a read-failure fall-through.
- [ ] **A duplicate-line loss is not additive.** A fixture where the survivor has the same or greater line count than the base but dropped one duplicated base line is quarantined — the assertion that forces a multiset comparison rather than a length or set check. Evidence: `quarantined` contains the path.
- [ ] **A reordering is still additive.** A fixture where the survivor contains every base line but in a different order resolves — the assertion that rules out a contiguous-substring check. Evidence: the path resolves and `git show :<path>` returns the survivor.
- [ ] **A blob read failure warns and falls through.** A path with no stage 1 or 2 logs a WARN and returns false without panicking. Evidence: WARN log line naming the path.
- [ ] **The unchanged arms still behave as today.** A theirs-deleted path under `_conflicts/` still accepts the deletion; an ours-deleted path still takes the upstream version. Evidence: `expectModifyDeleteResolved` (`pkg/git/git_test.go:2890`, ours-deleted) and the `_conflicts/tracked.md` acceptance assertion at `pkg/git/resolve_conflict_merge_test.go:899–905`.
- [ ] **The doc comment matches the behaviour.** `grep -n 'additive' pkg/git/git.go` returns ≥1 line inside the `resolveModifyDelete` doc comment, and `grep -A1 'spec 015 Desired Behavior' pkg/git/git.go` returns ≥1 match (wrap-tolerant — the phrase currently wraps across two lines).
- [ ] **`CHANGELOG.md` carries the change under `## Unreleased`** — `awk '/^## /{sec=$0} /additive/{print sec}' CHANGELOG.md | head -1` prints `## Unreleased`.
- [ ] **`make precommit` exits 0** from the repo root.

Scenario coverage — none. Every assertion is reachable through in-process integration tests against the real `git` binary in a temp working tree; no multi-service interaction is load-bearing, so a scenario would add ceremony without signal.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs |
|---|---|---|---|
| 1 | The additive arm in `resolveModifyDelete` + the `keepOursSide` helper extraction + the doc comment | 1, 2, 3, 4, 5 | 1, 2, 3, 4, 6 |
| 2 | The `Pull`-level integration case, `CHANGELOG.md`, `docs/deployment.md` | — | 7, 8 |

One prompt can carry both if kept small; the split exists so the load-bearing `git.go` change is independently shippable and testable before the docs/CHANGELOG tail.

## Verification

### Container-executable

```
make precommit
```

`make precommit` includes `test`, so it covers AC #1–#4. AC #5 and #6 are grep assertions; run them explicitly if the reviewer wants them machine-checked.

### Operator-executable (Rung-1 replay, vault-write mode)

Per `docs/verifying-specs.md`, replay the real prod artifact against a locally built binary in vault-write mode (`--vault-write`), which is what the vault pods run and what reproduces the quarantine:

```bash
# 1. Build from the branch under test.
go build -o /tmp/git-rest-additive .
# 2. Fetch the artifact committed at the merge (23 lines, zero markers).
git -C ~/Documents/workspaces/obsidian-openclaw show \
  '266dee14cf:_conflicts/tasks/Update Go bborbe-bolttkv bbfbd7c.1794556643.md' > /tmp/artifact.md
# 3. Replay it through the resolver against a two-parent fixture with -vault-write; assert:
#    - the merge logs resolved=[tasks/Update Go bborbe-bolttkv bbfbd7c.md] quarantined=[]
#    - the surviving file keeps its 23 lines headed "# Update Go: bborbe/bolttkv"
#    - _conflicts/ gains no entry for the base name
```

Known-positive control first: the same replay against a lossy fixture must quarantine, proving the probe can fire. Bound: assert within two 30 s pull intervals. Fallback when the `obsidian-openclaw` checkout or `266dee14cf` is absent: reconstruct the two-parent modify/delete from parents `a24e276c` (modification) and `7dfe2368` (removal) after `git fetch`, or from the committed artifact path above.

## Security / Abuse Cases

- The change reads two git blobs and writes the surviving version, which is already local and already in history — no new disclosure and no new remote surface.
- It only narrows when the fall-through fires; it does not remove the quarantine fallback, so a lossy modification still quarantines for human review under the YAML resolver.
- Git is invoked with `--` and paths from git's own output, as today.
