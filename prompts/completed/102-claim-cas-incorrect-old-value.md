---
status: completed
summary: Added `incorrect old value provided` as a recognised lost compare-and-swap reason in isLeaseConflict, with two new table rows and a changelog entry
execution_id: git-rest-lockprobe-exec-102-claim-cas-incorrect-old-value
dark-factory-version: v0.198.17
created: "2026-11-06T08:59:41Z"
queued: "2026-11-06T08:59:41Z"
started: "2026-11-06T11:40:19Z"
completed: "2026-11-06T11:43:22Z"
---

# Classify a server-side lost claim race as a ref conflict

<summary>
- When two writers race to renew the same claim, the loser now gets the ordinary "conflict" answer on every git version, instead of a raw push error.
- Newer git servers report a lost compare-and-swap as "incorrect old value provided"; that wording is now recognised.
- A hook refusal or a transport failure is still not a conflict.
- The two claim-renew race tests pass again inside the agent image.
- No behaviour change on git versions that already used a recognised wording.
</summary>

<objective>
Make `isLeaseConflict` recognise `incorrect old value provided` as a lost compare-and-swap, so a claim renew that loses a race returns `ErrRefConflict`. In `bborbe/github-update-go-agent:v0.21.11` (git 2.52.0) the losing push of a race is rejected server-side with that reason, which no current marker matches, so `PutClaimRef` returns a raw wrapped push error, and `TestClaimRenewRace` / `TestClaimRenewRaceIdenticalBody` fail deterministically — which keeps `make precommit` red in the image the update-go agent runs.
</objective>

<context>
Measured on `origin/master` `3576405` inside `bborbe/github-update-go-agent:v0.21.11`, 3 of 3 runs:

```
--- FAIL: TestClaimRenewRace
    claim_refs_test.go:246: unexpected result: sha="" err=git push refs/claims/race/renew-0: To /tmp/.../origin.git
         ! [remote rejected] a20c6431… -> refs/claims/race/renew-0 (incorrect old value provided)
--- FAIL: TestClaimRenewRaceIdenticalBody
    claim_refs_test.go:369: … (incorrect old value provided)
```

Both pass on the host (git 2.56.0), where the loser is rejected with a wording the classifier already knows. Adding the one marker below was verified in the agent image: `go test -run "TestClaimRenewRace|TestIsLeaseConflict" ./pkg/git/` → `ok`.

The race tests reproduce only on git 2.52; this container's git may reject the loser with a wording already recognised, so they can pass before and after. The new `TestIsLeaseConflict` row is the test that distinguishes before from after here.

Read these before changing anything:

- `pkg/git/claim_refs.go` — `isLeaseConflict`. Its second loop takes the reason inside the last `(...)` of each `[remote rejected]` line and matches it against `expected`, `failed to update ref`, `reference already exists`, `reference does not exist`.
- `pkg/git/claim_refs_test.go` — `assertOneWinner`: each racer must return `nil` or an error matching `errors.Is(err, git.ErrRefConflict)`; anything else fails the test.
- `pkg/git/claim_refs_internal_test.go` — `TestIsLeaseConflict`, the table the new case joins. Note its `hook decline mentioning expected is not a conflict` case: matching is on the parenthesised reason only, never on other output lines.
- `CHANGELOG.md` — check whether a `## Unreleased` heading already exists (a sibling prompt on this branch may have added one).

Coding guides (in-container paths):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
</context>

<requirements>

## 1. Classifier (`pkg/git/claim_refs.go`)

1.1 In `isLeaseConflict`, add `"incorrect old value provided"` to the `[remote rejected]` reason markers, after `"reference does not exist"`. Match it the same way as the others: inside the parenthesised reason only.

1.2 Change nothing else in `isLeaseConflict`, `PutClaimRef`, or any caller.

## 2. Tests (`pkg/git/claim_refs_internal_test.go`)

2.1 Add two rows to the `TestIsLeaseConflict` table:

- `name: "remote rejected incorrect old value provided"`, `output: " ! [remote rejected] abc -> x (incorrect old value provided)"`, `want: true`.
- `name: "hook decline mentioning incorrect old value is not a conflict"`, `output: "remote: incorrect old value provided\n" + " ! [remote rejected] abc -> x (pre-receive hook declined)"` (split across two lines with `+`, like the existing hook-decline row, to stay under golines 100), `want: false` — the wording on a non-reason line must not count.

2.2 Do not change any existing row, and do not change `claim_refs_test.go`.

## 3. Changelog (`CHANGELOG.md`)

3.1 If a `## Unreleased` heading exists, append the bullet to the end of that section; otherwise create `## Unreleased` directly above the first `## v` heading. Exactly one `## Unreleased` must remain. Bullet:

```
- fix: Classify a claim push rejected server-side with `incorrect old value provided` as a lost race, so a claim renew that loses on newer git returns `ErrRefConflict` instead of a raw push error
```

## 4. Self-check

4.1 Before finishing, re-run every `<verification>` command and confirm each exits 0 / prints `ok`.
</requirements>

<constraints>
- An undetermined or non-conflict rejection must stay a non-conflict — never widen matching beyond the parenthesised reason.
- No new sentinel, flag, metric or label.
- Errors are wrapped with `github.com/bborbe/errors`, never `fmt.Errorf`.
- Precommit budgets: funlen 80/50, gocognit 20, nestif 4, maintidx 20, dupl, golines 100.
- `.dark-factory.yaml` sets `workflow: direct`, `pr: false`, `autoRelease: false`: dark-factory commits to the branch directly.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run inside the container, from the repository root:

```
GOFLAGS=-buildvcs=false go build ./...
GOFLAGS=-buildvcs=false go test ./pkg/git/ -count=1 -run 'TestIsLeaseConflict|TestClaimRenewRace'
make precommit
```

All must exit 0. Then every line must print `ok`:

```
test "$(grep -cF '"incorrect old value provided",' pkg/git/claim_refs.go)" = "1" && echo ok
test "$(grep -cF 'remote rejected incorrect old value provided' pkg/git/claim_refs_internal_test.go)" = "1" && echo ok
test "$(grep -cF 'hook decline mentioning incorrect old value is not a conflict' pkg/git/claim_refs_internal_test.go)" = "1" && echo ok
test "$(grep -c '^## Unreleased$' CHANGELOG.md)" = "1" && echo ok
awk '/^## /{sec=$0} /incorrect old value provided/{print sec}' CHANGELOG.md | grep -qx '## Unreleased' && echo ok
```
</verification>
