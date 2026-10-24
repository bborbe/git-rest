---
status: verifying
approved: "2026-10-24T15:41:45Z"
generating: "2026-10-24T15:41:45Z"
prompted: "2026-10-24T15:54:37Z"
verifying: "2026-10-24T16:16:13Z"
branch: dark-factory/claim-ref-local-mirror
---

## Summary

- A claim-ref read currently shells out to the network on **every** request, then fetches each listed claim commit. The read is therefore as slow as the network and dies when the caller's 30 s deadline cancels it.
- The served clone already fetches on a timer (30 s by default). This spec makes that fetch also carry the claim namespace into a local mirror, and answers the read from the mirror — no network on the request path.
- The mirror is bounded and its two failure shapes stay distinguishable: a read whose mirror is older than the refresh bound falls back to today's live path, and only a fetch that ran with the claim refspec actually present may mark the mirror fresh — so a clone that never got the refspec can never answer with an empty list. A write on the same instance updates the mirror immediately, so a read-after-write never sees its own claim missing.
- Cross-cluster visibility moves from "always current" to "current within one refresh cycle", which is 30 s against a lease measured in minutes.
- Each read logs which source answered, so "served locally" and "still shelling out" are distinguishable in the pod log without any new metric.

## Problem

A claim-ref read is on the hot path of every executor's reconcile pass. On prod `vault-obsidian-openclaw` the executor caps a git-rest claim call at 30 s; the network round trip to GitHub under load exceeds it, the child is killed, git-rest answers `500`, and the pass aborts. The measured consequence is that a pass completes 35 of 13,213 tasks. The clone holds no claim refs to answer from because the default fetch refspec covers only branches, so there is nothing local to serve and no way to avoid the network.

## Goal

A git-rest instance whose origin is reachable answers `GET /api/v1/refs` from a locally maintained claim mirror with no network call on the request path, while remaining correct: another cluster's claim is visible after at most one refresh cycle, a claim this instance just wrote is visible to the next read immediately, a deleted claim stops being listed, and a mirror that is stale — or was never configured — falls back to the live origin rather than answering from it.

## Non-goals

- No change to the executor's 30 s caller cap, and no git-rest-side request deadline.
- No change to the claim read-back recovery context — that is a separate defect on the same call path.
- No change to the claim conflict rate, the lease duration, or any executor-side claim policy.
- No `push` / `ls-remote` duration instrumentation. `git_rest_git_operation_duration_seconds` does not carry those labels today and adding them is its own task; this spec's evidence is a log line and a response body, not a metric.
- No new endpoint, no new query parameter, no change to the response body or status codes.
- No new CLI flag: the staleness bound is derived from the configured pull interval.
- No change to file endpoints, readiness, the pull state machine, or the served branch.
- No in-memory claim cache as an alternative to the mirror — the mirror is git's own object database and ref store, so a restart costs one fetch, not a cold cache.

## Assumptions

- A plain `git fetch` honours `remote.<name>.prune=true` and prunes a custom destination namespace, not only `refs/remotes/<name>/*`. Verified 2026-10-24 on git 2.54.0: with `+refs/claims/*:refs/claim-mirror/*` and `remote.origin.prune=true`, a claim created on the origin appears after one fetch and disappears after one fetch following its deletion. The container image's own git build is re-checked at rung 2 by AC13.
- The origin advertises `refs/claims/*` to a normal fetch — no server-side `uploadpack.hideRefs` masks it. It already must, because the current read lists the namespace with `ls-remote`.
- A fetch that transfers a claim ref also transfers the claim commit object, so reading the message needs no network.
- `git config --add` is **not** idempotent: applied twice it writes the entry twice. The configuration step must test for the entry before adding it.
- No ref under `refs/heads/claims/` is expected on the origin; the mirror namespace `refs/claim-mirror/*` is reachable by the heads refspec only through such a branch, and this spec's destination was chosen to be disjoint from it.
- The served clone is long-lived on a PVC, so the configuration step runs against an already-cloned repository on every boot, not only on a fresh clone.

