---
status: verifying
approved: "2026-09-25T09:34:46Z"
generating: "2026-09-25T10:29:14Z"
prompted: "2026-09-25T11:08:53Z"
verifying: "2026-09-25T11:50:37Z"
branch: dark-factory/bug-pull-cannot-recover-from-dirty-working-tree
---

## Summary

- The puller handles a *committed* divergence (specs 006/010/011), auto-commits *untracked* files on startup, and quarantines unresolvable merge conflicts (012/013) — but has no path for a **modified tracked file**.
- When the working tree holds uncommitted changes and the remote has moved, git refuses to fast-forward rather than overwrite them — and the puller returns that refusal without ever clearing or preserving the state.
- Nothing in the codebase then clears, preserves or parks the dirty state, so readiness reports not-ready on every subsequent cycle, indefinitely.
- Observed in production on `vault-obsidian-agent-0` (2026-09-24): one wedged puller stalled the whole agent-task-executor reconcile loop, so no Seibert-Data PR got a bot review.
- The fix adds a rescue path — capture the dirty state on a `rescue/<timestamp>` branch, then return the working tree to the remote — and never discards a change.

## Problem

`Pull`'s four-state machine routes a clean local at the merge-base to `git merge --ff-only` (`pkg/git/git.go:1212`). That command refuses to run when a tracked file has uncommitted modifications that the incoming commit also touches — it aborts rather than clobbering the working tree. The error is wrapped as `fast-forward merge failed` and returned; the repository is left exactly as it was, dirty and diverged, and the next pull cycle repeats the same abort. The pod's readiness probe therefore reports not-ready forever, and every consumer of that vault — including the agent-task-executor's reconcile loop — stalls behind it.

The three states the puller *does* recover are each a different shape: committed divergence (006/010/011), untracked files (auto-committed on startup), and resolver-failed conflicts (012/013 quarantine). A modified **tracked** file is the one shape with no handling, and it is the shape that took production down.

## Goal

A `git-rest` instance whose working tree holds uncommitted changes to tracked files recovers from a failed fast-forward on its own: the changes are preserved on a `rescue/<timestamp>` branch on the vault's own remote, the working tree is returned to the remote state, the pull completes, and readiness returns to ready within one pull interval. No operator action is required, and no change is ever discarded.

## Assumptions

- The vault's remote accepts new branch pushes from the pod's existing credentials. The pod already pushes to the same remote today (`git push` in the `remoteSHA == baseSHA` branch, and `commitAndPushMerge` on the conflict-resolver path), so the credential and ruleset path is proven; a `rescue/*` branch is a new ref name, not a new permission.
- The failure requires the local commit to be at the merge-base (`localSHA == baseSHA`) **and** the incoming commit to touch a path the working tree has modified. A dirty tree with no incoming collision still fast-forwards today and is unaffected.
- The dirty state must exist **after** the process has booted. Two boot-path behaviours would otherwise mask it: `recoverUntracked` (`main.go:158-186`) runs `git add -A && git commit` on any `??` line — staging tracked edits too — and `syncOnStartup` (`main.go:212-234`) performs a raw `git pull` before the HTTP server serves. The reproduction recipe below is ordered around this; it is not an incidental detail.
- `alerts.enabled` is irrelevant to this spec — it adds no alert (see Constraints). The counter is scraped regardless.
- **The dev image is not built from this repo's working tree.** `agent/git-rest/Dockerfile` in `seibert-data/agent` is a one-line mirror (`FROM docker.io/bborbe/git-rest:<pinned tag>`), so `BRANCH=dev make buca` there re-tags whatever that pin names; the `--build-arg BUILD_GIT_COMMIT` on that path is inert because the mirror's Dockerfile declares no `ARG`. Getting this change onto dev therefore requires publishing a new git-rest image *and* moving that pin — see Verification, rung 2. This is a pre-existing property of the environment, not something this spec changes.

## Reproduction

Deterministic, from a bare origin and two clones. **Ordering is load-bearing**: the binary boots against a clean, level clone, and the remote is advanced and the tree dirtied only afterwards. Booting with the dirty state already present lets `recoverUntracked` commit it away and `syncOnStartup` consume the divergence, so the puller never reaches the dirty-tree fast-forward failure at all — it takes the 006/012 divergence route instead and the symptom does not appear.

