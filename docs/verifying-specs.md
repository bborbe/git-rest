# Verifying Specs in git-rest

Project-specific extension of the generic `dark-factory:verify-spec` workflow. When a spec moves to `verifying` (all prompts completed, autoRelease tagged), walk it through three rungs before `dark-factory spec complete`.

The principle from `~/.claude/plugins/marketplaces/dark-factory/docs/spec-verification.md` applies: **tests passing ≠ feature works**. Find live evidence at every rung.

## The three rungs

| Rung | Where | What it catches | When sufficient |
|---|---|---|---|
| 1. Local binary against temp repo | host, fresh `go build`, ephemeral repo + bound port | HTTP semantics, file-on-disk + git-commit side effects, command/arg validation, idempotent retries | Pure-server specs (no k8s manifest change, no operator-visible behavior shift in prod) |
| 2. Dev cluster e2e | dev k8s, deployed image consumed by `vault-obsidian-{openclaw,personal,trading}` | Real PVC, real SSH key/git remote, agent-task-controller round-trip, cross-pod retry semantics | Anything that depends on the StatefulSet template, cron pull cadence, or in-cluster networking |
| 3. Prod cluster e2e | prod k8s | Real-traffic behavior at production scale (vault commits across workdays) | Specs that change throughput-sensitive paths or operator-visible behavior |

Rule of thumb: **always rung 1**. Rung 2 if anything in the Dockerfile or the StatefulSet env contract changed. Rung 3 promotes immediately after rung 2 passes — no mandatory soak. git-rest is small, the change set per spec is well-bounded, and rollback is fast (revert the `VERSION` pin in `~/Documents/workspaces/nuke/git-rest/Makefile` + re-apply). If dev passes, prod follows.

## Rung 1: local binary against a temp repo

git-rest is a single binary serving an HTTP API against a single git repo on disk. Build a fresh binary, point it at a throwaway repo, and exercise the API directly with `curl`.

```bash
# 1. Build fresh from current source
cd ~/Documents/workspaces/git-rest
go build -o /tmp/git-rest-verify .

# 2. Init temp repo
REPO=/tmp/git-rest-verify-repo && rm -rf "$REPO" && mkdir -p "$REPO"
cd "$REPO" && git init -q -b main
git config user.email verify@local && git config user.name verify
git commit -q --allow-empty -m init

# 3. Start server in background on a non-default port
/tmp/git-rest-verify -listen=:18444 -repo="$REPO" -pull-interval=24h -v=1 &
sleep 1

# 4. Drive the API with curl, asserting HTTP status + git side-effects
curl -s -w 'HTTP %{http_code}\n' -o /dev/null \
  -X POST -d 'hello' http://localhost:18444/api/v1/files/test.md
git -C "$REPO" log --oneline

# 5. Cleanup
pkill -f /tmp/git-rest-verify
rm -rf "$REPO" /tmp/git-rest-verify
```

What to assert per spec category:

| Spec touches | Assert |
|---|---|
| HTTP status semantics (e.g. spec-007 idempotent writes) | `HTTP 200`/`HTTP 404`/`HTTP 500` matches the spec's table; replay the spec's exact `## Reproduction` curl |
| File-on-disk side effects | File present/absent + content via `cat $REPO/path` |
| Git commit semantics | `git -C $REPO log --oneline` shows expected commits AND no spurious ones |
| Path validation | Test invalid paths (`../`, `/..`, absolute) return 400 |
| New CLI arg / env var | Pass via `-flag` and `ENV_VAR=...`; verify behavior changes; verify default unchanged |
| `git pull` cadence (e.g. spec-006 readiness) | Use a remote-clone fixture; observe pull cycles; cycle adjustments via `-pull-interval` |

### Rung 1 recipe: dirty working tree + moved remote

The shape: the served repo's working tree holds uncommitted changes to tracked files while the remote has moved. `git merge --ff-only` refuses rather than clobbering the working tree, so the pull aborts with `fast-forward merge failed`. This is **recoverable state, not a terminal one** — a healthy puller must report ready and heal the tree on its own.

