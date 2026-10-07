---
status: approved
spec: [015-bug-pull-wedges-on-modify-delete-conflict]
created: "2026-10-07T18:52:08Z"
queued: "2026-10-07T19:36:29Z"
branch: dark-factory/bug-pull-wedges-on-modify-delete-conflict
---

# Document the modify/delete and upstream-drain replay recipe in the verification guide

<summary>
- The next person debugging a wedged puller finds a replayable recipe for the modify/delete shape
- The recipe records the boot-ordering trap, so a replayer does not let the boot path consume the divergence before the puller's first cycle
- The recipe records that the fixture must be built from the worktree holding the fix, not the main checkout on master
- Both fixture shapes are documented: the modify/delete conflict on a canonical path and the upstream drain of a `_conflicts/` entry
- The shape that must stay refused — a `_conflicts/` path both sides changed — is documented alongside them, with its resolver configuration
- The YAML-resolver run is flagged as the discriminating one, so a replayer does not accept a resolution that merely quarantines the path
- Nothing about the code, the metrics or the tests changes
</summary>

<objective>
Make the modify/delete and upstream-drain shapes replayable: add their rung-1 recipe to `docs/verifying-specs.md`, beside spec 014's recipe, carrying the load-bearing boot-ordering constraint, the `REPO_UNDER_TEST` default, both fixture shapes, the negative shape that must stay refused, and the assertions that distinguish a fix from a quarantine.
</objective>

<context>
This is a documentation-only prompt. No Go file, no metric, no test, no spec and no manifest changes.

Read these before editing:

- `docs/verifying-specs.md` — the whole file. Its `## Rung 1: local binary against a temp repo` section holds spec 014's recipe under the heading `### Rung 1 recipe: dirty working tree + moved remote`, which ends with the sentence `Spec 014's `## Reproduction` is the authoritative form of this recipe; the version above is a condensation of it. The rung-2 section below is the next rung for this shape.` The new recipe goes immediately after that paragraph and before `## Rung 2: dev cluster e2e`.
- `specs/in-progress/015-bug-pull-wedges-on-modify-delete-conflict.md` — `## Reproduction` (the authoritative recipes A, B and C, to be condensed), `## Assumptions` (the boot-ordering bullet), `## Acceptance Criteria` (the probes to assert), `## Verification` (why no `git` command belongs in the container rung).
- `pkg/git/git.go` — `resolveConflictMerge` with the empty-conflict-list abort, `classifyConflicts`, `stageUpstreamVersion`, `acceptUpstreamDeletion`, `validateConflictPathsNotNested`, `refreshQuarantinedBacklog` (all shipped by prompts 1 and 2; read them so the prose describes what actually happens).
- `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md` — house style for prose, callouts, tables and code fences.
- `docs/dod.md` — the definition of done.

**Facts to carry into the doc (verified; do not paraphrase the commands or the ports):**