## Reproduction

Run from the worktree that holds the change. The origin is severed **after** the mirror is populated, so the only thing that differs between the two reads is whether the request may reach the network.

```bash
set -e
REPO_UNDER_TEST="${REPO_UNDER_TEST:-$(git rev-parse --show-toplevel)}"
BASE=/tmp/git-rest-mirror-repro && rm -rf "$BASE" && mkdir -p "$BASE" && cd "$BASE"
BRANCH=test/repro

# 1. Bare origin, seeded with one commit on the served branch.
git init -q --bare -b "$BRANCH" origin.git
git clone -q origin.git seed && cd seed
git config user.email repro@local && git config user.name repro
git commit -q --allow-empty -m init && git push -q origin HEAD:refs/heads/test/repro
cd "$BASE"

# 2. One claim ref written directly into the bare origin — never through git-rest,
#    and never pushed from a working clone (a claim ref is not a branch, so a push
#    guardrail refuses it). It is the same object a PUT would leave: a parentless
#    commit on the empty tree whose message is the claim metadata.
git -C origin.git config user.email repro@local
git -C origin.git config user.name repro
EMPTY=4b825dc642cb6eb9a060e54bf8d69288fbee4904
CLAIM_SHA=$(git -C origin.git commit-tree "$EMPTY" -m '{"cluster":"nuke","task_id":"t1","lease_until":"2026-10-25T00:00:00Z","nonce":"0123456789abcdef0123456789abcdef"}')
git -C origin.git update-ref refs/claims/task/t1 "$CLAIM_SHA"

# 3. Work clone — the repo the puller serves — then boot the binary.
git clone -q origin.git work
git -C work config user.email repro@local && git -C work config user.name repro
cd "${REPO_UNDER_TEST}" && go build -o /tmp/git-rest-mirror-bin .
# sleep 4 is enough only because the boot-time sync runs before the server serves;
# it is the boot pull, not the 10 s interval, that must populate the mirror.
/tmp/git-rest-mirror-bin -listen=:18451 -repo="$BASE/work" -pull-interval=10s -v=1 > "$BASE/run.log" 2>&1 &
sleep 4

# 4. Read with the origin reachable: one claim.
curl -s -o "$BASE/before.json" -w 'reachable HTTP %{http_code}\n' \
  'http://localhost:18451/api/v1/refs?prefix=claims/'

# 4b. The two config reads AC12 asserts, taken while the mirror is populated.
git -C "$BASE/work" config --get-all remote.origin.fetch
git -C "$BASE/work" for-each-ref refs/claim-mirror/

# 5. Sever the origin. The mirror is now the only possible source.
git -C "$BASE/work" remote set-url origin /nonexistent/origin.git

# 6. Read again, inside the staleness bound.
curl -s -o "$BASE/after.json" -w 'severed  HTTP %{http_code}\n' \
  'http://localhost:18451/api/v1/refs?prefix=claims/'

# 7. Teardown — a survivor keeps answering :18451 with stale evidence.
kill "$(pgrep -f /tmp/git-rest-mirror-bin)" 2>/dev/null || true
```

Observed on `bborbe/git-rest` `origin/master` tip `ba7d420` (= `v0.35.0`, 2026-10-24, run as `/tmp/repro-claim-mirror.sh`):

```
origin claims:      1
work local claims:  0
work mirror claims: 0
fetch refspec:      +refs/heads/*:refs/remotes/origin/*
prune:              <unset>
reachable HTTP 200
severed  HTTP 500
before: [{"ref":"refs/claims/task/t1","sha":"ac59887f…","cluster":"nuke","task_id":"t1","lease_until":"2026-10-25T00:00:00Z"}]
after:  {"error":{"code":"INTERNAL_ERROR","message":"list claim refs: list claim refs: git ls-remote origin: git [ls-remote --refs origin refs/claims/*]: fatal: '/nonexistent/origin.git' does not appear to be a git repository …"}}
bodies-identical: no
```

