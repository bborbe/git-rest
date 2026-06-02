---
status: prompted
tags:
    - dark-factory
    - spec
approved: "2026-06-02T09:23:36Z"
generating: "2026-06-02T09:31:55Z"
prompted: "2026-06-02T09:31:55Z"
branch: dark-factory/yaml-merge-resolver
---

## Summary

- Add a new `YAMLMergeResolver` to git-rest that auto-resolves merge conflicts in markdown files with YAML frontmatter by deep-merging frontmatter keys (theirs wins on overlap) and concatenating bodies.
- Wire it as the resolver for git-rest pods that serve agent vault writes; non-vault pods keep the existing `MarkerResolver` unchanged.
- Selection is per-process via a new `--vault-write` flag / `VAULT_WRITE_MODE` env — one resolver per pod, no per-request routing.
- On any YAML parse failure or missing frontmatter delimiters, the resolver returns the existing `ErrConflictResolutionFailed` so the puller aborts the merge (same blast radius as today).
- Operator can distinguish the new resolver outcomes from existing ones via new label values on the existing `git_rest_merge_outcome_total` and `git_rest_conflict_paths_total` Prometheus counters.

## Problem

git-rest's only conflict resolver today is `MarkerResolver`, which stages conflicted files with the `<<<<<<<` / `=======` / `>>>>>>>` markers in place. For human-touched repos that is fine — the next edit cleans them up. For the agent vault, it is not: the agent controller scans frontmatter and skips any file whose frontmatter is unparseable. Within minutes of [bborbe/agent#14](https://github.com/bborbe/agent/pull/14) (`v0.64.0`, shipped 2026-06-02) instrumenting the controller, the new `agent_controller_vault_scanner_skipped_files_total{reason="duplicate_frontmatter_invalid"}` counter reported 157 skips in prod — direct evidence that conflict markers leaking into vault files silently park agent tasks. The agent needs a resolver that produces a syntactically valid frontmatter on every commit so the controller never silently drops a task.

## Goal

When a git-rest pod is configured for agent vault writes and `git pull` produces a conflict in a markdown file with YAML frontmatter, the merge commit contains valid YAML frontmatter on every conflicted file. When the resolver cannot produce valid YAML (parse failure, missing delimiters), the puller aborts the merge — never commits an invalid file. Non-vault pods are unchanged.

## Non-goals

- Do NOT remove, modify, or deprecate `MarkerResolver` — it remains the default and the right behavior for human-touched repos.
- Do NOT add AI-backed resolvers (`GeminiResolver` etc.) in this spec — deferred to a separate follow-up task.
- Do NOT add `LastWriteWinsResolver` or `DeterministicUUIDResolver` — they remain documented alternatives only.
- Do NOT change the `ConflictResolver` interface signature — must remain `Resolve(ctx context.Context, conflictedPaths []string) error` exactly.
- Do NOT regenerate the counterfeiter mock (interface is unchanged).
- Do NOT add per-request routing between resolvers — selection is per-process only.
- Do NOT add a "disable YAMLMergeResolver" flag on vault-write pods — invariant; if a future consumer demands per-pod variation, that is a separate spec.
- Do NOT add tunables for "prefer ours vs theirs" — invariant; theirs wins on overlap, full stop.
- Do NOT pull in a new YAML library if a project-standard one is already vendored — reuse the in-use lib.

## Desired Behavior

1. A new resolver named `YAMLMergeResolver` exists in the same package as `MarkerResolver` and satisfies the existing `ConflictResolver` interface unchanged.
2. The resolver is selected per-process by a new `--vault-write` flag / `VAULT_WRITE_MODE` env (boolean, default false). When false, `MarkerResolver` is used (unchanged from today). When true, `YAMLMergeResolver` is used.
3. On a conflicted markdown file with valid YAML frontmatter on both sides (ours = local HEAD, theirs = pulled remote), the resolver writes back a single file containing:
   - One frontmatter block (delimited by `---` lines) whose keys are the union of both sides; on overlapping keys, the value from theirs wins.
   - A body which is the body from theirs if non-empty, else the body from ours; if both bodies are non-empty and differ, ours followed by a blank line followed by theirs.
4. After writing, the resolver stages the file with `git add -- <path>` so the puller's subsequent commit picks it up.
5. On any failure — YAML parse error on either side, missing `---` delimiter pair on either side, file-write error, `git add` error — the resolver returns the existing sentinel `ErrConflictResolutionFailed`. The puller's existing behavior (run `git merge --abort`, surface the error) is unchanged.
6. Every conflicted path the resolver touches is counted: success increments the existing merge-resolved metric path; each distinct failure category (`yaml_parse_failed`, `no_frontmatter`, `write_failed`, `git_add_failed`) is observable on `/metrics` via a label on either `git_rest_merge_outcome_total` or a sibling counter — exact label scheme is an implementation detail, but the four outcomes must be distinguishable on `/metrics`.
7. `MarkerResolver` keeps its existing behavior bit-for-bit: same signature, same staging logic, same tests pass.

## Constraints