> **The ordering below is a hard requirement, not an incidental detail.** The binary must boot against a **clean, level clone**; the remote is advanced and the tree dirtied only afterwards. Booting with the dirty state already present produces a false negative: the boot path's `recoverUntracked` runs `git add -A && git commit` on any `??` line (which stages tracked edits too), and `syncOnStartup` performs a raw `git pull` before the HTTP server serves. Either one clears the condition before the puller's first cycle, so the pull takes the committed-divergence route instead and the dirty-tree fast-forward failure never happens.

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

Assertions:

- **Tree clean:** `git -C "$BASE/work" status --porcelain` returns 0 lines.
- **Level again:** `git -C "$BASE/work" rev-parse HEAD` equals `git -C "$BASE/work" rev-parse origin/"$BRANCH"`.
- **Ready:** `curl -s -o /dev/null -w '%{http_code}' http://localhost:18445/readiness` returns `200`.
- **Exactly one rescue ref, legally named:** `git -C "$BASE/work" ls-remote --heads origin 'refs/heads/rescue/*'` returns exactly 1 ref matching `| grep -Eq 'refs/heads/rescue/[0-9]{8}T[0-9]{6}Z$'`.
- **Counted:** `curl -s localhost:18445/metrics | grep '^git_rest_pull_rescues_total'` reads `1`, asserted **while the process is still alive**. The counter is per-process and pre-initialised to 0 in `init()`, so the assertion is the absolute value, not a delta across restarts.

Then derive the ref from the **authoritative remote** (not from a tracking ref) and assert all four rescue-branch contents:

```bash
REF=origin/$(git -C "$BASE/work" ls-remote --heads origin 'refs/heads/rescue/*' | awk '{print $2}' | sed 's#refs/heads/##')

# The tracked edit and the untracked file are both in the rescue tree.
git -C "$BASE/work" show "${REF}:tasks/x.md"          # contains LOCAL UNCOMMITTED EDIT
git -C "$BASE/work" show "${REF}:tasks/scratch.md"    # contains scratch

# The staged deletion is a tree-vs-parent diff, and the path is ABSENT from the
# rescue tree (the tree is built from a temporary index where `git add -A`
# applied the deletion). Asserting `git show "${REF}:tasks/doomed.md"` would
# fail a correct implementation.
git -C "$BASE/work" diff --name-status "${REF}^" "${REF}" | grep -qE '^D\tasks/doomed\.md$'   # exits 0
git -C "$BASE/work" ls-tree -r --name-only "${REF}" | grep -c 'tasks/doomed\.md'               # 0

# The .gitignore'd file is excluded by construction.
git -C "$BASE/work" ls-tree -r --name-only "${REF}" | grep -cE 'secrets\.env$'                 # 0
```

The brace form `${REF}` is required: in zsh, `"$VAR:tasks/x.md"` is mangled by the `:t` / `:r` modifiers.

Negative probes. Three of them pin "only a dirty-tree failure is rescued":

