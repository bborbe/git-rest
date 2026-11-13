---
status: completed
spec: [027-scope-claim-ref-listing-by-prefix]
summary: Verified the already-committed prefix-scoped mirror for-each-ref change and probe extension; focused tests, both probe modes and make precommit pass, and the 7000-ref small-prefix listing measured 13 ms (under the 100 ms index threshold).
execution_id: 7185c73c-c70f-559f-ae84-eddce61cd208-exec-109-spec-027-scope-mirror-for-each-ref
dark-factory-version: dev
created: "2026-11-13T09:21:44Z"
queued: "2026-11-13T09:37:46Z"
started: "2026-11-13T09:48:00Z"
completed: "2026-11-13T09:53:22Z"
branch: dark-factory/scope-claim-ref-listing-by-prefix
---

<summary>
- A claim-ref listing served from the local mirror now tells git which prefix to walk, instead of walking the entire claim namespace on every request.
- A request for one narrow prefix no longer pays for the thousands of refs stored under other prefixes such as `claims/success/`.
- The refs returned, their order, and their metadata are exactly the same as before.
- The existing golden test and the existing whole-path-component prefix-semantics test still pass, and no second golden file is created.
- The reproduction probe now seeds a small synthetic fixture prefix alongside the large `claims/success/` fixture, lists that small prefix, and reports how long the listing took.
- The probe prints the exact `for-each-ref` argument line it recorded and fails when that line names the whole namespace instead of the requested prefix.
- A changelog entry records the performance fix under a newly created `Unreleased` heading.
- If the probe's 7,000-ref measurement still exceeds 100 ms, this prompt stops and reports it; the prefix-keyed index is a follow-up spec, not work done here.
</summary>

<objective>
Make a mirror-backed claim-ref listing cost what its answer costs by passing the caller's prefix to `git for-each-ref` instead of walking `refs/claim-mirror/`. Extend `hack/claim-ref-listing-probe.sh` to seed and list a small synthetic fixture prefix, print the recorded `for-each-ref` argv, assert it is prefix-scoped, and report the small-prefix listing duration. Record the change in `CHANGELOG.md`.
</objective>

<context>
There is no repo-root `CLAUDE.md` in this worktree — `.gitignore` lists `/CLAUDE.md`. Do not look for one. The definition of done is `docs/dod.md`.

Read these files before changing anything:
- `pkg/git/claim_mirror.go` — `claimMirrorNamespace` (`"refs/claim-mirror/"`), `claimRefNamespace`, `claimMirrorRefspec` (`"+refs/claims/*:refs/claim-mirror/*"`), `listClaimRefsFromMirror`, `parseMirrorRefs`. Note the refspec strips the `claims/` component, so `refs/claims/target/1` mirrors to `refs/claim-mirror/target/1`.
- `pkg/git/claim_refs.go` — `claimKeyPrefix` (`"claims/"`), `matchesClaimPrefix` (whole-path-component matching), `ListClaimRefs`.
- `pkg/git/claim_mirror_test.go` — `TestClaimMirrorPrefixSemantics`, `newMirrorRepo`, `newMirrorReadFake`. The prefix-semantics test is the regression lock and must stay unchanged.
- `pkg/git/claim_refs_list_test.go` — `TestListClaimRefsGolden` and `pkg/git/testdata/claim_refs_golden.json`. The golden is the answer-shape lock and must keep passing unchanged.
- `pkg/git/claim_refs_internal_test.go` — package `git`, plain `testing.T`, the home for the new internal helper test.
- `hack/claim-ref-listing-probe.sh` — read the whole script: the `PROBE_REFS` seed block, the `--mirror` mode, the counting shim that appends `"$*"` to `$counter`, the success-prefix GET, and the `refs_returned == PROBE_REFS` exit condition.
- `docs/api.md` — the claim-ref listing contract and the whole-path-component prefix rule. Do not restate or change it.
- `CHANGELOG.md` — the preamble ends at the MAJOR/MINOR/PATCH bullets, then `## v0.44.1`. There is no `## Unreleased` heading today.

