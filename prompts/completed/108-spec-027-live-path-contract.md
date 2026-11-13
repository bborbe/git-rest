---
status: completed
spec: [027-scope-claim-ref-listing-by-prefix]
summary: Added the live-path prefix-scoping regression test and probe assertion, scoped the mirror for-each-ref walk to the prefix, and recorded both under a new CHANGELOG Unreleased section.
execution_id: 7185c73c-c70f-559f-ae84-eddce61cd208-exec-108-spec-027-live-path-contract
dark-factory-version: dev
created: "2026-11-13T09:21:44Z"
queued: "2026-11-13T09:32:26Z"
started: "2026-11-13T09:32:29Z"
completed: "2026-11-13T09:47:58Z"
branch: dark-factory/scope-claim-ref-listing-by-prefix
---

<summary>
- The live-origin listing path is proven to ask the origin only for refs under the requested prefix, not for the whole claim namespace.
- A regression test records the exact ref pattern the live path sends to the origin and proves the whole-path-component filter still drops a look-alike prefix.
- The reproduction probe's default (live) mode now prints and checks the recorded `ls-remote` argument line.
- The mirror mode's `for-each-ref` check added by the previous prompt keeps working, and mirror mode prints that it makes no `ls-remote` call.
- No production behavior changes: the refs returned, their order, and their metadata are identical.
- Both listing paths now have an executable guard against a future change that reintroduces a whole-namespace walk.
</summary>

<objective>
Prove and lock the live-origin listing path's prefix scoping, so Desired Behavior 6 ("both listing paths honour the contract") is enforced by a test and by the reproduction probe. The live path already passes a prefix-scoped pattern to `git ls-remote`; this prompt adds the evidence and the regression lock rather than a behavior change.
</objective>

<context>
There is no repo-root `CLAUDE.md` in this worktree — `.gitignore` lists `/CLAUDE.md`. Do not look for one. The definition of done is `docs/dod.md`.

Read these files before changing anything:
- `pkg/git/claim_refs.go` — `listClaimRefsLive` calls `g.origin.ListRefs(ctx, "refs/"+prefix+"*")`, then filters with `matchesClaimPrefix`. The pattern is already prefix-scoped; the Go filter is the authority for whole-path-component semantics.
- `pkg/git/claim_refs.go` — `gitOrigin.ListRefs` runs `git ls-remote --refs origin <pattern>`. Note the comment on `claimRefStillAt`: `ls-remote` matches a pattern by ref-name tail, so the exact-ref re-list is a separate concern.
- `pkg/git/claim_refs_list_test.go` — plain `testing.T`; `newClaimListFake`, `claimShaA`..`claimShaE`, `claimMessages`, `captureSlogLogs`. This is where the new live-path test goes.
- `pkg/git/export_test.go` — `NewGitWithClaimOrigin(origin ClaimOrigin) Git`. A Git built this way has a zero `claimMirrorFreshAt`, so `claimMirrorAge()` reports not fresh and every listing takes the live path.
- `hack/claim-ref-listing-probe.sh` — the version extended by prompt 1: `PROBE_FIXTURE_PREFIX` (default `claims/target/`), the fixture GET, the counter truncation, and the `for-each-ref argv:` line.
- `docs/api.md` — the claim-ref listing contract. Do not restate or change it.

Spec: `specs/in-progress/027-scope-claim-ref-listing-by-prefix.md` — Desired Behaviors 1, 2, 6; Acceptance Criterion 5; Constraints; Failure Modes.

Coding-plugin docs (in-container path): `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`. The dark-factory logging rule is at `/home/node/.claude/plugins/marketplaces/dark-factory/docs/rules/logging-conventions.md`; this repo has no `docs/rules/`.
</context>

<requirements>
1. Leave the production behavior of `listClaimRefsLive` unchanged: it already passes `"refs/"+prefix+"*"` to `g.origin.ListRefs`, and `matchesClaimPrefix` is the authority for whole-path-component semantics. Add a short doc comment above that `ListRefs` call stating that the pattern scopes the remote listing to the caller's prefix and that the Go filter remains authoritative. Make no other production change.