The two zeroes are the whole defect: the origin holds the claim and the served clone holds neither a local copy nor a mirror of it, so the read has nothing to answer from and reaches the network. Expected after the fix: step 6 prints `severed  HTTP 200` and `bodies-identical: yes`.

## Expected vs Actual

| | |
|---|---|
| **Expected** | The periodic fetch the clone already runs carries the claim namespace, so a refs read answers from the local mirror and does not need the origin. A read that cannot reach the origin inside the staleness bound still returns the same claim list. |
| **Actual** | `gitOrigin.ListRefs` (`pkg/git/claim_refs.go:96`) runs `git ls-remote --refs origin refs/claims/*` per request. The clone's fetch refspec is git's default `+refs/heads/*:refs/remotes/origin/*`, so no claim ref ever lands locally, and with the origin unreachable the read fails `500`. |

## Acceptance Criteria

Container-executable (unit / integration tests and greps):

- [ ] AC1 — **fetch refspec configured, idempotently, without ever aborting startup.** After the startup configuration step runs twice against one clone, `git -C <clone> config --get-all remote.origin.fetch` prints exactly two lines — `+refs/heads/*:refs/remotes/origin/*` and `+refs/claims/*:refs/claim-mirror/*` — and `git -C <clone> config --get remote.origin.prune` prints `true`. A configuration write that fails (a read-only `.git/config`) is logged and still yields a working client whose reads take the live path; the process does not abort and does not crashloop. Test: `TestClaimMirrorConfigureFetch`, plus `TestClaimMirrorConfigFailureKeepsServing` for the read-only case. Evidence: a Go test that calls the configuration step twice and asserts both reads, plus the second test making `.git/config` read-only and asserting the step returns without panicking and that a subsequent read takes the live path.
- [ ] AC2 — **one pull refreshes and prunes the mirror, and a failed fetch does not mark it fresh.** Against a local bare origin: with no claim refs, `Pull` leaves `for-each-ref refs/claim-mirror/` empty and returns nil; after a claim ref is created directly in the origin, one `Pull` makes `refs/claim-mirror/<key>` exist at the origin's sha; after that ref is deleted directly in the origin, one `Pull` removes it locally. Then: record the freshness after a successful `Pull`, sever the origin, `Pull` again so its fetch fails, and assert the freshness value is **unchanged** and that a read still serves the mirror. Test: `TestClaimMirrorPullRefreshesAndPrunes`. Evidence: a Go test asserting the three mirror states with `for-each-ref` and `rev-parse`, then the freshness value before and after the failed pull plus a mirror-served read.
- [ ] AC3 — **a fresh mirror answers without the network, and says so with its age.** Two tests. (i) `TestClaimMirrorServesWithoutNetwork`: with the mirror fresh, a `ListClaimRefs` call observes **zero** calls to `ClaimOrigin.ListRefs` and **zero** calls to `ClaimOrigin.FetchCommits` (`pkg/git/claim_refs.go:80-83`), returns the mirror's entries, and logs exactly one INFO line carrying `served_from=mirror` **and** an `age_s=<n>` field whose value is a non-negative integer. (ii) `TestClaimMirrorSeveredOrigin`: against a real bare origin, `Pull` then `git remote set-url origin <nonexistent>`, then `ListClaimRefs` still returns the claim with its metadata. Evidence: (i) both recorded call lists are empty and the captured log record has both fields; (ii) the returned slice equals the pre-severing result.
- [ ] AC4 — **prefix semantics are unchanged on the local path.** Fixture refs `claims/task/1`, `claims/task/10`, `claims/tasks/1`, `claims/repo/a/b` present in the mirror: `prefix=claims/task/1` returns exactly `refs/claims/task/1`; `prefix=claims/task` returns exactly `refs/claims/task/1` and `refs/claims/task/10`, never `refs/claims/tasks/1`; `prefix=claims/repo/a` returns exactly `refs/claims/repo/a/b`; `prefix=claims/` returns all four. Results are sorted by `ref`, and each returned `ref` is the claim ref (`refs/claims/…`), never the mirror ref (`refs/claim-mirror/…`). Test: `TestClaimMirrorPrefixSemantics`. Evidence: one test asserting each returned ref list.
- [ ] AC5 — **a stale, absent or unconfigured mirror falls back to the live origin.** Four cases, each observing at least one call to `ClaimOrigin.ListRefs` and returning the same entries the mirror path would. (a) The last-claims-fetch time is unset (a process that has not pulled). (b) It is older than the bound. (c) **The claims refspec is absent from `remote.origin.fetch` on an otherwise-fresh clone**: after a successful `Pull`, a read must still take the live path — severing the origin then makes it fail naming `ls-remote`, and it must never answer `[]` from an empty mirror. (d) **A mirror ref exists but its commit object does not** (`git cat-file` of its sha fails): the read takes the live path rather than returning a partial list or an entry with empty metadata. Tests: `TestClaimMirrorStaleFallsBack`, `TestClaimMirrorRefspecAbsentFallsBack`, `TestClaimMirrorMissingObjectFallsBack`. Evidence: the fake's lister was called in each case; one INFO log line per request carrying `served_from=origin`; case (c) returns an error whose text names `ls-remote`; case (d) returns the live path's full list.
- [ ] AC6 — **read-after-write is consistent on the same instance, and the read took the mirror path.** Against a real bare origin with a fresh mirror: a `PutClaimRef` followed immediately by `ListClaimRefs` (no pull in between) lists the new claim at the sha the PUT returned; a `DeleteClaimRef` followed immediately by `ListClaimRefs` does not list it; and both reads are mirror-served — zero `ClaimOrigin.ListRefs` calls and a `served_from=mirror` log line — so the assertion cannot be satisfied by falling back to the live path. Test: `TestClaimMirrorReadAfterWrite`, plus `TestClaimMirrorUpdateFailureSwallowed` for the branch where the local mirror write itself fails — the request must still succeed and the origin write must stand, because the mirror is a cache and the origin is authoritative. Evidence: one test asserting the listed ref/sha pair after each operation, plus the empty lister call list and the `served_from=mirror` log record; and one asserting a blocked mirror write leaves the request successful, the origin ref written, and the failure logged.
- [ ] AC7 — **cross-cluster visibility is bounded by one pull cycle.** Two independent clones of one bare origin, each served by its own git client (modelling two clusters): cluster A's `PutClaimRef` is **not** visible to cluster B's fresh-mirror read before B pulls; after exactly one `Pull` on B, B's read lists it at A's sha. Test: `TestClaimMirrorCrossCluster`. Evidence: one test asserting both observations.
- [ ] AC8 — **the response contract is unchanged.** For a mirror-served read: the key set of every returned object is exactly `ref`, `sha`, `cluster`, `task_id`, `lease_until` with no `nonce`; a claim whose commit message is not JSON is still listed with its `ref`/`sha` and empty metadata strings; with no claims the body is `[]` and not `null`; a `prefix` containing `*`, `?` or `[` is still rejected with `400` before any git command runs. Test: `TestClaimMirrorResponseContract`. Evidence: one test asserting the JSON body and the status codes.
- [ ] AC9 — **a ref operation changes nothing about the served clone's sync state.** After a `GET`, a `PUT` and a `DELETE`, both `git -C <clone> config --get-all remote.origin.fetch` and the whole `git -C <clone> for-each-ref refs/remotes/origin` listing are byte-identical to their pre-operation values. Test: `TestClaimMirrorSyncStateUnchanged`. Evidence: before/after equality in a test, over a namespace that is non-empty (the clone carries `refs/remotes/origin/HEAD` and one ref per branch), so the comparison can actually fail.
- [ ] AC10 — **docs and changelog.** `docs/api.md`'s `GET /api/v1/refs` section states that the read is served from a locally maintained claim mirror, that it falls back to the origin when the mirror is older than the bound or was never configured, that the bound is **twice the pull interval with a minimum of 60 seconds**, and that another cluster's claim is visible after at most one pull cycle. `CHANGELOG.md` has a `## Unreleased` `fix:` bullet naming the local claim-ref mirror. Evidence: `grep -c 'claim-mirror' docs/api.md` ≥ 1, `grep -c 'one pull cycle' docs/api.md` ≥ 1, `grep -ci 'falls back' docs/api.md` ≥ 1, `grep -c 'twice the pull interval' docs/api.md` ≥ 1, and `awk '/^## /{sec=$0} /claim-ref mirror|claim ref mirror/{print sec; exit}' CHANGELOG.md` prints `## Unreleased`.
- [ ] AC11 — `make precommit` exits 0. Evidence: exit code.

