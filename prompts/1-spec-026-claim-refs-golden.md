---
spec: ["026-batch-claim-ref-commit-reads"]
status: draft
created: "2026-11-09T11:00:00Z"
---

# Capture the pre-fix claim-ref listing golden

<summary>
- Adds a deterministic claim-ref fixture and a deep-equal test that pins the exact listing output of the current, unmodified implementation.
- The pinned output is committed as a JSON fixture before any batching change lands, so a later "identical values" claim is provable rather than circular.
- The fixture exercises mixed ref shapes: multiple depths, lexicographic ordering traps, a parseable message with missing fields, and an unparseable message.
- The golden is regenerated only by an explicit opt-in environment variable; a normal test run compares against the committed file.
- No production code changes in this prompt; the test passes against today's per-ref implementation.
</summary>

<objective>
Capture the claim-ref listing output of the current implementation into a committed golden fixture and add a test that deep-equals the listing against it. This prompt must land as its own commit that precedes the batching implementation, so the later equality check is anchored to genuinely pre-fix behavior.
</objective>

<context>
Read `CLAUDE.md` (workflow and test conventions) and `docs/dod.md`.

Coding guides (in-container paths): `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` and `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md`.

Read these files fully before writing anything:

- `pkg/git/claim_refs.go` — the `ClaimRef` struct (JSON contract: `ref`, `sha`, `cluster`, `task_id`, `lease_until`), `ListClaimRefs`, `listClaimRefsLive`, `readClaimRef`, and `ReadCommitMessage`. The listing sorts matched refs by ref name and returns one `ClaimRef` per surviving ref.
- `pkg/git/claim_refs_list_test.go` — a plain `testing.T` file (package `git_test`). This is where the golden test and its helpers go. Do NOT convert this file to Ginkgo.
- `pkg/git/claim_refs_test.go` — the shared fixture and helpers: `newClaimFixture(t)` returns a struct with `bare`, `cloneA`, `cloneB`, `clientA`, `clientB`; `mustGit(t, dir, args...)` runs git and fails on non-zero exit; `configureClaimIdentity(t, dir)`; `createClaimCommit(t, bare, ref, message)` creates a parentless claim commit on the empty tree and points a ref at it. `claimEmptyTreeSHA` is the empty-tree object id.
- `pkg/git/export_test.go` — `NewGitWithClaimOrigin`, `SetClaimMirrorFreshAtForTest`, and other test hooks.
- `pkg/git/git_suite_test.go` — the package's Ginkgo entry point is `TestSuite`. It cannot be selected with `go test -run`; this is exactly why the new test must be a plain `testing.T` function in `claim_refs_list_test.go`.

The golden fixture path is `pkg/git/testdata/claim_refs_golden.json` (create the `testdata` directory).

Why the fixture must be deterministic: the golden is generated in this prompt and compared in the next one. Any nondeterminism (commit timestamps, identities, map iteration order) would make the later comparison flaky or, worse, force a regeneration that makes the equality check vacuous. Fix the git author/committer identity and both dates. Do NOT sort either side before comparing: the golden pins the listing's own order, and requirement 2.2 depends on that. Ref-name sorting is the implementation's contract, not something the test applies.
</context>

<requirements>

## 1. Add deterministic git helpers (in `pkg/git/claim_refs_list_test.go`)

1.1 Add `mustGitEnv(t *testing.T, dir string, env []string, args ...string) string`. It runs `git` in `dir` with `cmd.Env = append(os.Environ(), env...)`, returns combined stdout+stderr, and calls `t.Fatalf` on a non-zero exit. This mirrors `mustGit` in `claim_refs_test.go` but adds environment entries; do not modify `mustGit`.

1.2 Add `createDatedClaimCommit(t *testing.T, bare, ref, message string) string`. It creates a parentless claim commit on `claimEmptyTreeSHA` in `bare` and points `ref` at it, exactly like `createClaimCommit`, but with a fixed identity and fixed dates so the resulting sha is deterministic:

```go
env := []string{
    "GIT_AUTHOR_NAME=Claim User",
    "GIT_AUTHOR_EMAIL=claim@example.com",
    "GIT_AUTHOR_DATE=2026-01-01T00:00:00 +0000",
    "GIT_COMMITTER_NAME=Claim User",
    "GIT_COMMITTER_EMAIL=claim@example.com",
    "GIT_COMMITTER_DATE=2026-01-01T00:00:00 +0000",
}
sha := strings.TrimSpace(mustGitEnv(t, bare, env,
    "-c", "user.name=Claim User",
    "-c", "user.email=claim@example.com",
    "commit-tree", claimEmptyTreeSHA, "-m", message,
))
mustGit(t, bare, "update-ref", ref, sha)
return sha
```

