---
status: verifying
tags:
    - dark-factory
    - spec
    - bug
approved: "2026-10-24T20:34:18Z"
generating: "2026-10-24T20:44:29Z"
prompted: "2026-10-24T20:54:42Z"
verifying: "2026-10-24T20:59:29Z"
branch: dark-factory/bug-coalesced-flush-discards-the-commit
---

## Summary

- When a coalesced write flush cannot push, git-rest destroys the commit it just made: it resets the branch back to the pre-flush revision and leaves the change as staged paths plus a pending-set entry.
- The consequence is that a later pull sees an uncommitted dirty tree instead of a local commit, so it takes the dirty-tree rescue path — which resets and replays — and never reaches the pull path's conflict machinery.
- The vault-api merge resolver lives in that conflict machinery. Coalescing is on for every write-enabled vault, so the resolver added in v0.35.0 and wired per vault in v0.36.0 can never fire: the feature is dead on every write-enabled vault.
- One rule covers the whole fix: any flush that committed keeps its commit and clears the pending set. That holds for a rejected push and for a flush cut by its own deadline, and it supersedes spec 021's "leaves the paths staged" guarantee.
- A second, independent bypass exists in the push-retry helper, which aborts its own merge on conflict and states in its doc comment that the pull path's conflict machinery is never entered from there.

## Problem

git-rest's coalesced write path commits a batch of staged writes and then pushes. When the push fails — the common case being a second writer that moved the remote first — the failure handler runs `git reset --soft` back to the pre-flush revision (`pkg/git/git.go:981-989` calling `restorePendingIndexLocked`, `pkg/git/git.go:1015-1033`), whose doc comment states the intent: "moves HEAD back to preFlushSHA while keeping the committed tree in the index, so a failed push leaves the paths staged and the pending set durable". The commit is deleted; the write survives only as staged paths plus an in-memory pending entry that the next flush re-commits. The push-retry helper carries a second copy of the bypass (`retryPush`, `pkg/git/git.go:416-457`): it aborts its own half-finished merge and states that "a merge failure aborts the half-finished merge so the pull path's conflict machinery is never entered from here". Coalescing is the default (`WRITE_COALESCE`, `main.go:63`, `default:"true"`) and the deployment chart sets no override, so every write-enabled vault runs it; the non-coalesced path (`writeFileLocked`, `pkg/git/git.go:876`) returns on push error without a reset and keeps its commit. Measured on dev on 2026-10-24 (`vault-obsidian-personal-0`, image v0.35.0, `VAULT_API_URL=http://vault-api-personal:9090` set and reachable) across five manufactured modify/modify divergences: zero `vault-api-merge-resolver` log lines, `git_rest_conflict_paths_total 0`, `git_rest_merge_outcome_total{result="resolved"} 0`, while `git_rest_write_flushes_total{outcome="error"}` reached 4 and `git_rest_pull_rescues_total` reached 3.

**Considered and rejected: declare the bypass.** Leave the flush path alone and emit a startup log line plus a metric stating that the resolver is unreachable under coalescing. Rejected because a vault that sets `VAULT_API_URL` — wired per vault in v0.36.0 so an operator can enable the resolver from the values file — would still get no resolution. The declared-bypass option turns a shipped feature permanently dead and documents the deadness instead of removing it, and it leaves the discarded-commit behavior (a write whose commit vanishes on any push failure) in place on every write-enabled vault.

**Considered and rejected: keep the deadline cut staged.** Apply the retained-commit rule to rejected pushes only, and leave a flush cut by its own deadline leaving its paths staged as spec 021 and `docs/deployment.md:52` describe. Rejected because the error path would then have to distinguish a cut from a rejection before deciding what to do with the commit it just made, and the repository would carry two divergent post-failure contracts — one where a committed write is a commit, one where the same write is staged paths — with the same `git_rest_write_flushes_total{outcome="error"}` counter and the same WARN record describing both. One rule, one contract: any flush that committed keeps its commit and clears the pending set.

## Goal

A failed coalesced push leaves the write as a real local commit that the next pull merges with upstream, so a modify/modify divergence reaches the pull path's conflict machinery and the configured resolver runs. The same rule covers a flush cut by its own deadline. Coalescing keeps its default, and the non-coalesced path keeps its current behavior.

## Assumptions

- The pull state machine already delivers a local-ahead commit: `syncWithUpstream` pushes when the local side is ahead and the remote has not moved, and merges then pushes when both sides are ahead. A retained commit therefore needs no new delivery mechanism.
- Nothing in production code depends on the staged-index-after-failure shape: the boot recovery path commits staged paths only when a crash left them, and no endpoint or reader inspects that state. Three tests do depend on it, and this spec rewrites all three. `pkg/git/coalesce_durability_test.go` `TestFlushPushFailureKeepsPending` asserts today that a failed push leaves the path staged (`git diff --cached --name-only`) with `write_pending` above zero; post-fix it asserts the commit is retained and the pending gauge reads zero, keeping its name as the audit handle with a comment stating what it now covers. `pkg/git/flush_deadline_test.go` `TestFlushDeadlineCut` asserts today that a flush cut by its own deadline leaves `tasks/x.md` in `git status --porcelain`; post-fix it asserts the cut flush's commit is retained and the tree is clean, keeping its other claims (the `git-rest: flush deadline exceeded before pull` record, a `Pull` that returns without error, and the elapsed-time bound). `pkg/git/flush_rescue_test.go` `TestFlushRescueLandsAfterFlushDeadline` asserts today that a cut flush still lets the dirty-tree rescue push a `rescue/*` ref and log `rescue branch pushed`; post-fix the cut leaves a clean tree, so it asserts instead that the cut flush's commit survives into the pull's merge, that no `rescue/*` ref reaches the origin, and that the cut-off record and the `git-rest: flush pending writes before pull failed` record still appear.
- Resolver unreachability is the sole cause of the dead feature. The resolver's wiring, the live pod's `VAULT_API_URL`, and vault-api's merge endpoint are in place, so once a real local commit reaches the merge, the resolver fires.

## Reproduction

Observed on dev on 2026-10-24, and replayable on the host with the recipes below.