- The shape: a `git merge` that produces `CONFLICT (modify/delete):` is invisible to the conflict-path parser when the parser matches only the content-conflict form, so the conflict list comes back empty, the resolver is never reached, and the pull returns without running `git merge --abort` — the repository is left mid-merge, `/readiness` answers non-200, and every write fails with `Committing is not possible because there are unmerged files`. The quarantine flow manufactures the shape: quarantining a file `git rm`s the original path, so a later upstream edit to that canonical produces exactly this conflict.
- **The boot-ordering constraint is a hard requirement, not an incidental detail.** The binary must boot against a **clean, level clone**, and the divergence must be formed only afterwards, **within one pull interval**. The puller's ticker has no immediate first tick, so the first pull happens one interval after boot; booting with the divergence already present lets the boot path's raw `git pull` consume it and the merge path is never exercised. This is the same trap spec 014's recipe documents, for a different symptom.
- `REPO_UNDER_TEST` must default to the worktree that holds the fix — `REPO_UNDER_TEST="${REPO_UNDER_TEST:-$(git rev-parse --show-toplevel)}"` — because the recipe's step 3 builds `$REPO_UNDER_TEST` and the default resolves to the tree the recipe is invoked from. Building the main checkout on master replays the unfixed binary and fails every assertion falsely.
- Fixture A (modify/delete on a canonical): bare origin `origin.git` on branch `test/repro` with `tasks/doomed.md` tracked at the merge base; a `work` clone; boot the fresh binary FIRST on `:18445` with `-repo="$BASE/work" -pull-interval=10s -v=1`; then advance the remote by **modifying** `tasks/doomed.md` to `line one\nUPSTREAM EDIT\n`; then **delete** it locally with `git rm -q tasks/doomed.md` plus a commit (the commit is required — HEAD must differ from the merge base or the puller takes the fast-forward path and never merges); both steps 4 and 5 must finish inside one pull interval; wait one interval; then read the merge state and readiness.
  - Assertions: `test ! -e "$BASE/work/.git/MERGE_HEAD"` exits 0; `git -C "$BASE/work" status --porcelain` prints 0 lines; `git -C "$BASE/work" show HEAD:tasks/doomed.md` contains `UPSTREAM EDIT`; `git -C "$BASE/work" ls-tree -r --name-only HEAD | grep -c 'tasks/doomed.md'` prints `1`; `git -C "$BASE/work" log -1 --format=%s` matches `^merge: resolved=\[tasks/doomed\.md\] quarantined=\[\]$`; `git -C "$BASE/work" rev-parse HEAD` equals `git -C "$BASE/work" rev-parse origin/"$BRANCH"`.
  - Replay it **twice**: once with `-vault-write=false` (the marker resolver) and once with `-vault-write=true` (the YAML merge resolver). In both runs `find "$BASE/work/_conflicts" -type f 2>/dev/null | wc -l` must print `0`. The YAML-resolver run is the discriminating one: the version git leaves in the tree for a modify/delete conflict carries no conflict markers, so a resolution that merely delegates to the YAML merge resolver quarantines the path instead of restoring it.
- Fixture B (upstream drain of a `_conflicts/` entry): bare origin `origin.git` carrying `_conflicts/25 Tasks/Prev A.1791388434.md` (content `---\ntitle: a\n---\nbody\n`) committed at the merge base; a `work` clone; boot the fresh binary FIRST on `:18446` with `-repo="$BASE/work" -pull-interval=10s -v=1`; then the operator drains the remote by **deleting** that path (`git rm -q "_conflicts/25 Tasks/Prev A.1791388434.md"` plus a commit and push); then the replica makes its **own local change** to the same path and commits, so HEAD diverges from origin; both steps 4 and 5 must finish inside one interval; wait one interval; then read readiness, the metrics and the merge state.
  - Assertions: `test ! -e "$BASE/work/_conflicts/25 Tasks/Prev A.1791388434.md"` exits 0; `git -C "$BASE/work" ls-tree -r --name-only HEAD | grep -c 'Prev A.1791388434'` prints `0`; `git -C "$BASE/work" status --porcelain` prints 0 lines; `git -C "$BASE/work" rev-parse HEAD` equals `git -C "$BASE/work" rev-parse origin/"$BRANCH"`; `curl -s -o /dev/null -w '%{http_code}' http://localhost:18446/readiness` prints `200`; `curl -s http://localhost:18446/metrics | grep '^git_rest_quarantined_backlog'` prints `git_rest_quarantined_backlog 0`; and the no-content-discarded probe `git -C "$BASE/work" merge-base --is-ancestor "$LOCAL_SHA" HEAD` exits 0, because the local commit is a parent of the merge and its content stays reachable in history. `$LOCAL_SHA` is captured as `git rev-parse HEAD` in the work clone immediately after the local commit in step 5.
  - `git_rest_resolver_failures_total{category="nested_source"}` read after the pull must equal the reading taken before it (delta 0) — the drain must not trip the nesting guard.
  - The INFO log line: `$BASE/run.log` contains one record naming `_conflicts/25 Tasks/Prev A.1791388434.md` and containing the substring `upstream deletion`.