Operator-executable:

- [ ] AC12 — **rung 1.** The `## Reproduction` sequence prints `reachable HTTP 200`, then `severed  HTTP 200`; `cmp "$BASE/before.json" "$BASE/after.json"` exits 0; and step 4b prints the two refspecs plus a `refs/claim-mirror/task/t1` entry. Complete steps 5–6 promptly: at `-pull-interval=10s` the freshness window is `max(2 × 10 s, 60 s) = 60 s`, so a slow operator would see a *legitimate* fallback and a `500`. Evidence: recipe stdout.
- [ ] **Post-Deploy (Rung-2):** AC13 — on nuke dev against `vault-obsidian-openclaw`, a refs `GET` returns `200` while the pod log carries one INFO line for that request with `served_from=mirror` and an `age_s=` field, and the served clone's `refs/claim-mirror/` is non-empty — read through the API-server proxy, no `exec` and no `port-forward`.
  - `deploy_check:` `kubectlnukedev -n dev get --raw /api/v1/namespaces/dev/pods/vault-obsidian-openclaw-0:9090/proxy/metrics | grep -m1 '^git_rest_build_info' | sed -E 's/.*commit="([^"]+)".*/\1/' | grep -E '^[0-9a-f]{7,40}$'`
  - `deploy_target:` `$(git -C ~/Documents/workspaces/git-rest rev-parse --short vX.Y.Z^{commit})` (`vX.Y.Z` = the release tag that carries this spec)