- Pod: `vault-obsidian-personal-0`, cluster/namespace `kubectlnukedev -n dev`
- Image: `bborbe/git-rest:v0.35.0`; `WRITE_COALESCE` unset, so coalescing is at its default `true`
- Five manufactured modify/modify divergences (a remote edit to a path the pod had also written) produced: `grep -c 'vault-api-merge-resolver'` = 0, `git_rest_conflict_paths_total 0`, `git_rest_merge_outcome_total{result="resolved"} 0`, `git_rest_write_flushes_total{outcome="error"} 4`, `git_rest_pull_rescues_total 3`
- **Read the live object, not a local checkout's values file.** `vault-obsidian-personal-0` carries `VAULT_API_URL=http://vault-api-personal:9090` and runs image v0.35.0. The local `~/Documents/workspaces/nuke` checkout is 45 commits behind: its `git-rest/Makefile` pins `CHART_VERSION := 0.1.1` / `VERSION ?= v0.34.3` and its `values-dev.yaml` pins `image.tag: v0.25.0` with no `vaultApi` block. At `origin/master` the pin is `CHART_VERSION := 0.1.2` / `VERSION ?= v0.35.0` and `values-dev.yaml` carries `vaultApi: url: http://vault-api-personal:9090` for the `personal` vault. The chart also enables keel with `match-tag: "true"` and `pullPolicy: Always`, which is why the running pod is ahead of even a current values file. A verifier who reads the stale checkout first would conclude no resolver was configured and dismiss the zero-resolver-line evidence as confounded; it is not — the resolver was configured, reachable, and never called.

The rung-1 replay below manufactures one such divergence against a locally built binary. It runs in two variants, selected by `VARIANT`, because the post-failure snapshot and the merge cycle need different pull cadences; a third recipe covers the deadline cut.

> **The ordering below is a hard requirement, not an incidental detail.** The binary must boot against a **clean, level clone**; the remote is advanced and the local write made only afterwards. Booting with the divergence already present produces a false negative: `syncOnStartup` performs a raw `git pull` before the HTTP server serves, so the boot path fast-forwards onto the remote commit and the local write then lands on top of it with nothing to conflict against. The same trap is documented for the dirty-tree and modify/delete recipes in `docs/verifying-specs.md`.

> **Run the recipes from the worktree that holds the fix.** Step 3 builds `${REPO_UNDER_TEST}`, whose default is `$(git rev-parse --show-toplevel)` — the tree you invoke it from. Building the main checkout on master replays the **unfixed** binary and fails every assertion falsely.

> **`VARIANT=a` is what makes the post-failure snapshot a settled observation.** The flush fails after **one** retry for this fixture's shape: the first push is rejected as non-fast-forward, `retryPush` fetches and merges, that merge conflicts on `notes/x.md`, and `retryPush` aborts and returns its own error, which `pushWithRetry` returns immediately. `reset --soft` runs only after that error, so the retry window is roughly one backoff plus a fetch and a merge — not the five attempts `pushMaxAttempts` allows, which is reached only when each retry merge succeeds and the push keeps being rejected. During that window the commit is at HEAD and the tree is clean, so a snapshot taken then reads exactly like the fixed behaviour. Variant a boots with `-pull-interval=10m`, so nothing runs after the flush settles; variant b keeps `-pull-interval=10s` for the assertions that need ticks.

```bash
set -e
# VARIANT selects the pull cadence, and therefore which assertions are reachable:
#   a  -pull-interval=10m  no tick interleaves; the post-failure snapshot is
#                          settled (AC 1, AC 2's gauge read, AC 7)
#   b  -pull-interval=10s  the merge/rescue cycle runs (AC 2's flat counter and
#                          commit count, AC 3, AC 5)
VARIANT="${VARIANT:-b}"
case "$VARIANT" in
  a) PULL_INTERVAL=10m ;;
  b) PULL_INTERVAL=10s ;;
  *) echo "VARIANT must be a or b" >&2; exit 1 ;;
esac

REPO_UNDER_TEST="${REPO_UNDER_TEST:-$(git rev-parse --show-toplevel)}"
BASE=/tmp/git-rest-coalesce-repro && rm -rf "$BASE" && mkdir -p "$BASE" && cd "$BASE"

# 1. Bare origin. notes/x.md is tracked HERE, at the merge-base, so the remote
#    edit in step 4 and the coalesced write in step 5 are both plain
#    modifications of the same path — the shape that makes the push retry's merge
#    conflict, which is what forces the flush down its failure path.
#    The path is under notes/, NOT tasks/: overwriting an EXISTING task file with
#    content that carries no frontmatter is refused with 422 before anything is
#    staged (pkg/git/git.go:817-825 via isTaskFilePath), so a tasks/ fixture would
#    stage nothing, enqueue nothing, and never reach the flush at all.
git init -q --bare -b test/repro origin.git
git clone -q origin.git seed && cd seed
git config user.email repro@local && git config user.name repro
mkdir -p notes && printf 'line one\n' > notes/x.md
git add -A && git commit -q -m init
BRANCH=$(git rev-parse --abbrev-ref HEAD) && git push -q origin "$BRANCH"
cd "$BASE"

# 2. Work clone — the repo the server serves. Its remote is LEVEL at this point.
git clone -q origin.git work && cd work
git config user.email repro@local && git config user.name repro

# 3. Boot the server FIRST, against the clean, level clone. Coalescing stays at
#    its DEFAULT — no -write-coalesce flag — because the bug is specific to the
#    deployed configuration. The puller has no immediate first tick, so the first
#    pull lands one interval after boot; with 10m none lands during the run.
cd "${REPO_UNDER_TEST}" && go build -o /tmp/git-rest-coalesce-bin .
/tmp/git-rest-coalesce-bin -listen=:18448 -repo="$BASE/work" \
  -pull-interval="$PULL_INTERVAL" -write-coalesce-delay=2s -write-coalesce-max-delay=5s \
  -v=1 > "$BASE/run.log" 2>&1 &
sleep 3

# 4. NOW the second writer advances the remote on the SAME path.
cd "$BASE/seed" && printf 'line one\nREMOTE EDIT\n' > notes/x.md
git add -A && git commit -q -m "remote touches x" && git push -q origin "$BRANCH"

# 5. NOW the coalesced write, through the API, to the same path. It returns 200
#    without committing; the flusher commits and pushes about 2s later.
curl -s -o /dev/null -w 'write HTTP %{http_code}\n' \
  -X POST --data-binary 'line one
LOCAL EDIT
' "http://localhost:18448/api/v1/files/notes/x.md"

# 6. Poll until the flush reports its error, then snapshot. The error is counted
#    only AFTER reset --soft, so the counter flipping is the settled-state
#    trigger, not the retry window. In variant a the state then stays put.
err=0
for i in $(seq 1 60); do
  err=$(curl -s http://localhost:18448/metrics | awk '/^git_rest_write_flushes_total\{outcome="error"\}/{print $2}')
  if [ "${err:-0}" != "0" ]; then break; fi
  sleep 0.25
done
echo "flush errors: $err"
git -C "$BASE/work" log -1 --format='HEAD %H %s'
git -C "$BASE/work" status --porcelain
git -C "$BASE/work" rev-list --count "origin/$BRANCH..HEAD"
curl -s http://localhost:18448/metrics | grep -E '^git_rest_(write_pending|write_flushes_total|pull_rescues_total|conflict_paths_total|merge_outcome_total)'

# 7. Variant b only: let two more pull intervals run, so the merge — or, pre-fix,
#    the rescue — lands. Variant a stops at the snapshot above.
if [ "$VARIANT" = b ]; then
  sleep 25
  git -C "$BASE/work" log --format='%s'
  git -C "$BASE/work" status --porcelain
  git -C "$BASE/work" rev-parse HEAD "origin/$BRANCH"
  git -C "$BASE/work" ls-remote --heads origin 'refs/heads/rescue/*'
  curl -s http://localhost:18448/metrics | grep -E '^git_rest_(write_pending|write_flushes_total|pull_rescues_total|conflict_paths_total|merge_outcome_total)'
  grep -c 'vault-api-merge-resolver' "$BASE/run.log" || true
fi

# 8. Teardown. Every AC re-runs this recipe on the same port, so each run must
#    start from a clean process — a survivor keeps answering :18448 and returns
#    stale evidence, and every counter is per-process.
kill "$(pgrep -f /tmp/git-rest-coalesce-bin)" 2>/dev/null || true
```

