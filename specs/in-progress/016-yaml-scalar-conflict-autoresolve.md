---
status: prompted
approved: "2026-10-19T20:45:37Z"
generating: "2026-10-19T21:22:59Z"
prompted: "2026-10-19T21:22:59Z"
branch: dark-factory/yaml-scalar-conflict-autoresolve
---

## Summary

- A merge conflict whose hunks lie entirely inside a markdown file's YAML frontmatter and whose hunks are single-line scalar `key: value` pairs is resolved in place instead of quarantined.
- Counters (`trigger_count`, `retry_count`, integer-valued `metrics_*`) take the maximum of the two sides; every other scalar takes the value from the merge side with the newer commit.
- The resolved file preserves every non-conflicting frontmatter line and the whole body verbatim — only the conflict regions are replaced.
- Anything outside that shape (non-scalar hunks, hunks outside the frontmatter, one-sided keys, duplicate keys) falls back to today's quarantine path unchanged.
- Each auto-resolved path increments a new `git_rest_autoresolved_total` counter, pre-initialised to zero.

## Problem

Today a frontmatter-only conflict — the common case in agent-written vaults, where two writers disagree on one scalar such as `trigger_count` — falls through `splitConflictSides` → `splitFrontmatter` with no closing `---`, is counted as `no_frontmatter`, and is quarantined. The canonical is restored by v0.29.6, but the `_conflicts/` copy is never drained, so `git_rest_quarantined_backlog` stays non-zero and `VaultObsidian<Realm>QuarantineBacklog` re-fires until a human removes the file. Measured 2026-10-19: both `vault-obsidian-openclaw` pods run v0.29.6 and both report `git_rest_quarantined_backlog 1`; two same-shape quarantines occurred that day (`trigger_count: 3` vs `0`, and `2` vs `0`). The resolver already knows how to merge frontmatter — it fails only because the conflict region sits inside the block and the extracted side has no closing delimiter.

## Goal

When a merge produces a conflict confined to single-line scalar frontmatter keys, git-rest resolves it deterministically and commits the result, so the vault keeps one canonical file and no `_conflicts/` entry is created. Every other conflict shape behaves exactly as it does today.

## Non-goals

- Do NOT change the existing deep-merge path for conflicts whose region already carries both `---` delimiters (`deepMergeTheirsWins` keeps theirs-wins). Known consequence: in that shape the same `trigger_count` disagreement can still resolve to `0`; this spec deliberately leaves it unchanged.
- Do NOT auto-resolve body conflicts, list items, block scalars, nested mappings, one-sided add/delete, or duplicate keys — those keep quarantining.
- Do NOT drain, delete, or rewrite existing `_conflicts/` entries.
- Do NOT change the `ConflictResolver` interface, `MarkerResolver`, the quarantine path, or the pull loop.
- Do NOT add per-repository or per-key configuration for the rule.

## Assumptions

- The conflict path is a **merge**, not a rebase: `pkg/git/git.go` `pullMergeAndPush` runs `git merge --no-edit`, so `MERGE_HEAD` exists when the resolver runs. `pullRebaseAndPush` still exists but is unused (`//nolint:unused`); if the pull path reverts to rebase, the rule intentionally does not apply and files quarantine.
- The baseline is v0.29.6, which restores the canonical after quarantine.
- The rule is markdown-only.
- The counter allowlist (`trigger_count`, `retry_count`, integer-valued `metrics_*`) is the operator's 2026-10-19 pick recorded in the parent task [[A No-Frontmatter Quarantine Has No Drain Path]]; only `trigger_count` is measured in this spec's evidence.

## Acceptance Criteria

