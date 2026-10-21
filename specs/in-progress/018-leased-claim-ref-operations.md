---
status: prompted
approved: "2026-10-21T18:03:32Z"
generating: "2026-10-21T18:03:33Z"
prompted: "2026-10-21T18:16:37Z"
branch: dark-factory/leased-claim-ref-operations
---

## Summary

- git-rest gains three endpoints that list, create/renew and delete **claim refs** under `refs/claims/` on the origin, so agent executors on several clusters can claim a task without each holding GitHub push credentials.
- Every write is a compare-and-swap: the caller names the sha it expects the ref to hold (empty = "must not exist"), and the origin's own ref update decides the winner. The loser gets `409` and git-rest never retries it.
- Each claim is a commit on the empty tree whose message is the claim metadata (`cluster`, `task_id`, `lease_until`) plus a server-generated random `nonce`, so every claim commit is unique and the metadata travels with the ref and `GET` reads it back from the origin.
- Only `refs/claims/` is reachable through these endpoints. Any other ref name — `heads/main`, a traversal like `claims/../heads/main`, an empty key — is rejected with `400` before anything is pushed; the served branch is never touched.
- A new counter `git_rest_ref_requests_total{op,result}` makes claim traffic and lost races visible on `/metrics`.

## Problem

Agent tasks are moving from a central cluster assigner to pull-based claims: each cluster's executor claims a task by atomically creating a ref on the shared vault origin, renews it while the task runs, and deletes it when done. A spike proved the origin (GitHub) accepts such refs with `--force-with-lease`, including the empty lease for "create only if absent". But executors do not talk to GitHub directly — every cluster already reaches the vault through its git-rest instance, which today serves only file CRUD on the served branch. Without a ref operation in git-rest there is no way for an executor to claim a task, and the claim-based scheduler (and with it "a task runs on exactly one cluster" and "a lost cluster loses no task") cannot start.

## Goal

A git-rest instance with an origin remote serves claim-ref operations restricted to `refs/claims/`. Two git-rest instances on different clusters, racing the same claim key against one origin, produce exactly one winner per race; the winner learns the new ref sha, every loser gets `409`, and nobody retries. Claim metadata written by a `PUT` is readable by any instance through `GET` straight from the origin. No request through these endpoints can create, move or delete a ref outside `refs/claims/`, and the served branch's sha is identical before and after any sequence of claim operations.

## Non-goals

- No lease-expiry enforcement. git-rest stores `lease_until` verbatim and never compares it to a clock; takeover of an expired claim is the caller's decision, expressed by passing the expired claim's sha as `expected_sha`.
- No executor claim client, renewal loop or takeover logic — that is the executor's work.
- No typed vault API wrapper around these endpoints.
- No ref namespace other than `refs/claims/` — no tags, no branches, no `refs/heads/*` writes.
- No single-ref `GET /api/v1/refs/claims/<key>` endpoint; the list endpoint with a narrower `prefix` covers lookup.
- No server-side retry of any ref push, for any failure class.
- No claim support in local-only mode (no origin remote); the endpoints answer `500` there.
- No change to the puller, the file endpoints, or the repository's configured fetch refspec.
- No new scenario: the behaviour is fully reachable with in-process tests against local bare repositories plus the rung-1 recipe below.
- Do NOT add a configuration flag to enable/disable the endpoints or to change the namespace — `refs/claims/` is invariant; if a future consumer demands another namespace, that is a separate spec.

## Assumptions

- The origin performs the ref update as a compare-and-swap: `git push --force-with-lease=<ref>:<expected>` is rejected client-side with `stale info` when the advertised ref differs, and server-side (`remote rejected`, ref lock / "expected" mismatch) when two pushes interleave after advertisement. The spike proved GitHub accepts claim refs with an empty lease.
- The served repository has a git author identity configured (`--git-user-name` / `--git-user-email` in cluster, `git config` locally); `git commit-tree` needs it.
- The default fetch refspec (`+refs/heads/*:refs/remotes/origin/*`) does not include `refs/claims/*`, so the puller never sees claim refs and every claim read must go to the origin explicitly (`ls-remote` plus an explicit fetch of the claim commits).
- Every claim commit message carries a server-generated random `nonce` (alongside `cluster`, `task_id`, `lease_until`), so no two `PUT`s ever produce the same commit sha. Exclusivity therefore rests only on the lease: a repeated `PUT` with a stale or empty `expected_sha` against an existing ref gets `409`, whoever sends it. The request contract is unchanged; `nonce` is not returned by `GET`.
- AC22(b) sends both concurrent curls to one pod. If ref operations share that pod's file lock, AC22(b) measures only the local compare-and-swap path, not interleaved pushes at the origin. GitHub's behaviour under interleaved pushes rests on the completed spike, where 10 of 10 concurrent empty-lease creates had exactly one winner; the two-clone tests (AC1–AC3, AC16, AC17) cover interleaving against a local bare origin.
- Callers already reach git-rest through the `/api/v1` subrouter and send the gateway headers when `--gateway-secret` is set.
- Claim keys follow the agreed shapes `claims/repo/<owner>-<repo>` and `claims/task/<id>`, so keys contain `/`; git-rest validates the full ref name, not a fixed shape.