- Fixture C (the shape that must stay refused): the same fixture as B, except step 4 **modifies** the `_conflicts/` path on the remote with invalid YAML frontmatter (`---\ntitle: [unclosed\n---\nREMOTE CHANGE\n`) and step 5 modifies it locally with valid frontmatter (`---\ntitle: a\n---\nLOCAL CHANGE\n`) — a content conflict in which the resolver cannot merge the remote side. Run the puller with `-vault-write=true`, the deployed configuration for the personal and openclaw vaults (`values-dev.yaml` sets `writeMode: true`), which selects the YAML merge resolver. Assertions: `/readiness` is non-200; `git_rest_resolver_failures_total{category="nested_source"}` climbs by one per pull interval; no path exists at `_conflicts/_conflicts`; `git -C "$BASE/work" status --porcelain` prints 0 lines (the abort restored the worktree); and the log carries a WARN naming the nested path.
- **Teardown matters.** Fixture A re-runs on the same port as spec 014's recipe (`:18445`), so each run must start from a clean process — a survivor keeps answering the port and returns stale evidence. Use `kill "$(pgrep -f /tmp/git-rest-md-repro-bin)" 2>/dev/null || true` (and the drain equivalent for the `:18446` binary).
- The recipe carries **no** `**Post-Deploy**` marker: it is rung 1 against a locally built binary, matching the `Rung 1` entry in spec 015's `## Verification`. The released-image replay (`docker run` of the built image against fixture B) and the dev/prod cluster rungs stay on the spec's operator ladder and are NOT part of this recipe.
- No `git` command belongs in a container-executable rung: the daemon runs with `hideGit=true`, so an in-container `git log` dies with `fatal: not a git repository` and prints 0 lines — indistinguishable from the `0 lines` a branch-relative assertion expects. Every command in this recipe is operator-side, which is exactly why it lives in the prose of `docs/verifying-specs.md` and not in a prompt's `<verification>` block.

**Sibling prompts (do not do their work):** prompts 1-3 shipped the parser, the empty-conflict-list abort, the modify/delete resolution, the guard narrowing, the drain acceptance, the tests, the spec 013 amendment and the `CHANGELOG.md` entry. Do NOT change any Go file, `mocks/`, `CHANGELOG.md`, `CLAUDE.md`, `specs/` or `helm/` in this prompt.
</context>

<requirements>

## 1. Add the modify/delete and drain rung-1 recipe to `docs/verifying-specs.md`

In the `## Rung 1: local binary against a temp repo` section, immediately after spec 014's recipe (the paragraph ending `Spec 014's `## Reproduction` is the authoritative form of this recipe; the version above is a condensation of it. The rung-2 section below is the next rung for this shape.`) and before the `## Rung 2: dev cluster e2e` heading, add a new subsection titled exactly:

```
### Rung 1 recipe: modify/delete conflict and upstream drain
```

It must contain, in this order:

1. One paragraph stating what the shape is: a `git merge` producing `CONFLICT (modify/delete):` is invisible to a parser that matches only the content-conflict form, so the conflict list comes back empty, the resolver is never reached, and the pull returns without running `git merge --abort` — the repository is left mid-merge, `/readiness` answers non-200, and every write fails with `Committing is not possible because there are unmerged files`. State that the quarantine flow manufactures the shape, because quarantining a file `git rm`s the original path, so a later upstream edit to that canonical produces exactly this conflict. State that the failure is not exotic and that the puller must either resolve or abort — never return mid-merge.
2. A callout, set off as a blockquote or its own short paragraph, stating the ordering constraint as a **hard requirement, not an incidental detail**: the binary boots against a **clean, level clone**, and the divergence is formed only afterwards, **within one pull interval**. Explain why: the puller's ticker has no immediate first tick, so the first pull happens one interval after boot; booting with the divergence already present lets the boot path's raw `git pull` consume it and the merge path is never exercised, so the reproduction passes vacuously. Note that this is the same trap spec 014's recipe documents, for a different symptom.
3. A callout stating that the recipe must be run **from the worktree that holds the fix**, because the build step compiles `$REPO_UNDER_TEST`, whose default is `$(git rev-parse --show-toplevel)`. Building the main checkout on master replays the unfixed binary and fails every assertion falsely.
4. **Fixture A** in a fenced `bash` block, condensed from spec 015's `## Reproduction` fixture A but complete enough to run: `set -e`, the `REPO_UNDER_TEST` default, the bare origin on branch `test/repro` with `tasks/doomed.md` tracked at the merge base, the `work` clone, booting the freshly built binary on `:18445` with `-pull-interval=10s` **before** the divergence exists, then the remote modification, then the local `git rm` plus commit, then a one-interval wait and the merge-state and readiness reads, then teardown. Keep the spec's inline comments that explain why each step is shaped that way — in particular why the local side needs its own commit and why steps 4 and 5 must both finish inside one pull interval.
5. A short list of fixture A's assertions with the exact commands, then a statement that the recipe is replayed **twice** — once with `-vault-write=false` (marker resolver) and once with `-vault-write=true` (YAML merge resolver) — and that `find "$BASE/work/_conflicts" -type f 2>/dev/null | wc -l` must print `0` in both runs. State plainly that the YAML-resolver run is the discriminating one: the version git leaves in the tree for a modify/delete conflict carries no conflict markers, so a resolution that merely delegates to the YAML merge resolver quarantines the path instead of restoring it, and the marker-resolver run alone cannot tell the two apart.
6. **Fixture B** in a fenced `bash` block, condensed from spec 015's `## Reproduction` fixture B: the bare origin carrying `_conflicts/25 Tasks/Prev A.1791388434.md` at the merge base, the `work` clone, booting the binary on `:18446` before the divergence, the operator's remote deletion of that path, the replica's own local edit plus commit with `LOCAL_SHA` captured, the one-interval wait, and the readiness / metrics / merge-state reads. Then a short list of fixture B's assertions with the exact commands, including the `merge-base --is-ancestor "$LOCAL_SHA" HEAD` no-content-discarded probe and the `nested_source` delta-0 read. State that the pre-pull `nested_source` reading must be taken in the same process lifetime as the post-pull one, because the counter is per-process.
7. **Fixture C** as a short subsection: the shape that must stay refused. Describe it as fixture B with the remote side **modifying** the `_conflicts/` path with invalid YAML frontmatter and the local side modifying it with valid frontmatter, run with `-vault-write=true` — the deployed configuration, since `values-dev.yaml` sets `writeMode: true` for the personal and openclaw vaults. List its assertions (non-200 readiness, `nested_source` climbing once per interval, no `_conflicts/_conflicts` path, empty `git status --porcelain`, a WARN naming the nested path) and state that this is the shape the nesting guard exists for, so a recipe run that "fixes" it is a regression, not a pass.
8. A teardown note: fixture A shares `:18445` with spec 014's recipe, so each run must start from a clean process; a survivor keeps answering the port and returns stale evidence. Name the `pgrep -f` kill form.
9. A closing line stating that spec 015's `## Reproduction` is the authoritative form of this recipe, that the recipe carries no `**Post-Deploy**` marker because it is rung 1 against a locally built binary, and that the released-image `docker run` replay plus the dev and prod cluster rungs live on spec 015's operator verification ladder.

Use fenced `bash` blocks for every command list. Use the `${VAR}` brace form in any shell snippet that appends a `:path` or `:refs/...` suffix to a variable — in zsh, `"$VAR:suffix"` is mangled by the `:t` / `:r` modifiers.

Do NOT remove, reword, reorder or condense any existing content in `docs/verifying-specs.md`, including spec 014's recipe and the rung-2 and rung-3 sections.

## 2. Self-check before finishing