Spec: `specs/in-progress/027-scope-claim-ref-listing-by-prefix.md` — Desired Behaviors 1, 2, 3, 6, 7; Acceptance Criteria 1, 2, 5, 6, 8, 9; Constraints; Failure Modes.

Coding-plugin docs (in-container paths): `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`, `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`. The dark-factory logging rule is at `/home/node/.claude/plugins/marketplaces/dark-factory/docs/rules/logging-conventions.md`; this repo has no `docs/rules/`.

<!-- OPEN QUESTION FOR REVIEWER: AC 1 gives the example pattern `refs/claim-mirror/claims/target/`. That pattern cannot match anything in this codebase: the mirror refspec is `+refs/claims/*:refs/claim-mirror/*`, which strips the `claims/` component, so `refs/claims/target/1` lives at `refs/claim-mirror/target/1`. The correct scoped pattern for the fixture prefix `claims/target/` is `refs/claim-mirror/target/`. This prompt asserts the correct pattern and flags the example discrepancy here rather than implementing a pattern that would return an empty listing. -->
</context>

<requirements>
1. In `pkg/git/claim_mirror.go`, add this unexported helper next to the namespace constants (do not change the constants):
   ```go
   // claimMirrorPattern maps a caller prefix ("claims/target/") to the mirror
   // ref pattern for for-each-ref ("refs/claim-mirror/target/"). The claim key
   // prefix is stripped because the mirror refspec strips it too.
   func claimMirrorPattern(prefix string) string {
       return claimMirrorNamespace + strings.TrimPrefix(prefix, claimKeyPrefix)
   }
   ```
   `strings` is already imported in this file. Do not add a new import.

2. In `listClaimRefsFromMirror`, change the `for-each-ref` invocation from the bare namespace constant to the prefix-scoped pattern. Exact old → new:
   ```go
   // old
   out, err := g.runCmdOutput(
       ctx,
       g.repoPath,
       "for-each-ref",
       "--format=%(objectname) %(refname)",
       claimMirrorNamespace,
   )

   // new
   out, err := g.runCmdOutput(
       ctx,
       g.repoPath,
       "for-each-ref",
       "--format=%(objectname) %(refname)",
       claimMirrorPattern(prefix),
   )
   ```
   Keep `--format`, the error handling, `parseMirrorRefs(out, prefix)`, the sort, the zero-match early return, and every fallback exactly as they are. `parseMirrorRefs` continues to filter with `matchesClaimPrefix`; the prefix filter stays the authority, the scoped walk is only an accelerator. Do not change `parseMirrorRefs`.

3. Update the doc comment on `listClaimRefsFromMirror` to say the mirror walk is scoped to the caller's prefix and that the prefix filter is retained as the authority for whole-path-component semantics.

4. Add `TestClaimMirrorPattern` to `pkg/git/claim_refs_internal_test.go` (package `git`, plain `testing.T`). Table-test `claimMirrorPattern`:
   - `"claims/"` → `"refs/claim-mirror/"`
   - `"claims/target/"` → `"refs/claim-mirror/target/"`
   - `"claims/target"` → `"refs/claim-mirror/target"`
   - `"claims/redrive-reason/r-1/"` → `"refs/claim-mirror/redrive-reason/r-1/"`
   For each narrow case also assert the result is not the bare namespace root `"refs/claim-mirror/"`. Use `t.Run(test.name, ...)`; the file already imports `testing`.

