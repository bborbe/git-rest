---
status: completed
spec: [013-quarantine-nesting-and-drain]
summary: Corrected the quarantine conflict-handling claims, documented the backlog gauge/alert/drain procedure, and updated the deploy paths in docs/deployment.md and docs/verifying-specs.md to nuke/git-rest and the nukedev/nukeprod wrappers
execution_id: git-rest-quarantine-drain-exec-053-spec-013-docs-and-changelog
dark-factory-version: dev
created: "2026-09-13T21:20:00Z"
queued: "2026-09-14T06:08:34Z"
started: "2026-09-14T06:16:58Z"
completed: "2026-09-14T06:19:13Z"
branch: dark-factory/quarantine-nesting-and-drain
---

# Refresh the deployment and spec-verification docs

<summary>
- The deployment guide's description of conflict handling matches what the puller actually does today
- The resolver-failure category list an operator greps for is complete, including the new nested-source category
- The deployment guide documents the quarantined-file backlog gauge and the per-vault alert that watches it
- The drain procedure is written down where an operator will look: inspect the quarantined file, repair or deliberately discard it, then remove it so the count goes down
- The spec-verification guide points at the real deploy source and the real cluster wrappers instead of the retired trading worktrees
- The vault names in the verification guide match the vaults that actually run
- The release notes carry one bullet for each of the three shipped changes
</summary>

<objective>
Close the documentation loop for the quarantine changes: correct `docs/deployment.md` where it still claims a resolver failure aborts the merge, publish the real resolver-failure category set (including `nested_source`), document the `git_rest_quarantined_backlog` gauge and the per-vault `VaultObsidian<Realm>QuarantineBacklog` alert together with the drain procedure, and correct `docs/verifying-specs.md`, whose rung-2/rung-3 sections still target the retired `kubectlquant` / `trading-dev` / `shared/base` deploy path so the next spec does not inherit the same drift.
</objective>

<context>
This repo has no `CLAUDE.md`; project conventions live in `docs/dod.md`, `docs/deployment.md`, and the coding-plugin guides below.

Read these coding-plugin guides before implementing (paths inside the YOLO container):

- `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md` — doc voice, structure, and when to add a section rather than a bullet
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — `## Unreleased` rules, `feat:` / `fix:` prefixes, one bullet per logical change

Files to read in full before editing:

- `docs/deployment.md` (173 lines) — the `## Monitoring` section (~155-166) and the `## Operational notes` section (~168-173), whose "Conflict handling" and "Vault-write mode" bullets carry the stale claims
- `docs/verifying-specs.md` (165 lines) — the three-rung table (~7-15), `## Rung 2: dev cluster e2e` (~59-116), `## Rung 3: prod cluster e2e` (~118-122), and `## Rung selection by spec type` (~137-148)
- `CHANGELOG.md` — the `## Unreleased` section written by prompts 1-3, and the `## v0.25.4` heading below it
- `pkg/metrics/metrics.go` — the `ResolverFailuresTotal` doc comment and `Help` string are the source of truth for the category list you are documenting. It currently documents six categories; prompt 1 extends both with `nested_source`, so read the file *after* prompt 1 lands and treat the extended list as authoritative
- `pkg/git/git.go` — `resolveConflictPaths`, `validateConflictPathsSafe`, `validateConflictPathsNotNested`, `ensureConflictsDir`, and `Pull`'s deferred `refreshQuarantinedBacklog` call are the source of truth for the behaviour you are documenting
- `helm/templates/alerts.yaml` — the rendered alert name, expression, window, and severity are the source of truth for the alert paragraph you are documenting

**Preconditions from prompts 1-3 (already on this branch):**

- The nesting guard rejects a conflicted path already under `_conflicts/` with `ErrConflictResolutionFailed` and a `nested_source` counter increment.
- The `git_rest_quarantined_backlog` gauge reports the recursive regular-file count under `_conflicts/`, refreshed once per pull cycle; a missing directory reports 0, an unreadable one reports 0 plus one WARN.
- The chart renders `VaultObsidian<Realm>QuarantineBacklog` per vault when `alerts.enabled` is true: `git_rest_quarantined_backlog{app="vault-obsidian-<name>"} > 0` for `1h`, severity `warning`.