- [ ] A conflict of `trigger_count: 3` (ours) vs `trigger_count: 0` (theirs), with both hunks inside the frontmatter, is resolved in place: the written file contains `trigger_count: 3` and no conflict markers remain — evidence: Ginkgo test calling `NewYAMLMergeResolver(...).Resolve(ctx, []string{path})`; `grep -c '^trigger_count: 3$' <file>` = 1, `grep -c '^<<<<<<<' <file>` = 0. The no-`_conflicts/` assertion belongs to the rung-1 probe, where the puller creates the entry.
- [ ] The per-key rule holds: allowlisted counters take the max; every other scalar takes the newer side; a tie takes ours; an unlisted integer scalar takes the newer side — evidence: a Ginkgo `DescribeTable` over (ours value, theirs value, ours commit time, theirs commit time, expected value) covering `trigger_count` 3 vs 0 → 3, `retry_count` 1 vs 5 → 5, `metrics_retries` 2 vs 7 → 7, `assignee` newer-theirs → theirs, `assignee` newer-ours → ours, equal times → ours, an unlisted `attempt_count` 1 vs 9 with newer-theirs → 9, and a quoted counter `trigger_count: "3"` vs `0` with newer-theirs → `0` (non-integer counter values fall back to the newer side).
- [ ] The non-counter newer-side rule is proven end-to-end, not only at helper level — evidence: a Ginkgo case through `Resolve` where a non-counter scalar differs and the newer side's raw line appears in the written file while the older side's does not.
- [ ] The fallback cases quarantine: a non-scalar hunk (one case each for a list item, a block scalar, a nested mapping, and a flow collection), a hunk outside the frontmatter, a one-sided key, and a duplicate key each return `ErrConflictResolutionFailed` and increment `git_rest_resolver_failures_total{category="no_frontmatter"}` — evidence: Ginkgo cases; every fixture carries no closing `---` on either side, and all but the outside-frontmatter fixture place the region inside the frontmatter block; assert the error via `errors.Is` and the metric on `FakeMetrics`. The one-sided fixture is ours `a: 1\nb: 2` vs theirs `a: 1`, so the new key-set guard is what rejects; a fully empty side is pre-existing behavior and is not evidence of the new rule.
- [ ] A file with two conflict regions, one resolvable and one not, quarantines the whole file (all-or-nothing) — evidence: a Ginkgo case asserting `ErrConflictResolutionFailed` and that the file is left unmodified.
- [ ] The resolved file preserves content outside the conflict regions: frontmatter keys not in conflict, the closing `---`, and the body are byte-identical to the original — evidence: a Ginkgo case with a fixture carrying a second key and a body, asserting the full written file against a golden string.
- [ ] `git_rest_autoresolved_total` increments once per auto-resolved path and is present on `/metrics` at value `0` before the first event — evidence: unit test gathering `prometheus.DefaultGatherer.Gather()` after `metrics.NewMetrics()` (reuse the existing `gatherGauge` / pre-init helpers), plus a Ginkgo call-count assertion on `FakeMetrics`.
- [ ] `make precommit` exits 0 and `CHANGELOG.md` carries a `## Unreleased` bullet naming the rule — evidence: exit code 0; `awk '/^## /{s=$0} /scalar/{print s}' CHANGELOG.md` prints `## Unreleased`.
- [ ] `docs/deployment.md` documents the scalar-frontmatter rule in its vault-write conflict-handling bullet — evidence: `grep -n 'scalar frontmatter' docs/deployment.md` returns ≥1 line; `grep -n 'auto-resolve' docs/deployment.md` returns ≥1 line; `grep -n 'scalar frontmatter.*human inspection' docs/deployment.md` returns 0 lines.
- [ ] **Post-Deploy (Rung-2):** on dev, a `trigger_count: 3` vs `0` probe conflict is auto-resolved — no new `_conflicts/` entry, the canonical carries `trigger_count: 3`, and `git_rest_quarantined_backlog` does not increase from its pre-probe baseline.
  - `deploy_check:` `kubectlnukedev -n dev get --raw "/api/v1/namespaces/dev/pods/vault-obsidian-openclaw-0:9090/proxy/metrics" | grep -m1 '^git_rest_build_info' | sed -E 's/.*version="([^"]+)".*/\1/' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$'`
  - `deploy_target:` `$(git describe --tags --abbrev=0)`
- [ ] **Post-Deploy (Rung-3):** the same probe and the same evidence are reproduced on prod after promotion.
  - `deploy_check:` `kubectlnukeprod -n prod get --raw "/api/v1/namespaces/prod/pods/vault-obsidian-openclaw-0:9090/proxy/metrics" | grep -m1 '^git_rest_build_info' | sed -E 's/.*version="([^"]+)".*/\1/' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$'`
  - `deploy_target:` `$(git describe --tags --abbrev=0)`

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — format, generate, test, lint clean
- `make test` — Ginkgo suite passes
- `grep -n 'autoresolve' pkg/git/yaml_merge_resolver.go pkg/metrics/metrics.go` — the rule and the counter are present

### Operator-executable (host, after merge; rung ladder)

Rung 1 — fresh binary against a temp repo (the discriminating rung):

