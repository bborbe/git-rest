# Verifying Specs in git-rest

Project-specific extension of the generic `dark-factory:verify-spec` workflow. When a spec moves to `verifying` (all prompts completed, autoRelease tagged), walk it through three rungs before `dark-factory spec complete`.

The principle from `~/.claude/plugins/marketplaces/dark-factory/docs/spec-verification.md` applies: **tests passing ≠ feature works**. Find live evidence at every rung.

## The three rungs

| Rung | Where | What it catches | When sufficient |
|---|---|---|---|
| 1. Local binary against temp repo | host, fresh `go build`, ephemeral repo + bound port | HTTP semantics, file-on-disk + git-commit side effects, command/arg validation, idempotent retries | Pure-server specs (no k8s manifest change, no operator-visible behavior shift in prod) |
| 2. Dev cluster e2e | dev k8s, deployed image consumed by `vault-obsidian-{openclaw,trading}` | Real PVC, real SSH key/git remote, agent-task-controller round-trip, cross-pod retry semantics | Anything that depends on the StatefulSet template, cron pull cadence, or in-cluster networking |
| 3. Prod cluster e2e | prod k8s | Real-traffic behavior at production scale (vault commits across workdays) | Specs that change throughput-sensitive paths or operator-visible behavior |

Rule of thumb: **always rung 1**. Rung 2 if anything in the Dockerfile or the StatefulSet env contract changed. Rung 3 promotes immediately after rung 2 passes — no mandatory soak. git-rest is small, the change set per spec is well-bounded, and rollback is fast (revert the two `vault/obsidian-*-sts.yaml` image tags + re-apply). If dev passes, prod follows.

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

For specs whose ACs include a Reproduction section (`kind: bug` specs always do), replay the EXACT reproduction commands. Their HTTP status codes are the contract.

## Rung 2: dev cluster e2e

git-rest runs as `vault-obsidian-openclaw` and `vault-obsidian-trading` in the dev cluster (consumed by the agent-task-controller and the dark-factory pipelines).

Pre-conditions:
- Master is at the autoRelease tag for the spec (`git describe --tags --abbrev=0` matches the CHANGELOG entry's version)
- Image `bborbe/git-rest:vX.Y.Z` is pushed to docker.io (autoRelease only tags + pushes commits; image build is `make buca` from the git-rest repo — same flow as `[[git-rest - Deploy New Version]]` runbook in Personal vault)
- trading repo's `shared/base/Makefile` `BASE_IMAGES` list and the two `vault/obsidian-*/`*-sts.yaml` references are bumped + merged + pushed
- `trading-dev` worktree synced; `make build` from `shared/base/` mirrored the new tag to the quant registry

Apply + verify:

```bash
cd ~/Documents/workspaces/trading-dev
git pull && git merge master --no-edit && git push

# Mirror image to quant registry (registry shared across dev/prod, no BRANCH= needed)
cd shared/base && make build

# Apply manifests
cd ../../vault/obsidian-openclaw && BRANCH=dev make buca
cd ../obsidian-trading && BRANCH=dev make buca

# Force-restart pods (the StatefulSet template uses `random:` annotation but a manual restart guarantees fresh pull)
kubectlquant -n dev rollout restart statefulset/vault-obsidian-openclaw
kubectlquant -n dev rollout restart statefulset/vault-obsidian-trading

kubectlquant -n dev rollout status statefulset/vault-obsidian-openclaw --timeout=120s
kubectlquant -n dev rollout status statefulset/vault-obsidian-trading --timeout=120s

# Verify image + readiness
kubectlquant -n dev get pod vault-obsidian-{openclaw,trading}-0 \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.containers[0].image}{"\t"}{.status.conditions[?(@.type=="Ready")].status}{"\n"}{end}'
```

Then drive **real traffic** through the deployed pods. For most git-rest specs, the agent-task-controller is the canonical client. To exercise it:

```bash
# Trigger a build watcher poll → controller publishes → vault-obsidian-openclaw writes
kubectlquant -n dev exec maintainer-watcher-github-build-0 -- rm -f /data/cursor.json
kubectlquant -n dev exec maintainer-watcher-github-build-0 -- wget -qO- http://localhost:9090/trigger
sleep 6

# Verify controller ↔ vault-server interaction (no retry spam, single-line success)
kubectlquant -n dev logs agent-task-controller-0 --since=30s \
  | grep -E "create-task|update|attempt|consume"

# For bug specs, the canonical assertion is "the regression doesn't reproduce":
kubectlquant -n dev logs agent-task-controller-0 --since=30m \
  | grep -c "failed after 5 attempts"   # spec-007: must be 0 in steady state
```

The vault server's own logs are useful for white-box verification (see what HTTP status it returned per request):

```bash
kubectlquant -n dev logs vault-obsidian-openclaw-0 --since=5m \
  | grep -E "POST|status="
```

## Rung 3: prod cluster e2e

Promote immediately after rung 2 passes. Same shape as rung 2 but `trading-prod` worktree, `BRANCH=prod`, and `kubectlquant -n prod`. Reference: `[[git-rest - Deploy New Version]]` runbook for the dev→prod promotion pattern (mirror image, apply manifests, watch one full task-controller poll cycle).

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

If unsure: rung 1 always; rung 2 if any of `k8s/` (does not exist in git-rest itself; its k8s lives in `trading/vault/`), `Dockerfile`, or HTTP contract changed; rung 3 if rung 2 looked clean for ≥24h.

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