The resolver assertion needs the resolver wired, which requires both `-vault-write=true` and `-vault-api-url` (`selectResolver` in `main.go` returns the marker resolver whenever `-vault-write=false`, so a URL alone never reaches vault-api). Replay variant b with these extra flags and a stub resolver, and add step 7a:

```bash
# Stub vault-api: answers every merge with "merged" and echoes the request path.
cat > "$BASE/stub-vault-api.py" <<'PY'
import json
from http.server import BaseHTTPRequestHandler, HTTPServer

class H(BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        req = json.loads(self.rfile.read(n) or b"{}")
        print("merge request:", req.get("path"), req.get("apply"), flush=True)
        body = json.dumps(
            {"result": "merged", "reason": "", "content": "line one\nLOCAL WINS\n"}
        ).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass

HTTPServer(("127.0.0.1", 18447), H).serve_forever()
PY
python3 -I "$BASE/stub-vault-api.py" > "$BASE/stub.log" 2>&1 &
sleep 1

# Boot step 3 gains: -vault-write=true -vault-api-url=http://127.0.0.1:18447

# 7a. After step 7: the resolver was reached, and vault-api answered.
grep -c 'vault-api-merge-resolver: "notes/x.md" merged by vault-api' "$BASE/run.log"
cat "$BASE/stub.log"
kill "$(pgrep -f stub-vault-api.py)" 2>/dev/null || true
```

Variant c — the deadline cut. Same divergence shape, but the pull budget is small and the origin stalls the push, so the pull's leading flush is cut at its own deadline (a third of the budget). The one-hour coalescing delay keeps the background flusher out of the way, so the pull's flush is the only one that runs. The hook allows `rescue/*` so a rescue is observable when it happens.

```bash
set -e
REPO_UNDER_TEST="${REPO_UNDER_TEST:-$(git rev-parse --show-toplevel)}"
BASE=/tmp/git-rest-cut-repro && rm -rf "$BASE" && mkdir -p "$BASE" && cd "$BASE"

# 1. Bare origin, notes/x.md tracked at the merge-base.
git init -q --bare -b test/repro origin.git
git clone -q origin.git seed && cd seed
git config user.email repro@local && git config user.name repro
mkdir -p notes && printf 'line one\n' > notes/x.md
git add -A && git commit -q -m init
BRANCH=$(git rev-parse --abbrev-ref HEAD) && git push -q origin "$BRANCH"
cd "$BASE"

# 2. Work clone — level at this point.
git clone -q origin.git work && cd work
git config user.email repro@local && git config user.name repro

# 3. Boot FIRST, against the clean, level clone. -pull-timeout=3s puts the flush
#    deadline at ~1s; the coalescing delay is an hour, so only the pull's leading
#    flush runs.
cd "${REPO_UNDER_TEST}" && go build -o /tmp/git-rest-cut-bin .
/tmp/git-rest-cut-bin -listen=:18449 -repo="$BASE/work" \
  -pull-interval=20s -pull-timeout=3s \
  -write-coalesce-delay=1h -write-coalesce-max-delay=1h \
  -v=1 > "$BASE/run.log" 2>&1 &
sleep 3

# 4. NOW the remote moves on the same path, so the pull after the cut has a merge
#    to perform and a rescue to take if the cut left a dirty tree.
cd "$BASE/seed" && printf 'line one\nREMOTE EDIT\n' > notes/x.md
git add -A && git commit -q -m "remote touches x" && git push -q origin "$BRANCH"

# 5. NOW stall the push. The hook rejects main after sleeping past the flush
#    deadline and allows rescue/* immediately, so a rescue is visible when it
#    happens. Point the bare repo at its OWN hooks dir: a global core.hooksPath
#    makes git ignore $GIT_DIR/hooks and the hook silently no-ops.
cat > "$BASE/origin.git/hooks/pre-receive" <<'HOOK'
#!/bin/sh
while read -r old new ref; do
  case "$ref" in
    refs/heads/rescue/*) continue ;;
    *) sleep 8; exit 1 ;;
  esac
done
exit 0
HOOK
chmod +x "$BASE/origin.git/hooks/pre-receive"
git -C "$BASE/origin.git" config core.hooksPath hooks

# 6. NOW the coalesced write.
curl -s -o /dev/null -w 'write HTTP %{http_code}\n' \
  -X POST --data-binary 'line one
LOCAL EDIT
' "http://localhost:18449/api/v1/files/notes/x.md"

# 7. The first pull tick cuts the flush at ~1s, merges on the remaining budget,
#    and has its push killed at the pull deadline. The 20s interval leaves a wide
#    quiet window before the next tick, so this read never races one — with a 10s
#    ticker the same read lands on top of the third tick.
sleep 26
git -C "$BASE/work" log --format='%s'
git -C "$BASE/work" status --porcelain
git -C "$BASE/work" rev-list --count "origin/$BRANCH..HEAD"
git -C "$BASE/work" ls-remote --heads origin 'refs/heads/rescue/*'
curl -s http://localhost:18449/metrics | grep -E '^git_rest_(write_pending|write_flushes_total|pull_rescues_total)'
grep -c 'git-rest: flush deadline exceeded before pull' "$BASE/run.log" || true

# 8. Teardown.
kill "$(pgrep -f /tmp/git-rest-cut-bin)" 2>/dev/null || true
```