`claimEmptyTreeSHA` is already declared in `claim_refs_test.go`; do not redeclare it.

## 2. Add the fixture builder

2.1 Add `newGoldenClaimFixture(t *testing.T) *claimFixture` that calls `newClaimFixture(t)` and then creates these refs in `fixture.bare` with `createDatedClaimCommit`. Use these exact refs and messages so the golden is stable:

| ref | message |
|---|---|
| `refs/claims/task/1` | `{"cluster":"one","task_id":"t-1","lease_until":"2026-10-21T12:05:00Z","nonce":"00000000000000000000000000000001"}` |
| `refs/claims/task/10` | `{"cluster":"two","task_id":"t-10","lease_until":"2026-10-22T12:05:00Z","nonce":"00000000000000000000000000000002"}` |
| `refs/claims/task/2` | `{"cluster":"three","task_id":"t-2","lease_until":"2026-10-23T12:05:00Z","nonce":"00000000000000000000000000000003"}` |
| `refs/claims/repo/a/b` | `{"cluster":"four","task_id":"t-ab","lease_until":"2026-10-24T12:05:00Z","nonce":"00000000000000000000000000000004"}` |
| `refs/claims/tasks/1` | `{"cluster":"five","task_id":"t-s1","lease_until":"2026-10-25T12:05:00Z","nonce":"00000000000000000000000000000005"}` |
| `refs/claims/task/partial` | `{"cluster":"six"}` |
| `refs/claims/task/broken` | `this is not json` |
| `refs/other/x` | `{"cluster":"seven","task_id":"t-x","lease_until":"2026-10-26T12:05:00Z"}` |

`refs/other/x` is outside the `claims/` prefix and must not appear in the result. `refs/claims/task/broken` must appear with `cluster`, `task_id` and `lease_until` empty (the existing code logs a WARN and keeps the ref). `refs/claims/task/partial` must appear with `cluster: "six"` and the other two fields empty.

2.2 The expected listing order (lexicographic by ref name) is:

```
refs/claims/repo/a/b
refs/claims/task/1
refs/claims/task/10
refs/claims/task/2
refs/claims/task/broken
refs/claims/task/partial
refs/claims/tasks/1
```

Seven entries. Do not assert this order separately from the golden; it is captured in the golden file.

## 3. Add the golden test

3.1 Add `TestListClaimRefsGolden(t *testing.T)` in `pkg/git/claim_refs_list_test.go` (plain `testing.T`). It must:

- Build the fixture with `newGoldenClaimFixture(t)`.
- Call `fixture.clientA.ListClaimRefs(context.Background(), "claims/")`. `clientA` is a real `git` on `cloneA` with `origin` = `fixture.bare` and no configured claim mirror, so this exercises the live-origin path; the mirror path's equality with the same golden is covered by prompt 2's tests.
- If `os.Getenv("UPDATE_GOLDEN") == "1"`, marshal the result with `json.MarshalIndent(claims, "", "  ")`, append a trailing newline, and write it to the package-relative golden path defined in 3.3 (`filepath.Join("testdata", "claim_refs_golden.json")`) with mode `0600`; check the `os.WriteFile` error (`errcheck` is enabled in `.golangci.yml`), log the path, and return. This is the ONLY way the file is written.
- Otherwise read the same package-relative path from 3.3, unmarshal into `[]git.ClaimRef`, and assert `reflect.DeepEqual(got, want)`. On mismatch, print both slices so the diff is obvious. The test runs with its working directory set to `pkg/git`, so a literal `pkg/git/testdata/...` path would resolve to `pkg/git/pkg/git/testdata/...`.

3.2 Add a comment above the test recording that the golden was generated against the unmodified implementation and the exact command:

```go
// The golden was generated from the unmodified implementation with:
//   UPDATE_GOLDEN=1 go test -run TestListClaimRefsGolden ./pkg/git -count=1
// Regenerate only when the listing contract intentionally changes; the
// batching change (spec 026) must not alter a single byte.
```

3.3 Use the path constant `filepath.Join("testdata", "claim_refs_golden.json")` so the test works from the package directory.

## 4. Generate and commit-ready the golden

4.1 Run the generation command once:

```bash
UPDATE_GOLDEN=1 go test -run TestListClaimRefsGolden ./pkg/git -count=1
```

4.2 Then run it again without the variable and confirm it passes:

```bash
go test -run TestListClaimRefsGolden ./pkg/git -count=1
```

4.3 Confirm the file contains seven entries and that the unparseable ref is present with empty metadata:

```bash
jq 'length' pkg/git/testdata/claim_refs_golden.json
jq '[.[] | select(.ref == "refs/claims/task/broken")][0]' pkg/git/testdata/claim_refs_golden.json
jq '[.[] | select(.ref == "refs/other/x")] | length' pkg/git/testdata/claim_refs_golden.json
```

Expected: `7`; the broken object with `"cluster": ""`, `"task_id": ""`, `"lease_until": ""`; and `0`.

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- This prompt MUST produce a commit that contains `pkg/git/testdata/claim_refs_golden.json` and that precedes the batching implementation commit. Do not fold any production-code change into this prompt.
- Do NOT change any non-test Go file. `pkg/git/claim_refs.go`, `pkg/git/claim_mirror.go`, `pkg/git/git.go`, and `mocks/` are out of scope for this prompt.
- Do NOT convert `pkg/git/claim_refs_list_test.go` or `pkg/git/claim_mirror_test.go` to Ginkgo. Both are deliberately plain `testing.T` (see the comment at the top of `claim_mirror_test.go`). The Ginkgo/`DescribeTable` rule applies only to new files.
- Do NOT use `time.Now()`, the ambient git identity, or the ambient clock inside the fixture. The shas and therefore the golden must be reproducible.
- Do NOT hand-write the golden file; generate it with `UPDATE_GOLDEN=1`.
- The repo does not vendor (`.gitignore` ignores `/vendor`; `make ensure` runs `rm -rf vendor`): never pass `-mod=vendor` and never run `go mod vendor`, whatever `CLAUDE.md`'s Development Standards line says.
- Existing tests must still pass.
- `docs/dod.md` is this project's `validationPrompt`, so its Documentation criterion ("CHANGELOG.md has an entry under `## Unreleased`") fires on every run. Do NOT satisfy it in this prompt: the CHANGELOG entry is owned by a later prompt in this spec, and `origin/master` carries no `## Unreleased` heading yet. Report that one criterion as deliberately deferred, not as a blocker.
- Copy of spec constraint: test style is per-file — `claim_mirror_test.go` and `claim_refs_list_test.go` are plain `testing.T` and stay so; new tests added to them stay plain `testing.T` and are selected with `go test -run`.
- Copy of spec constraint: do not change the HTTP surface; `ClaimRef`'s JSON field set stays exactly five keys.
</constraints>

<verification>
Run from the repo root. Every command must produce the stated result.

The golden does not exist yet, so generate it first — this is the only run that may write it:

```bash
UPDATE_GOLDEN=1 go test -run TestListClaimRefsGolden ./pkg/git -count=1
```

Must pass (exit 0). Then the plain run must pass against it:

```bash
go test -run TestListClaimRefsGolden ./pkg/git -count=1
```

Must pass (exit 0). Then prove the golden is reproducible — regenerate it and confirm the bytes do not move:

```bash
BEFORE=$(git hash-object pkg/git/testdata/claim_refs_golden.json)
UPDATE_GOLDEN=1 go test -run TestListClaimRefsGolden ./pkg/git -count=1
AFTER=$(git hash-object pkg/git/testdata/claim_refs_golden.json)
[ "$BEFORE" = "$AFTER" ] && echo golden-stable
```

`golden-stable`. Then:

```bash
test -f pkg/git/testdata/claim_refs_golden.json && echo present
jq 'length' pkg/git/testdata/claim_refs_golden.json
jq -r '.[].ref' pkg/git/testdata/claim_refs_golden.json
jq '[.[] | select(.ref == "refs/other/x")] | length' pkg/git/testdata/claim_refs_golden.json
```

`present`; `7`; the seven refs in the order listed in requirement 2.2; `0`.

```bash
make precommit
```

Must exit 0.

Self-check before finishing: `git status --short` must list exactly two paths — `?? pkg/git/testdata/claim_refs_golden.json` and ` M pkg/git/claim_refs_list_test.go`. If any other path appears (for example an in-repo `.dark-factory.daemon.out`, which `.gitignore` does not cover), name it in your report rather than reverting or committing it. Confirm no production file changed, then re-run the golden test once more without `UPDATE_GOLDEN`.
</verification>