- The `ConflictResolver` interface in `pkg/git/conflict_resolver.go` is frozen — signature must not change. The counterfeiter directive and generated mock must not change.
- `ErrConflictResolutionFailed` is the only error sentinel the puller knows; the new resolver must reuse it (no new exported error types for failure modes — the metric label carries the category).
- The existing tests for `MarkerResolver` (`pkg/git/conflict_resolver_test.go`) and the puller (`pkg/git/git_test.go`) must continue to pass without edits.
- YAML library: **`gopkg.in/yaml.v3`** is the chosen library. It is currently in `go.mod` as `// indirect` (no `.go` file imports any YAML lib directly today — verified). The implementation MUST promote it to a direct dependency (drop the `// indirect` marker via `go mod tidy` after the first import). Do NOT add any other YAML library; the other yaml-named modules in `go.mod` (`go.yaml.in/yaml/v2`, `go.yaml.in/yaml/v3`, `go.yaml.in/yaml/v4`, `sigs.k8s.io/yaml`) stay indirect and unused by this code.
- Errors via `github.com/bborbe/errors` — no `fmt.Errorf`, no bare `return err`.
- Logging: `glog.Info` calls are `V(n)`-gated; `Warningf` / `Errorf` are unconditional. Each unresolved-conflict path produces at least one `Warningf` line with the path and category.
- Metrics extend the existing `Metrics` interface in `pkg/metrics/metrics.go`. The existing `IncMergeOutcome(result string)` / `IncConflictPaths(n int)` calls are the integration point — extension may add label values or add new counter methods, but must not break existing callers.
- Tests via Ginkgo/Gomega; mocks via counterfeiter (project convention — `make generate` regenerates `mocks/`). NOT needed for this spec since the `ConflictResolver` interface is unchanged; `mocks/conflict_resolver.go` MUST stay byte-identical (asserted in Verification).
- Build via `make precommit` from the repo root (single-module repo).
- Deployment pattern: one git-rest pod per repo; the vault-write pod sets `VAULT_WRITE_MODE=true`. Documented in `docs/deployment.md` (or equivalent existing doc).

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection | Reversibility | Concurrency |
|---|---|---|---|---|---|
| YAML parse fails on ours or theirs | Return `ErrConflictResolutionFailed`; puller runs `git merge --abort`; no file is staged or committed | Operator inspects the file on disk (still in pre-merge state); next pull retries; if persistent, human resolves manually | `git_rest_merge_outcome_total{result="aborted"}` increments; sibling label or counter distinguishes `yaml_parse_failed` from other abort causes; `Warningf` log line names the path | Reversible — no commit was made; merge aborted | Pull loop is single-threaded per pod; a second pull cannot start mid-resolve |
| File has no `---` delimiter pair (not a frontmatter doc) | Same as above; treated as parse failure category `no_frontmatter` | Same as above | Metric label `no_frontmatter`; `Warningf` log line | Reversible | Same |
| `git add` fails after writing merged content | Return `ErrConflictResolutionFailed`; puller aborts | Next pull retries from a clean working tree | Metric label `git_add_failed`; `Warningf` log | Reversible — `merge --abort` resets the working tree | Same |
| Disk write fails (no space, perms) | Return `ErrConflictResolutionFailed`; puller aborts | Operator fixes disk; next pull retries | Metric label `write_failed`; `Warningf` log | Reversible | Same |
| Conflict is in a non-markdown file (binary, code) | Same failure path — `no_frontmatter` category | Operator notices the parked pod via metric; either commits manually or routes that repo to a non-vault-write pod | Metric label `no_frontmatter` | Reversible | Same |
| Pod crash mid-resolve (after write, before `git add`) | On restart, puller runs in a working tree with merge state on disk; existing puller startup behavior handles this (no change in this spec) | Existing puller restart path | Existing pod-restart metrics | Reversible — git's merge state is on-disk and resumable | Single-pod-per-repo deployment means no second writer |
| Operator misconfigures: sets `VAULT_WRITE_MODE=true` on a non-vault repo | Resolver fails on every code/binary conflict with `no_frontmatter`; pod parks merges; visible on `/metrics` | Operator flips the flag back; next pull resumes | `git_rest_merge_outcome_total{result="aborted"}` rises sharply | Reversible | N/A |

## Security / Abuse Cases

- Input path origin: conflicted paths come from `git merge` output, not from HTTP — same trust boundary as `MarkerResolver` today.
- YAML parsing: a malicious remote could craft a YAML payload designed to exhaust memory (billion-laughs-style). The chosen YAML library must be configured to reject excessive nesting / aliasing if the project's existing YAML usage already does so; otherwise document the risk and bound parse with a context deadline (the puller's existing per-pull timeout already bounds total work).
- File-write path: writes go to the path reported by `git merge`, joined under the configured `Repo` root. The resolver must not follow symlinks out of the repo root nor write outside the repo. Document the check; existing `MarkerResolver` does not need this because it only runs `git add`.
- No new HTTP surface, no new user input crosses a trust boundary in this spec.

## Acceptance Criteria