**Observed (pre-fix).** Variant a, at the settled snapshot: `git log -1` shows `init` — `preFlushSHA` is captured before the flush commits, and the failure path resets to it — `git status --porcelain` shows `M  notes/x.md`, `git rev-list --count origin/$BRANCH..HEAD` prints `0`, and `git_rest_write_pending` reads `1`. Variant b, at step 7: a `rescue/<timestamp>` ref exists on origin, `git_rest_pull_rescues_total` reads `1`, `git_rest_conflict_paths_total` reads `0`, `git_rest_write_flushes_total{outcome="error"}` reads `2` (it climbs once per pull cycle, because the pending set survives and is re-committed), and `grep -c 'vault-api-merge-resolver'` prints `0`. Variant c: a `rescue/*` ref reaches the origin, `git_rest_pull_rescues_total` reads `1`, `git_rest_write_pending` stays at `1`, HEAD reads the upstream commit `remote touches x` with `rev-list --count origin/$BRANCH..HEAD` = `0`, and the local write survives only on the rescue branch — the cut flush left a dirty tree, the pull rescued it, and the write never became a commit.

The retry window is the trap this replay has to survive: over a 45 s run against the unfixed binary, a rejected coalesced push drove `git_rest_write_flushes_total{outcome="error"}` to 11 while `git_rest_write_pending` stayed at `1`, and a snapshot taken inside the retry window read `git-rest: update <path>` at HEAD with `rev-list --count origin/test/repro..HEAD` = `1` and a clean tree — indistinguishable from the fixed behaviour. The commit is at HEAD until the last retry fails, and `reset --soft` removes it only afterwards. The settled state — staged paths, a pending gauge that never returns to zero, an error counter that keeps climbing — is the discriminator, which is why every snapshot above is taken after the counter has flipped and why AC 1 asserts the gauge.

**Expected (post-fix).** Variant a, at the settled snapshot: `git log -1` shows `git-rest: update notes/x.md`, `git status --porcelain` prints nothing, `git rev-list --count origin/$BRANCH..HEAD` prints `1`, and `git_rest_write_pending` reads `0`. Variant b, at step 7: no `rescue/*` ref exists, `git_rest_pull_rescues_total` reads `0`, `git_rest_conflict_paths_total` reads `1`, `git_rest_merge_outcome_total{result="resolved"}` reads `1`, `git log -1 --format=%s` reads `merge: resolved=[notes/x.md] quarantined=[]`, `git_rest_write_flushes_total{outcome="error"}` still reads `1`, and the vault-api variant prints at least one `vault-api-merge-resolver: "notes/x.md" merged by vault-api` line. Variant c: the tree is clean, `git_rest_write_pending` reads `0`, no `rescue/*` ref exists, `git_rest_pull_rescues_total` reads `0`, `git log --format=%s` contains both `git-rest: update notes/x.md` and `merge: resolved=[notes/x.md] quarantined=[]`, `git rev-list --count origin/$BRANCH..HEAD` prints `2` (the flush commit and the merge commit, neither pushed because the origin stalls), and the log carries `git-rest: flush deadline exceeded before pull`.

## Expected vs Actual

**Expected.** A write that has been committed is committed. A push failure — a rejection, or a cut at the flush's own deadline — is a delivery failure, not a reason to unmake the commit: the local repository keeps the work as a commit, and the pull loop's existing state machine (local ahead and remote unchanged → push; both ahead → merge, resolve, push) carries it to the remote on the next cycle. When the remote moved onto the same path, that cycle produces a genuine modify/modify conflict, which is precisely the case the conflict machinery and the vault-api resolver exist to handle — and v0.35.0's changelog states that the resolver exists so a vault-write pod resolves pull conflicts through vault-api's merge endpoint.

**Actual.** The failed flush resets HEAD below its own commit, so the divergence becomes a dirty working tree. The next pull rescues it — capturing a `rescue/<timestamp>` branch, resetting, replaying — and the conflict machinery is never entered. The resolver never runs, `git_rest_conflict_paths_total` stays `0`, and `git_rest_merge_outcome_total{result="resolved"}` stays `0` (measured on dev, 2026-10-24, five divergences). The pending set survives the failure, so the same paths are re-committed and re-failed on the next cycle, and `git_rest_write_flushes_total{outcome="error"}` climbs once per pull interval.

## Why this is a bug

Two documented contracts are violated. First, the commit path's own contract: the write API's documented behavior is that a write is committed and pushed within the coalescing window (`docs/api.md`, `docs/deployment.md` § Write coalescing), and the flush helper's doc comment presents the reset as a durability feature. In fact the reset deletes the only commit that recorded the write, and durability rests on a re-commit that can fail again indefinitely. Second, the resolver's contract: v0.35.0 shipped pull-conflict resolution through vault-api, and v0.36.0 made it reachable from a values file; the changelog's promise is that enabling it "can only add resolutions, never remove one". Under the default configuration — the one every write-enabled vault runs, since `WRITE_COALESCE` defaults to `true` and no chart sets it — the resolver is unreachable, so the feature is dead on arrival on every write-enabled vault. A service whose job is to automate git must not silently discard the commit that is its own unit of work, and a feature that cannot fire under the default configuration is a defect in the configuration's default path, not in the feature.

## Acceptance Criteria

