---
status: draft
spec: [024-bug-body-conflict-hunks-quarantine]
created: "2026-11-02T18:50:00Z"
branch: dark-factory/bug-body-conflict-hunks-quarantine
---

# Lock the regression surface, document the body rule, and record the change

<summary>
- The committed body-conflict reproduction stays byte-identical: the fix only adds specs, it never rewrites the four committed ones.
- No test file outside `pkg/git/yaml_merge_resolver_test.go` is touched, and the full suite stays green with the spec 016 scalar cases unedited.
- The deployment guide's vault-write bullet documents the body rule in behavioural terms beside the existing scalar-frontmatter sentence.
- The operator-facing text describes the concatenation (local body, a blank line, then the incoming body) and never names an internal helper.
- The changelog carries one unreleased bullet naming the body conflict rule.
- No Go behaviour changes in this prompt.
</summary>

<objective>
Freeze the regression surface and make the rule durable: verify the committed reproduction and the untouched suite, add the body rule to `docs/deployment.md` in behavioural terms, and add a `## Unreleased` changelog bullet naming the body rule. This is a docs-and-lock prompt — no Go behaviour changes.
</objective>

<context>
There is no repo-root `CLAUDE.md` in this worktree — do not look for one. The project conventions are restated in `<constraints>` below.

Read these coding-plugin guides before implementing (paths inside the YOLO container):

- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — `## Unreleased` heading and `fix:` / `feat:` prefix rules
- `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md` — durable-doc expectations and prose style

Files to read in full before editing (anchor by symbol name, not line number):

- `docs/deployment.md` — the `**Vault-write mode**` bullet under `## Operational notes`. It already documents the deep-merge path and the scalar-frontmatter rule and ends with the `git_rest_resolver_failures_total{category}` sentence. The body rule goes into this bullet, after the scalar-frontmatter sentence and before the `On YAML parse failure…` sentence.
- `CHANGELOG.md` — the newest heading today is `## v0.39.0`; there is no `## Unreleased` section.
- `pkg/git/yaml_merge_resolver_test.go` — the committed `Describe("Resolve body-only conflict regions")` block and prompt 1's `Describe("Body-only conflict regions: extended", …)` block.

**Verified facts — do not re-derive, do not contradict:**

