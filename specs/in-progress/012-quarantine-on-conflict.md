---
status: prompted
tags:
    - dark-factory
    - spec
approved: "2026-06-02T19:40:49Z"
generating: "2026-06-02T19:53:08Z"
prompted: "2026-06-02T20:27:38Z"
branch: dark-factory/quarantine-on-conflict
---

## Summary

- When the conflict resolver fails on a single file mid-merge, git-rest moves only that file aside and continues the merge — instead of aborting the entire pull and wedging the pod.
- The offending file is preserved on disk under a `_conflicts/` directory with a timestamp suffix so an operator (or future repair job) can inspect or replay it.
- Every other conflicted file in the same merge goes through the resolver normally; the merge produces a single commit that includes the quarantine move.
- A new Prometheus counter exposes how often this happens; a WARN log line names each quarantined path with the resolver error.
- One bad markdown file no longer takes the pod (and every consumer of it) offline for hours.

## Problem

On 2026-06-02 a single corrupt-frontmatter file in the Obsidian vault caused the `vault-obsidian-openclaw-0` git-rest pod to flip Ready=False for 3.5 hours: the `YAMLMergeResolver` failed on that one file, the puller ran `git merge --abort`, and every subsequent pull retried the same merge and hit the same file. While the pod was unready, the gateway returned 503 for all writes — blocking PR-reviewer + agent-task writes fleet-wide. The blast radius of one bad file is currently "every write to that repo until a human intervenes". The incident was discovered during a vault-cli#7 PR review attempt; the broken file was identified hours after it first jammed the merge.

## Goal

When the resolver fails on a strict subset of the conflicted files in a single merge, git-rest commits the merge anyway: the failing files are moved out of the working tree into a quarantine directory, the rest of the merge is resolved and committed normally, the pod stays Ready, and pulls keep flowing. The pod only wedges if either (a) every conflicted file in a merge fails the resolver or (b) git itself returns an error unrelated to the resolver.

## Non-goals

- Do NOT auto-repair quarantined files — operator (or a follow-up task) handles repair; quarantine only preserves and isolates.
- Do NOT change behavior for non-resolver git errors (push failures, network errors, `git merge` returning a non-conflict error). Only the conflict-resolver failure branch is affected.
- Do NOT change the `ConflictResolver` interface signature — must remain `Resolve(ctx context.Context, conflictedPaths []string) error` exactly.
- Do NOT change `MarkerResolver` or `YAMLMergeResolver` internals — quarantine wraps the resolver call from the caller side (in `resolveConflictMerge`).
- Do NOT add Prometheus alerts on the new counter in this spec — sibling task `[[VaultObsidianNotReady Prometheus alert]]` covers alerting.
- Do NOT add a "disable quarantine" flag — invariant; if a future consumer demands variation, that is a separate spec. An escape hatch on the Goal is itself a regression of the Goal.
- Do NOT add a quarantine-size cap or retention policy in this spec — preservation is best-effort, growth is bounded by the rate of corrupt files (currently rare; quarantine count is observable on the new counter).
- Do NOT add a per-repo label on the new counter — git-rest deploys one pod per repo, so the label would have cardinality 1; the pod identity already carries the repo via Kubernetes labels at scrape time.

## Desired Behavior

1. When `resolveConflictMerge` calls `g.resolver.Resolve(ctx, conflictPaths)` and gets a non-nil error, the existing behavior (`git merge --abort` + return `ErrConflictResolutionFailed`) is replaced with a per-file retry loop.
2. The per-file loop calls `g.resolver.Resolve(ctx, []string{path})` once per conflicted path. For each path:
   - On `nil`: file is staged by the resolver (existing contract), nothing more to do.
   - On non-nil error: the file is **quarantined** — moved within the working tree from `<path>` to `_conflicts/<path>.<unix-timestamp-seconds>.md`, where the directory tree under `_conflicts/` mirrors the original path, and the timestamp is inserted before the final `.md` extension. If the original path does not end in `.md`, the timestamp is appended after the full original filename with a `.quarantined` suffix (so `foo.bin` becomes `_conflicts/foo.bin.<ts>.quarantined`). The move uses `git mv` so the move is staged.