Run every command in `<verification>` from the repo root and confirm each result. Then walk each requirement above against the change: the recipe states the boot-ordering constraint as a hard requirement, carries the `REPO_UNDER_TEST` default and its rationale, includes both fixture shapes with their assertion lists, flags the YAML-resolver run as the discriminating one, documents fixture C as the shape that must stay refused, names the shared-port teardown, and points at spec 015's `## Reproduction` as authoritative. Confirm spec 014's recipe and the rung-2 / rung-3 sections are byte-identical to before.

End your final message with the standard dark-factory completion report (the template dark-factory appends to this prompt), then stop. Report `"status":"success"` only if `make precommit` exited 0; report `"partial"` if the docs are complete but `make precommit` failed on an unrelated pre-existing issue; report `"failed"` otherwise. Include the verification command and its exit code.

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- This prompt is docs-only. Do NOT change any Go file, `mocks/`, `pkg/metrics/`, `CLAUDE.md`, `CHANGELOG.md`, `specs/`, `helm/`, `main.go` or `docs/api.md`. The public HTTP contract in `docs/api.md` is frozen by the spec.
- No new file may be created in `docs/` — the recipe goes into the existing `docs/verifying-specs.md`.
- Do NOT remove, reword, reorder or condense spec 014's recipe or the `## Rung 2` / `## Rung 3` sections. The new recipe is ADDED alongside them.
- Do NOT embed any `kubectl*`, `docker`, `make buca`, `dark-factory` or `git push` command in a prompt `<verification>` block — those are operator-only and belong in the documentation prose only, where an operator reads them. This prompt's own `<verification>` uses filesystem checks only.
- The recipe MUST state the boot-ordering constraint as a hard requirement, not as an incidental detail. Replaying it with the divergence already present at boot produces a false negative, because the boot path consumes it before the puller's first cycle.
- The recipe MUST record the `REPO_UNDER_TEST` default and why it matters. Building the main checkout on master replays the unfixed binary.
- The recipe MUST flag the YAML-resolver run as the discriminating one. A resolution that merely delegates to the YAML merge resolver quarantines the path instead of restoring it, so the marker-resolver run alone cannot distinguish a fix from a quarantine.
- The recipe MUST use the `${VAR}` brace form in shell snippets that append `:path` or `:refs/...` to a variable. In zsh, `"$VAR:suffix"` is mangled by the `:t` / `:r` modifiers.
- The recipe MUST document fixture C as the shape that must stay refused, and MUST name the `-vault-write=true` configuration it is observed under.
- The recipe MUST NOT carry a `**Post-Deploy**` marker or describe cluster or `docker run` steps as part of the recipe; those stay on spec 015's operator verification ladder.
- Do NOT assert facts beyond those listed in the context block. In particular do NOT invent a new flag, a new metric, a new endpoint, a different port, a different fixture path, or a rung-3 procedure.
- Markdown MUST stay lint-clean for `trivy fs --scanners secret`: do not paste real key material, tokens or credentials into the docs. The example paths and commands are fine.
- `make precommit` is not required for a docs-only change, because the format / generate / test / lint chain has nothing new to validate; if it is run it must still exit 0. Every other command in `<verification>` must pass.
</constraints>

<verification>
Run from the repo root. All checks are filesystem reads — no git, no docker, no cluster commands.

Recipe present with its heading and ordering constraint (each must print at least one match):

```bash
grep -n 'Rung 1 recipe: modify/delete conflict and upstream drain' docs/verifying-specs.md
grep -n 'clean, level clone' docs/verifying-specs.md
grep -n 'hard requirement' docs/verifying-specs.md
grep -n 'REPO_UNDER_TEST' docs/verifying-specs.md
grep -n 'show-toplevel' docs/verifying-specs.md
grep -n 'no immediate first tick' docs/verifying-specs.md
```

Both fixture shapes and the discriminating run are documented (each must print at least one match):