- [ ] **A failed coalesced push retains its commit.** Replay `## Reproduction` with `VARIANT=a` and read the snapshot only after `git_rest_write_flushes_total{outcome="error"}` has reached `1`: `git -C "$BASE/work" log -1 --format=%s` prints `git-rest: update notes/x.md`, `git -C "$BASE/work" status --porcelain` prints 0 lines, `git -C "$BASE/work" rev-list --count "origin/$BRANCH..HEAD"` prints `1`, and `curl -s http://localhost:18448/metrics | grep '^git_rest_write_pending'` prints `git_rest_write_pending 0` — evidence: stdout match, empty stdout, an exact count, and a metric value. The pending gauge is load-bearing: during the flush's retry window the commit is already at HEAD and the tree is already clean, so the other three probes pass on the unfixed binary; only the settled state after `reset --soft` exposes the defect, and the gauge is what separates the two.
- [ ] **The pending set is cleared, and the same write is not re-committed on top of its own commit.** `VARIANT=a`: the gauge reads `git_rest_write_pending 0` at the settled snapshot. `VARIANT=b`: `git_rest_write_flushes_total{outcome="error"}` still reads `1` at step 7 (it does not climb once per pull cycle), and `git -C "$BASE/work" log --format=%s | grep -c '^git-rest: update notes/x.md$'` prints `1` over the whole run — evidence: metric value, metric value held flat across ≥2 pull intervals, and a line count.
- [ ] **The next pull merges the retained commit instead of rescuing a dirty tree, and the conflict reaches the conflict machinery.** `VARIANT=b`, at step 7: `git -C "$BASE/work" ls-remote --heads origin 'refs/heads/rescue/*'` prints 0 lines, `git_rest_pull_rescues_total` reads `0` (the counter is per-process and pre-initialised to zero, so an absolute `0` is a zero delta), `git_rest_conflict_paths_total` reads `1`, `git_rest_merge_outcome_total{result="resolved"}` reads `1`, `git -C "$BASE/work" log -1 --format=%s` matches `^merge: resolved=\[notes/x\.md\] quarantined=\[\]$`, `git -C "$BASE/work" status --porcelain` prints 0 lines, and `git -C "$BASE/work" rev-parse HEAD` equals `git -C "$BASE/work" rev-parse "origin/$BRANCH"` — evidence: empty remote-ref listing, two metric values, one metric value, a stdout match, empty stdout, and SHA equality.
- [ ] **A flush cut by its own deadline retains its commit too, and the rescue is no longer its recovery path.** Replay variant c, reading in the quiet window after the first pull tick: `git -C "$BASE/work" ls-remote --heads origin 'refs/heads/rescue/*'` prints 0 lines, `git_rest_pull_rescues_total` reads `0`, `git_rest_write_pending` reads `0`, `git -C "$BASE/work" status --porcelain` prints 0 lines, `git -C "$BASE/work" log --format=%s` contains `git-rest: update notes/x.md` and `merge: resolved=[notes/x.md] quarantined=[]`, `git -C "$BASE/work" rev-list --count "origin/$BRANCH..HEAD"` prints `2`, `grep -c 'git-rest: flush deadline exceeded before pull' "$BASE/run.log"` prints ≥1, and `git_rest_write_flushes_total{outcome="error"}` reads ≥1 — evidence: empty remote-ref listing, three metric values, empty stdout, two stdout matches, an exact count, and a log line count. The cut-off record is spec 021's surviving guarantee; the absent rescue ref is the superseded one.
- [ ] **Under the default coalescing configuration, the vault-api resolver fires.** Replay the `VARIANT=b` recipe with `-vault-write=true -vault-api-url=http://127.0.0.1:18447` and the stub reachable, no `-write-coalesce` flag passed: `grep -c 'vault-api-merge-resolver: "notes/x.md" merged by vault-api' "$BASE/run.log"` prints ≥1, `$BASE/stub.log` contains one `merge request: notes/x.md False` line, and `git_rest_merge_outcome_total{result="resolved"}` reads `1` — evidence: log line count (the leading assertion), a log line naming the path and `apply=False`, and a metric value. The flat `git_rest_pull_rescues_total` is secondary evidence only; the resolver log line is the assertion that fails pre-fix.
- [ ] **The non-coalesced path is unchanged.** Replay the recipe with `-write-coalesce=false` and the same divergence: the POST returns `HTTP 500`, `git -C "$BASE/work" log --format=%s | grep -c '^git-rest: update notes/x.md$'` prints `1` (read from history, not from HEAD: a pull tick can have merged the commit by the time the probe runs), `git -C "$BASE/work" status --porcelain` prints 0 lines, and `git_rest_write_flushes_total{outcome="error"}` reads `0` — evidence: HTTP status, stdout match, empty stdout, and a metric value of zero. This path keeps its commit before the change and after it, and never touches the flush counters.
- [ ] **The retained commit is observable.** `VARIANT=a`, at the settled snapshot: `grep -c 'git-rest: flush push failed, commit retained' "$BASE/run.log"` prints `1`, the same record carries a `commit=` field matching `^[0-9a-f]{40}$` and a `paths=1` field, that SHA equals `git -C "$BASE/work" rev-parse HEAD`, and `git -C "$BASE/work" rev-list --count "origin/$BRANCH..HEAD"` prints `1` — evidence: log line count, log field shapes, SHA equality, and an exact count. With `git_rest_write_pending 0` at the same moment, the record and the ahead-count are what distinguish "committed, not yet pushed" from "delivered".
- [ ] **The tests are rewritten, and the new behaviour fails on the pre-change commit by assertion, not by compile error.** Three parts. (a) The two spec-021 tests assert the new contract and the suite is green: `go test ./pkg/git/` exits 0, and the stale assertion is gone — `grep -c 'still staged' pkg/git/flush_deadline_test.go` prints `0`. (b) The failed-push durability test is inverted under its original name rather than deleted: `grep -rl 'func TestFlushPushFailureKeepsPending' pkg/git/ | wc -l` prints `1`, with its assertions rewritten to the retained commit, the clean tree, the zero pending gauge, and a subsequent pull that pushes the retained commit to origin once the remote accepts pushes again. (c) Replaying the new test against the pre-change tree (`git worktree add /tmp/git-rest-prefix cac512e`, copy the new test into the same package there) gives `go vet ./pkg/git/` exit 0 — it compiles against the old tree, which is what makes this a behavioral failure — and `go test ./pkg/git/` exit non-zero with an assertion message — evidence: exit codes (three), a line count of `0`, a file count of `1`, and a stdout match on the failure output.
- [ ] **`make precommit` exits 0** from the repo root — evidence: exit code.
- [ ] **The change is recorded where an operator looks, and the superseded claim is named.** `awk '/^## /{sec=$0} /retained commit/{print sec}' CHANGELOG.md` prints `## Unreleased`; the Unreleased section names what it supersedes — `awk '/^## /{sec=$0} /v0\.34\.1/{print sec}' CHANGELOG.md` prints `## Unreleased`; both "leaves the paths staged" clauses at `docs/deployment.md:52` are gone — `grep -c 'leaves the paths staged' docs/deployment.md` prints `0`; and the replacement is stated — `grep -c 'retained commit' docs/deployment.md` prints ≥1 — evidence: two stdout matches, one line count of `0`, and one line count ≥1.
- [ ] **Post-Deploy (Rung-2):** the three dev vault StatefulSets run the released tag, each one's live pod image matches its own spec image, every pod is Ready, and the resolver wiring survived the apply — `vault-obsidian-personal`'s spec still carries a non-empty `VAULT_API_URL` — evidence: `deploy_check` output equals `deploy_target`, per StatefulSet `kubectlnukedev -n dev get pod -l app=<name>` resolves to exactly one pod whose `Ready` condition is `True`, and `kubectlnukedev -n dev get pod vault-obsidian-personal-0 -o jsonpath='{.spec.containers[0].env[?(@.name=="VAULT_API_URL")].value}'` prints `http://vault-api-personal:9090`. The running build is confirmed independently, because the chart enables keel with `match-tag: "true"` and `pullPolicy: Always`, so a tag comparison can move without the nuke pin moving: `kubectlnukedev -n dev exec vault-obsidian-personal-0 -- wget -qO- http://localhost:9090/metrics | grep -m1 '^git_rest_build_info' | sed -E 's/.*commit="([^"]+)".*/\1/'` prints the short SHA of the deployed tag.
  - `deploy_check:` `url=$(kubectlnukedev -n dev get statefulset/vault-obsidian-personal -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="VAULT_API_URL")].value}'); [ -n "$url" ] || { echo 'vault-obsidian-personal: VAULT_API_URL missing'; exit 1; }; for sts in vault-obsidian-personal vault-obsidian-openclaw vault-obsidian-trading; do spec=$(kubectlnukedev -n dev get statefulset/$sts -o jsonpath='{.spec.template.spec.containers[0].image}'); pod=$(kubectlnukedev -n dev get pod -l app=$sts -o jsonpath='{.items[0].spec.containers[0].image}'); ready=$(kubectlnukedev -n dev get pod -l app=$sts -o jsonpath='{.items[0].status.conditions[?(@.type=="Ready")].status}'); n=$(kubectlnukedev -n dev get pod -l app=$sts -o jsonpath='{.items[*].metadata.name}' | wc -w); [ "$n" -eq 1 ] || { echo "$sts: want 1 pod, got $n"; exit 1; }; [ "$spec" = "$pod" ] || { echo "$sts: spec image $spec != pod image $pod"; exit 1; }; [ "$ready" = "True" ] || { echo "$sts: pod not Ready"; exit 1; }; echo "${spec##*:}"; done | sort -u`
  - `deploy_target:` `$(git -C ~/Documents/workspaces/git-rest fetch -q --tags origin && git -C ~/Documents/workspaces/git-rest describe --tags --abbrev=0 origin/master)`

    `deploy_check` prints one tag line only when all three StatefulSets agree: a lagging or floating tag on any one of them prints a second line, and `sort -u` output then differs from the target. The `app=<name>` selector is the chart's own pod label, so the pod read is bound to the StatefulSet that names it.

    The leading `VAULT_API_URL` clause is what stops this gate from going green on a deploy that removed the resolver wiring: an apply from a nuke checkout that predates chart 0.1.2 renders no per-vault `vaultApi` support and strips the variable, while every image and Ready probe below still passes.

    `deploy_target` resolves against the **remote** revision after a fetch, not against the local checkout: the checkout sits at v0.35.0 while `origin/master` is at v0.36.0 (`cac512e`), so a bare `git describe --tags --abbrev=0` prints the stale tag and produces a false mismatch on a correct deploy.