## Verification

### Container-executable (prompt time)

```
make precommit
go test -race -count=1 ./...
go test -race -count=1 -run 'TestClaimMirror' -v ./pkg/git/ | grep -cE '^=== RUN   TestClaimMirror[A-Za-z]+$'   # >= 14
grep -c 'claim-mirror' docs/api.md            # >= 1
grep -c 'one pull cycle' docs/api.md          # >= 1
grep -ci 'falls back' docs/api.md             # >= 1
grep -c 'twice the pull interval' docs/api.md # >= 1
awk '/^## /{sec=$0} /claim-ref mirror|claim ref mirror/{print sec; exit}' CHANGELOG.md   # ## Unreleased
```

The `=== RUN` count is not decoration: `go test -run` exits 0 when it matches **no** test, so a renamed test would turn every AC above it into a silent pass. The pattern ends in `[A-Za-z]+$` so a subtest line (`=== RUN   TestClaimMirrorPrefixSemantics/claims_task`) cannot inflate the count. The fourteen tests named in AC1–AC9 are plain Go test functions in `pkg/git/` (the package already mixes plain Go tests with Ginkgo suites — see `TestClaimCreateRace` at `pkg/git/claim_refs_test.go:218`), so the `-run` pattern is a real selector rather than a filter that can never fire.

### Operator-executable — rung 1 (local binary, temp bare origin)