```bash
grep -n 'tasks/doomed.md' docs/verifying-specs.md
grep -n '18445' docs/verifying-specs.md
grep -n '18446' docs/verifying-specs.md
grep -n 'Prev A.1791388434' docs/verifying-specs.md
grep -n 'vault-write=false' docs/verifying-specs.md
grep -n 'vault-write=true' docs/verifying-specs.md
grep -n 'discriminating' docs/verifying-specs.md
grep -n 'UPSTREAM EDIT' docs/verifying-specs.md
grep -n 'upstream deletion' docs/verifying-specs.md
grep -n 'merge-base --is-ancestor' docs/verifying-specs.md
grep -n 'nested_source' docs/verifying-specs.md
grep -n 'invalid YAML' docs/verifying-specs.md
grep -n 'writeMode: true' docs/verifying-specs.md
grep -n 'pgrep -f' docs/verifying-specs.md
grep -n 'git_rest_quarantined_backlog' docs/verifying-specs.md
grep -n 'Post-Deploy' docs/verifying-specs.md
```

Each must print at least one match.

Brace-form check — the recipe must use the `${VAR}` form, not the zsh-mangled `"$VAR:suffix"` form:

```bash
grep -n '\${REPO_UNDER_TEST}' docs/verifying-specs.md
grep -n '\${LOCAL_SHA}' docs/verifying-specs.md
```

Both must print a match — `${REPO_UNDER_TEST}` is guaranteed by requirement 4 and `${LOCAL_SHA}` by the fixture recipe — and the mangled form must be absent:

```bash
! grep -q '"\$LOCAL_SHA:' docs/verifying-specs.md
```

Existing content must be intact (each must print at least one match):

```bash
grep -n 'Rung 1 recipe: dirty working tree + moved remote' docs/verifying-specs.md
grep -n 'recoverUntracked' docs/verifying-specs.md
grep -n 'Rung 2 recipe: the GKE agent vault' docs/verifying-specs.md
grep -n '## Rung 2: dev cluster e2e' docs/verifying-specs.md
grep -n '## Rung 3: prod cluster e2e' docs/verifying-specs.md
```

The new recipe must sit in the rung-1 section, before the rung-2 heading — check the line ordering directly:

```bash
grep -n 'Rung 1 recipe: modify/delete conflict and upstream drain' docs/verifying-specs.md
grep -n '^## Rung 2: dev cluster e2e' docs/verifying-specs.md
```

The first line number must be smaller than the second.

No Go, spec, changelog or metrics file touched by this prompt (each `grep -n` must print at least one match and exit 0; each `! grep -q` must exit 0; the `ls` must list exactly the four existing docs):

```bash
grep -n 'git_rest_quarantined_backlog' pkg/metrics/metrics.go
grep -n 'merge: resolved=' pkg/git/git.go
! grep -q 'git_rest_modify' pkg/metrics/metrics.go
! grep -q 'git_rest_drain' pkg/metrics/metrics.go
ls docs/
```

The first two prove no Go file changed; the two `!` probes prove no metric was added. This prompt declares no dependency on prompt 3, so it deliberately carries no probe of prompt 3's artifacts (`CHANGELOG.md`'s `## Unreleased`, spec 013's `modify/delete`) — those belong to prompt 3's own verification, and probing them here would fail this prompt through no fault of its own if the batch runs concurrently (`maxContainers: 8`) or prompt 3 fails. Absence is written as `! grep -q`, never as `grep -c` — `grep -c` prints `0` and exits non-zero, so an absence assertion written that way fails the step it was meant to pass. `ls docs/` must list exactly `api.md`, `deployment.md`, `dod.md`, `verifying-specs.md`.

Self-check before finishing: walk each requirement above against the change — the recipe states the boot-ordering constraint as a hard requirement, carries the `REPO_UNDER_TEST` default and its rationale, includes fixtures A and B with their assertion lists, flags the YAML-resolver run as the discriminating one, documents fixture C as the shape that must stay refused under `-vault-write=true`, names the shared-port teardown, and points at spec 015's `## Reproduction` as authoritative; and spec 014's recipe plus the rung-2 / rung-3 sections are unchanged.
</verification>