Scenario coverage — none. Every behavioral assertion above is reachable by replaying the rung-1
recipes against a freshly built binary and a temp bare-repo fixture, plus the cluster reads of the
deploy rung. The bug is a state-machine defect inside one process, no multi-service interaction is
load-bearing, and the existing failed-push durability test already owns the seam — it is inverted
here rather than supplemented with a scenario.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

```
make precommit
make test
go test ./pkg/git/ -count=1
! grep -q 'still staged' pkg/git/flush_deadline_test.go                  # the staged assertion is gone
test "$(grep -rl 'func TestFlushPushFailureKeepsPending' pkg/git/ | wc -l)" -eq 1   # inverted, not deleted
! grep -q 'leaves the paths staged' docs/deployment.md                  # both superseded clauses rewritten
grep -c 'retained commit' docs/deployment.md                            # >= 1
grep -c 'retained commit' CHANGELOG.md                                  # >= 1
```

Every command exits 0 on a correct tree. The two negative checks use `! grep -q` rather than `grep -c` precisely so an exit-code-driven verifier reads a correct absence as exit 0, not as the `1` that `grep -c` returns when it prints `0`. The retained-commit record's presence is asserted at runtime by AC 7 rather than by grepping a Go string literal: a source grep proves the text exists, not that the failure path reaches it.

No `git` state read belongs in this rung. Every branch-relative assertion above is a host-side read of a fixture repository, so it belongs to the operator rung — a `git log` against the served repo from inside the container is not the same read the ACs describe.

### Operator-executable (runs on the host, spec verification ladder)