The `## Reproduction` sequence above, in full: step 4b carries AC12's two config reads and steps 5–6 carry its severed read. Run it from the worktree that holds the change: the build step compiles `$REPO_UNDER_TEST`, and building the main checkout replays the unfixed binary.

### Operator-executable — rung 2 (nuke dev)

Follows `docs/verifying-specs.md` rung 2 (the read path's network behaviour changes → rung 2 required). Preconditions: release tag cut manually (`autoRelease: false`), image pushed, `VERSION` pin in `~/Documents/workspaces/nuke/git-rest/Makefile` bumped and merged, `BRANCH=dev make apply` from the nuke deploy worktree, StatefulSets rolled.

- Freshness: the `deploy_check` / `deploy_target` pair on AC13.
- Drive one refs read against the dev pod and read the pod log for the `served_from=` line on that request.
- Confirm the mirror is populated by reading the served clone's `refs/claim-mirror/` inside the pod through the API-server proxy, without `exec` or `port-forward`.

Rung 3 (nuke prod) promotes after rung 2 per `docs/verifying-specs.md`; the prod measurement (reconcile-pass coverage and the 24-hour 5xx count) is owned by the vault task, not by this spec's ACs.

## Desired Behavior

1. **Startup configures the fetch refspec, before the boot sync.** The clone's `remote.origin.fetch` carries `+refs/claims/*:refs/claim-mirror/*` in addition to git's default heads refspec, and `remote.origin.prune` is `true`. The step is idempotent: a restart against an already-configured clone adds no duplicate entry and leaves the value set unchanged. It runs inside `createGitClient` (`main.go:392`), which is called at `main.go:79` **before** `syncOnStartup` at `main.go:88` — so the boot-time pull's fetch already carries claim refs and the mirror is populated before the first request is served. A failure to configure is logged and does not abort startup; the read path then never has a fresh mirror and keeps using the live origin.
2. **The pull cycle refreshes and prunes the mirror.** The fetch the puller already runs updates `refs/claim-mirror/*` from the origin and prunes entries the origin no longer advertises, so a deleted or released claim stops being listed locally within one cycle. The client records the completion time of the last **successful `git fetch` step** of a pull, independent of whether the subsequent merge / rescue / push state machine succeeds; a failed fetch does not advance it. That time is process-local, held in memory: a restart resets it, and the boot pull re-establishes it before the first request is served (DB1's ordering).
3. **Freshness is claims-specific, not fetch-specific.** The freshness time is advanced only by a fetch that completed while the claims refspec was present in `remote.origin.fetch`. A fetch on a clone without the refspec leaves it unset. This is what keeps "configured and genuinely empty" (answer `[]`) distinguishable from "not configured" (must never answer `[]`): a fetch succeeds either way, so a timestamp advanced by any successful fetch would let an unconfigured clone serve an empty mirror and an executor would claim a task another cluster already holds.
4. **A fresh mirror answers the read.** `GET /api/v1/refs?prefix=…` lists the mirror, maps each `refs/claim-mirror/<key>` back to `refs/claims/<key>`, applies the existing path-component prefix rule, and reads each claim's commit message from the local object database. No `ls-remote` and no fetch runs for that request. The JSON body, the sort order, the metadata parsing and the status codes are exactly what the live path returns.
5. **A stale, absent or unconfigured mirror falls back to the origin.** When the last claims-specific fetch is unset or older than the staleness bound, the request takes the existing live path unchanged. The bound is twice the configured pull interval, with a floor of 60 seconds; it is derived, never configured separately.
6. **A local write updates the mirror.** A successful claim `PUT` or `DELETE` updates or removes the corresponding `refs/claim-mirror/<key>` ref locally, so the next read on that instance reflects it without waiting for a pull. A concurrent fetch that overwrites the mirror with a value the origin advertised earlier is acceptable and self-heals on the next cycle.
7. **The answer names its source.** Each refs read logs exactly one INFO line naming `served_from=mirror` or `served_from=origin` and, when served from the mirror, its age in seconds.
8. **Cross-cluster visibility is bounded, not instant.** A claim written by another cluster becomes visible to this instance's mirror-served read after at most one pull cycle. Within a cycle the local answer is the previous cycle's truth, which is inside the lease semantics the endpoint serves.
9. **Docs and changelog** describe the mirror, the fallback and the visibility bound.

## Constraints

- The `GET /api/v1/refs` response body, key set, ordering and status codes are unchanged; so is the `400` for a glob-bearing prefix and for an invalid prefix.
- The served branch is never fetched into, reset or pushed by a ref read; the served branch's tracking ref is unchanged by any ref operation.
- The mirror lives in `refs/claim-mirror/*`, a namespace the default heads refspec `+refs/heads/*:refs/remotes/origin/*` cannot reach except through an upstream branch literally named `claims/…`. That disjointness is deliberate: it keeps a claim from ever being mistaken for a remote branch and vice versa.
- A ref operation never writes configuration and never writes `refs/remotes/origin/*`.
- `git-rest`'s existing metric names, labels and pre-initialised series are unchanged; this spec adds no metric.
- The read path must not take the repository mutex: it runs concurrently with the puller and with file operations, as the claim endpoints already do. Reading a mirror ref concurrently with the fetch that updates it is safe — git publishes the objects before the ref.
- Git is driven by the system `git` binary, matching the existing client.
- Every git subprocess on the request path is bound by the request context.
- `autoRelease: false` — the release tag is cut manually after merge.
- **Supersession, stated exactly.** This spec supersedes spec 018's assumption that the puller never sees claim refs, and its AC8(i) requirement that a claim written directly in the origin appears in the next `GET` **with no pull cycle in between** — under this spec that visibility is bounded by one refresh cycle by design. Everything else in 018 is unchanged and still holds: the endpoint contract, the `409`/`400` mapping, the no-retry rule, the `refs/claims/` namespace guard, its AC8(ii) list/fetch behaviour, and its AC9 invariant that a ref operation leaves `remote.origin.fetch` and `refs/remotes/origin` untouched — which this spec's own AC9 re-asserts. 018's `TestClaimListRealRepo` and `TestClaimListFetchBySha` must still pass unchanged: both run against a client with no fresh mirror, so they take the live path — which is precisely why the fallback must not be conditioned on anything except the claims-specific freshness signal.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection |
|---|---|---|---|
| Origin unreachable while the mirror is fresh | Read answers `200` from the mirror, `served_from=mirror` | None needed; the next successful pull refreshes | Log line per request; the puller's own fetch error |
| Origin unreachable and the mirror is stale | Read falls back to the live path and fails as it does today (`500`) | Restore the origin; the puller heals the mirror | `served_from=origin` plus the error log |
| **Refspec absent or the configuration step failed** | **The freshness time stays unset, so every read takes the live path (`served_from=origin`) — never an empty mirror and never a silent `[]`** | Fix the clone's config and restart | `served_from=origin` on every read; the startup warning |
| Pull succeeds at fetch and fails at merge / rescue | The mirror is fresh and reads serve it; the failure is the pull's own problem | Unblock the pull; the existing pull-error signal carries it | `served_from=mirror` with a small age while pull errors are logged |
| Puller wedged so the mirror never refreshes | After the bound, reads take the live path: correct but slow | Restart or unblock the puller | Mirror age stops advancing in `served_from=mirror` lines |
| A claim deleted on the origin but not yet pruned | It stays listed until the next pull cycle | None; bounded by the pull interval | Cross-check against the origin on a `served_from=origin` read |
| Another cluster writes between two pull cycles | The local answer is one cycle behind | None; bounded by the pull interval | Not visible locally; by design |
| A fetch fails mid-cycle (partial progress) | The previous mirror is retained; refs already updated stay updated; the freshness time is not advanced | Next cycle retries | Puller fetch error |
| Concurrent fetch overwrites a just-written mirror ref with an older advertised value | That instance's next read can be one cycle stale for that key | Self-heals next cycle | — |
| The local mirror update fails after a successful origin write | The origin is correct and authoritative; this instance's next read misses the claim it just wrote (a PUT) or still lists a released one (a DELETE) until the next pull | Self-heals next cycle; the origin's state is never wrong | The failed local ref update is logged |
| Claim commit object missing locally while its mirror ref exists | The request falls back to the live path rather than returning partial data | Next pull refetches | `served_from=origin` |
| `remote.origin.prune` prunes a branch deleted upstream | Correct: the remote no longer advertises it. The served branch's tracking ref is affected only if that branch is gone upstream, where the pull already fails | Re-create the upstream branch | `rev-parse @{u}` failure |
| An upstream branch literally named `claims/…` | The heads refspec would write `refs/remotes/origin/claims/…`, which is disjoint from `refs/claim-mirror/*`; the branch is a branch, the mirror is a mirror | — | `for-each-ref refs/remotes/origin/claims/` non-empty |
| Wall clock steps backwards | The mirror can look fresh for longer than the bound and serve a mirror older than intended | Self-heals on the next fetch | Mirror age in `served_from=mirror` |

## Security / Abuse Cases

- **Attacker controls:** the `prefix` query, as today. The mirror adds no new input: the ref list is produced by `git fetch` from the configured origin, not by the caller.
- **Trust boundary:** unchanged. Claim refs remain reachable only under `refs/claims/`, and the endpoints still cannot write outside it.
- **Refspec injection:** the claims refspec is a package constant, never built from request input.
- **Resource exhaustion:** the mirror is one ref per live claim, a few hundred bytes each, and pruning bounds it. Fetching claim commits adds their objects to the same object database the per-request fetch already populated.
- **Metric cardinality:** unchanged — no new label carries a claim key.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Fetch-refspec configuration: idempotent startup step writing `remote.origin.fetch` and `remote.origin.prune`, called from `createGitClient` so it precedes the boot sync, with the twice-applied and config-failure tests | 1 | 1 | — |
| 2 | Puller refresh: the existing fetch now carries and prunes claim refs; record the last claims-specific successful fetch time (fetch step only, refspec verified present); the refresh/prune test | 2, 3 | 2 | 1 |
| 3a | Read path: serve from the mirror with the existing prefix/metadata/status contract, fall back on stale / absent / unconfigured / missing-object, one `served_from=` INFO line carrying `age_s` per request; the no-network, stale-fallback and response-contract tests | 4, 5, 7 | 3, 4, 5, 8 | 2 |
| 3b | Local write path and visibility bound: update the mirror ref on `PUT`/`DELETE`, the read-after-write test, the cross-cluster bound test, the sync-state invariant test | 6, 8 | 6, 7, 9 | 3a |
| 4 | `docs/api.md` section, `CHANGELOG.md` `## Unreleased` bullet, `make precommit` green | 9 | 10, 11 | 3b |

Rationale: prompt 1 establishes the configuration every later prompt depends on and is independently testable. Prompt 2 proves the fetch actually carries and prunes the namespace before any read depends on it, and pins the freshness signal so the read path can trust it. Prompt 3a is the read behaviour — the largest surface — and prompt 3b is the smaller write-side and visibility work that depends on it, split so neither prompt has to hold the whole graph. Prompt 4 documents the final contract. AC12 (rung 1) and AC13 (rung 2) are operator rungs run after merge and release; no prompt covers them.

## Do-Nothing Option

Every claim read keeps costing a network round trip, and the executor's 30 s cap keeps killing the child under load, so the reconcile pass keeps covering a fraction of the corpus and the queue stays an hour deep. The alternative fixes are worse: raising the caller cap trades a fast `500` for a long-held connection, and giving every cluster's executor direct origin credentials removes the guarded namespace that spec 018 exists to provide.