2. Add `TestClaimListLiveScopedPattern` to `pkg/git/claim_refs_list_test.go` (plain `testing.T`):
   - Build a fake with `newClaimListFake` over exactly two refs: `{SHA: claimShaA, Ref: "refs/claims/target/1"}` and `{SHA: claimShaB, Ref: "refs/claims/targets/1"}`.
   - Call `git.NewGitWithClaimOrigin(fake).ListClaimRefs(context.Background(), "claims/target/")`. This Git has no fresh mirror, so the call takes the live path.
   - Assert `fake.ListRefsCallCount() == 1` and `fake.ListRefsArgsForCall(0)` equals `"refs/claims/target/*"`. This is the boundary assertion: it records the exact argument the production code sends to the origin.
   - Assert the result has exactly one entry, `refs/claims/target/1`, proving the whole-path-component filter still drops `refs/claims/targets/1` even though the `ls-remote` glob `refs/claims/target*` can return it.
   - Assert the log carries exactly one `served_from=origin` line (use `captureSlogLogs` and count matching lines; never assert on `grep`'s exit status).
   - Do not assert git process totals; per-path process counts are spec 026's invariant and are not re-asserted here.

3. Extend `hack/claim-ref-listing-probe.sh` (build on prompt 1's version; do not remove the fixture prefix, timing, or `for-each-ref` assertion):
   - In live mode, after the fixture GET, extract the last counter line beginning with `ls-remote` (`grep '^ls-remote' "$counter" | tail -n 1 || true`; the `|| true` keeps `set -euo pipefail` from aborting when no line matches) and print it as:
     ```
     ls-remote argv: <line, or "(none)">
     ```
   - In live mode, assert the line's last field equals `"refs/${PROBE_FIXTURE_PREFIX}*"` (for the default fixture prefix, `refs/claims/target/*`) and is not `"refs/claims/*"`. Fail via `die` with a message that prints both the expected pattern and the recorded line.
   - In `--mirror` mode, print `ls-remote argv: (none)`; the mirror path makes no network call and must not be made to look like it does.
   - Keep the mirror-mode `for-each-ref` assertion and the `small-prefix listing:` timing line from prompt 1.
   - Update the script header comment to describe the live-path `ls-remote` assertion alongside the mirror-path `for-each-ref` assertion.
   - Exit 0 only when the existing conditions hold (success-prefix count, fixture-prefix count, the active mode's argv assertion).

4. Do not change the HTTP surface, the `ClaimRef` JSON shape, the sort order, the error mapping, or `validateRefPrefix`. Do not implement an index. Do not touch `PutClaimRef` or `DeleteClaimRef`.

5. Run the tests and the probe:
   - `go test ./pkg/git -run 'TestClaimListLiveScopedPattern|TestClaimMirror|TestListClaimRefs' -count=1`
   - `bash -n hack/claim-ref-listing-probe.sh`
   - `bash hack/claim-ref-listing-probe.sh` (live mode; must exit 0 and print `ls-remote argv:` ending in `refs/claims/target/*`)
   - `bash hack/claim-ref-listing-probe.sh --mirror` (must exit 0, print `ls-remote argv: (none)`, and keep the scoped `for-each-ref argv:` line)
   - `make precommit`
</requirements>

<constraints>
- The `[]ClaimRef` response shape, the `prefix` query parameter, and the error mapping are unchanged. Prefix semantics are whole-path-component matching, already documented in `docs/api.md`; do not restate or change them.
- `TestClaimMirrorPrefixSemantics` and `TestListClaimRefsGolden` must keep passing unchanged. Do not create a second golden file.
- The live-origin path must not be deleted or narrowed; it is the fallback when the mirror is stale, absent, or missing an object.
- No new runtime dependency.
- Do not widen what a prefix can reach; `validateRefPrefix` is unchanged.
- Do not add a new log key and do not repurpose `served_from`; it stays `mirror` or `origin`.
- Logging follows `/home/node/.claude/plugins/marketplaces/dark-factory/docs/rules/logging-conventions.md`.
- Tests are plain `testing.T`, matching the existing `pkg/git` suite. Do not convert `claim_refs_list_test.go` or `claim_mirror_test.go` to Ginkgo.
- Counterfeiter mocks are generated by `go generate ./...` inside `make precommit`; do not hand-edit `mocks/`. This prompt changes no interface.
- Precommit budgets: funlen 80/50, gocognit 20, nestif 4, maintidx 20, dupl, golines 100.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run from the repository root inside the container:

```
go test ./pkg/git -run 'TestClaimListLiveScopedPattern|TestClaimMirror|TestListClaimRefs' -count=1
bash -n hack/claim-ref-listing-probe.sh
make precommit
```

All must exit 0. `make precommit` is the authoritative gate; if it fails, fix the failure and re-run only the failing target, then run `make precommit` once more.

Then, if the environment permits (the probe builds a binary, seeds a temp repo, binds a localhost port, and boots the server), run both modes:

```
bash hack/claim-ref-listing-probe.sh
```

Expect exit 0 and a printed `ls-remote argv:` line whose last field is `refs/claims/target/*`, not `refs/claims/*`.

```
bash hack/claim-ref-listing-probe.sh --mirror
```

Expect exit 0, `ls-remote argv: (none)`, and a `for-each-ref argv:` line whose last field is `refs/claim-mirror/target/`.
</verification>