- **Rung 1**: `go build -o /tmp/git-rest-coalesce-bin .` from the worktree that holds the fix, then replay `## Reproduction` with `VARIANT=a` for AC 1, AC 2's gauge read and AC 7, with `VARIANT=b` for AC 2's counter and commit count, AC 3 and AC 5, and variant c for AC 4. Replay the vault-api variant with the stub for AC 5, and the `-write-coalesce=false` variant for AC 6.
- **Rung 1 (pre-fix)**: `git worktree add /tmp/git-rest-prefix cac512e`, copy the new test into the same package there, run `go vet ./pkg/git/` (exit 0 — the test compiles against the old tree) and `go test ./pkg/git/` (exit non-zero with an assertion message). Remove the worktree afterwards.
- **Rung 2** (dev cluster, per `docs/verifying-specs.md` and `[[git-rest - Deploy New Version]]`): release through the repo's flow after the merge, `VERSION=<released tag> make buca` from `~/Documents/workspaces/git-rest` to publish `bborbe/git-rest:<tag>`, and bump `VERSION` in `~/Documents/workspaces/nuke/git-rest/Makefile` through its own PR. **Fast-forward the nuke checkout before applying:** `git -C ~/Documents/workspaces/nuke fetch && git -C ~/Documents/workspaces/nuke merge --ff-only origin/master`, then `cd ~/Documents/workspaces/nuke/git-rest && BRANCH=dev make apply`. Skipping the fast-forward silently strips the premise this spec rests on: the checkout is 45 commits behind, its `Makefile` pins `CHART_VERSION := 0.1.1`, and chart 0.1.1 has no per-vault `vaultApi` support, so the apply renders no `VAULT_API_URL` and removes it from the live StatefulSet — the resolver wiring AC 5 depends on — while AC 11's image and Ready probes still go green. At `origin/master` the pin is `CHART_VERSION := 0.1.2` / `VERSION ?= v0.35.0`. Then `kubectlnukedev -n dev rollout restart statefulset/vault-obsidian-personal`, `...-openclaw` and `...-trading`, and wait for each `rollout status --timeout=120s`. Assert the Post-Deploy (Rung-2) AC, including the build-commit and `VAULT_API_URL` reads, before asserting anything else about the cluster.
- **Rung 3** (prod promotion): `cd ~/Documents/workspaces/nuke/git-rest && BRANCH=master make apply`, then `kubectlnukeprod -n prod get pod vault-obsidian-personal-0` shows `1/1 Running`. Promote immediately after rung 2 with no soak, per `docs/verifying-specs.md`.

## Desired Behavior

1. A flush that committed keeps its commit when the push fails: HEAD is never moved below the flush commit, the working tree and index are clean, and the write is preserved in local history as an unpushed commit. The ahead-count at the settled state is one for a rejected push whose retry merge conflicts, and two when the retry merge succeeded cleanly and the following push failed — in that case the commit at HEAD is the merge commit and the flush commit is its parent.
2. That failure clears the pending set and the pending gauge, so the same paths are not re-committed on top of their own commit. The retained commit is the only record of the write, and the flush-error counter stops climbing at one per failure instead of one per pull cycle.
3. The next pull merges the retained commit with upstream through the ordinary diverged-history path: no dirty-tree rescue is taken, no `rescue/<timestamp>` ref is pushed, and a modify/modify conflict reaches the pull path's conflict machinery. Under the default coalescing configuration with a vault-api URL and a reachable vault-api, the vault-api resolver runs and the merge resolves.
4. The same rule covers a flush cut by its own deadline: the commit is retained, the pending set is cleared, and the dirty-tree rescue is no longer the recovery path for a cut flush. The cut still logs `git-rest: flush deadline exceeded before pull`, the pull still continues and merges on the remaining budget, and the flush still cannot consume more than a third of that budget.
5. A retained commit is never stranded. When the remote has not moved, the next pull pushes it; when the remote has moved, the next pull merges and pushes. The failure path drops no write and requires no operator action.
6. The failure is observable at the moment it happens: exactly one WARN record naming the commit HEAD holds at failure time — the flush commit, or a merge commit when the retry merge succeeded and the following push failed — with its full SHA and the retained path count. The existing `git_rest_write_flushes_total{outcome="error"}` counter and `git rev-list --count origin/<branch>..HEAD` carry the rest; no new series is added.
7. The non-coalesced write path is unchanged: it commits, surfaces the push error to the caller as an HTTP error, keeps its commit, and touches neither the pending set nor the flush counters.
8. The two spec-021 tests are rewritten to the new contract, and the failed-push durability test is inverted under its original name to assert the retained commit, the clean tree, the zero pending gauge, and a subsequent pull that delivers the commit. The replacement assertions fail on the pre-change tree by assertion, not by compile error.

## Constraints