## Acceptance Criteria

Container-executable (unit / integration tests and greps):

- [ ] AC1 — **create race.** A Go test builds one local bare origin and **two independent clones**, each served by its own git client instance (modelling two clusters; correctness must not come from an in-process lock). Per iteration, both contenders concurrently `PUT` a fresh key with empty `expected_sha` and **distinct `cluster` values**. Over **100 iterations**: every iteration has exactly one success and one lease-mismatch result (mapped to `409`), and the origin's `git rev-parse refs/claims/<key>` equals the winner's returned sha. Evidence: `go test -race -count=1 -run 'TestClaim(Create|Renew|Delete)Race' ./...` exits 0 (this AC's test: `TestClaimCreateRace`); the test fails on any iteration with 0 or 2 winners.
- [ ] AC2 — **renew race / stale sha.** Same two-clone setup; the key exists at sha `S0`. Per iteration both contenders concurrently `PUT` with `expected_sha=S0` and distinct `cluster` values. Over 100 iterations exactly one wins, the other gets `409`, and the origin ref equals the winner's sha. Additionally a single `PUT` with a stale `expected_sha` returns `409` and the origin ref sha is unchanged (`git rev-parse` before == after). Evidence: `go test -race -count=1 -run 'TestClaim(Create|Renew|Delete)Race' ./...` exits 0 (this AC's test: `TestClaimRenewRace`).
- [ ] AC3 — **delete race.** Same setup; the key exists at sha `S0`. Per iteration both contenders concurrently `DELETE` with `expected_sha=S0`. Over 100 iterations exactly one returns `200`, the other `409`, and `git -C <bare> rev-parse --verify --quiet refs/claims/<key>` exits non-zero afterwards. A `PUT`/`DELETE` with non-empty `expected_sha` against an absent ref returns `409`, and a second `DELETE` of the already-deleted key returns `409`. Evidence: `go test -race -count=1 -run 'TestClaim(Create|Renew|Delete)Race' ./...` exits 0 (this AC's test: `TestClaimDeleteRace`).
- [ ] AC4 — **namespace guard.** An HTTP-level test sends `PUT` and `DELETE` to each of: `heads/main`, `tags/x`, `claims/../heads/main` (literal, unnormalized), `claims/%2E%2E/heads/main`, `claims/` (empty key), `claims` (no key), `claims/a..b`, `claims/x.lock`, `claims/a b`, `claims/a~1`, `claims/a:b`, `claims//x`, `claims/x/`. Every request returns `400`. Evidence: (a) handler-level test with a recording fake git client: after the whole table the fake has observed **zero** calls to any ref-write or origin-contacting operation (list, create/renew, delete); (b) integration test against a real bare origin: `git -C <bare> for-each-ref` output is byte-identical before and after the table.
- [ ] AC5 — **request validation.** `PUT` with a missing/empty `cluster`, missing/empty `task_id`, `lease_until` that is not RFC3339, an `expected_sha` that is neither empty nor a full-length lowercase hex object id, a body that is not JSON, or a body over 64 KiB returns `400` (`413` for the oversized body); `DELETE` without `expected_sha` returns `400`; `GET` with a missing `prefix`, a `prefix` not starting with `claims/`, a `prefix` containing `..`, or a `prefix` containing any glob metacharacter `*`, `?` or `[` (rows: `claims/*`, `claims/task/?`, `claims/[a]`) returns `400`. Evidence: HTTP status per row in a table-driven test; bare repo ref list unchanged.
- [ ] AC6 — **served branch untouched.** A test records `git -C <bare> rev-parse refs/heads/<served branch>` and the full `for-each-ref refs/heads refs/tags` listing, then runs create → renew → stale renew → delete → every AC4 request, then re-reads both. Evidence: both values are byte-identical before and after; test exit 0.
- [ ] AC7 — **metadata round-trip.** After `PUT` of `{cluster:"nuke", task_id:"t-42", lease_until:"2026-10-21T12:05:00Z"}`: `GET /api/v1/refs?prefix=claims/` returns a JSON array containing exactly one object whose key set is exactly `ref`, `sha`, `cluster`, `task_id`, `lease_until` — **no `nonce` key** — with `ref:"refs/claims/<key>"`, `sha:<sha returned by PUT>`, and the three written values; and `git -C <bare> cat-file -p <sha>` shows `tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904`, no `parent` line, and a message that parses as JSON with **exactly the 4 keys** `cluster`, `task_id`, `lease_until`, `nonce`, the first three holding the written values and `nonce` matching `^[0-9a-f]{32}$`. Evidence: test assertions on both outputs.
- [ ] AC8 — **reads are live from the origin.** Two tests.
  - **(i) Real-repo test `TestClaimListRealRepo`** against a local bare origin:
    - A claim ref created directly in the origin (`git update-ref`, not through git-rest) appears in the next `GET`, and one deleted directly disappears from it, with no pull cycle in between.
    - Metadata parsing, one ref per commit message written directly into the origin: (a) the 4-key form `{cluster,task_id,lease_until,nonce}` → the three fields listed; (b) the 4 keys plus an unknown key `{…,"extra":"x"}` → the three fields listed, the unknown key ignored and absent from the response; (c) the legacy 3-key form without `nonce` → valid, the three fields listed; (d) a non-JSON message → listed with its `ref` and `sha` and empty-string `cluster`/`task_id`/`lease_until`.
    - **Prefix is path-component based.** Fixture refs: `claims/task/1`, `claims/task/10`, `claims/tasks/1`, `claims/repo/a/b`. `prefix=claims/task/1` returns exactly `refs/claims/task/1`; `prefix=claims/task` returns exactly `refs/claims/task/1` and `refs/claims/task/10` (never `refs/claims/tasks/1`); `prefix=claims/repo/a` returns exactly `refs/claims/repo/a/b`.
    - For every returned object, its metadata equals the message of `git -C <bare> cat-file -p <object.sha>`.
    - With no claim refs, `GET` returns `[]` (not `null`).
  - **(ii) Recording-fake test `TestClaimListFetchBySha`:** the fake lists two refs with shas `A` and `B`; the fake records that each commit was fetched/resolved by its listed sha (`A`, `B`), never by ref name. **Vanished-sha row:** when fetching `B` fails because the ref moved or was deleted between list and fetch, `GET` returns `200` with only the `A` entry and logs one WARN naming the dropped ref; it never returns `500` for this case.
  - Evidence: both tests exit 0 under `go test -race -count=1 -run 'TestClaimList(RealRepo|FetchBySha)' ./...`, asserting on the JSON body, the fake's recorded calls and the captured log.
- [ ] AC9 — **no side effects on the served clone's sync state.** After a `GET` and a `PUT`, `git -C <served clone> config --get-all remote.origin.fetch` is unchanged, and `git -C <served clone> for-each-ref refs/remotes/origin` is unchanged. Evidence: before/after equality in a test.
- [ ] AC10 — **metric.** `git_rest_ref_requests_total` is exposed with all 12 series `op∈{get,put,delete} × result∈{200,400,409,500}` at value 0 before any request. One successful `PUT` increments `{op="put",result="200"}` by exactly 1, and one lease-mismatch `PUT` increments `{op="put",result="409"}` by exactly 1, with no other series of this metric changing. Evidence: counter read via `prometheus/testutil` (or equivalent) before and after each call.
- [ ] AC11 — **bounded HTTP path label.** After `PUT`s to three distinct claim keys, `git_rest_http_requests_total` carries no `path` label containing a claim key; all ref requests are recorded under one fixed path template per route. Evidence: test that gathers the registry and asserts zero series whose `path` label contains any of the three keys.
- [ ] AC12 — **auth applies.** With a gateway secret configured, a `PUT` without the `X-Gateway-Secret` header is rejected by the existing middleware (`401`, or `500` when `X-Gateway-Initator` is missing — the middleware's existing contract) and the bare repo ref list is unchanged, and all 12 `git_rest_ref_requests_total` series read the same value after the rejected request as before it. Evidence: HTTP status + ref-list equality + per-series counter equality in a test.
- [ ] AC13 — **docs.** `README.md` `## Features` lists the claim-ref endpoints, and `docs/api.md` documents `GET /api/v1/refs`, `PUT /api/v1/refs/claims/{key}` and `DELETE /api/v1/refs/claims/{key}` with request body, response body and the `200`/`400`/`409`/`500` table, including "a `409` must not be retried". Evidence: `grep -c 'api/v1/refs' README.md` ≥ 1 and `grep -c 'api/v1/refs' docs/api.md` ≥ 3.
- [ ] AC14 — **changelog.** `CHANGELOG.md` has a `## Unreleased` section above `## v0.31.0` containing a `feat:` bullet naming the claim-ref endpoints. Evidence: `awk '/^## /{sec=$0} /api\/v1\/refs/{print sec; exit}' CHANGELOG.md` prints `## Unreleased`.
- [ ] AC15 — `make precommit` exits 0. Evidence: exit code.
- [ ] AC16 — **identical-body create race.** AC1's two-clone setup, but both contenders send a **byte-identical** body (same `cluster`, `task_id`, `lease_until`, empty `expected_sha`). Over 100 iterations: exactly one `200`, one `409`, and the origin ref equals the winner's sha. Evidence: `go test -race -count=1 -run 'TestClaim(Create|Renew|Delete)Race' ./...` exits 0 (this AC's test: `TestClaimCreateRaceIdenticalBody`).
- [ ] AC17 — **identical-body renew race.** AC2's setup with byte-identical bodies (`expected_sha=S0`, same `cluster`, `task_id`, `lease_until`). Over 100 iterations: exactly one `200`, one `409`, origin ref equals the winner's sha. Evidence: same command (this AC's test: `TestClaimRenewRaceIdenticalBody`).
- [ ] AC18 — **sequential identical repeat.** A create with empty `expected_sha` returns `200` and sha `S1`; the byte-identical `PUT` sent again returns `409`, and `git -C <bare> rev-parse refs/claims/<key>` still equals `S1`. Evidence: `go test -race -count=1 -run 'TestClaimRepeatIdenticalCreate' ./...` exits 0, asserting status and sha.
- [ ] AC19 — **nonce makes every commit unique.** Create → delete → create on one key with byte-identical bodies yields two `200`s whose returned shas differ, and the two commits' `nonce` values differ and each match `^[0-9a-f]{32}$`. A request body that carries a `nonce` key returns `200`; the key is ignored and does not set the commit's nonce (the committed `nonce` differs from the supplied value). Evidence: `go test -race -count=1 -run 'TestClaimNonceUnique' ./...` exits 0, asserting on statuses, shas and `cat-file -p` messages.
- [ ] AC20 — **timed-out renew recovery.** A holder renews (`expected_sha=S0`) and gets `200` with sha `S1`; the test then discards that response (modelling a lost response / timeout) and repeats the byte-identical renew. The repeat returns `409` (the ref moved off `S0`); the origin ref still equals `S1`; and `GET` lists the key with `sha == S1` and a (`cluster`, `task_id`, `lease_until`) triple equal to the one the holder wrote — the information a holder needs to conclude it still holds the claim and adopt `S1` as its next `expected_sha`. A follow-up renew with `expected_sha=S1` returns `200`. Evidence: `go test -race -count=1 -run 'TestClaimRenewTimeoutRecovery' ./...` exits 0, asserting statuses, sha and the GET body.

Operator-executable:

- [ ] AC21 — **rung 1.** The rung-1 recipe in `## Verification` prints, in order, `HTTP 200` (create), `HTTP 409` (byte-identical duplicate create), `HTTP 200` (renew), `HTTP 409` (stale renew), `HTTP 409` (stale delete), `HTTP 200` (delete), then `HTTP 400` for each of the 5 namespace probes; `for-each-ref refs/claims/` on the origin prints 0 lines at the end; `branch-unchanged` is printed; and the metric lines read `{op="put",result="200"} 2`, `{op="put",result="409"} 2`, `{op="put",result="400"} 5`, `{op="delete",result="409"} 1`, `{op="delete",result="200"} 1`, `{op="get",result="200"} 1`. Evidence: recipe stdout.
- [ ] **Post-Deploy (Rung-2):** (AC22) on nuke dev, against the `vault-obsidian-openclaw` git-rest: (a) the curl sequence create / byte-identical duplicate create / renew / delete on key `claims/test/verify-<unix-ts>` returns `200` / `409` / `200` / `200`, and `git ls-remote <origin> 'refs/claims/test/*'` lists the ref with the returned sha after create and renew and returns 0 lines after delete; (b) **10 runs** of two concurrent create `curl`s (backgrounded, same body except `cluster`) on a fresh key `claims/test/race-<unix-ts>-<i>` each yield exactly one `200` and one `409`, and the winner is deleted with its returned sha; (c) across (a)+(b) `/metrics` shows `git_rest_ref_requests_total{op="put",result="200"}` +12, `{op="put",result="409"}` +11, `{op="delete",result="200"}` +11; (d) `git ls-remote <origin> 'refs/claims/test/*'` returns 0 lines at the end. Evidence: curl status codes, `ls-remote` output, metric deltas.
  - `deploy_check:` `kubectlnukedev -n dev get --raw /api/v1/namespaces/dev/pods/vault-obsidian-openclaw-0:9090/proxy/metrics | grep -m1 '^git_rest_build_info' | sed -E 's/.*commit="([^"]+)".*/\1/' | grep -E '^[0-9a-f]{7,40}$'`
  - `deploy_target:` `$(git -C ~/Documents/workspaces/git-rest rev-parse --short vX.Y.Z^{commit})` (`vX.Y.Z` = the release tag that carries this spec)

## Verification

### Container-executable (prompt time)

```
make precommit
go test -race -count=1 ./...
go test -race -count=1 -run 'TestClaim(Create|Renew|Delete)Race' ./...   # AC1-3, AC16-17
go test -race -count=1 -run 'TestClaim(List(RealRepo|FetchBySha)|RepeatIdenticalCreate|NonceUnique|RenewTimeoutRecovery)' ./...   # AC8, AC18-20
grep -c 'api/v1/refs' README.md          # >= 1
grep -c 'api/v1/refs' docs/api.md        # >= 3
awk '/^## /{sec=$0} /api\/v1\/refs/{print sec; exit}' CHANGELOG.md   # ## Unreleased
```

### Operator-executable — rung 1 (local binary, temp bare origin)

Follows `docs/verifying-specs.md` rung 1. Run from the worktree that holds the change; the served branch is named `test/claims-verify` so the recipe replays on a host whose push guardrail gates `master`/`main`.

```bash
set -e
REPO_UNDER_TEST="${REPO_UNDER_TEST:-$(git rev-parse --show-toplevel)}"
BASE=/tmp/git-rest-claims-verify && rm -rf "$BASE" && mkdir -p "$BASE" && cd "$BASE"
BRANCH=test/claims-verify

git init -q --bare -b "$BRANCH" origin.git
git clone -q origin.git seed && cd seed
git config user.email verify@local && git config user.name verify
git commit -q --allow-empty -m init && git push -q origin "$BRANCH"
cd "$BASE" && git clone -q origin.git work
git -C work config user.email verify@local && git -C work config user.name verify
BRANCH_BEFORE=$(git -C origin.git rev-parse "refs/heads/$BRANCH")

cd "${REPO_UNDER_TEST}" && go build -o /tmp/git-rest-claims-bin .
/tmp/git-rest-claims-bin -listen=:18450 -repo="$BASE/work" -pull-interval=24h -v=1 > "$BASE/run.log" 2>&1 &
sleep 2
U=http://localhost:18450/api/v1/refs
K=claims/test/k1
body() { printf '{"expected_sha":"%s","cluster":"%s","task_id":"t1","lease_until":"2026-10-21T12:05:00Z"}' "$1" "$2"; }
put()  { curl -s -o "$BASE/out.json" -w "$1 HTTP %{http_code}\n" -X PUT -d "$(body "$2" "$3")" "$U/$K"; }
del()  { curl -s -o /dev/null -w "$1 HTTP %{http_code}\n" -X DELETE "$U/$K?expected_sha=$2"; }

put create "" nuke;          S1=$(jq -r .sha "$BASE/out.json")    # HTTP 200
put dup-create "" nuke                                            # HTTP 409 (byte-identical body)
put renew "$S1" nuke;        S2=$(jq -r .sha "$BASE/out.json")    # HTTP 200
put stale-renew "$S1" sun                                         # HTTP 409

curl -s "$U?prefix=claims/" | jq .                                # one object: sha == $S2, cluster == "nuke", task_id == "t1"
git -C "$BASE/origin.git" cat-file -p "$S2"                       # tree 4b825dc…, no parent line, JSON message

del stale-delete "$S1"                                            # HTTP 409
del delete "$S2"                                                  # HTTP 200

# Namespace probes: --path-as-is stops curl from normalizing `..`. Each prints HTTP 400.
for P in "heads/$BRANCH" heads/main 'claims/../heads/main' 'claims/%2E%2E/heads/main' 'claims/'; do
  curl -s --path-as-is -o /dev/null -w "probe $P HTTP %{http_code}\n" -X PUT -d "$(body '' nuke)" "$U/$P"
done

# Assertions while the process is still alive (counters are per-process, pre-initialised to 0).
git -C "$BASE/origin.git" for-each-ref 'refs/claims/' | wc -l                                   # 0
test "$(git -C "$BASE/origin.git" rev-parse "refs/heads/$BRANCH")" = "$BRANCH_BEFORE" && echo branch-unchanged
git -C "$BASE/origin.git" for-each-ref --format='%(refname)' | grep -vc "^refs/heads/$BRANCH$" || true   # 0
curl -s http://localhost:18450/metrics | grep '^git_rest_ref_requests_total'
#   {op="put",result="200"} 2 · {op="put",result="409"} 2 · {op="put",result="400"} 5
#   {op="delete",result="409"} 1 · {op="delete",result="200"} 1 · {op="get",result="200"} 1

# Teardown — a survivor keeps answering :18450 with stale evidence.
kill "$(pgrep -f /tmp/git-rest-claims-bin)" 2>/dev/null || true
```


### Operator-executable — rung 2 (nuke dev)

Follows `docs/verifying-specs.md` rung 2 (HTTP contract changed → rung 2 required). Preconditions: release tag cut manually (`autoRelease: false`), image `bborbe/git-rest:vX.Y.Z` pushed, `VERSION` pin in `~/Documents/workspaces/nuke/git-rest/Makefile` bumped and merged, `BRANCH=dev make apply` from the nuke deploy worktree, StatefulSets rolled.

- Freshness: the `deploy_check` / `deploy_target` pair on AC22 (read-only, API-server proxy; no `exec`, no `port-forward`).
- Origin URL: `kubectlnukedev -n dev get statefulset vault-obsidian-openclaw -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="GIT_REMOTE_URL")].value}'` (if the value is sourced from a Secret/ConfigMap, the operator resolves it without printing any credential).
- Base URL and gateway secret for the dev openclaw git-rest are resolved by the operator at verification time; the secret is passed from its store into the `X-Gateway-Secret` header and never echoed.
- Run AC22's sequence on `claims/test/verify-$(date +%s)` and its 10 race runs on `claims/test/race-$(date +%s)-<i>`; the final `DELETE`s leave no `refs/claims/test/*` ref on the origin. If any step returns an unexpected status, delete the test ref with its last known sha before stopping.

Rung 3 (nuke prod) promotes after rung 2 per `docs/verifying-specs.md` and is tracked by the owning vault task, not by this spec's ACs.

## Desired Behavior

1. **List.** `GET /api/v1/refs?prefix=claims/<optional sub-prefix>` asks the origin for claim refs, fetches each listed commit **by its listed sha** (never by ref name, never via the configured fetch refspec), and returns a JSON array of `{ref, sha, cluster, task_id, lease_until}` sorted by `ref`. The prefix matches whole path components: a prefix ending in `/` matches every ref under that directory; any other prefix matches the ref equal to `refs/<prefix>` and refs under `refs/<prefix>/`, so `claims/task/1` never matches `claims/task/10`. A prefix containing `*`, `?` or `[` is rejected with `400`. Metadata parsing reads `cluster`, `task_id`, `lease_until`, ignores `nonce` and any unknown key, and accepts the legacy 3-key form without `nonce`; `nonce` never appears in the response. A commit whose message is not JSON is still listed, with empty metadata fields and one WARN log line naming the ref. If a listed sha can no longer be fetched because the ref moved or was deleted between list and fetch, that entry is dropped and one WARN line names the ref; the response is still `200`, never `500`. No claim refs → `[]`.
2. **Create / renew.** `PUT /api/v1/refs/claims/<key>` with body `{expected_sha, cluster, task_id, lease_until}` builds a parentless commit on the empty tree whose message is exactly the JSON object `{"cluster":…,"task_id":…,"lease_until":…,"nonce":…}`, where `nonce` is 128 bits from `crypto/rand` rendered as 32 lowercase hex characters, generated fresh per request (a `nonce` key in the request body is ignored — the request still succeeds — and never used), then pushes it to `refs/claims/<key>` on the origin with `--force-with-lease=refs/claims/<key>:<expected_sha>`. Empty `expected_sha` means the ref must be absent. Success → `200` with `{"ref":"refs/claims/<key>","sha":"<new sha>"}`.
3. **Delete.** `DELETE /api/v1/refs/claims/<key>?expected_sha=<sha>` pushes a deletion of `refs/claims/<key>` with the same lease. Success → `200` with `{"ok":true}`.
4. **Outcome mapping, no retry.** A push rejected because the ref no longer matches `expected_sha` — client-side `stale info`, or a server-side `remote rejected` whose reason is a ref-lock / expected-value mismatch — returns `409` with a JSON error naming the ref. A `PUT` or `DELETE` with a non-empty `expected_sha` against an absent ref is the same class — git reports `stale info` or `remote ref does not exist` — and returns `409`. Every other failure (no origin remote, transport/auth error, hook decline, timeout, identity missing) returns `500` with a JSON error naming the failing step. git-rest pushes each request at most once; it never retries a `409` or a `500`.
5. **Namespace guard.** The ref name is taken from the URL path after percent-decoding, without path cleaning. It must start with `claims/`, have a non-empty key, contain no `.`/`..` component, no empty component, and pass `git check-ref-format` as `refs/claims/<key>`. Every `PUT`/`DELETE` under `/api/v1/refs/` reaches the ref handler (no router-level `404`/`405` for a malformed name), so a violation is answered — and counted — as `400`, before any git subprocess that writes or contacts the origin runs. The served branch and every ref outside `refs/claims/` are never written by these endpoints.
6. **Routing and auth.** The three routes live on the existing `/api/v1` subrouter, so the gateway-secret middleware applies exactly as it does to file routes. The HTTP request metric records ref requests under fixed path templates (one per route), never the raw key.
7. **Metric.** `git_rest_ref_requests_total{op,result}` counts every request that reaches a ref handler, `op` ∈ `get|put|delete`, `result` = the HTTP status code returned; the 12 series `{get,put,delete} × {200,400,409,500}` are pre-initialised to 0. Requests rejected by the auth middleware leave all 12 series unchanged.
8. **Docs and changelog.** `README.md` and `docs/api.md` document the endpoints, bodies, status table and the no-retry rule; `CHANGELOG.md` gains a `## Unreleased` `feat:` bullet.

## Constraints

- File endpoints (`/api/v1/files/...`), the puller, readiness and the probes keep their current behaviour and tests.
- The served branch is never pushed, reset or fetched-into by a ref operation; `remote.origin.fetch` and `refs/remotes/origin/*` of the served clone are unchanged by any ref operation (claim commits are fetched with an explicit refspec into a location that is not `refs/remotes/origin/*` and without writing `FETCH_HEAD` that the puller reads — exact local location agent decides at impl time).
- A file write's `git push` keeps pushing only the served branch; local copies of claim commits never become refs that a default push would publish.
- Lease correctness comes from the origin's compare-and-swap, never from git-rest's in-process lock. Whether ref operations share the existing file-operation lock is agent decides at impl time; AC1–AC3, AC16 and AC17 hold either way because they use independent clones.
- Every git subprocess of a ref request is bound by the request context; the existing server write timeout (60 s) is the upper bound.
- Existing metric names, labels and pre-initialised series are unchanged; the new counter is additive.
- Git is driven by the system `git` binary, matching the existing client.
- `autoRelease: false` — the release tag is cut manually after merge.

## Failure Modes

| Trigger | Expected behavior | Recovery | Reversibility / Concurrency |
|---|---|---|---|
| Two clusters `PUT` the same absent key at once | Origin accepts one; the other gets `stale info` or `remote rejected` → `409` | Loser does not retry; it moves to another task | Single winner by origin CAS |
| Renew with an `expected_sha` another holder already replaced | `409`; origin ref unchanged | Caller treats claim as lost (re-run after lost lease is allowed by the agent contract) | Reversible: no write happened |
| Holder's renew times out (`500`) after the origin accepted it, and the holder retries the byte-identical renew | `409`: the ref already moved off the stale `expected_sha` (to the holder's own commit, a different sha because of the random `nonce`) | Holder `GET`s and compares the (`cluster`, `task_id`, `lease_until`) triple it wrote itself; match → it still holds the claim and adopts the listed sha as its next `expected_sha`; no match → claim lost | Not retried by git-rest; AC20 |
| Delete of an already-deleted claim | `409` (`remote ref does not exist` / `stale info`) | Caller treats the claim as already released | Nothing to undo |
| Origin unreachable / auth failure / DNS (external unavailability) | `500` naming the push or ls-remote step; nothing retried | Caller retries on its own schedule; `GET` shows true state | Reversible |
| Push times out after the origin accepted it (partial progress) | `500`; the ref may already hold the caller's new commit | Caller `GET`s and compares the (`cluster`, `task_id`, `lease_until`) triple it wrote; match → adopt the listed sha; no match → not its claim | Ambiguous outcome resolved by read (AC20) |
| Origin hook declines `refs/claims/*` (schema/policy drift) | `500` naming `remote rejected` with the hook reason; not mapped to `409` | Operator fixes origin policy; visible as `{op="put",result="500"}` growth | Reversible |
| Origin rate-limits pushes | `500`; no retry inside git-rest | Caller backs off; metric shows `result="500"` | Reversible |
| A foreign tool writes a claim ref with a non-JSON message | Listed with empty metadata + WARN log naming the ref | Caller deletes it with its sha | Reversible |
| No origin remote (local-only mode) | `500` "no origin remote configured"; nothing written locally | Configure `--git-remote-url` | — |
| Git identity missing in the served clone | `500` from commit construction; nothing pushed | Set `--git-user-name` / `--git-user-email` | — |
| Many claim commits accumulate as loose objects in the served clone (resource exhaustion) | Each is a few hundred bytes on the empty tree; normal `git gc` reclaims unreachable ones | None in this spec | — |
| `lease_until` in the past or far future, or clocks skewed across clusters | Stored verbatim; git-rest never judges expiry | Takeover policy belongs to the executor | — |
| Crafted ref name (`claims/../heads/main`, `%2E%2E`, empty key) | `400`, no write subprocess, no origin contact | — | Nothing to undo |

## Security / Abuse Cases

- **Attacker controls:** the URL path (ref name), the `prefix` query, `expected_sha`, and the JSON body. All are validated before any git subprocess runs; values are passed to git as separate arguments, never through a shell.
- **Trust boundary:** these endpoints let an HTTP caller write refs on the shared origin with git-rest's credentials. The `refs/claims/` guard is therefore the security boundary — traversal (`..`, percent-encoded or literal, with `SkipClean` routing), empty components, refspec metacharacters (`:`, `+`, `^`, `~`, `*`, `?`, `[`, `\`, space, control characters) and `.lock` suffixes are rejected with `400`.
- **Refspec injection:** the push refspec is assembled only from the validated ref name and a sha produced locally by `git commit-tree`; `expected_sha` must be empty or a full-length lowercase hex object id, so no `:` or `+` reaches the refspec or the lease argument.
- **Message injection:** the commit message is produced by JSON-encoding the three validated fields plus the server-generated `nonce`; a field containing newlines or quotes cannot forge a `parent`/`tree` header or extra JSON keys, and a client cannot choose the `nonce` to force a sha collision with another holder's commit.
- **Hangs:** every subprocess is bound by the request context and the 60 s server write timeout; nothing retries forever.
- **Auth:** the gateway-secret middleware applies to all three routes, identical to file routes.
- **Metric cardinality:** the claim key never becomes a metric label value.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Git-layer claim operations: list from origin (ls-remote + explicit fetch, metadata parse), lease push for create/renew/delete, outcome classification (`409`-class vs other), ref-name validation; two-clone race tests and served-branch / fetch-refspec invariants against local bare repos | 1, 2, 3, 4, 5 | 1, 2, 3, 6, 7 (git side), 8, 9, 16, 17, 18, 19, 20 | — |
| 2 | HTTP layer: three handlers with body/query validation and status mapping, route wiring on the `/api/v1` subrouter, fixed path templates in the HTTP metric, `git_rest_ref_requests_total` with pre-initialised series | 4, 5, 6, 7 | 4, 5, 7 (HTTP side), 10, 11, 12 | prompt 1 |
| 3 | `README.md`, `docs/api.md`, `CHANGELOG.md` `## Unreleased` bullet; `make precommit` green | 8 | 13, 14, 15 | prompt 2 |

Rationale: prompt 1 establishes the lease semantics and proves them where they matter (two independent clones, one origin) with no HTTP noise; prompt 2 is a thin, testable adapter on top; prompt 3 documents the final contract once statuses are fixed. AC21–AC22 are operator rungs run after merge and release.

## Do-Nothing Option

Executors cannot claim tasks through git-rest. The alternatives are giving every cluster's executor direct GitHub push credentials for the vault repo (more secrets to distribute, and an unrestricted ref namespace instead of a guarded one) or keeping the central cluster assigner, which leaves "a powered-off cluster loses no task" unsolved and blocks sun from joining the pool under claims. Neither is acceptable for the goal of scheduling agent tasks across clusters by claim.