```bash
set -e
REPO_UNDER_TEST="${REPO_UNDER_TEST:-$(git rev-parse --show-toplevel)}"
BASE=/tmp/git-rest-scalar-repro && rm -rf "$BASE" && mkdir -p "$BASE" && cd "$BASE"
git init -q --bare -b test/repro origin.git
git clone -q origin.git seed && cd seed
git config user.email repro@local && git config user.name repro
mkdir -p tasks && printf -- '---\ntrigger_count: 9\ntitle: probe\n---\nbody\n' > tasks/probe.md
git add -A && git commit -q -m init && BRANCH=$(git rev-parse --abbrev-ref HEAD) && git push -q origin "$BRANCH"
cd "$BASE"
git clone -q origin.git work && cd work
git config user.email repro@local && git config user.name repro
cd "$REPO_UNDER_TEST" && go build -o /tmp/git-rest-scalar-bin .
/tmp/git-rest-scalar-bin -listen=:18447 -repo="$BASE/work" -pull-interval=10s -vault-write=true > "$BASE/run.log" 2>&1 &
sleep 3
cd "$BASE/seed" && printf -- '---\ntrigger_count: 0\ntitle: probe\n---\nbody\n' > tasks/probe.md
git add -A && git commit -q -m "remote resets counter" && git push -q origin "$BRANCH"
cd "$BASE/work" && printf -- '---\ntrigger_count: 3\ntitle: probe\n---\nbody\n' > tasks/probe.md
git commit -q -am "local increments counter"
sleep 12
git -C "$BASE/work" show HEAD:tasks/probe.md
git -C "$BASE/work" log -1 --format=%s
find "$BASE/work/_conflicts" -type f 2>/dev/null | wc -l
curl -s localhost:18447/metrics | grep -E '^git_rest_(autoresolved_total|quarantined_backlog|resolver_failures_total)'
kill "$(pgrep -f /tmp/git-rest-scalar-bin)" 2>/dev/null || true
```

Expected: the file contains `trigger_count: 3` and `title: probe`, the commit subject is `merge: resolved=[tasks/probe.md] quarantined=[]`, `_conflicts/` holds 0 files, `git_rest_autoresolved_total 1`, and `git_rest_quarantined_backlog 0`.