**Documentation-content paths are not container paths:** `docs/verifying-specs.md` documents an operator workflow that runs on the operator's machine, so paths like `~/Documents/workspaces/nuke` are content strings inside the document. Do NOT try to read them from this container — the repo files are the only inputs.

**Environment fact:** the container's `.git` is masked (`hideGit`), so branch-relative `git log` / `git diff` checks are not runnable here. Do not run `git diff`, `git log`, or `git status` against the project repo, and do not attempt to commit.

**Scope:** this prompt is documentation plus the CHANGELOG only. Do NOT edit any Go file, any file under `helm/`, or any test.
</context>

<requirements>

## 1. Correct the conflict-handling claims in `docs/deployment.md`

In the `## Operational notes` section, the "Vault-write mode" bullet currently claims that a YAML parse failure or missing frontmatter delimiters makes the resolver return `ErrConflictResolutionFailed` and the puller abort the merge, and it lists only four resolver-failure categories. Both are stale.

Replace that sentence with the behaviour the code implements:

```
On YAML parse failure or missing frontmatter delimiters, the resolver fails for that file only: the puller moves the file to `_conflicts/<path>.<unix-ts>.md` and continues the merge, so one corrupt file no longer wedges the pod. The merge aborts only when every conflicted path fails both resolve and quarantine, or when a pre-flight guard rejects the merge — an unsafe path, a conflicted path that already lives under `_conflicts/`, or `_conflicts/` existing as a regular file.
```

Replace the trailing "Watch `git_rest_resolver_failures_total{category}`" sentence so the category list matches `pkg/metrics/metrics.go` exactly:

```
Watch `git_rest_resolver_failures_total{category}` on `/metrics` to distinguish failure modes (`yaml_parse_failed`, `no_frontmatter`, `write_failed`, `git_add_failed`, `unsafe_path`, `quarantine_io_failed`, `nested_source`).
```

Keep the surrounding sentences (the deep-merge description, the `VAULT_WRITE_MODE` advice for human-touched repos) unchanged. Do not invent behaviour that is not in the code: the guard records and aborts, it never repairs, restores, or deletes.

## 2. Document the backlog gauge and the alert in `docs/deployment.md`

In the `## Monitoring` section, extend the "Key metrics" line to mention the quarantine backlog gauge, and add a subsection after it:

```markdown
### Quarantine backlog

Quarantined files accumulate under `_conflicts/` in the served repo until an operator drains them. Two series make that visible:

| Series | Meaning |
|--------|---------|
| `git_rest_quarantined_files_total` | Monotonic counter over lifetime quarantine *events*. It never decreases, so it cannot express a backlog. |
| `git_rest_quarantined_backlog` | Gauge: regular files currently under `_conflicts/`, counted recursively. Refreshed once per pull cycle. A missing `_conflicts/` directory reports 0; an unreadable one reports 0 and logs one WARN, and never fails a pull. |

Every vault with `alerts.enabled=true` also gets an alert, `VaultObsidian<Realm>QuarantineBacklog`: `git_rest_quarantined_backlog{app="vault-obsidian-<name>"} > 0` for `1h`, severity `warning`. The one-hour window keeps a single transient quarantine event from paging an operator who is already handling it.

To drain: inspect `_conflicts/` inside the pod, repair the file or deliberately discard it, then remove it from the repo. The gauge follows the directory, so a removal is reflected on the next pull cycle and the alert clears once the count returns to zero. Quarantine surfaces and preserves; the drain is a signal, not an automatic repair.
```

The gauge name, the expression, the window, and the severity MUST match the implementation and `helm/templates/alerts.yaml` exactly.

## 3. Correct the stale deploy path in `docs/verifying-specs.md`

The rung-2 and rung-3 sections still describe the retired deploy path. Replace it with the current one: the deploy source is the `nuke` repository's `git-rest/` directory (a `BRANCH=<branch> make apply` mirror plus Helm upgrade), and the cluster wrappers are `kubectlnukedev` for dev and `kubectlnukeprod` for prod. Three vaults are served — `vault-obsidian-openclaw`, `vault-obsidian-personal`, and `vault-obsidian-trading` — so every vault list and rollout block names all three.