5. Extend `hack/claim-ref-listing-probe.sh` (keep `set -euo pipefail`, the `die` helper, the `EXIT` trap, the counting shim, and both modes):
   - Add near the `PROBE_REFS` block:
     ```bash
     PROBE_FIXTURE_PREFIX="${PROBE_FIXTURE_PREFIX:-claims/target/}"
     PROBE_FIXTURE_REFS="${PROBE_FIXTURE_REFS:-3}"
     mirror_namespace="refs/claim-mirror/"
     ```
     Validate `PROBE_FIXTURE_REFS` with the same positive-integer check as `PROBE_REFS`. Validate that `PROBE_FIXTURE_PREFIX` starts with `claims/`.
   - In the existing single `update-ref --stdin` seed block, append `PROBE_FIXTURE_REFS` refs named `refs/${PROBE_FIXTURE_PREFIX}task<N>/phase<N>/<N>` (for the default, `refs/claims/target/task1/phase1/1`). Reuse the same `claim_shas` cycle. Keep the `PROBE_REFS` refs under `refs/claims/success/task<N>/phase<N>/<N>` unchanged. The fixture prefix is synthetic: it does not exist in production, so do not try to find it there.
   - Keep the existing success-prefix GET and capture `cat-file invocations`, `refs returned`, and `served_from` from it before the next step.
   - After that, add a second GET to `http://127.0.0.1:${port}/api/v1/refs?prefix=claims%2Ftarget%2F` (derive the URL-encoded fixture prefix from `PROBE_FIXTURE_PREFIX`; do not hard-code `claims/target/` in the URL if the variable is overridden). Truncate `$counter` immediately before this GET. In `--mirror` mode the mirror is already fresh from the success GET, so a single GET is enough; in live mode a single GET is enough.
   - Time this second GET with curl's own timer: `elapsed_s="$(curl -sS -o "$fixture_body" -w '%{time_total}' "$fixture_url")"` and `elapsed_ms="$(awk -v t="$elapsed_s" 'BEGIN{printf "%d", t*1000}')"`. Do not use `date +%s%N` (not portable to macOS bash 3.2).
   - Derive `fixture_refs_returned` from `$fixture_body` the same way as `refs_returned` (`grep -o '"ref":' | wc -l`).
   - Print these additional lines, each on its own line:
     ```
     fixture prefix: <PROBE_FIXTURE_PREFIX>
     fixture refs returned: <n>
     small-prefix listing: <n> ms
     for-each-ref argv: <last counter line beginning with "for-each-ref", or "(none)">
     ```
     Extract the argv line with `grep '^for-each-ref' "$counter" | tail -n 1 || true` (the `|| true` keeps `set -euo pipefail` from aborting when no line matches). Extract the pattern as the last field (`${line##* }`).
   - In `--mirror` mode, assert the recorded `for-each-ref` line's last field equals `"${mirror_namespace}${PROBE_FIXTURE_PREFIX#claims/}"` (for the default fixture prefix, `refs/claim-mirror/target/`) and is not the bare `"${mirror_namespace}"`. Fail via `die` with a message that prints both the expected and the recorded line. In live mode print `for-each-ref argv: (none)` and skip this assertion (prompt 2 adds the live-path assertion).
   - Exit 0 only when `refs_returned` equals `PROBE_REFS`, `fixture_refs_returned` equals `PROBE_FIXTURE_REFS`, and the mirror-mode argv assertion passed. Keep the existing behavior of printing, not asserting, the `cat-file` count.
   - Do not make the 100 ms threshold a hard failure. Print the measured milliseconds. If `elapsed_ms` is greater than 100, additionally print `small-prefix listing exceeds 100 ms: index fallback indicated` so the reviewer sees the signal; do not change the exit status for it.
   - Update the script header comment to describe the fixture prefix, the timing line, the argv assertion, and the pre-fix vs post-fix discrimination.

6. In `CHANGELOG.md`, create the `## Unreleased` section immediately after the SemVer preamble (after the MAJOR/MINOR/PATCH bullets) and before the first `## v` heading. Add one entry that contains the literal `claim ref` and starts with a conventional prefix:
   ```
   ## Unreleased

   - perf: Scope a claim ref listing's `git for-each-ref` invocation to the requested prefix, so a one-ref answer no longer pays for every ref under `claims/success/`; the returned refs, order and metadata are unchanged.
   ```
   Do not invent a version number. Do not move or edit the preamble.

7. Do not implement a prefix-keyed index, do not add a dependency, and do not touch `PutClaimRef`, `DeleteClaimRef`, `updateClaimMirrorRef`, `removeClaimMirrorRef`, `listClaimRefsLive`, or the HTTP handler. The index is a follow-up spec and is only warranted if the 7,000-ref measurement below misses 100 ms.

