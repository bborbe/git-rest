---
spec: ["026-batch-claim-ref-commit-reads"]
status: draft
created: "2026-11-09T11:20:00Z"
---

# Add the claim-ref listing probe and the changelog entry

<summary>
- Ships a reproducible rung-1 harness that boots the real binary against a temporary origin with 300 claim refs and counts the git processes a listing actually starts.
- The harness measures, never hard-codes: the `cat-file` count comes from a `git` shim's counter file and the ref count comes from the HTTP response body.
- The same harness run against a pre-fix binary reports 300 `cat-file` invocations, so it discriminates a fixed build from a broken one.
- It exercises both the live-origin path and the claim-mirror path and prints which one served the request.
- Records the fix in `CHANGELOG.md` under `## Unreleased`, creating the heading if the release bot has removed it.
- No production code changes in this prompt.
</summary>

<objective>
Add the operator-run rung-1 reproduction harness for the claim-ref listing process count and record the fix in the changelog. The harness is the only verification that can show the pre-fix and post-fix process counts differ on a real binary; the changelog entry makes the release carry the fix.
</objective>

<context>
Read `CLAUDE.md` and `docs/verifying-specs.md` (the rung ladder; this prompt ships the rung-1 tool, it does not run rung 1 in the container).

Coding guides (in-container paths): `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` and `/home/node/.claude/plugins/marketplaces/coding/docs/git-workflow.md`.

Read these files fully before writing anything:

- `main.go` — the CLI flags: `-repo` (required, path to the git repository), `-listen` (default `:9090`), `-pull-interval` (default `30s`, parsed by `libargument` as a duration), `-pull-timeout`, `-gateway-secret` (empty means the `/api/v1` routes are unauthenticated). Startup order is: `bootstrap` → `createGitClient` (which calls `ConfigureClaimMirror`) → `RecoverPendingWrites` → `syncOnStartup` (one `Pull`) → `service.Run`. Probe routes `/healthz` and `/readiness` are always unauthenticated.
- `pkg/git/claim_mirror.go` — `ConfigureClaimMirror` adds `+refs/claims/*:refs/claim-mirror/*` and enables pruning; `recordClaimMirrorFetch` records freshness only after a successful `Pull` and only when the refspec is present; `claimMirrorStalenessBound` is `2 * pull-interval` with a 60 s floor; `claimMirrorAge` reports fresh while `age <= bound`.
- `pkg/git/git.go` — `Pull` fails at `rev-parse --abbrev-ref --symbolic-full-name @{u}` when the branch has no upstream, before any fetch, so no mirror freshness is recorded in that case.
- `pkg/git/claim_refs.go` — `ListClaimRefs` logs `served_from=mirror` or `served_from=origin`.
- `pkg/handler/refs_get.go` — `GET /api/v1/refs?prefix=...` returns a JSON array of `ClaimRef`; `validateRefPrefix` requires a non-empty prefix.
- `hack/build-cluster.sh` — the existing shell style in this repo (`#!/usr/bin/env bash`, `set -euo pipefail`).

Why the harness must force the live path for its first run: `createGitClient` configures the claim mirror BEFORE `syncOnStartup` runs `Pull`. With a normal clone (branch has an upstream), that first pull succeeds and records mirror freshness, so the very first listing would be served from the mirror, not the live origin. To exercise the live path, the harness clones into a separate repository and runs `git branch --unset-upstream` on it before boot: `Pull` then fails at `@{u}` before fetching, no freshness is recorded, and `ListClaimRefs` takes the live path. The puller logs a warning once per interval; keep the interval long (`24h`) so it does not run again during the measurement. The mirror run uses a second, normally configured clone with a short interval so the startup pull populates and freshens the mirror.

The pre-fix binary is built by the operator from a clean `origin/master` checkout and passed via `GIT_REST_BIN`; the harness itself only measures and prints, so the same script reports `300` for a pre-fix binary and `1` for a post-fix one.
</context>

<requirements>

## 1. Write `hack/claim-ref-listing-probe.sh`

