---
status: completed
spec: [026-batch-claim-ref-commit-reads]
summary: Added hack/claim-ref-listing-probe.sh (live-origin and mirror modes, counting git shim, 300 seeded refs) and an Unreleased fix entry in CHANGELOG.md; verified post-fix prints 1 cat-file invocation/300 refs, pre-fix prints 300, and make precommit exits 0.
execution_id: 198dced9-6730-5c5f-96c5-3b4df9ebed9a-exec-107-spec-026-probe-and-changelog
dark-factory-version: dev
created: "2026-11-11T15:36:03Z"
queued: "2026-11-11T16:01:48Z"
started: "2026-11-11T16:23:46Z"
completed: "2026-11-11T16:29:24Z"
branch: dark-factory/batch-claim-ref-commit-reads
---

<summary>
- A host-runnable probe seeds 300 claim refs, puts a counting `git` shim on the server's `PATH`, boots the binary, and GETs the listing.
- The probe reads the `cat-file` process count from the shim's counter file and the ref count from the response body; neither is hard-coded.
- The probe discriminates pre-fix from post-fix: the fixed build prints `cat-file invocations: 1`, the pre-fix build prints `cat-file invocations: 300`.
- A mirror mode uses a short pull interval, confirms `served_from=mirror`, and still prints one `cat-file` invocation.
- `CHANGELOG.md` gains an `## Unreleased` section with the user-visible fix.
</summary>

<objective>
Add the rung-1 reproduction harness `hack/claim-ref-listing-probe.sh` and the `CHANGELOG.md` entry, so the only verification rung that discriminates this fix can be run on any host without Docker or a cluster, and the change is recorded for the next release.
</objective>

<context>
There is no repo-root `CLAUDE.md` in this worktree — `.gitignore` lists `/CLAUDE.md`. Do not look for one. The definition of done is `docs/dod.md`.

Read:
- `hack/build-cluster.sh` and `hack/cluster-alias.sh` for script conventions (`#!/usr/bin/env bash`, `set -euo pipefail`, a `die` helper, macOS-compatible bash 3.2 constructs).
- `main.go` flags: `--repo` (path to the git repository on disk), `--listen`, `--pull-interval`, `--pull-timeout`. The server exposes `/healthz`, `/readiness`, and `GET /api/v1/refs?prefix=...`.
- `pkg/puller/puller.go`: the puller waits one full interval before its first pull, so a long `--pull-interval` keeps a fresh boot on the live-origin path, and a short one syncs the mirror.
- `CHANGELOG.md` top: the newest heading is `## v0.43.0`; there is no `## Unreleased` (the release bot renames it and does not re-add it).
- `pkg/git/claim_refs.go` `claimCommitMessage` for the claim commit JSON payload (`cluster`, `task_id`, `lease_until`, `nonce`).

Spec: `specs/in-progress/026-batch-claim-ref-commit-reads.md` — Acceptance Criteria 6 and 7, and the Verification section's rung 1 (which owns the operator-side pre-fix comparison).

Coding-plugin docs (in-container paths): `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` and `/home/node/.claude/plugins/marketplaces/coding/docs/go-cli-guide.md`.
</context>

<requirements>
1. Create `hack/claim-ref-listing-probe.sh`, executable (mode 0o755), starting with `#!/usr/bin/env bash`, `set -euo pipefail`, and a `die()` helper. Resolve the repo root from `BASH_SOURCE[0]`.

2. Interface:
   - `GIT_REST_BIN`: if set, use that binary; otherwise build one from the current tree with `go build -o "$tmp/git-rest" .` from the repo root.
   - `PROBE_REFS`: default 300.
   - First argument `--mirror` (or `mirror`) selects the mirror mode; the default is the live-origin mode.
   - `PORT`: optional; otherwise allocate a free localhost port (for example with `python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'`, or retry a random high port).

3. Seed a temporary bare origin and clone:
   - `git init --bare "$origin"`.
   - Create a seed repo, configure a user identity, commit a `README.md`, push `HEAD:main` to `$origin`, and set `git -C "$origin" symbolic-ref HEAD refs/heads/main` so the clone has a branch.
   - Configure a user identity on `$origin`, create a small set of claim commits with `git -C "$origin" commit-tree <empty-tree> -m '<json claimCommitMessage>'`, and create `PROBE_REFS` refs named `refs/claims/success/task<N>/phase<N>/<N>` in one `git -C "$origin" update-ref --stdin` invocation. Use the same empty-tree object id as `pkg/git/claim_refs.go` (`4b825dc642cb6eb9a060e54bf8d69288fbee4904`).
   - `git clone "$origin" "$work"`.

4. Install the counting shim:
   - Capture the real git absolute path with `command -v git` **before** prepending the shim directory.
   - Write `$shim_dir/git` (mode 0o755) that appends `"$*"` to `$counter` and then `exec`s the captured absolute path. Do **not** call `command -v git` inside the shim — that recurses.
   - Prepend `$shim_dir` to `PATH` only for the server process (`PATH="$shim_dir:$PATH" "$bin" ... &`), so the script's own git commands use the real git.