- **(a) Clean tree, successful fast-forward.** Run the recipe with step 5 skipped: `git -C "$BASE/work" ls-remote --heads origin 'refs/heads/rescue/*'` returns 0 lines and `git_rest_pull_rescues_total` reads `0`.
- **(b) Clean tree, `merge --ff-only` fails for an environmental reason.** Run the recipe through step 4 with step 5 skipped — the remote **must** be ahead, or `localSHA == remoteSHA` returns before any merge is attempted. Then `touch "$BASE/work/.git/index.lock"` **while the binary runs** (booting with the lock present does not work: `main.go`'s `cleanupStaleLocks` deletes every `*.lock` under `.git` at startup) and wait one pull interval. Assert: 0 rescue refs, `git_rest_pull_rescues_total` reads `0`, and `grep -c 'fast-forward merge failed' "$BASE/run.log"` returns ≥1. This probe is constructible and definitively not a dirty tree, which is what pins the "every other cause" clause.
- **(c) `git status` itself fails.** Same setup as (b), with `chmod 000 "$BASE/work/.git/index"` in place of the lock, then the same three assertions.

Two more negatives close the mechanism:

- **(d) A rejected rescue push never resets.** Install a `pre-receive` hook in `origin.git` that exits 1 for `refs/heads/rescue/*`, **and point that repo at its own hooks directory** with `git -C "$BASE/origin.git" config core.hooksPath hooks` — a global `core.hooksPath` makes git ignore the per-repo `hooks/` directory entirely, so without that line the hook silently no-ops, the push succeeds, the implementation correctly resets, and this probe false-fails against correct code. Then run the recipe and assert: `git -C "$BASE/work" status --porcelain` still lists `tasks/x.md` as ` M` (the edit is still in the working tree, not discarded), `git -C "$BASE/work" rev-parse HEAD` is unchanged, and the log contains `rescue push failed, leaving repo for inspection`.
- **(e) The committed-divergence path is unchanged.** With a committed local divergence (no dirty file) against a moved remote, the two commits still merge — `git -C "$BASE/work" log --oneline` contains both subjects — and `ls-remote --heads origin 'refs/heads/rescue/*'` returns 0 lines.

Spec 014's `## Reproduction` is the authoritative form of this recipe; the version above is a condensation of it. The rung-2 section below is the next rung for this shape.

For specs whose ACs include a Reproduction section (`kind: bug` specs always do), replay the EXACT reproduction commands. Their HTTP status codes are the contract.

## Rung 2: dev cluster e2e

git-rest runs as `vault-obsidian-openclaw`, `vault-obsidian-personal`, and `vault-obsidian-trading` in the dev cluster (consumed by the agent-task-controller and the dark-factory pipelines).

Pre-conditions:
- Master is at the autoRelease tag for the spec (`git describe --tags --abbrev=0` matches the CHANGELOG entry's version)
- Image `bborbe/git-rest:vX.Y.Z` is pushed to docker.io (autoRelease only tags + pushes commits; image build is `make buca` from the git-rest repo — same flow as `[[git-rest - Deploy New Version]]` runbook in Personal vault)
- The `VERSION` pin in `~/Documents/workspaces/nuke/git-rest/Makefile` is bumped to the released tag and merged

Apply + verify:

```bash
cd ~/Documents/workspaces/nuke && git pull && cd git-rest && BRANCH=dev make apply

# Force-restart pods (the StatefulSet template uses `random:` annotation but a manual restart guarantees fresh pull)
kubectlnukedev -n dev rollout restart statefulset/vault-obsidian-openclaw
kubectlnukedev -n dev rollout restart statefulset/vault-obsidian-personal
kubectlnukedev -n dev rollout restart statefulset/vault-obsidian-trading

kubectlnukedev -n dev rollout status statefulset/vault-obsidian-openclaw --timeout=120s
kubectlnukedev -n dev rollout status statefulset/vault-obsidian-personal --timeout=120s
kubectlnukedev -n dev rollout status statefulset/vault-obsidian-trading --timeout=120s

# Verify image + readiness
kubectlnukedev -n dev get pod vault-obsidian-{openclaw,personal,trading}-0 \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.containers[0].image}{"\t"}{.status.conditions[?(@.type=="Ready")].status}{"\n"}{end}'
```

Then drive **real traffic** through the deployed pods. For most git-rest specs, the agent-task-controller is the canonical client. To exercise it:

```bash
# Trigger a build watcher poll → controller publishes → vault-obsidian-openclaw writes
kubectlnukedev -n dev exec maintainer-watcher-github-build-0 -- rm -f /data/cursor.json
kubectlnukedev -n dev exec maintainer-watcher-github-build-0 -- wget -qO- http://localhost:9090/trigger
sleep 6

# Verify controller ↔ vault-server interaction (no retry spam, single-line success)
kubectlnukedev -n dev logs agent-task-controller-0 --since=30s \
  | grep -E "create-task|update|attempt|consume"

# For bug specs, the canonical assertion is "the regression doesn't reproduce":
kubectlnukedev -n dev logs agent-task-controller-0 --since=30m \
  | grep -c "failed after 5 attempts"   # spec-007: must be 0 in steady state
```

The vault server's own logs are useful for white-box verification (see what HTTP status it returned per request):

```bash
kubectlnukedev -n dev logs vault-obsidian-openclaw-0 --since=5m \
  | grep -E "POST|status="
```

### Rung 2 recipe: the GKE agent vault (`vault-obsidian-agent`)

The nuke-hosted `vault-obsidian-{openclaw,personal,trading}` vaults are described above. The **agent vault** is a different deployment: a hand-written StatefulSet in namespace `agent` on GKE, reached with `kubectldev -n agent` on dev and `kubectlprod -n agent` on prod. The two are different clusters — the `kubectlnuke{dev,prod}` wrappers do not reach the agent vault.

**The repo inside the agent pod is `/data`, not `/data/repo`.** `vault-obsidian-agent-sts.yaml` sets `REPO` to `/data` and mounts the PVC at `mountPath: /data` with `subPath: repo`. `subPath` selects a directory *within the volume*; it is not appended to the container path, so the clone lands at `/data` and `git -C /data/repo` fails with `fatal: not a git repository` even on a correct deploy.

**The dev image is not built from this repo's working tree.** `agent/git-rest/Dockerfile` in `seibert-data/agent` is a one-line mirror (`FROM docker.io/bborbe/git-rest:<pinned tag>`), so `BRANCH=dev make buca` there re-tags whatever that pin names, and `--build-arg BUILD_GIT_COMMIT` on that path is inert because the mirror's Dockerfile declares no `ARG`. Getting a change onto dev therefore requires publishing a new git-rest image **and** moving that pin. Four steps, in order:

```bash
# 1. Publish the new git-rest image from the source repo. The `build` target is
#    gated by `check-version-tag`, which refuses any $(VERSION) whose tag is not
#    exactly at HEAD, so an untagged working tree needs ALLOW_UNTAGGED_BUILD=1.
cd ~/Documents/workspaces/git-rest
VERSION=dev ALLOW_UNTAGGED_BUILD=1 make buca     # publishes docker.io/bborbe/git-rest:dev

# 2. Move the one-line mirror's FROM pin to the tag published in step 1:
#      FROM docker.io/bborbe/git-rest:dev
cd ~/Documents/workspaces/sm-octopus/agent/git-rest

# 3. Rebuild the mirror image. This Makefile includes ../Makefile.docker, which is
#    where `buca` lives. (`vault/obsidian-agent/`'s Makefile does NOT include
#    Makefile.docker, so it carries no `buca` target — it is only where the
#    StatefulSet manifest lives.)
BRANCH=dev make buca

# 4. Restart the StatefulSet. Its image is the floating `git-rest:dev` tag and it
#    sets imagePullPolicy: Always, so a restart suffices — no manifest edit needed.
kubectldev -n agent rollout restart statefulset/vault-obsidian-agent
```

> **Step 2 MUST be reverted.** The pin move is a deliberate temporary deviation from the mirror's version-pin convention. The same Dockerfile also serves the **prod mirror** (`BRANCH=master make buca`), so a pin left at `:dev` would build the prod image from the dev tag. Restoring it to a released `vX.Y.Z` tag belongs to the prod-promotion task, not to a pre-merge verification.

Post-deploy assertions, after one pull interval:

```bash
kubectldev -n agent get pod vault-obsidian-agent-0 -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}'   # True
kubectldev -n agent exec vault-obsidian-agent-0 -- git -C /data rev-parse HEAD                                     # equals origin/master
kubectldev -n agent logs vault-obsidian-agent-0 --since=10m | grep -c 'rescue branch pushed'                        # >= 1
kubectldev -n agent get pod vault-obsidian-agent-0 -o jsonpath='{.status.startTime}'                                # later than the deploy
```

Build-identity check — the freshness anchor is the **build commit**, not the image tag:

```bash
# deploy_check: the build commit the pod is actually running. The trailing grep
# makes a missing metric exit non-zero, so the gate fails rather than passing falsely.
kubectldev -n agent exec vault-obsidian-agent-0 -- wget -qO- http://localhost:9090/metrics \
  | grep -m1 '^git_rest_build_info' | sed -E 's/.*commit="([^"]+)".*/\1/' | grep -E '^[0-9a-f]{7,40}$'

# deploy_target: the commit the source repo is at
$(git -C ~/Documents/workspaces/git-rest rev-parse --short HEAD)
```

The StatefulSet renders `image: '{{"IMAGE_PREFIX" | env}}/git-rest:{{"BRANCH" | env}}'`, so dev runs the floating tag `git-rest:dev`; comparing a pod's image tag against `:dev` would succeed no matter which build is deployed, i.e. a check that cannot fail. The commit baked into the binary can differ from `HEAD`, so this check discriminates. It is a **short-SHA against short-SHA** comparison: `Makefile` passes `--build-arg BUILD_GIT_COMMIT=$(git rev-parse --short HEAD)`, so `deploy_target` must use `--short` — a full 40-character `rev-parse HEAD` would never compare equal even on a correct deploy. `wget` is present in the image, and the metric renders as `git_rest_build_info{commit="2b492ed",date="…",version="…"} 1`.

Rung 3 (prod promotion and verifying the prod symptom) is a separate task for this spec and is deliberately not covered here.

## Rung 3: prod cluster e2e

Promote immediately after rung 2 passes. Same shape as rung 2 but the `nuke/git-rest` apply with `BRANCH=master` and `kubectlnukeprod -n prod`: `cd ~/Documents/workspaces/nuke && git pull && cd git-rest && BRANCH=master make apply`. Reference: `[[git-rest - Deploy New Version]]` runbook for the dev→prod promotion pattern (mirror image, apply manifests, watch one full task-controller poll cycle).

Real prod traffic exercises more repos and longer running times than dev's narrow allowlist; transient failures (rate limits, ssh-key permission changes, conflicted merges) only show up here. Rollback is fast (revert the image tag + re-apply) so promote without soak.

## Closing the spec

After all relevant rungs pass:

```bash
dark-factory spec complete <id>
```

If verification fails on any rung, do NOT mark complete. Either:

- File a follow-up bug spec with the failing reproduction (preferred for distinct regressions)
- Or write a fix prompt that closes the gap and re-run the rung that failed

## Rung selection by spec type

| Spec touches | Run rung 1 | Run rung 2 | Run rung 3 |
|---|---|---|---|
| Pure code path under `pkg/git/` (no HTTP shape change) | yes | optional | no |
| HTTP status semantics / new endpoint / new query param | yes | yes (real client = task-controller) | promote after soak |
| New CLI arg / env var | yes | yes (verify env injection from `dev.env`) | promote after soak |
| New k8s manifest / StatefulSet template change | rung 1 doesn't catch this | yes | promote after soak |
| Pull-cadence / readiness probe behavior | yes | yes (real cron) | promote after soak |
| Pure refactor / doc change | optional | no | no |

If unsure: rung 1 always; rung 2 if any of `k8s/` (does not exist in git-rest itself; its k8s lives in `nuke/git-rest/`), `Dockerfile`, or HTTP contract changed; rung 3 if rung 2 looked clean for ≥24h.

## Anti-patterns

- **"`make precommit` passed, marking complete."** Tests prove what the author thought. The dev cluster's task-controller proves what production sees. The two diverge regularly.
- **Skipping rung 1 because rung 2 is "more thorough".** Rung 1 is faster, deterministic, and exercises HTTP + git semantics with shorter feedback loop. Rung 2 surfaces deployment topology bugs but is high-overhead.
- **Marking the spec complete the same minute the rollout finishes.** Both pods are `Running` long before the controller has driven a real write through them. Wait at least one task-controller poll cycle.
- **Replaying the spec's Reproduction against the OLD installed binary.** Always build a fresh `/tmp/git-rest-verify` from current source — the OS-installed binary is whatever was last `go install`d.
- **Verifying without freezing the temp repo.** A temp repo with no commits behaves differently than one with an `init` commit (e.g. `git rm` semantics). Always `git commit --allow-empty -m init` first.

## See also

- Generic verification: `~/.claude/plugins/marketplaces/dark-factory/docs/spec-verification.md`
- Bug-spec verification (stricter; mandatory Reproduction replay): `~/.claude/plugins/marketplaces/dark-factory/docs/bug-workflow.md`
- API reference: `docs/api.md`
- Deployment: `docs/deployment.md`
- Definition of Done: `docs/dod.md`
- Deploy procedure (dev → prod): `[[git-rest - Deploy New Version]]` (Obsidian Personal vault)