1.1 Create `hack/claim-ref-listing-probe.sh`, executable (`chmod 0755`), starting with `#!/usr/bin/env bash` and `set -euo pipefail`. Do not add a license header (the repo's `addlicense` target only covers `*.go`).

1.2 Structure the script so every temporary artifact lives under one `mktemp -d` directory removed by a `trap ... EXIT`. Preflight `command -v git curl jq` and exit non-zero naming the missing binary before any work. Resolve the repo root as `$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)`. Resolve the real git binary with `REAL_GIT=$(command -v git)` BEFORE any `PATH` change, and keep the absolute path.

1.3 Binary selection:
- If `GIT_REST_BIN` is set and non-empty, use it as-is (this is how the pre-fix comparison is run).
- Otherwise build the current tree into the temp directory: `(cd "$ROOT" && go build -o "$TMP/git-rest" .)`. Do not run `make build`; it shells out to Docker.

1.4 Seed the origin:
- `git init --bare -b main "$TMP/origin.git"`.
- Create a seed worktree, commit a file on branch `main`, add the bare as `origin`, push `main`, and `git -C "$TMP/origin.git" symbolic-ref HEAD refs/heads/main`.
- Create 300 claim commits in the bare with `git -C "$TMP/origin.git" -c user.name=... -c user.email=... commit-tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904 -m '<json>'` and point `refs/claims/success/task-%03d/phase-%d/%d` at each. The message must be valid claim JSON: `{"cluster":"probe","task_id":"t-<i>","lease_until":"2026-10-25T00:00:00Z","nonce":"<32 hex>"}`.
- Use a fixed count variable (`COUNT=300`); do not scatter the literal.

1.5 Install the counting shim in `$TMP/shim/git` (mode 0755):

```sh
#!/bin/sh
printf '%s\n' "$*" >> "$CLAIM_REF_SHIM_COUNTER"
exec "$CLAIM_REF_SHIM_REAL_GIT" "$@"
```

The shim MUST exec the absolute real-git path; resolving git inside the shim would recurse into itself.

1.6 Add helpers:
- `start_server <clone> <pull_interval> <logfile>`: boot `PATH="$TMP/shim:$PATH" CLAIM_REF_SHIM_COUNTER="$COUNTER" CLAIM_REF_SHIM_REAL_GIT="$REAL_GIT" "$BIN" -repo "<clone>" -listen "127.0.0.1:$PORT" -pull-interval "<interval>" > "<logfile>" 2>&1 &`, record the pid, and wait up to 30 s for `curl -fsS --max-time 5 "http://127.0.0.1:$PORT/healthz"` to succeed. Fail loudly with the log contents if the server does not come up.
- `stop_server`: kill the recorded pid and `wait` for it, tolerating an already-exited process.
- `count_cat_file`: `awk '/^cat-file /{n++} END{print n+0}' "$COUNTER"`.
- `fetch_refs`: `curl -fsS --max-time 30 "http://127.0.0.1:$PORT/api/v1/refs?prefix=claims%2Fsuccess%2F"`.

1.7 Run 1 — live origin:
- Clone the bare into `$TMP/live` and run `git -C "$TMP/live" branch --unset-upstream`.
- `start_server "$TMP/live" 24h "$TMP/live.log"`.
- Truncate the counter (`: > "$COUNTER"`), then `fetch_refs` into `$TMP/live.json`.
- Print `cat-file invocations: <count_cat_file>` and `refs returned: $(jq length "$TMP/live.json")`.
- Print the `served_from=` line from the log and require it to be `served_from=origin`; otherwise fail with the log (this proves the run really exercised the live path).
- `stop_server`.

1.8 Run 2 — claim mirror:
- Clone the bare into `$TMP/mirror` (normal clone, upstream intact).
- `start_server "$TMP/mirror" 1s "$TMP/mirror.log"`.
- Poll `fetch_refs` (discarding output) up to 30 s until `grep -q 'served_from=mirror' "$TMP/mirror.log"`; fail with the log if it never appears.
- Truncate the counter, then `fetch_refs` into `$TMP/mirror.json`.
- Print `cat-file invocations: <count_cat_file>` and `refs returned: $(jq length "$TMP/mirror.json")`.
- Print the `served_from=mirror` log line.
- `stop_server`.

1.9 The script prints measured values only; it must never hard-code `1` or `300` as an output. Exit 0 after both runs complete. Any boot failure, missing `jq` or `curl`, unparsable body, a `refs returned` count that does not equal `$COUNT`, or a wrong served path exits non-zero with the relevant log. Both runs assert `[ "$(jq length "$TMP/<run>.json")" -eq "$COUNT" ]` — compared against the `COUNT` variable, never a literal — so a run that seeds nothing (0 refs) cannot exit 0 and pass vacuously.

1.10 `PORT` is overridable (`PORT=${PORT:-19090}`). Document `GIT_REST_BIN` and `PORT` in a header comment, together with the expected pre-fix (`300`) and post-fix (`1`) readings.

## 2. Update `CHANGELOG.md`

2.1 Ensure exactly one `## Unreleased` heading exists immediately after the intro bullets and before the first `## v` heading. The release bot renames the heading to `## vX.Y.Z` and does not re-add it, so the heading may be absent when you read the file; create it if absent. If it already exists, keep it and do not add a second one.

2.2 Add exactly one bullet under that heading:

```markdown
- fix: Read every claim ref commit message in one `git cat-file --batch` invocation instead of one per ref, so a listing's git process count no longer grows with the prefix size and the success-record snapshot answers inside its caller's deadline
```

The line must contain the literal substring `claim ref` (with a space) and must sit between the `## Unreleased` heading and the next `## v` heading.

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- This prompt adds no Go code. Do not change `pkg/`, `main.go`, or `mocks/`.
- Do NOT run the probe script in this container as a verification step. It boots a server and binds a port; it is the operator's rung-1 command after the PR merges (see `docs/verifying-specs.md`). Verify it structurally with `bash -n`.
- Do NOT run `make build` or `make buca` from this container — they shell out to Docker.
- The script must not hard-code the measured numbers. The count comes from the shim's counter file; the ref count comes from the JSON body via `jq`.
- The shim must exec the absolute real-git path, never `command -v git` inside the shim (recursion).
- Copy of spec constraint: never code outside the dark-factory pipeline.
- Copy of spec constraint: do not change the HTTP surface; the probe uses the existing route and prefix.
- Copy of spec constraint: the rung-3 production check is operator-only and is not part of this prompt.
</constraints>

<verification>
Run from the repo root. Every command must produce the stated result.

```bash
test -x hack/claim-ref-listing-probe.sh && echo executable
bash -n hack/claim-ref-listing-probe.sh && echo syntax-ok
```

Must print `executable` and `syntax-ok`.

The script must reference the shim and the measured sources, not hard-coded outputs:

```bash
grep -c 'CLAIM_REF_SHIM_COUNTER' hack/claim-ref-listing-probe.sh
grep -c 'jq length' hack/claim-ref-listing-probe.sh
grep -c 'cat-file invocations:' hack/claim-ref-listing-probe.sh
```

Each must print at least `1`.

The changelog entry sits in the right section and contains the required phrase:

```bash
awk '/^## Unreleased/{f=1;next} /^## v/{f=0} f && /claim ref/{n++} END{print n+0; exit (n>0?0:1)}' CHANGELOG.md
```

Must print a number >= 1 and exit 0 (the explicit `exit` turns an empty section into a failure instead of a silent pass).

```bash
test "$(grep -c '^## Unreleased$' CHANGELOG.md)" = 1
```

Must exit 0 (a duplicate heading, count `2`, fails).

```bash
make precommit
```

Must exit 0.

Self-check before finishing: re-read the script once end to end; confirm no literal `1` or `300` is printed as a result, the trap cleans up, and both runs print a `served_from=` line.
</verification>