8. Run the tests and the probe:
   - `go test ./pkg/git -run 'TestClaimMirror|TestListClaimRefs|TestClaimMirrorPattern' -count=1`
   - `bash -n hack/claim-ref-listing-probe.sh`
   - `bash hack/claim-ref-listing-probe.sh --mirror` (default `PROBE_REFS=300`; must exit 0 and print a `for-each-ref argv:` line ending in `refs/claim-mirror/target/`)
   - `bash hack/claim-ref-listing-probe.sh` (live mode; must exit 0)
   - `PROBE_REFS=7000 bash hack/claim-ref-listing-probe.sh --mirror` and record the printed `small-prefix listing:` value in the final report. If it is 100 ms or less, the index is not needed; if it is greater than 100 ms, report that and stop — do not implement the index in this prompt.
   - `make precommit`

<!-- SCOPE NOTE FOR REVIEWER: the prefix-keyed index is deliberately not part of this prompt. The spec makes it conditional on this prompt's 7,000-ref measurement, and an unconditional index prompt would build the fallback even when the scoped invocation is sufficient, which the spec forbids. If the measurement misses 100 ms, the operator should carry the index into a follow-up spec rather than this one. -->
</requirements>

<constraints>
- The `[]ClaimRef` response shape, the `prefix` query parameter, and the error mapping are unchanged. Prefix semantics are whole-path-component matching, already documented in `docs/api.md`; do not restate or change them.
- `TestClaimMirrorPrefixSemantics` is the regression lock for prefix semantics; it must keep passing unchanged.
- `TestListClaimRefsGolden` and `pkg/git/testdata/claim_refs_golden.json` are the answer-shape lock; they must keep passing unchanged. Do not create a second golden file.
- No new runtime dependency. Do not add a key-value store.
- Do not delete the live-origin path; it is the fallback when the mirror is stale, absent, or missing an object.
- Do not change the write path (`PutClaimRef`, `DeleteClaimRef`) or its mirror-ref updates in this prompt.
- Do not add a new log key and do not repurpose `served_from`; it stays `mirror` or `origin` because no index exists.
- Logging follows `/home/node/.claude/plugins/marketplaces/dark-factory/docs/rules/logging-conventions.md`.
- Tests are plain `testing.T`, matching the existing `pkg/git` suite. Do not convert `claim_refs_list_test.go`, `claim_mirror_test.go`, or `claim_refs_internal_test.go` to Ginkgo.
- Counterfeiter mocks are generated by `go generate ./...` inside `make precommit`; do not hand-edit `mocks/`. This prompt changes no interface.
- Do not widen what a prefix can reach; `validateRefPrefix` is unchanged.
- Precommit budgets: funlen 80/50, gocognit 20, nestif 4, maintidx 20, dupl, golines 100.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run from the repository root inside the container:

```
go test ./pkg/git -run 'TestClaimMirror|TestListClaimRefs|TestClaimMirrorPattern' -count=1
bash -n hack/claim-ref-listing-probe.sh
awk '/^## Unreleased/{f=1;next} /^## v/{f=0} f && /claim ref/' CHANGELOG.md | grep -q . && echo ok
make precommit
```

All must exit 0. `make precommit` is the authoritative gate; if it fails, fix the failure and re-run only the failing target, then run `make precommit` once more.

Then, if the environment permits (the probe builds a binary, seeds a temp repo, binds a localhost port, and boots the server), run the mirror mode and confirm the printed `for-each-ref argv:` line names the scoped fixture pattern:

```
bash hack/claim-ref-listing-probe.sh --mirror
```

Expect the output to include a line whose last field is `refs/claim-mirror/target/`, and a `small-prefix listing: <n> ms` line. It must not print `refs/claim-mirror/` as the `for-each-ref` pattern.

```
bash hack/claim-ref-listing-probe.sh
```

Expect exit 0 (live mode prints `for-each-ref argv: (none)` in this prompt).

```
PROBE_REFS=7000 bash hack/claim-ref-listing-probe.sh --mirror
```

Record the `small-prefix listing:` value in the final report. Do not treat a value above 100 ms as a prompt failure; report it, because it decides whether the follow-up index spec is needed.
</verification>