The post-deploy tag check is sufficient here because the release is cut from the merge commit and the pod's baked version is that tag; the trailing `grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$'` makes a missing metric fail rather than pass silently.

Negative fixture: repeat with a list-item conflict (`- a` vs `- b`) in the frontmatter — expected: `_conflicts/` holds 1 file, the commit subject names `quarantined=[tasks/probe.md]`, and `git_rest_resolver_failures_total{category="no_frontmatter"}` increments.

Rung 2 — dev: bump `VERSION` in `~/Documents/workspaces/nuke/git-rest/Makefile`, `BRANCH=dev make apply`, then run the probe against a scratch file in `bborbe/obsidian-openclaw`; read the gauge from the pod's `/metrics` and the result from a local clone. Rung 3 — prod: the same after `BRANCH=master make apply`. The task `[[A No-Frontmatter Quarantine Has No Drain Path]]` owns the dev/prod probe evidence.

## Desired Behavior

1. The resolver first attempts the existing frontmatter-split path. Only when the conflict sides split successfully and the frontmatter split fails on either side is the new rule attempted. Each conflict region is resolved from its own line range in the original file; multiple regions are never concatenated.
2. The rule applies only when every conflict region lies entirely inside the frontmatter block (after the opening `---`, before the closing `---`), regions do not overlap, and every line on both sides of every region is a single-line scalar mapping entry `key: value` whose parsed value is a scalar (string, integer, float, bool, or null) — not a map or list. Both sides must carry the same key set; a key present on only one side rejects the whole file.
3. Per conflicting key: if the key is `trigger_count`, `retry_count`, or matches `metrics_*` and both values parse as integers, the result is the greater integer. Otherwise the result is the value from the newer side.
4. The newer side is the merge parent with the greater committer timestamp (HEAD vs MERGE_HEAD). Equal timestamps take HEAD (ours). A missing, multi-valued, or unreadable MERGE_HEAD, or an unreadable timestamp, means the rule does not apply.
5. The written file is the original file with each conflict region replaced by the resolved lines: the winning side's raw line is re-emitted verbatim for a non-counter (preserving quoting and trailing comments), and `key: <max>` is emitted for a counter. Every line outside the regions, the closing `---`, and the body are byte-identical to the original.
6. When the rule does not apply, the resolver returns the existing conflict-failure sentinel with the existing `no_frontmatter` category, and the puller quarantines exactly as today.
7. Each path resolved by the new rule increments `git_rest_autoresolved_total` once, after both the write and `git add` succeed, and does not increment any resolver-failure category.

## Constraints

- Extend `pkg/git/yaml_merge_resolver.go`; the `ConflictResolver` interface and its counterfeiter mock stay byte-identical.
- The new rule must not reuse the existing side-extraction helper (`splitConflictSides`), which concatenates every region into one block; regions are enumerated from the original file.
- The existing deep-merge path, `MarkerResolver`, the quarantine path, and the pull loop must not regress — their tests pass unedited.
- The conflict-failure sentinel remains the only failure error; no new exported error types.
- Errors via `github.com/bborbe/errors` — no `fmt.Errorf`, no bare `return err`.
- Keep `glog` in `pkg/git/yaml_merge_resolver.go` (the file's legacy logger); do not introduce `slog` there and do not mix loggers.
- The timestamp lookup is a resolver struct field defaulting to a git-backed getter (`git rev-parse HEAD`, `git rev-parse MERGE_HEAD`, `git show -s --format=%ct`), so unit tests can stub both sides from a `package git` test (precedent: `pkg/git/resolve_conflict_merge_test.go`) or an `export_test.go`. `NewYAMLMergeResolver`'s signature does not change; `main.go` and the resolver-selection test stay untouched.
- Timestamps come from git, not `time.Now()`.
- Tests use Ginkgo v2 / Gomega and Counterfeiter mocks; `make precommit` is the gate.
- No new dependency; use the already-direct `gopkg.in/yaml.v3`.
- The rule applies to markdown files only; non-markdown conflicts keep the current path.
- The conflict-handling section of `docs/deployment.md` is the durable home for the rule and must be updated in the same change.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection | Reversibility |
|---|---|---|---|---|
| A conflict region crosses the frontmatter/body boundary | Rule does not apply; `no_frontmatter`; quarantine | Operator drains the `_conflicts/` entry as today | `git_rest_resolver_failures_total{category="no_frontmatter"}` rises; pod log names the path | Reversible by draining |
| A hunk is non-scalar (list, block scalar, nested map, flow collection) | Rule does not apply; quarantine | Same | Same | Reversible by draining |
| A key exists on only one side, or appears twice on one side | Rule does not apply; quarantine | Same | Same | Reversible by draining |
| `MERGE_HEAD` missing, multi-valued, or a timestamp unreadable | Rule does not apply; quarantine | Same | Same | Reversible by draining |
| An allowlisted counter value is not an integer on either side | Counter rule does not apply; newer side wins | None — deterministic | `git_rest_autoresolved_total` increments; merge commit diff shows the value | A wrong pick is fixed by a new commit |
| Disk write fails | `write_failed`; conflict-failure sentinel; quarantine | Operator drains the `_conflicts/` entry | `git_rest_resolver_failures_total{category="write_failed"}`; pod log | Reversible by draining |
| `git add` fails | `git_add_failed`; conflict-failure sentinel; quarantine | Operator drains the `_conflicts/` entry | `git_rest_resolver_failures_total{category="git_add_failed"}`; pod log | Reversible by draining |
| Two conflict regions, one resolvable and one not | The whole file quarantines (all-or-nothing) | Operator drains the `_conflicts/` entry | `no_frontmatter` counter; pod log | Reversible by draining |
| Clock skew between the two writers picks the wrong newer side | Newer-side rule resolves to the skewed side | Operator corrects the value in a new commit | Merge commit diff; `git_rest_autoresolved_total` | Reversible by a new commit |

## Security / Abuse Cases

- Conflict paths come from `git merge` output, not HTTP — same trust boundary as the existing resolver.
- Non-scalar single lines (flow collections, anchors, aliases, nested values) are rejected before any merge, so the rule cannot introduce a YAML surface beyond one scalar per line.
- Path safety is unchanged; no new filesystem writes outside the conflicted path.
- No new HTTP surface and no new user input.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Per-region enumeration inside the frontmatter + all-or-nothing fallback detection | 1, 2, 6 | 4, 5 | — |
| 2 | Per-key rule (counters max, newer side) + byte-preserving splice + tests | 3, 4, 5 | 1, 2, 3, 6 | prompt 1 |
| 3 | `git_rest_autoresolved_total` metric + registry pre-init + CHANGELOG + `docs/deployment.md` | 7 | 7, 8, 9 | prompt 2 |
| 4 | Deploy verification — no code; operator rung ladder | — | 10, 11 | prompt 3 + release/deploy |

## Do-Nothing Option

Every frontmatter-scalar conflict continues to quarantine. The canonical is restored (v0.29.6), so there is no data loss, but each occurrence leaves a permanent `_conflicts/` file, keeps `git_rest_quarantined_backlog` non-zero, and re-fires the alert until a human drains it — two occurrences on 2026-10-19. The alert class stays in the manual-triage loop, which is the cost this spec removes. Auto-drain was the rejected alternative: it removes the file but leaves the class to recur, whereas auto-resolve removes it at the source.