3. After the per-file loop, if at least one file was resolved successfully OR at least one file was quarantined, the merge proceeds to commit. The commit message follows the fixed format `merge: resolved=[<comma-separated-paths>] quarantined=[<comma-separated-paths>]` — both lists are repo-relative paths, sorted alphabetically, and either list may be empty (rendered as `[]`).
4. If **every** conflicted path failed quarantine itself (e.g. `git mv` errored on all of them), the existing abort path runs: `git merge --abort` + return `ErrConflictResolutionFailed`. The pod can still wedge in this pathological case — but a single bad file no longer triggers it.
5. Every quarantined path produces:
   - A `slog.WarnContext` log line containing the original path, the quarantine destination path, and the resolver error message.
   - One increment of a new Prometheus counter `git_rest_quarantined_files_total` (unlabeled, single counter — see Non-goals on the `repo` label).
6. The new counter is pre-initialised to `0` at process start so `/metrics` exposes the time series before any quarantine event occurs.
7. The puller's `runOnce` is unchanged — it continues to wrap and log `Pull` errors as today. The change is purely inside `resolveConflictMerge`.
8. The pod's `/readiness` endpoint returns 200 after a pull that quarantined files (because the working tree is clean — the quarantine move was committed), restoring write availability.

## Constraints

- The `ConflictResolver` interface in `pkg/git/conflict_resolver.go` is frozen — signature must not change. The counterfeiter directive and generated `mocks/conflict_resolver.go` must not change.
- `ErrConflictResolutionFailed` is reused only for the all-files-failed-quarantine path (Desired Behavior #4). The common per-file failure does NOT surface as an error to the puller.
- Errors via `github.com/bborbe/errors` — no `fmt.Errorf`, no bare `return err`.
- Logging via `log/slog` (matches the `slog.InfoContext` / `slog.WarnContext` calls already in `git.go` and `puller.go`).
- Metrics: new counter registered via `pkg/metrics/metrics.go` following the same pattern as `ConflictPathsTotal` (unlabeled `prometheus.NewCounter`); registered in `init()`. See `~/Documents/workspaces/coding/docs/go-prometheus-metrics-guide.md` for the `Counter` vs `CounterVec` rule and pre-initialisation requirement (unlabeled `Counter` is automatically initialised to 0 at registration; no `.Add(0)` needed). New label value `git_mv_failed` must be added to `ResolverFailuresTotal{category}` and pre-initialised at `init()` time alongside the existing label values.
- Commit message format is fixed: `merge: resolved=[<comma-separated-paths>] quarantined=[<comma-separated-paths>]`. Paths are repo-relative, sorted alphabetically, comma-separated with no spaces. Empty lists render as `[]` (e.g. all files quarantined: `merge: resolved=[] quarantined=[a.md,b.md]`). The format must be greppable by operators inspecting `git log` for quarantine activity.
- Tests via Ginkgo v2 + Gomega; suite file per package; no `t.Run` tables. See `~/Documents/workspaces/coding/docs/go-testing-guide.md`.
- The "1 corrupt + 10 clean" test is an integration test: in-process real git (via `os/exec`), real temp working tree, no network. Same `*_test.go` file as the existing `git_test.go` resides in. No build tags. See `~/Documents/workspaces/coding/docs/go-test-types-guide.md`.
- Build via `make precommit` from the repo root (single-module repo).
- The `_conflicts/` directory name MUST NOT collide with any existing top-level directory in any served repo. If a repo already contains `_conflicts/`, the implementation MUST surface this on first conflict by logging an ERROR and falling back to the existing abort path. (Detection: `os.Stat` on the directory at quarantine time; if it exists and is not a directory, abort; if it exists and is a directory, use it.)
- Existing `pkg/git/conflict_resolver_test.go` and the unchanged subset of `pkg/git/git_test.go` must continue to pass without edits.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection | Reversibility | Concurrency |
|---|---|---|---|---|---|
| Resolver fails on 1 of N conflicted files | Quarantine the 1 file, resolve the other N-1, commit, push | None needed; pull succeeded | `git_rest_quarantined_files_total` increments by 1; WARN log line names path; commit message lists quarantined path | Reversible — operator can `git mv` the file back from `_conflicts/` after repair | Pull loop is single-threaded per pod; no second pull mid-resolve |
| Resolver fails on all N conflicted files (quarantine path fires for every file) | All N quarantined, commit includes all moves, push, pull succeeds | None needed | Counter increments by N; one WARN per path | Reversible | Same |
| `git mv` fails during quarantine (e.g. destination collides, disk full) | That single path falls back to the existing per-file outcome: counted as a failed quarantine attempt. If at least one other path succeeded (resolved or quarantined) the merge still commits. If every path failed both resolve AND quarantine: run `git merge --abort` + return `ErrConflictResolutionFailed` (existing wedge behavior preserved as the floor) | Operator inspects disk; clears collision or frees space | `git_rest_resolver_failures_total{category="git_mv_failed"}` increments (new label value — `git_mv_failed` is distinct from `git_add_failed` because quarantine uses `git mv` whereas the existing label tracks `git add` failures); ERROR log line names path and `git mv` stderr | Reversible | Same |
| Working tree already contains a `_conflicts/` path that is a file, not a directory | Treat as catastrophic config error: log ERROR, run `git merge --abort`, return `ErrConflictResolutionFailed` (no silent overwrite) | Operator renames the colliding file in the repo | ERROR log; `git_rest_merge_outcome_total{result="aborted"}` increments | Reversible | N/A |
| `git commit` fails after a successful per-file loop | Run `git merge --abort`, return wrapped commit error (unchanged from today's behavior at the same site) | Next pull retries | `git_rest_git_operation_errors_total{operation="merge"}` increments (unchanged) | Reversible | Same |
| Pod crash between `git mv` and `git commit` | On restart, the existing puller startup path (`recoverAbandonedMerge`) detects `MERGE_HEAD` and runs `git merge --abort`, which undoes the staged `git mv`. The next pull retries from a clean tree. | Existing puller restart path | Existing pod-restart metrics | Reversible — git's merge state is on-disk and resumable | Single-pod-per-repo deployment ensures no concurrent writer |
| Timestamp collision: two quarantines of the same path in the same second | The second quarantine's `git mv` fails with "destination exists"; falls back to the "git mv fails" row above | Operator removes one of the duplicates | ERROR log includes both paths | Reversible | Single pull loop guarantees serial execution; collision only possible if operator manually quarantines too |
| Quarantine destination path exceeds filesystem max path length | `git mv` fails; falls back to the "git mv fails" row above | Operator shortens or moves the file | Same | Reversible | Same |

## Security / Abuse Cases

- Input path origin: conflicted paths come from `git merge` output, not from HTTP — same trust boundary as the resolver today. The quarantine destination is computed from this path by joining with `_conflicts/` and appending a timestamp suffix.
- Path traversal: a conflicted path of the form `../escape.md` could in theory point outside the repo. `git merge` does not produce such paths from honest history, but a crafted remote commit could. The implementation MUST reject any conflicted path that does not stay within the repo root after `filepath.Clean` joining (returns `git_rest_resolver_failures_total{category="unsafe_path"}` — label value already pre-initialised in `metrics.go`). Such paths are neither resolved nor quarantined; they cause an abort.
- The `_conflicts/` directory is inside the repo root; quarantined content is committed to git history and pushed to the remote. This is intentional — the file is preserved for an operator to inspect — but operators must be aware that quarantined corrupt frontmatter is now part of the remote repo's history.
- No new HTTP surface, no new user input crosses a trust boundary in this spec.

## Acceptance Criteria

- [ ] Integration test in `pkg/git/`: fixture creates a working tree with 1 file whose frontmatter is invalid YAML and 10 markdown files with valid frontmatter; both sides of a merge diverge on all 11 files; `g.Pull(ctx)` returns nil — evidence: Ginkgo assertion `Expect(err).To(BeNil())`.
- [ ] After the same test, the corrupt file no longer exists at its original path and a single file matching `_conflicts/<original-path>.<digits>.md` exists in the working tree — evidence: `Eventually` is not needed (synchronous); Gomega assertion uses `filepath.Glob` to find exactly one matching path; `os.Stat` on the original path returns `IsNotExist`.
- [ ] After the same test, all 10 clean files exist at their original paths with content matching the resolver's chosen winner (matches `YAMLMergeResolver` semantics: theirs wins on frontmatter key overlap, body concatenated per existing rules) — evidence: read each file, parse frontmatter, assert against the expected merged result.
- [ ] After the same test, the new counter `git_rest_quarantined_files_total` has value exactly 1 — evidence: scrape `prometheus.DefaultGatherer.Gather()` in-process and assert the counter value.
- [ ] After the same test, exactly one merge commit was added to the branch (compared to pre-test `HEAD`) and `git status --porcelain` returns empty (working tree clean) — evidence: `git rev-list --count HEAD ^<pre-test-sha>` returns `1`; `git status --porcelain` output length is 0.
- [ ] During the test, at least one WARN log line is emitted containing the original path of the quarantined file and the substring `quarantine` — evidence: tests capture `slog` output via a test handler and assert on the log record.
- [ ] The new counter is registered in `pkg/metrics/metrics.go` and appears in `/metrics` output with value `0` before any conflict — evidence: in-process gather after `init()` finds `git_rest_quarantined_files_total` with value 0.
- [ ] Pathological case (all-files-fail-quarantine): when `git mv` is forced to fail for every conflicted path (e.g. via a fixture where `_conflicts/` exists as a regular file), `g.Pull(ctx)` returns a non-nil error wrapping `ErrConflictResolutionFailed` and the working tree is clean (merge was aborted) — evidence: Ginkgo case using `errors.Is(err, ErrConflictResolutionFailed)`; `git status --porcelain` empty.
- [ ] `ConflictResolver` interface signature and counterfeiter directive in `pkg/git/conflict_resolver.go` are byte-identical to before — evidence: `git diff pkg/git/conflict_resolver.go mocks/conflict_resolver.go` shows zero changes.
- [ ] Existing `MarkerResolver` and `YAMLMergeResolver` tests pass unedited — evidence: `git diff pkg/git/conflict_resolver_test.go pkg/git/yaml_merge_resolver_test.go` shows zero changes; `make test` exits 0.
- [ ] Unsafe path is rejected: a conflicted path that resolves outside the repo root after `filepath.Clean` triggers `git_rest_resolver_failures_total{category="unsafe_path"}` increment, is not quarantined, and contributes to the abort path — evidence: Ginkgo case asserts counter increment, that `os.Stat` of the path outside the repo returns `IsNotExist`, AND `filepath.Glob("_conflicts/**")` returns no match for any path mentioning the escape attempt (defends against a buggy impl that `filepath.Clean`s the path back inside `_conflicts/` without re-validating containment).
- [ ] Commit message follows the fixed format `merge: resolved=[<sorted-comma-paths>] quarantined=[<sorted-comma-paths>]` — evidence: `git log -1 --format=%s` matches regex `^merge: resolved=\[[^\]]*\] quarantined=\[[^\]]*\]$`; in the 1-corrupt-of-11 case, also asserts `quarantined=[<corrupt-path>]` and the resolved list contains the 10 clean paths sorted alphabetically.
- [ ] `make precommit` exits 0 from the repo root — evidence: exit code 0.
- [ ] `CHANGELOG.md` has a new entry under `## Unreleased` describing per-file quarantine and the new counter — evidence: `grep -n 'quarantine' CHANGELOG.md` returns a line inside the `## Unreleased` section.

Scenario coverage — none. All ACs reach the behavior via in-process integration tests (real git binary, temp working tree, no network, no cluster). No real-cluster behavior is load-bearing for the fix; the deploy-time signal is the new counter on `/metrics`, which is verified in-process.

## Verification

```
make precommit
make test
grep -n 'git_rest_quarantined_files_total' pkg/metrics/metrics.go
grep -n 'quarantine' CHANGELOG.md
git diff pkg/git/conflict_resolver.go mocks/conflict_resolver.go pkg/git/conflict_resolver_test.go pkg/git/yaml_merge_resolver_test.go
```

All commands exit 0 (the `grep` lines return ≥1 match; the `git diff` shows zero changes for the listed files).

## Do-Nothing Option

If we ship nothing, the next corrupt-frontmatter file from any agent or human writer wedges the receiving git-rest pod for as long as it takes a human to diagnose the 503s, identify the bad file, and clear it manually. The 2026-06-02 incident took 3.5 hours from first 503 to recovery, and was only diagnosed because it blocked a PR review the user was actively trying to do. With agent traffic increasing (157 frontmatter-invalid skips in the first minutes after the controller's counter went live on the same day), the next wedge is a matter of weeks, not months. Not acceptable.