5. Boot the server with `--repo="$work" --listen="127.0.0.1:$port" --pull-timeout=60s` and the pull interval below, redirecting stdout+stderr to `$server_log`. Install an `EXIT` trap that kills the server and removes the temp dir.

6. Mode behavior:
   - Live mode: `--pull-interval=1h`. Wait for `/healthz` to return 200 (poll with `curl -fsS`, with a timeout; fail with the server log if the process exits). Truncate `$counter` immediately before the GET, then GET once.
   - Mirror mode: `--pull-interval=2s`. Wait for `/healthz`, sleep long enough for at least one pull (for example `pull_seconds + 2`). Then loop up to ~15 attempts: truncate `$counter`, GET, and break when `$server_log` contains `served_from=mirror` (match both `served_from=mirror` and `"served_from":"mirror"`); otherwise sleep 1s and retry. The final GET's counter is the one printed.

7. GET `http://127.0.0.1:$port/api/v1/refs?prefix=claims%2Fsuccess%2F` and save the body.

8. Derive both printed numbers from live data, never hard-code them:
   - `cat-file invocations:` = the number of lines in `$counter` beginning with `cat-file` (`grep -c '^cat-file' "$counter" || true`).
   - `refs returned:` = the number of `"ref":` occurrences in the body (`grep -o '"ref":' "$body" | wc -l | tr -d ' '`).
   - Also print the served-from value read from `$server_log` (the last `served_from=` line; for example `served_from=origin` or `served_from=mirror`).
   The output must include, on separate lines:
   ```
   served_from=<origin|mirror>
   cat-file invocations: <n>
   refs returned: <n>
   ```
9. Exit 0 when the listing returned `PROBE_REFS` refs; exit non-zero (after printing) otherwise. Do not assert the `cat-file` count in the script: the pre-fix build must still print its real count (300) so the reviewer can compare.

10. Add the `## Unreleased` section at the top of `CHANGELOG.md`, after the intro and before the first `## v` heading. It must carry a `- fix:` entry containing the literal `claim ref` and describing the user-visible effect, for example:
    ```
    ## Unreleased

    - fix: Read claim ref commit messages in one batched `git cat-file --batch` invocation instead of one `git cat-file -p` process per matching ref. A listing's git process count is now constant, so the largest claim prefix answers inside the caller's deadline again and success records that already exist are no longer re-driven.
    ```
    Do not invent a version number.

11. Record the pre-fix discrimination recipe in a comment near the top of the script: build a binary from a clean `origin/master` checkout separately, then `GIT_REST_BIN=/path/to/prefix-binary bash hack/claim-ref-listing-probe.sh` must print `cat-file invocations: 300`; the fixed binary must print `cat-file invocations: 1`; the mirror run must print `served_from=mirror` and still `cat-file invocations: 1`. The spec's Verification section owns running this comparison.

12. `bash -n hack/claim-ref-listing-probe.sh` and `make precommit` must pass. If the environment can bind a port and run the binary, run `bash hack/claim-ref-listing-probe.sh` once against the current tree and confirm `cat-file invocations: 1` and `refs returned: 300`; if it cannot, say so in the final report.

<!-- DEVIATION FOR REVIEWER: the spec's rung-1 text says `make build`, but this repo's `make build` builds a Docker image (or a cluster Job), not a local binary. The probe therefore builds with `go build` by default or accepts `GIT_REST_BIN`, so it runs on any host without Docker. This is the only deviation from the spec's wording. -->
</requirements>

<constraints>
- Never code outside the dark-factory pipeline; do not commit — dark-factory handles git.
- The script must not depend on Docker, kubectl, the dark-factory CLI, or the cluster. It must run on a plain host with `bash`, `git`, `go`, and `curl`.
- The `cat-file` count and the ref count must come from the counter file and the response body; hard-coded numbers fail Acceptance Criterion 6.
- The shim must exec the real git by absolute path, never via `command -v git`.
- Do not modify production Go code or tests in this prompt.
- `CHANGELOG.md` must gain a new `## Unreleased` heading; `grep -n Unreleased` is not enough because the heading is what this change creates.
- Existing tests must still pass.
- Do NOT commit — dark-factory handles git.
</constraints>

<verification>
Run from the repository root inside the container:

```
bash -n hack/claim-ref-listing-probe.sh
test -x hack/claim-ref-listing-probe.sh && echo ok
awk '/^## Unreleased/{f=1;next} /^## v/{f=0} f && /claim ref/' CHANGELOG.md | grep -q . && echo ok
make precommit
```

All must exit 0. `make precommit` is the authoritative gate; if it fails, fix the failure and re-run only the failing target, then run `make precommit` once more.

Then, if the environment permits (the probe builds a binary, seeds a temp repo, binds a localhost port, and boots the server):

```
bash hack/claim-ref-listing-probe.sh
```

Expect:
```
served_from=origin
cat-file invocations: 1
refs returned: 300
```

And:

```
bash hack/claim-ref-listing-probe.sh --mirror
```

Expect:
```
served_from=mirror
cat-file invocations: 1
refs returned: 300
```
</verification>