- **Do not change `WRITE_COALESCE`'s default.** It stays `true`, and no flag, environment variable, or chart value is added to make the fix opt-in. The behavior change applies to every write-enabled vault on upgrade.
- **Spec 021's "leaves the paths staged" guarantee is superseded**, together with the rescue-after-cut claim that depends on it. `specs/in-progress/021-flush-timeout-isolation.md` states both — its Goal ("a pull cycle in which the flush cannot complete still performs the dirty-tree rescue and pushes the rescue branch"), its assumption that "a rejected flush is recoverable: the staged work survives on disk", and its failure-mode row that the batch "stays staged and is retried next cycle". The implementing prompt amends that spec so a reader of 021 finds the supersession, and rewrites its two tests: `TestFlushDeadlineCut` and `TestFlushRescueLandsAfterFlushDeadline`. What 021 established and this spec keeps is the budget rule — the flush deadline stays at most a third of the pull budget, and the cut-off record keeps its exact text.
- **The budget split stays as spec 021 left it.** The pre-pull flush is still bounded to one third of the pull budget, and the retained commit's delivery now runs inside `syncWithUpstream` on the remaining two thirds. A contended remote can therefore consume the merge and rescue budget in that cycle, which is a real cost of this change and not a defect to fix: do not widen the budget, do not move the delivery push back into the flush, and do not add a separate push budget.
- **Do not touch the vault-api.** Its merge endpoint, request and response shapes, `apply: false` contract, gateway-secret header, and caller header are unchanged. This spec changes when git-rest reaches the resolver, not what the resolver or vault-api does.
- **The push-retry helper must not hide the conflict either.** `retryPush` still aborts its own half-finished merge before returning, and the doc comment that claims the pull path's conflict machinery is never entered from there is corrected to state that the retained commit carries the conflict to the next pull. Do not start resolving conflicts inside the retry.
- **Errors are wrapped with `github.com/bborbe/errors`** — never `fmt.Errorf`.
- **New logging uses `log/slog`** (`slog.WarnContext`), never `fmt.Print*`. `pkg/git/yaml_merge_resolver.go` keeps `glog` and is not touched; the two loggers are never mixed in one file.
- **Timestamps come from the injected `libtime` getter**, never `time.Now()` inside `pkg/`.
- **No `context.Background()` in `pkg/`.** The retained-commit record runs on the context the flush was given.
- **`git_rest_write_flushes_total{outcome="error"}` keeps its meaning**: it still increments once per failed coalesced flush, a cut included. The WARN record is additional, not a replacement, and no new Prometheus series is introduced.
- **No change to the public HTTP contract** in `docs/api.md`: a coalesced POST still returns 200 before any push is attempted, and the push failure stays asynchronous and invisible to that request. No new endpoint, status code, or response field.
- **The commit-subject format is unchanged** — `git-rest: create <path>`, `git-rest: update <path>`, `git-rest: update <n> files`. The commit-rate measurement greps `^git-rest:`.
- **The retained commit is delivered by the existing pull state machine.** No new retry loop, timer, or goroutine pushes it; the pull path already has the three states that carry it.
- **Single-writer assumption unchanged** (`replicas: 1`). No cross-pod coordination is added.
- **The durability test keeps its original function name.** `TestFlushPushFailureKeepsPending` is renamed by nothing; its assertions change and its name stays, so the inversion is auditable against the pre-change tree rather than looking like a delete-and-replace. A comment on the function states what the name now covers.
- **The CHANGELOG bullet and the `docs/deployment.md` § Write coalescing update are part of the implementation**, not follow-up work. The v0.34.1 CHANGELOG entry is released history and is not edited; the supersession is stated in the `## Unreleased` bullet.
- **The rejected alternatives stay rejected.** Neither the declared-bypass option nor the keep-the-cut-staged option recorded in `## Problem` is re-opened by the implementation, and no startup warning about an unreachable resolver is added.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection | Reversibility | Concurrency |
|---|---|---|---|---|---|
| Coalesced push rejected because the remote moved onto the same path (the bug's trigger) | Commit retained, tree clean, pending cleared; the next pull merges and resolves through the configured resolver, then pushes | None needed — self-healed within one pull interval | WARN `git-rest: flush push failed, commit retained` with `commit=` and `paths=`; `git_rest_write_flushes_total{outcome="error"}` increments once; `git rev-list --count origin/<branch>..HEAD` reads 1 | Reversible — the commit is local and the merge keeps both sides in history | `g.mu` serialises every git operation; `Pull` holds it for its whole body |
| The retry merge succeeded cleanly, then the following push failed | The merge commit is retained at HEAD with the flush commit as its parent; pending cleared | None needed — the next pull takes the local-ahead state and pushes both commits | WARN naming the merge commit's SHA; ahead-count reads 2; `git log --format=%s` shows the merge commit above `git-rest: update <path>` | Reversible — both commits are local | Single-threaded |
| A flush is cut by its own deadline | Commit retained, pending cleared, no dirty tree; the pull continues and merges on the remaining budget, and the cut logs `git-rest: flush deadline exceeded before pull` | None needed — the next pull pushes or merges the retained commit | The cut-off record plus the WARN; `git_rest_write_flushes_total{outcome="error"}` increments once; no `rescue/*` ref is pushed | Reversible — the commit is local | Single-threaded; the flush still cannot exceed a third of the budget |
| Coalesced push fails transiently (network, auth) with the remote unchanged | Commit retained, pending cleared; the next pull takes the local-ahead/remote-same state and pushes | None needed — the next pull retries the push | Same WARN; the commit appears on origin on a later cycle and the ahead-count returns to 0 | Reversible | Single-threaded |
| The remote throttles the push (GitHub 429 or a secondary rate limit) | The push fails with a retryable rejection; the in-attempt retries run out, then the commit is retained and the pending set cleared exactly as for any other push failure | None needed — the next pull retries the push, so the throttle window passes without operator action | Same WARN carrying the throttling error; `git_rest_write_flushes_total{outcome="error"}` increments once; `git_rest_push_retries_total` increments for the in-attempt retries | Reversible — the commit is local | Single-threaded; the retry backoff is bounded, so a throttle cannot stall the pull budget |
| Pod is killed between the flush commit and the pending clear | The commit is in HEAD; boot `RecoverPendingWrites` finds nothing staged and does nothing; the next pull pushes the commit | None needed | The write reaches origin with no WARN record for that batch | Reversible | Single-threaded; boot runs before the HTTP server serves |
| The commit step itself fails (index lock, disk full) | Unchanged: nothing is committed, the pending set is retained, and the next flush retries | Automatic on the next tick | The existing `git commit` error and `git_rest_write_flushes_total{outcome="error"}` | Reversible | Single-threaded |
| The retained commit's merge conflicts and the resolver fails | The path takes the existing quarantine fallback; the commit survives as a merge parent | Operator inspects `_conflicts/` and repairs or drains | `_conflicts/` entry; `git_rest_quarantined_files_total` increments | Reversible — content is in `_conflicts/` and in history | Single-threaded |
| The remote stays unreachable for hours | Retained commits accumulate locally; nothing is dropped; each one is named | Operator repairs the remote; the next pull pushes the backlog | `git rev-list --count origin/<branch>..HEAD` climbs; each failure carries a WARN naming its commit | Reversible — every commit is local | Single-threaded |
| The delivery push now runs on the remaining two-thirds of the pull budget after a retained flush | A contended remote can consume the merge and rescue budget in that cycle; the push fails and the commit stays local | The next cycle retries the push | The push error in the pull log beside the retained-commit WARN; ahead-count unchanged | Reversible | Single-threaded; `g.mu` held for the whole pull |
| An operator reads `git_rest_write_pending 0` as "delivered" | The gauge means "staged and awaiting a commit"; a retained commit is committed and awaiting a push, which the WARN record and the ahead-count distinguish | Operator compares `git rev-list --count origin/<branch>..HEAD` against the WARN records | WARN line and ahead-count | Reversible | N/A |
| Two flushes race on the same pending set | Impossible — the flush runs under `g.mu` | — | — | — | Mutex-serialised |
| Clock skew across pods | Not used on this path: the retained commit is identified by SHA, and no ordering decision reads a wall clock | — | — | Reversible | N/A |

## Suggested Decomposition

One prompt carries the whole change.

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | The one-rule flush path: keep HEAD at the commit any failed flush made, clear the pending set and the pending gauge, remove the reset helper, correct the retry helper's doc comment, emit the WARN record naming the commit and its path count, rewrite `TestFlushDeadlineCut` and `TestFlushRescueLandsAfterFlushDeadline` to the new contract, invert `TestFlushPushFailureKeepsPending` under its original name and add the regression test, amend spec 021, and record the change in `docs/deployment.md` § Write coalescing plus a `## Unreleased` CHANGELOG bullet naming the supersession | 1-8 | 1-10 | — |

Rationale: the flush path, the three test rewrites, the spec-021 amendment and the documentation are one behavior change under one rule; splitting them would leave a prompt whose only artifact is a log line, or a docs change describing a contract no prompt implements. AC 11 is operator-verified against the dev cluster after the PR merges and has no prompt row by design.