Specifically:

- Every `kubectlquant -n dev …` becomes `kubectlnukedev -n dev …`; every `kubectlquant -n prod …` becomes `kubectlnukeprod -n prod …`.
- The rung-2 pre-conditions that talk about the trading repo's `shared/base/Makefile` `BASE_IMAGES` list, the `vault/obsidian-*/`*-sts.yaml` references, and the `trading-dev` worktree mirror step become: the `VERSION` pin in `~/Documents/workspaces/nuke/git-rest/Makefile` is bumped to the released tag and merged, and the deploy runs `cd ~/Documents/workspaces/nuke && git pull && cd git-rest && BRANCH=dev make apply`.
- The rung-2 rollout commands and the image/readiness verify block cover all three served vaults (`vault-obsidian-openclaw`, `vault-obsidian-personal`, `vault-obsidian-trading`); `vault-obsidian-trading` is still deployed and must not be dropped.
- The rung-3 section's "`trading-prod` worktree, `BRANCH=prod`, and `kubectlquant -n prod`" becomes the same `nuke/git-rest` apply with `BRANCH=master` and `kubectlnukeprod -n prod`.
- Any remaining reference to `trading/vault/` as the place git-rest's manifests live becomes `nuke/git-rest/`.
- The "Rung selection by spec type" table's mention of `k8s/` / `trading/vault/` follows the same correction.

## 3a. Correct the same stale deploy path in `docs/deployment.md`

`docs/deployment.md` carries the same retired path, outside the two sections requirement 1 and 2 touch:

- Line 46's `## Kubernetes` "Reference deployment" line names `trading-agent-trade-analysis/vault/obsidian-trading/k8s/` and links to `bborbe/trading/tree/master/vault/obsidian-trading/k8s`. The manifests now live in `~/Documents/workspaces/nuke/git-rest/`, so the reference becomes that directory and the link points at the `nuke` repository.
- The `### Upgrades` section describes rolling restarts without naming where the image tag is pinned. Add one clause naming the `VERSION` pin in `~/Documents/workspaces/nuke/git-rest/Makefile` as the source of truth for the deployed tag.

Keep the document's structure, voice, and the rest of its content (the rung table, the anti-patterns, the "See also" list) intact. This is a correction pass, not a rewrite: change the deploy path and the vault names, and leave the surrounding reasoning as it is.

## 4. Complete the CHANGELOG

First confirm the `## Unreleased` section exists directly above `## v0.25.4` (between the SemVer preamble and the first `## vX.Y.Z` heading). It does not exist in the current tree, so create it there if absent — nothing above it may change. Then verify it carries one bullet for each of the three shipped changes:

- the nesting guard and the `nested_source` category (prompt 1, a `fix:` bullet),
- the `git_rest_quarantined_backlog` gauge (prompt 2, a `feat:` bullet),
- the per-vault `QuarantineBacklog` alert (prompt 3, a `feat:` bullet).

If the `## Unreleased` heading is missing, create it above `## v0.25.4`; if it is present but empty, fill it. If any of the three bullets is missing or merged into another bullet, add the missing one in the file's style with the right prefix. If all three are present, leave the section as it is — do not duplicate, reword, or reorder existing bullets, and do not create a second `## Unreleased` heading. The existing `## v0.25.4` heading and everything below it stay untouched.

## 5. Final verification

