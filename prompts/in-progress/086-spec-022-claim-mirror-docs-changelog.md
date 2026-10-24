---
status: approved
spec: [022-claim-ref-local-mirror]
created: "2026-10-24T15:44:42Z"
queued: "2026-10-24T15:56:08Z"
branch: dark-factory/claim-ref-local-mirror
cancelled: "2026-10-24T15:59:41Z"
---

<summary>
- The API documentation states that a claim-ref read is served from a locally maintained mirror.
- It states when the read falls back to the origin: the mirror is older than the staleness bound, or it was never configured.
- It states the bound explicitly as twice the pull interval with a minimum of 60 seconds.
- It states that another cluster's claim becomes visible after at most one pull cycle.
- The changelog carries an Unreleased fix entry naming the local claim-ref mirror.
- The full precommit gate passes with all thirteen mirror tests present.
</summary>

<objective>
Document the final claim-mirror contract in `docs/api.md`, record the change under `## Unreleased` in `CHANGELOG.md`, and run the full repository gate so the spec's container-executable acceptance criteria are all green.
</objective>

<context>
Read the approved spec at `specs/in-progress/022-claim-ref-local-mirror.md` — especially § Acceptance Criteria AC10 and AC11 and § Verification.

Read these files fully before editing:
- `docs/api.md` — the endpoint table near the top (the `GET /api/v1/refs?prefix=claims/` row currently says "List claim refs live from the origin") and the `### List Claim Refs` section, including its status table.
- `CHANGELOG.md` — the header block and the current first version section (`## v0.35.0`). Add `## Unreleased` between the header block and the first version heading.
- `prompts/completed/081-spec-021-flush-rescue-regression.md` — the house style for these prompts.

Coding docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — entry format, prefix rules, `## Unreleased` rules, anti-patterns.
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md` — final verification expectations.
</context>

<requirements>
## 1. `docs/api.md`

### 1.1 Endpoint table

Change the `GET /api/v1/refs?prefix=claims/` row's description from "List claim refs live from the origin" to a description that names the local mirror and the fallback, for example:

```
| `GET` | `/api/v1/refs?prefix=claims/` | List claim refs from the local claim mirror (falls back to the origin when stale) |
```

### 1.2 `### List Claim Refs` section

Add a paragraph directly after the request/curl description and before the response-shape description. It must state all of the following, and must contain the exact strings the acceptance criteria grep for:

- the read is served from a locally maintained claim mirror under `refs/claim-mirror/*`, refreshed by the puller's periodic fetch, so the request path needs no network call while the mirror is fresh;
- a read **falls back** to the live origin when the mirror is older than the staleness bound or was never configured;
- the bound is **twice the pull interval with a minimum of 60 seconds**, derived from `--pull-interval` and never configured separately;
- a claim written by another cluster is visible after at most **one pull cycle**;
- each read logs one INFO line naming its source: `served_from=mirror` (with an `age_s=` field) or `served_from=origin`.

Suggested wording (keep the bolded facts; the surrounding prose may be adjusted, but the four greps below must match):

> The read is served from a locally maintained claim mirror under
> `refs/claim-mirror/*`, which the puller's periodic fetch refreshes. While the
> mirror is fresh the request path makes no network call. A read **falls back**
> to the live origin when the mirror is older than the staleness bound — twice
> the pull interval with a minimum of 60 seconds — or was never configured. A
> claim written by another cluster is visible after at most one pull cycle. Each
> read logs one INFO line naming its source: `served_from=mirror` with an
> `age_s=` field, or `served_from=origin`.

Do not change the response body, the key set, the sort order, the `[]`-not-`null` rule, the prefix-matching rule, the `nonce` rule, or the existing status-code rows. The `500` row still applies: a stale mirror falls back to the origin, and the origin being unreachable is still a `500`.

## 2. `CHANGELOG.md`

**A `## Unreleased` heading already exists** — the earlier prompts in this batch each added their own bullet under it, and it sits between the SemVer bullet list and `## v0.35.0`. Do **not** create a second `## Unreleased` heading; a duplicate heading is a malformed changelog and the `awk` check below would still pass, so nothing would catch it.

Read `CHANGELOG.md` first, then append the bullet as the **last** line of the existing `## Unreleased` section (immediately before the blank line that precedes `## v0.35.0`):

```markdown
- fix: Serve `GET /api/v1/refs` from a locally maintained claim-ref mirror refreshed by the puller's periodic fetch, so a claim-ref read no longer needs the network while the mirror is fresh. A stale or unconfigured mirror falls back to the live origin, cross-cluster visibility is bounded by one pull cycle, and each read logs `served_from=mirror|origin`.
```

The bullet must contain the literal phrase `claim-ref mirror` (or `claim ref mirror`) — the acceptance check selects the section by that phrase. One bullet per logical change; do not append unrelated entries and do not create a new version heading. After editing, confirm exactly one `## Unreleased` heading exists: `grep -c '^## Unreleased$' CHANGELOG.md` must print `1`.

## 3. Final verification

Run the full gate and the spec's container-executable checks. All must pass; `make precommit` must exit 0.
</requirements>

<constraints>
- The `GET /api/v1/refs` response body, key set, ordering and status codes are unchanged; the docs must not promise a contract change.
- No new metric, no new endpoint, no new query parameter, no new CLI flag.
- The documented bound is exactly twice the pull interval with a minimum of 60 seconds.
- The changelog entry uses the `fix:` prefix and a `## Unreleased` heading, per `changelog-guide.md`.
- Do not describe what was verified; describe what changed.
- `autoRelease: false` — the release tag is cut manually after merge.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
```
set -e
make precommit

GOFLAGS=-buildvcs=false go test -mod=mod -race -count=1 ./...

COUNT=$(GOFLAGS=-buildvcs=false go test -mod=mod -race -count=1 -run 'TestClaimMirror' -v ./pkg/git/ | grep -cE '^=== RUN   TestClaimMirror[A-Za-z]+$' || true)
echo "mirror tests run: $COUNT"
test "$COUNT" -ge 14
test "$(grep -c 'claim-mirror' docs/api.md || true)" -ge 1
test "$(grep -c 'one pull cycle' docs/api.md || true)" -ge 1
test "$(grep -ci 'falls back' docs/api.md || true)" -ge 1
test "$(grep -c 'twice the pull interval' docs/api.md || true)" -ge 1

awk '/^## /{sec=$0} /claim-ref mirror|claim ref mirror/{print sec; exit}' CHANGELOG.md | grep -qx '## Unreleased'
```
`set -e` is load-bearing: without it the block's exit status is the last command's, so a failing `make precommit` would be masked by a passing `awk`. Each `grep -c` is wrapped in `test … -ge 1` because `grep -c` prints `0` and exits `1` on no match, and a bare `grep -c` would gate on the wrong thing. Requirements: every line exits 0; the `=== RUN` count is **at least 14**; the `awk` output is exactly `## Unreleased`.
</verification>