- The committed `Describe("Resolve body-only conflict regions")` block holds four `It`s named `AC-bodyonly-ours-empty`, `AC-bodyonly-theirs-empty`, `AC-bodyonly-both-differ`, `AC-bodyonly-nonmarkdown`. Their assertions are the acceptance evidence for spec ACs 1-3; they must stay byte-identical.
- The spec's AC 12 requires two host-side git checks: `git diff --name-only 69008a1 -- 'pkg/git/*_test.go'` must print exactly `pkg/git/yaml_merge_resolver_test.go`, and `git diff --numstat 69008a1 -- pkg/git/yaml_merge_resolver_test.go | awk '{s+=$2} END{print s+0}'` must print `0`. A ref-less `git diff --stat` is NOT usable (it compares working tree to index and is empty once the work is committed), and `grep -c '^-'` is NOT usable (`git diff` always emits the `--- a/<path>` header). These commands cannot run inside this container because `.git` is masked; the container-executable equivalents in `<verification>` assert the same outcome from file content and a green suite. <!-- OPEN QUESTION (audit): AC 12's git-diff evidence is host-side. Confirm the daemon/operator captures it on the host; the container cannot. -->
- `docs/deployment.md` currently contains no line with `combineBodies`, and the acceptance check requires it to stay absent.
- `CHANGELOG.md`'s `## Unreleased` section does not exist yet; add it at the top of the version list, immediately after the preamble and above `## v0.39.0`.
- This is a bug fix, so the changelog prefix is `fix:`.
</context>

<requirements>

## 1. Regression lock (no edits)

Do NOT edit any `pkg/git/*_test.go` file in this prompt. In particular:

- The committed `Describe("Resolve body-only conflict regions")` block stays byte-identical: zero lines removed or rewritten. The fix may only add specs.
- `conflict_resolver_test.go`, `git_test.go`, `yaml_merge_scalar_internal_test.go`, `frontmatter_internal_test.go`, `task_phase_test.go`, `resolve_conflict_merge_test.go` and `vault_api_merge_resolver_test.go` are all untouched.
- `make test` exits 0 with the spec 016 scalar cases passing unedited.

The spec's AC 12 evidence, to be captured on the host by the daemon/operator (the YOLO container masks `.git`):

```bash
git diff --name-only 69008a1 -- 'pkg/git/*_test.go'
# must print exactly: pkg/git/yaml_merge_resolver_test.go

git diff --numstat 69008a1 -- pkg/git/yaml_merge_resolver_test.go | awk '{s+=$2} END{print s+0}'
# must print: 0
```

Do not attempt to run these in the container, and do not edit tests to make a check pass.

## 2. Deployment guide

In `docs/deployment.md`, extend the `**Vault-write mode**` bullet. That bullet already contains the sentence `Every other shape — non-scalar hunks, hunks outside the frontmatter, one-sided keys, duplicate keys — keeps the quarantine path described below.` The clause `hunks outside the frontmatter` now contradicts the body rule, because a body-only hunk is exactly that. **First** replace `non-scalar hunks, hunks outside the frontmatter` with `non-scalar hunks that cross the frontmatter boundary`, **then** insert one sentence immediately after that amended sentence and before `On YAML parse failure or missing frontmatter delimiters, …`:

> A **body conflict** — one whose hunks lie entirely below the frontmatter's closing `---` — is also auto-resolved in place instead of quarantined: an empty side is replaced by the other side verbatim, identical sides are kept once, and two differing non-empty sides are joined as the local body, a blank line, then the incoming (theirs) body. Every line outside the hunks, including the whole frontmatter block and both `---` delimiters, is preserved, and `git_rest_body_merged_total` increments once per resolved path.

Requirements on the text:

- It must contain the exact phrase `body conflict`.
- It must contain the word `blank line` and the word `theirs`.
- It must NOT contain `combineBodies` or any other internal helper name.
- Do not create a new heading and do not touch the `**Conflict handling**` bullet above it.
- Do not describe the deep-merge path or the scalar-frontmatter rule as changed.

## 3. Changelog

Add a `## Unreleased` section at the top of the version list (immediately after the preamble, above `## v0.39.0`) containing exactly one bullet:

```
- fix: Auto-resolve a merge conflict confined to a markdown file's body. A body conflict — one whose hunks lie entirely below the frontmatter's closing `---` — now resolves in place instead of quarantining: an empty side takes the other side verbatim, identical sides are kept once, and two differing non-empty sides are joined as the local body, a blank line, then the incoming body, with every line outside the hunks preserved. Each resolved path increments `git_rest_body_merged_total`. Conflicts crossing the frontmatter boundary, non-markdown paths and markdown files without frontmatter keep quarantining.
```

Do not touch any released section, and do not add a second bullet. The bullet must contain the phrase `body conflict` (the acceptance check greps for it).

## 4. Self-check before finishing

Walk each requirement: the committed block is intact (the four `It` names are still present with their original assertion text); no other test file was edited; the deployment guide documents the concatenation in behavioural terms with no helper name; the changelog has one unreleased `fix:` bullet naming the body conflict rule; `make test` and `make precommit` pass.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- This prompt makes NO Go behaviour changes: do not edit `pkg/git/yaml_merge_resolver.go`, `pkg/metrics/metrics.go`, `mocks/`, or any non-test Go file.
- Do NOT edit any `pkg/git/*_test.go` file. The committed `Describe("Resolve body-only conflict regions")` block stays byte-identical.
- Do NOT change the `ConflictResolver` interface or its mock.
- Do NOT change the existing `git_rest_resolver_failures_total` categories or their pre-initialisation, and do NOT change `git_rest_autoresolved_total`.
- Do NOT change the `merge: resolved=[…] quarantined=[…]` commit-message format or any existing resolver log line.
- The deployment-guide edit is confined to the `**Vault-write mode**` bullet; do not create a heading and do not touch the `**Conflict handling**` bullet.
- The deployment guide must not name `combineBodies` or any internal helper.
- The changelog gets exactly one new `## Unreleased` bullet; released sections stay untouched.
- Do NOT edit `specs/`.
- Do NOT add a scenario file under `scenarios/`.
- Do NOT run `go mod vendor`, and do NOT write `-mod=vendor` in any command — this repo does not commit `vendor/`.
- `make precommit` is the gate.
</constraints>

<verification>
Run from the repo root. The daemon runs with `hideGit=true`, which masks `.git` inside the container — bare `git` commands die with `fatal: not a git repository`, so none appear in this block. `ROOTDIR=/workspace` is the dark-factory convention for that case; this repo's `Makefile` does not itself reference `ROOTDIR`, so the prefix is inert here and harmless.

```bash
ROOTDIR=/workspace make precommit
```

Must exit 0.

The committed reproduction block is intact (each `grep -n` must print exactly one match, and the assertion lines must still be present):

```bash
grep -n 'AC-bodyonly-ours-empty' pkg/git/yaml_merge_resolver_test.go
grep -n 'AC-bodyonly-theirs-empty' pkg/git/yaml_merge_resolver_test.go
grep -n 'AC-bodyonly-both-differ' pkg/git/yaml_merge_resolver_test.go
grep -n 'AC-bodyonly-nonmarkdown' pkg/git/yaml_merge_resolver_test.go
grep -n 'prefix + theirsBody + "\\n" + suffix' pkg/git/yaml_merge_resolver_test.go
grep -n 'prefix + oursBody + "\\n\\n" + theirsBody + "\\n" + suffix' pkg/git/yaml_merge_resolver_test.go
```

The deployment guide documents the rule (each `grep -n` must print at least one match; each `grep -c` must print a value ≥ 1):

```bash
grep -n 'body conflict' docs/deployment.md
sed -n '/body conflict/,/^$/p' docs/deployment.md | grep -c 'blank line'
sed -n '/body conflict/,/^$/p' docs/deployment.md | grep -c 'theirs'
! grep -q 'combineBodies' docs/deployment.md
```

The changelog section is new and is the topmost version heading:

```bash
grep -n '^## Unreleased' CHANGELOG.md
sed -n '/^## Unreleased/,/^## v/p' CHANGELOG.md | grep -c 'body conflict'
awk '/^## /{sec=$0} /^## Unreleased/{print "first section: " sec; exit}' CHANGELOG.md
```

The first prints one match; the second prints a value ≥ 1; the third prints `first section: ## Unreleased`.

Frozen-surface checks (each `grep -n` must print at least one match):

```bash
grep -n 'git_rest_body_merged_total' docs/deployment.md
grep -n 'ConflictResolver interface' pkg/git/conflict_resolver.go
ls mocks/conflict_resolver.go
```

Absence is written as `! grep -q`, never as `grep -c` — `grep -c` prints `0` and exits non-zero, so an absence assertion written that way fails the step it was meant to pass.

Self-check before finishing: re-run `ROOTDIR=/workspace make precommit`, then walk each requirement against the change.
</verification>