Run every command in `<verification>` from the repo root and confirm each result before finishing.

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT edit any Go file, any test file, or anything under `helm/` — this prompt is documentation and CHANGELOG only.
- Do NOT describe behaviour the code does not implement. In particular: the guard does not repair, restore, or delete quarantined content, there is no quarantine-size cap or retention policy, and there is no per-repo metric label.
- Do NOT add a `repo` label to any documented expression — one pod serves one repo.
- The documented category list MUST match `pkg/metrics/metrics.go` exactly: `yaml_parse_failed`, `no_frontmatter`, `write_failed`, `git_add_failed`, `unsafe_path`, `quarantine_io_failed`, `nested_source`.
- The documented gauge name (`git_rest_quarantined_backlog`), alert name (`VaultObsidian<Realm>QuarantineBacklog`), expression, window (`1h`), and severity (`warning`) MUST match the implementation and the chart template.
- `docs/verifying-specs.md` MUST end up free of `kubectlquant`, `trading-dev`, `trading-prod`, and `shared/base`. `vault-obsidian-trading` is NOT stale — it is a live StatefulSet and stays.
- Paths inside `docs/verifying-specs.md` that describe the operator's machine (e.g. `~/Documents/workspaces/nuke`) are document content — keep them as content and do NOT try to read them from this container.
- Keep the `## Unreleased` CHANGELOG section singular: append or correct in place, never create a second heading.
- `make precommit` from the repo root MUST exit 0.
- The container's `.git` is masked: do not run `git diff`, `git log`, or `git status` against the project repo as verification.
</constraints>

<verification>
Run from the repo root.

```bash
make precommit
make test
```
Both must exit 0 (`make precommit` runs the test target, so `make test` is the explicit second reading of the same suite).

`docs/deployment.md` checks:

```bash
grep -n 'nested_source' docs/deployment.md                      # ≥1 match
grep -n 'quarantine_io_failed' docs/deployment.md               # ≥1 match
grep -n 'git_rest_quarantined_backlog' docs/deployment.md       # ≥1 match
grep -n 'QuarantineBacklog' docs/deployment.md                  # ≥1 match
grep -n 'git_rest_quarantined_files_total' docs/deployment.md   # ≥1 match (the event-vs-backlog contrast)
grep -n 'To drain' docs/deployment.md                          # ≥1 match (the drain procedure)
! grep -q 'the puller aborts the merge' docs/deployment.md      # the stale claim is gone (present at deployment.md:172 before the edit)
! grep -q 'trading/vault' docs/deployment.md                    # the retired manifest path is gone (line 46 before the edit)
! grep -q 'trading-agent-trade-analysis' docs/deployment.md     # its link target is gone too
```

`docs/verifying-specs.md` checks (absence assertions use `! grep -q` — `grep -c` exits 1 when the count is 0 and would report a false failure):

```bash
! grep -q 'kubectlquant' docs/verifying-specs.md
! grep -q 'trading-dev\|trading-prod' docs/verifying-specs.md
! grep -q 'shared/base' docs/verifying-specs.md
grep -c 'vault-obsidian-trading' docs/verifying-specs.md    # 1 or more — the vault still runs (nuke values-dev/prod)
! grep -q 'trading/vault' docs/verifying-specs.md
! grep -q 'vault/obsidian-' docs/verifying-specs.md
grep -n 'kubectlnukedev' docs/verifying-specs.md    # ≥1 match
grep -n 'kubectlnukeprod' docs/verifying-specs.md   # ≥1 match
grep -n 'nuke' docs/verifying-specs.md              # ≥1 match
grep -n 'vault-obsidian-personal' docs/verifying-specs.md   # ≥1 match
grep -n 'vault-obsidian-openclaw' docs/verifying-specs.md   # ≥1 match
```

CHANGELOG checks:

```bash
grep -c '^## Unreleased' CHANGELOG.md                       # must print 1
grep -n 'nested_source' CHANGELOG.md                        # the nesting-guard bullet
grep -n 'git_rest_quarantined_backlog' CHANGELOG.md         # the gauge bullet
grep -n 'QuarantineBacklog' CHANGELOG.md                    # the alert bullet
grep -n 'quarantine' CHANGELOG.md                           # spec 013 AC20
grep -n '^## v0.25.4' CHANGELOG.md                          # the previous release heading is still present
```

Self-check before finishing: re-run `make precommit`, then walk each requirement above against the change — the deployment guide matches the implemented behaviour and the real category list, the gauge and alert are documented with the exact names and window, the verification guide no longer points at the retired deploy path, and the release notes carry one bullet per change.
</verification>