```bash
set -e
BASE=/tmp/git-rest-repro && rm -rf "$BASE" && mkdir -p "$BASE" && cd "$BASE"

# 1. Bare origin. tasks/doomed.md and .gitignore are tracked HERE, at the
#    merge-base: the staged deletion in step 5 then needs no commit of its own
#    (a commit would move HEAD off the merge-base and route the pull into the
#    diverged, out-of-scope path), and .gitignore survives the post-rescue
#    `git clean -fd` that would otherwise delete it and un-ignore secrets.env.
#    The branch is named explicitly so the recipe replays on a machine whose
#    push guardrail gates `master`.
git init -q --bare -b test/repro origin.git
git clone -q origin.git seed && cd seed
git config user.email repro@local && git config user.name repro
mkdir -p tasks && printf 'line one\n' > tasks/x.md && printf 'gone\n' > tasks/doomed.md
printf 'secrets.env\n' > .gitignore
git add -A && git commit -q -m init
BRANCH=$(git rev-parse --abbrev-ref HEAD) && git push -q origin "$BRANCH"
cd "$BASE"

# 2. Work clone — the repo the puller serves. Its remote is LEVEL at this point.
git clone -q origin.git work && cd work
git config user.email repro@local && git config user.name repro

# 3. Boot the puller FIRST, against the clean, level clone.
cd ~/Documents/workspaces/git-rest && go build -o /tmp/git-rest-repro-bin .
/tmp/git-rest-repro-bin -listen=:18445 -repo="$BASE/work" -pull-interval=10s -v=1 > "$BASE/run.log" 2>&1 &
sleep 3

# 4. NOW advance the remote, so the incoming commit touches tasks/x.md.
cd "$BASE/seed" && printf 'line one\nline two\n' > tasks/x.md
git add -A && git commit -q -m "remote touches x" && git push -q origin "$BRANCH"

# 5. NOW dirty the tree, four ways at once. No commit here: HEAD must stay at
#    the merge-base or the puller takes the diverged path instead.
#      - a TRACKED edit colliding with the incoming commit (this causes the abort)
#      - an UNTRACKED file that does not collide (the rescue must preserve it)
#      - a STAGED DELETION of a tracked file (the rescue must capture it)
#      - an IGNORED file, which the rescue must NOT capture
cd "$BASE/work" && printf 'line one\nLOCAL UNCOMMITTED EDIT\n' > tasks/x.md
printf 'scratch\n' > tasks/scratch.md
printf 'secret\n' > secrets.env
git rm -q tasks/doomed.md

# Guard the premise the fix depends on: local must still be at the merge-base.
echo "mergebase: $(git merge-base HEAD "origin/$BRANCH" | cut -c1-8)  HEAD: $(git rev-parse --short HEAD)"
git status --porcelain

# 6. Wait one pull interval, then read readiness AND the counter. Both must be
#    read while this process is alive — the counter is per-process.
sleep 12
curl -s -o /dev/null -w 'readiness HTTP %{http_code}\n' http://localhost:18445/readiness
curl -s http://localhost:18445/metrics | grep '^git_rest_pull_rescues_total'

# 7. Teardown. Every AC below re-runs this recipe on the same port, so each run
#    must start from a clean process — a survivor keeps answering :18445 and
#    returns stale evidence.
kill "$(pgrep -f /tmp/git-rest-repro-bin)" 2>/dev/null || true
```

Observed (verbatim from the production incident, `kubectlprod -n agent logs vault-obsidian-agent-0`):

```
puller.go:74 WARN git pull failed ... merge --ff-only origin/master ... Your local changes to the following files would be overwritten by merge: tasks/Fix Build - raw-marketplace-productlisting-fetcher dev lane fixture.md ... Aborting
```

Seen in 200/200 recent log lines; local HEAD pinned at `5b6f96afc` vs remote `3f78f27fc`; `/readiness` on `:9090` refusing since 17:34:05Z, restarts=9.

**Build/version evidence for this report** (the git-rest analogue of `dark-factory --version`, which does not apply to this repo): the deployed binary reports its own build identity as the gauge `git_rest_build_info{version,commit,date}` (`pkg/metrics/build_info.go:47-50`, fed by `BUILD_GIT_VERSION` / `BUILD_GIT_COMMIT` / `BUILD_DATE` declared at `main.go:46-48` and passed to `NewBuildInfoMetrics` at `main.go:59`). The `commit` label is a **short** SHA — `Makefile:156` passes `--build-arg BUILD_GIT_COMMIT=$(git rev-parse --short HEAD)`. Read it from the **production** pod that carried the incident:

```bash
kubectlprod -n agent exec vault-obsidian-agent-0 -- wget -qO- http://localhost:9090/metrics | grep '^git_rest_build_info'
```

(`kubectldev -n agent` is the equivalent read for the dev pod, and is what the Post-Deploy AC's `deploy_check` uses. Verified live 2026-09-25: `wget` is present in the image and the metric renders as `git_rest_build_info{commit="2b492ed",date="…",version="…"} 1`.)

## Expected vs Actual

**Expected.** Per spec 006's Desired Behavior, a `Pull` that cannot complete leaves the repository in a state the puller can heal on a later cycle, and per the readiness contract a healthy puller reports ready. A dirty working tree is recoverable state, not a terminal one: the changes are preserved and the sync completes.

**Actual.** `Pull` returns `fast-forward merge failed` with the repository untouched, and every subsequent cycle returns the same error. Readiness stays not-ready, so the whole consuming fleet stalls.

## Workaround

Operator-side only, and destructive if done carelessly: `git -C <repo> stash` or `git -C <repo> commit -am rescue` followed by `git pull`. There is no self-service recovery, which is why a single fixture loop became a fleet-wide outage. The spec prefers a rescue branch over a stash because a stash is local-only and invisible to anyone but the operator holding the volume — a rescue branch is recoverable from any clone of the vault's remote, and it survives the pod being replaced. That rationale belongs in `docs/` too, so it outlives this spec (see Suggested Decomposition, prompt 4).

## Why this is a bug

Spec 006 (`specs/completed/006-bug-pull-cannot-recover-from-divergence.md`) established that the puller must recover from divergence without operator intervention, and `recoverRepoState` already heals abandoned rebases, detached HEADs and leftover `MERGE_HEAD` — the codebase's stated posture is that recoverable git states are recovered. A dirty working tree is recoverable, is not covered by 006's committed-divergence path, and is not excluded by 006's constraints. The puller returning the raw `git` abort and never retrying is therefore a gap in the invariant 006 set, not a deliberate refusal.

## Acceptance Criteria

- [ ] **Dirty tracked file + remote ahead ⇒ pull completes and readiness returns.** Rung-1 recipe above, then: `git -C "$BASE/work" status --porcelain` returns 0 lines AND `git -C "$BASE/work" rev-parse HEAD` equals `git -C "$BASE/work" rev-parse origin/"$BRANCH"` AND `curl -s -o /dev/null -w '%{http_code}' http://localhost:18445/readiness` returns `200`. Readiness is the production symptom, so it is asserted here as well as at rung 2; one pull interval suffices, since `PullStateCache.lastSuccessAt` is set on the first success and its freshness threshold is `3 × PullInterval`.
- [ ] **Every dirty shape is preserved on the rescue branch, under a legal ref name — and ignored files are not.** `git -C "$BASE/work" ls-remote --heads origin 'refs/heads/rescue/*'` returns exactly 1 ref whose name matches the DB3 format (`| grep -Eq 'refs/heads/rescue/[0-9]{8}T[0-9]{6}Z$'` exits 0). Derive it from the authoritative remote rather than a tracking ref: `REF=origin/$(git -C "$BASE/work" ls-remote --heads origin 'refs/heads/rescue/*' | awk '{print $2}' | sed 's#refs/heads/##')`. Then assert all four: `git -C "$BASE/work" show "${REF}:tasks/x.md"` contains `LOCAL UNCOMMITTED EDIT`; `git -C "$BASE/work" show "${REF}:tasks/scratch.md"` contains `scratch`; the staged deletion is captured as a **tree-vs-parent diff** — `git -C "$BASE/work" diff --name-status "${REF}^" "${REF}" | grep -qE '^D\tasks/doomed\.md$'` exits 0 AND `git -C "$BASE/work" ls-tree -r --name-only "${REF}" | grep -c 'tasks/doomed\.md'` returns 0. The path is **absent** from the rescue tree, because DB2 builds that tree from a temporary index where `git add -A` applied the deletion; the deletion is what the tree records against its parent, and the pre-deletion content stays reachable at `HEAD:tasks/doomed.md`. Asserting `git show "${REF}:tasks/doomed.md"` would fail a correct implementation; and `git -C "$BASE/work" ls-tree -r --name-only "${REF}" | grep -cE 'secrets\.env$'` returns 0 (the `.gitignore`d file is excluded). The brace form `${REF}` is required — `"$REF:tasks/x.md"` is mangled by zsh's `:t` modifier.
- [ ] **The rescue is counted.** `curl -s localhost:18445/metrics | grep '^git_rest_pull_rescues_total'` reads `1`, asserted **while the recipe's process is still running** (the counter is pre-initialised to 0 in `init()` and the recipe starts a fresh process, so the assertion is the absolute value, not a delta across restarts).
- [ ] **Negative — only a dirty-tree failure is rescued.** Three probes, all required. (a) *Clean tree, successful fast-forward*: run the recipe with step 5 skipped — `ls-remote --heads origin 'refs/heads/rescue/*'` returns 0 lines and `git_rest_pull_rescues_total` reads `0`. (b) *Clean tree, `merge --ff-only` fails for an environmental reason*: run the recipe through step 4 with step 5 skipped (the remote **must** be ahead, or `localSHA == remoteSHA` returns at `git.go:1209` and no merge is attempted at all), then `touch "$BASE/work/.git/index.lock"` **while the binary runs** (booting with the lock present does not work — `main.go`'s `cleanupStaleLocks` deletes every `*.lock` under `.git` at startup) and wait one pull interval — `ls-remote --heads origin 'refs/heads/rescue/*'` returns 0 lines, `git_rest_pull_rescues_total` reads `0`, and `grep -c 'fast-forward merge failed' "$BASE/run.log"` returns ≥1 (the log lives under `$BASE`, never in the repo under audit). (c) *`git status` itself fails*: same setup as (b) with `chmod 000 "$BASE/work/.git/index"` in place of the lock, then the same three assertions. Probe (b) is constructible and definitively not a dirty tree, so it is what pins DB1's "every other cause" clause.
- [ ] **Negative — a failed rescue push never resets.** Install a `pre-receive` hook in `origin.git` that exits 1 for `refs/heads/rescue/*`, and **point that repo at its own hooks directory** — `git -C "$BASE/origin.git" config core.hooksPath hooks` — because a global `core.hooksPath` (set on this machine to `~/.git-hooks`) makes git ignore the per-repo `hooks/` directory entirely. Without that line the hook silently no-ops, the push succeeds, the implementation correctly resets, and this AC false-fails against correct code — on the very AC that legalises the 006 amendment. Then run the recipe and assert: `git -C "$BASE/work" status --porcelain` still lists `tasks/x.md` as ` M` (the edit is still in the working tree, not discarded) AND `git -C "$BASE/work" rev-parse HEAD` is unchanged AND the log contains `rescue push failed, leaving repo for inspection`.
- [ ] **Negative — spec 006's committed-divergence path is unchanged.** With a committed local divergence (no dirty file) against a moved remote, the two commits still merge and `git -C "$BASE/work" log --oneline` contains both subjects; `ls-remote --heads origin 'refs/heads/rescue/*'` returns 0 lines. DB7's remaining two behaviours — the `remoteSHA == baseSHA` push path and the `localSHA == remoteSHA` no-op path (`git.go:1209-1222`) — are covered by the existing `pkg/git` integration suite via `make test` rather than re-asserted here.
- [ ] **Post-Deploy (Rung-2):** on dev, a dirty tracked file plus a moved remote recovers without operator action — evidence: after one pull interval, `kubectldev -n agent get pod vault-obsidian-agent-0 -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}'` returns `True`, AND `kubectldev -n agent exec vault-obsidian-agent-0 -- git -C /data rev-parse HEAD` equals `origin/master`, AND `kubectldev -n agent logs vault-obsidian-agent-0 --since=10m | grep -c 'rescue branch pushed'` returns ≥1. The operator additionally confirms the pod rolled after the deploy: `kubectldev -n agent get pod vault-obsidian-agent-0 -o jsonpath='{.status.startTime}'` is later than the deploy.
  - `deploy_check:` `kubectldev -n agent exec vault-obsidian-agent-0 -- wget -qO- http://localhost:9090/metrics | grep -m1 '^git_rest_build_info' | sed -E 's/.*commit="([^"]+)".*/\1/' | grep -E '^[0-9a-f]{7,40}$'`
  - `deploy_target:` `$(git -C ~/Documents/workspaces/git-rest rev-parse --short HEAD)`

  **The comparison is short-SHA against short-SHA.** `deploy_target` must use `--short`: `Makefile:156` bakes `--build-arg BUILD_GIT_COMMIT=$(git rev-parse --short HEAD)`, so the metric's `commit` label is a short SHA and a full 40-character `rev-parse HEAD` would never compare equal even on a correct deploy.

  **The repo inside the pod is `/data`, not `/data/repo`.** `vault-obsidian-agent-sts.yaml` sets `REPO` to `/data` (line 44 names the key, line 45 carries the value) and mounts the PVC at `mountPath: /data` with `subPath: repo` (lines 100-102). `subPath` selects a directory *within the volume*; it is not appended to the container path, so the clone lands at `/data` and `git -C /data/repo` would fail with `fatal: not a git repository` even on a correct deploy. The trailing `grep -E '^[0-9a-f]{7,40}$'` is what makes `deploy_check` exit non-zero when the metric is missing, as the dark-factory plugin's `docs/rules/spec-writing.md` requires.

  **Cluster is `kubectldev -n agent`, not `kubectlquant -n dev`.** Verified live 2026-09-25: `kubectldev -n agent get statefulset vault-obsidian-agent -o jsonpath='{.spec.template.spec.containers[0].image}'` returns `europe-west3-docker.pkg.dev/smedia-octopus-dev/octopus/git-rest:dev`, and the prod peer answers to `kubectlprod -n agent` with `…:master`. The agent vault is a GKE deployment in namespace `agent` on both stages. `docs/verifying-specs.md`'s rung-2 recipe names `kubectlnukedev -n dev` and the nuke-hosted `vault-obsidian-{openclaw,personal,trading}` vaults; it does not describe the agent vault.

  **Why the freshness anchor is the build commit, not the image tag.** The StatefulSet renders `image: '{{"IMAGE_PREFIX" | env}}/git-rest:{{"BRANCH" | env}}'`, so dev runs the floating tag `git-rest:dev`. Comparing a pod's image tag against `:dev` succeeds no matter which build is deployed, so a tag-based `deploy_target` would be a check that cannot fail. The commit baked into the binary can differ from `HEAD`, so this check discriminates.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — format, generate, test and lint clean
- `make test` — unit + integration suite passes
- a test that scrapes `/metrics` and reads `git_rest_pull_rescues_total` as `0` before any rescue — a name grep over `metrics.go` proves neither declaration nor `init()` pre-registration
- a test assertion (not a grep) that the `reset --hard` and `git clean` call sites are unreachable unless the rescue push returned success in the same call path — `grep -rn 'reset --hard' pkg/git/` currently matches **zero** lines, so it cannot distinguish pre- from post-change and proves nothing

### Operator-executable (runs on the host after PR merge, per `docs/verifying-specs.md`)

- **Rung 1**: build a fresh binary and replay the `## Reproduction` recipe above; the three positive ACs and the three negative ACs are asserted there.
- **Rung 2** — four steps, in order, because dev does not build from source. **Operator-executable only, never a prompt deliverable**: step 2 edits `seibert-data/agent`, a different repo whose remote this project's container holds no credentials for.
  1. In `~/Documents/workspaces/git-rest`: `VERSION=dev ALLOW_UNTAGGED_BUILD=1 make buca` — the `build` target is gated by `check-version-tag`, which refuses any `$(VERSION)` whose tag is not exactly at HEAD, so an untagged working tree needs `ALLOW_UNTAGGED_BUILD=1`. This publishes `docker.io/bborbe/git-rest:dev`.
  2. In `~/Documents/workspaces/sm-octopus/agent/git-rest`: set the `FROM` pin in its one-line `Dockerfile` to the tag published in step 1 — `FROM docker.io/bborbe/git-rest:dev` for this pre-merge check. **This is a deliberate temporary deviation from the mirror's version-pin convention, and it must be reverted.** The same Dockerfile also serves the prod mirror (`BRANCH=master make buca`), so a pin left at `:dev` would build the prod image from the dev tag. Restoring it to a released version tag (`vX.Y.Z`, cut by the repo's own autoRelease flow once the PR merges) belongs to the prod-promotion task, which is out of scope here.
  3. In the same directory: `BRANCH=dev make buca` — this Makefile includes `../Makefile.docker`, which is where `buca` lives. (`vault/obsidian-agent/`'s Makefile does **not** include `Makefile.docker`, so it carries no `buca` target; it is only where the StatefulSet manifest lives.)
  4. `kubectldev -n agent rollout restart statefulset/vault-obsidian-agent` — the StatefulSet's image is the floating `git-rest:dev` tag and sets `imagePullPolicy: Always`, so a restart suffices and no manifest edit is needed.
  Then assert the Post-Deploy AC above.
- Rung 3 is **out of scope for this spec** — promoting to prod and verifying the prod symptom is a separate task (see the parent vault task).

## Desired Behavior

1. On `git merge --ff-only` failure, the puller distinguishes a dirty working tree from every other cause by inspecting the working tree **before** the merge is attempted, never by matching git's abort text — the abort wording is git-version-dependent. "Dirty" means `git status --porcelain` lists at least one path: a staged change, an unstaged change to a tracked file, a deletion, or an untracked file — the last because `merge` refuses to overwrite untracked paths too, and the same rescue preserves it. If the listing is empty, or if `git status` itself **fails**, the failure is treated as *not* a dirty-tree failure: the merge is still attempted, and the error returned is the merge's own, wrapped as today. (A failing `git status` must not short-circuit the merge — AC 4(c) asserts the merge's error is what surfaces.)
2. When the failure is a dirty working tree, the puller creates a commit object capturing **the whole working-tree state — staged, unstaged, deletions and untracked alike, excluding `.gitignore`d paths** — without moving HEAD and without touching the real index. The invariant is what matters: stage into a **temporary, non-existent** index path, write the tree, and create the commit against HEAD as parent. `GIT_INDEX_FILE=$(mktemp)` does **not** work — `mktemp` creates a zero-byte file and git rejects a zero-length index with `index file smaller than expected`; the path must not exist yet (`$(mktemp -u)`, or `"$(mktemp -d)/index"`). The real index and the working tree are left byte-for-byte as they were. `git stash create` is **not** sufficient and must not be used: it captures tracked changes only and returns an empty SHA for an untracked-only tree, so a rescue built on it would satisfy DB1's dirty definition on paper while silently omitting untracked content that the follow-on reset then destroys.
3. It points a local ref `refs/heads/rescue/<timestamp>` at that commit and pushes it to the configured upstream remote, where `<timestamp>` is ISO-8601 **basic** UTC (`20060102T150405Z`) — a legal git ref name, which the extended form is not, because git refs may not contain `:`. Creating the local ref is required, not cosmetic: it is what lets an operator re-push a failed rescue by hand (Failure Modes row 2).
4. Only if that push succeeds does it (a) `git reset --hard` the local branch to the upstream tracking ref, and (b) remove the captured untracked paths so the working tree actually returns to the remote state — `git clean -fd`, which leaves `.gitignore`d files alone. Both are required for AC 1's clean-tree assertion to be reachable: `git reset --hard` does not remove an untracked file when HEAD never moved, because the file is not in the reset target's tree and is not in the way of writing. It then re-evaluates `Pull`'s state machine **inline**. The re-evaluation is a no-op: after the reset `localSHA == remoteSHA`, so the first case returns immediately. It must be inline — calling `g.Pull(ctx)` recursively would deadlock, because `Pull` holds `g.mu` for its whole body and `sync.Mutex` is not reentrant.
5. If the rescue push fails for any reason, the puller does **not** reset, does **not** clean, does not retry destructively, and returns an error naming the failed rescue; the repository is left for inspection with the changes intact — HEAD unmoved, the working tree still dirty, and the local `rescue/<ts>` ref present so the push can be retried by hand.
6. Every successful rescue increments `git_rest_pull_rescues_total` exactly once, and logs exactly one INFO line beginning `rescue branch pushed`, naming the branch and the captured **paths** — e.g. `rescue branch pushed branch=rescue/20260925T120000Z files=3 paths=tasks/x.md,tasks/scratch.md,tasks/doomed.md`. Paths, not only a count: the line exists so an unexpected publication is visible, and a count cannot make that visible. No truncation threshold — the captured set is bounded by the working tree, which the puller already owns.
7. A clean fast-forward, a push, a no-op, and spec 006's committed-divergence merge all behave exactly as they do today.

## Constraints

- **Amends spec 006.** 006's constraint reads "MUST NOT auto-clobber local commits. … `git reset --hard` and `git rebase --abort` are NEVER invoked automatically." This spec narrows that ban: `git reset --hard` (and the paired `git clean -fd`) become legal **only** on the path where a rescue push has already succeeded, so no commit can be lost. `git rebase --abort` remains banned everywhere. The implementing prompt updates 006's constraint text to point at this spec, so a reader of 006 finds the amendment.
- **No new alert in this spec.** Alerting for the agent vault belongs in `seibert-data/agent/alerts/`, the only place an alert reaches `vault-obsidian-agent` — the agent vault is a hand-written StatefulSet in that repo, not a render of this repo's `helm/` chart, so an alert added to `helm/templates/alerts.yaml` would never reach it. That alert is a separate spec in that repo; this spec ships the counter that makes it expressible. The readiness concern belongs to a separate sibling task in the Personal vault ("VaultObsidianNotReady Prometheus alert") and must not be duplicated here.
- **Do not change spec 012/013's conflict path.** Quarantine, the resolver interface, and the `_conflicts/` layout are untouched. 012's non-goals explicitly exclude non-resolver git errors, which is the branch this spec adds to.
- **Do not change the public HTTP contract** in `docs/api.md` — no new endpoint, no changed status code, no changed response shape, and no change to the readiness semantics.
- **Preserve the repo's existing invariants.** Three are documented in `CLAUDE.md`: errors wrapped with `github.com/bborbe/errors` (never `fmt.Errorf`), metrics under the `git_rest_` prefix, and no `context.Background()` in `pkg/`. Three more hold in the code but are **not** in `CLAUDE.md` — `log/slog` for logging (`pkg/puller/puller.go:9`), timestamps from the injected `libtime` getter rather than `time.Now()` (`git.go`'s `currentDateTimeGetter`), and counters pre-initialised in `init()` (`pkg/metrics/metrics.go:67`). The prompt adds the undocumented three to `CLAUDE.md` so the conventions outlive this spec, rather than citing that file as a home it does not currently provide.
- **Adding a method to the `Metrics` interface** (`pkg/metrics/metrics.go:123-142`) requires regenerating `mocks/metrics.go` with counterfeiter (`make precommit` runs the generator); without it the build breaks.
- **`reset --hard` targets the upstream tracking ref**, never a hardcoded branch name — the branch is derived from `@{u}`, as `Pull` already does.
- **One rescue branch per rescue event**, named with a UTC timestamp; the puller never force-pushes and never deletes a rescue branch.
- **A rescue publishes working-tree content to a shared remote.** Untracked files are already auto-committed and pushed on startup, so the delta is small, and `.gitignore`d paths are excluded by construction — but the publication is real and is asserted in AC 2.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection | Reversibility |
|---|---|---|---|---|
| Working tree dirty, remote moved (the bug) | Capture the pending changes as an object-only commit (HEAD unmoved, index untouched), push `rescue/<ts>`, then reset and clean to the upstream state | None needed — self-healed | INFO log naming the rescue branch and its paths; `git_rest_pull_rescues_total` increments | Reversible — the change is on the rescue branch |
| Rescue push rejected (ruleset, auth, network) | Do **not** reset and do **not** clean; return an error naming the failed rescue; repo left dirty and intact, local ref present | Operator runs `git -C <repo> push origin rescue/<ts>` (the local ref DB3 creates makes this possible) and confirms `git ls-remote origin 'refs/heads/rescue/*'` lists it; readiness returns 200 within one pull interval | Error log `rescue push failed, leaving repo for inspection`; readiness stays not-ready | Reversible — nothing was discarded |
| `merge --ff-only` fails for a reason other than a dirty tree | Today's behavior unchanged: wrap and return the error | As today | Existing error log | As today |
| Two pull cycles overlap | Impossible — `Pull` holds `g.mu` for its whole body (`git.go:1174-1175`) | — | — | — |
| Rescue branch already exists for the same timestamp | Not reachable in practice; if it occurs the push fails and the no-reset path applies | Operator inspects; confirms the ref with `git ls-remote origin 'refs/heads/rescue/*'` | Error log as above | Reversible |
| Timestamp clock skew | Branch name is a label, not an ordering key; a skewed name still preserves the content | None | — | Reversible |
| Disk exhaustion while building the rescue commit | Commit fails; do **not** reset or clean; return the error | Operator frees space and confirms `git -C <repo> status --porcelain` is unchanged | Error log | Reversible — changes intact |
| Crash between a successful rescue push and the reset | The content is already safe on `rescue/<ts>`; the tree stays dirty and the next cycle creates a **second** `rescue/<ts>` branch | None needed — the content is preserved; the duplicate ref is harmless and is never deleted or force-pushed (Constraints) | Two `rescue/*` refs at adjacent timestamps; the `rescue branch pushed` INFO line appears twice | Reversible — both branches hold the content |
| Pod boots with a dirty tree | The boot path does **not** rescue, and it may also *consume* the dirty state: `recoverUntracked` runs `git add -A && git commit` on any `??` line (staging tracked edits too), and `syncOnStartup` performs a raw `git pull` before the server serves. Either can clear the condition before the puller's first cycle — so no rescue fires, and none is needed | None needed in the `recoverUntracked` case (the content is committed and pushed). If the tree is still dirty at the first pull cycle, the rescue fires normally | `recovering untracked files from prior crash` / `startup git pull failed (puller will retry)` warns; then either a normal pull or the `rescue branch pushed` INFO line | Reversible |
| Dirty tree in the **diverged** state (`localSHA != baseSHA`) | **Not covered by this spec — a known remaining gap of the same class.** `pullMergeAndPush` → `git merge --no-edit` (`git.go:696`) aborts on the same dirty-tree message, `parseMergeConflictPaths` finds no conflict, and readiness stays not-ready | Operator clears the tree by hand; extending the rescue to this path is a follow-up spec | Existing `git pull failed` warn; readiness stays not-ready | Reversible |
| The dev image mirror still pins the old release | The pod keeps running the old binary and the Post-Deploy AC's `deploy_check` never matches `deploy_target` — the gate fails rather than passing falsely, which is the intended behaviour | Operator completes rung-2 step 2 (move the `FROM` pin) and re-runs | `deploy_check` output differs from `deploy_target`; `git_rest_build_info` shows the old `version`/`date` | Reversible |

## Security / Abuse Cases

This fix adds a publication path: working-tree content that previously never left the pod is now pushed to a shared remote under `rescue/*`. The exposure is bounded and deliberate.

- **Ignored files are excluded by construction.** The rescue tree is built from a temporary index fed by `git add -A`, which honours `.gitignore`; `secrets.env` and `*.pem` are absent from the rescue commit. Asserted in AC 2.
- **The blast radius is a new ref name, not a new permission.** The pod already pushes to this remote on the `remoteSHA == baseSHA` path and via `commitAndPushMerge`, so the rescue adds no credential or ruleset surface.
- **The publication is observable.** DB6's log line names the captured paths, so an unexpected file appears in the pod log rather than only on the remote.
- **Untracked files were already published.** `recoverUntracked` auto-commits and pushes them on startup today, so the marginal disclosure is the *staged/unstaged tracked* content — content that would otherwise have been lost.
- **The rescue branch is never deleted or force-pushed** (Constraints), so a published secret is not silently rewritten; removing it is a deliberate operator action.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Pre-merge dirty-tree detection in `pkg/git`; the temporary-index object-only commit (tracked + untracked + deletions, HEAD unmoved, real index untouched); push to `rescue/<ts>`; reset + clean to `@{u}`; the no-reset/no-clean-on-failed-push guard; inline re-evaluation of the state machine | 1, 2, 3, 4, 5 | 1, 2, 5 | — |
| 2 | `git_rest_pull_rescues_total` counter + `Metrics` interface method + regenerated `mocks/metrics.go` + the INFO/error log lines naming the captured paths | 6 | 3, 4 | prompt 1 |
| 3 | Regression tests: clean fast-forward, non-dirty ff-only failure, untracked-only tree, deletion capture, `.gitignore` exclusion, 006's committed-divergence merge; 006 constraint amendment; `CLAUDE.md` gains the three undocumented invariants; CHANGELOG under `## Unreleased` | 7 | 4, 6 | prompts 1, 2 |
| 4 | Docs: `docs/verifying-specs.md` — add this shape's rung-1 recipe (including the boot-ordering constraint), correct the rung-2 section to the GKE agent vault, and record the mirror-pin step and its revert; `docs/` gains the stash-vs-rescue-branch rationale | — | 7 (operator-executable, not a prompt deliverable) | prompts 1, 2 |

Rationale: prompt 1 establishes the recovery contract and the safety guard; prompt 2 is the observable that makes it countable; prompt 3 locks the untouched paths and covers the shapes the mechanism exists to protect — untracked, deletion, and ignored-file exclusion; prompt 4 leaves the next puller bug a replayable shape (with the boot-ordering trap recorded) and a durable rationale instead of a re-derivation. AC 7 is the operator-executable rung-2 check and is not produced by any prompt — it is asserted during spec verification.