- [ ] `YAMLMergeResolver` exists in `pkg/git/` and satisfies the unchanged `ConflictResolver` interface — evidence: `grep -n 'NewYAMLMergeResolver' pkg/git/yaml_merge_resolver.go` returns ≥1; `go build ./...` exits 0.
- [ ] Constructor is exported and returns the interface (not a concrete struct) — evidence: `grep -E 'func NewYAMLMergeResolver.*ConflictResolver' pkg/git/yaml_merge_resolver.go` returns ≥1.
- [ ] `ConflictResolver` interface signature and counterfeiter directive in `pkg/git/conflict_resolver.go` are byte-identical to before — evidence: `git diff pkg/git/conflict_resolver.go` shows zero lines changed for the interface block and directive line.
- [ ] Deep-merge correctness: given two YAML frontmatters where ours has `{a: 1, b: 2}` and theirs has `{b: 3, c: 4}`, the staged file contains a frontmatter that parses to `{a: 1, b: 3, c: 4}` — evidence: Ginkgo test case asserts the parsed map equality.
- [ ] Body handling: ours body `"X"` + theirs body `"Y"` → staged body is `"X\n\nY"` — evidence: Ginkgo assertion on file contents.
- [ ] Body handling: ours body `"X"` + theirs body `""` → staged body is `"X"`; ours body `""` + theirs body `"Y"` → staged body is `"Y"` — evidence: two Ginkgo cases.
- [ ] YAML parse failure on either side → resolver returns `ErrConflictResolutionFailed`; no file modification beyond what git's merge already wrote; no `git add` runs — evidence: Ginkgo case using a `FakeMetrics` and a stubbed `exec`-equivalent confirms the error type via `errors.Is` and asserts the file content is unchanged.
- [ ] Missing `---` delimiter pair on either side → same `ErrConflictResolutionFailed` return with category `no_frontmatter` — evidence: Ginkgo case; metric assertion on the fake.
- [ ] Each of the four failure categories (`yaml_parse_failed`, `no_frontmatter`, `write_failed`, `git_add_failed`) is observable as a distinct label value or counter on `/metrics` — evidence: scrape `/metrics` in an integration test (or unit test against the metrics interface) and assert each label appears with a count column.
- [ ] Selection wiring: when `VAULT_WRITE_MODE=true` (or `--vault-write`), the constructed git client uses `YAMLMergeResolver`; when unset/false, it uses `MarkerResolver` — evidence: unit test on the application setup path asserts the constructed resolver's concrete type for both cases.
- [ ] `MarkerResolver` regression: all existing `pkg/git/conflict_resolver_test.go` and `pkg/git/git_test.go` test cases pass unedited — evidence: `git diff pkg/git/conflict_resolver_test.go pkg/git/git_test.go` shows zero changes; `make test` exits 0.
- [ ] `make precommit` exits 0 from the repo root — evidence: exit code 0.
- [ ] `CHANGELOG.md` has a new entry under `## Unreleased` describing the new resolver and the new flag/env — evidence: `grep -n 'YAMLMergeResolver' CHANGELOG.md` returns a line inside the `## Unreleased` section.
- [ ] Successful merge increments the result counter: a Ginkgo case running the happy-path merge through `YAMLMergeResolver.Resolve` against a `FakeMetrics` asserts `IncMergeOutcome("resolved")` is called exactly once per conflicted path — evidence: counterfeiter call-count assertion on the fake.
- [ ] Metrics registry exposes the four failure-category labels pre-initialised to zero before any conflict occurs — evidence: a unit test gathers `prometheus.DefaultGatherer.Gather()` after `metrics.New()` and asserts each of `yaml_parse_failed`, `no_frontmatter`, `write_failed`, `git_add_failed` is present with value `0`. (Replaces the original `curl /metrics` post-deploy check with an equivalent in-process verification — simpler, faster, equally informative.)

Scenario coverage — none. All ACs are reachable via unit + integration tests in `pkg/git/`. No real-cluster behavior is load-bearing for this spec; the post-deploy AC is a metric-shape check, not a scenario.

## Verification

```
make precommit
make test
grep -n 'NewYAMLMergeResolver' pkg/git/yaml_merge_resolver.go
grep -E 'func NewYAMLMergeResolver.*ConflictResolver' pkg/git/yaml_merge_resolver.go
git diff pkg/git/conflict_resolver.go pkg/git/conflict_resolver_test.go pkg/git/git_test.go mocks/conflict_resolver.go
grep -n 'YAMLMergeResolver' CHANGELOG.md
```

All commands exit 0 (the `grep` lines return ≥1 match; the `git diff` shows zero changes for the listed files).

## Do-Nothing Option

If we ship nothing, every git-rest pull conflict in the agent vault produces a file with `<<<<<<<` markers in its frontmatter, the agent controller's vault scanner skips that file, and the task silently parks. Empirically: 157 such skips in the first minutes after the controller's `duplicate_frontmatter_invalid` counter went live in prod. The do-nothing path means parked tasks continue invisibly except via that one counter. Not acceptable for agent vault traffic; acceptable (and current) for human-touched repos.
