---
status: completed
spec: [014-bug-pull-cannot-recover-from-dirty-working-tree]
summary: 'Documented the dirty-tree rescue: added the rung-1 recipe (with the load-bearing boot-ordering constraint and all five negative probes) and the GKE agent-vault rung-2 recipe to docs/verifying-specs.md, and the stash-vs-rescue-branch rationale plus the git_rest_pull_rescues_total counter to docs/deployment.md.'
execution_id: git-rest-exec-057-spec-014-docs
dark-factory-version: v0.196.0
created: "2026-09-25T10:39:18Z"
queued: "2026-09-25T11:34:36Z"
started: "2026-09-25T11:47:12Z"
completed: "2026-09-25T11:50:37Z"
branch: dark-factory/bug-pull-cannot-recover-from-dirty-working-tree
---

# Document the dirty-tree rescue and correct the agent-vault verification path

<summary>
- The next person debugging a wedged puller finds a replayable recipe for the dirty-working-tree shape
- The recipe records the boot-ordering trap, so a replayer does not accidentally let the boot path clear the state before the puller's first cycle
- The verification doc stops pointing only at the nuke-hosted vaults and describes the GKE agent vault where this incident actually happened
- The dev image's one-line mirror and its temporary pin step are written down, including the revert that must follow
- The choice of a rescue branch over a stash is recorded, so the next puller bug does not re-derive it
- Nothing about the code, the metrics or the tests changes
</summary>

<objective>
Make the puller's new dirty-tree rescue replayable and its rationale durable: add the shape's rung-1 recipe (with the boot-ordering constraint) and the GKE agent-vault rung-2 path to `docs/verifying-specs.md`, and record the stash-vs-rescue-branch decision plus the new counter in `docs/deployment.md`.
</objective>

<context>
This is a documentation-only prompt. No Go file, no metric, no test and no manifest changes.

Read these before editing:

- `docs/verifying-specs.md` — the whole file. Its `## Rung 1: local binary against a temp repo` section is where the new recipe goes; its `## Rung 2: dev cluster e2e` section currently names `kubectlnukedev -n dev` and the nuke-hosted `vault-obsidian-{openclaw,personal,trading}` vaults and does not describe the agent vault.
- `docs/deployment.md` — its `## Operational notes` section (the "Conflict handling" bullet documents the puller's recovery behaviour today) and its `### Quarantine backlog` subsection (the precedent for documenting a metric and its drain path).
- `specs/in-progress/014-bug-pull-cannot-recover-from-dirty-working-tree.md` — `## Reproduction` (the authoritative recipe to condense), `## Workaround` (the stash-vs-rescue rationale), `## Acceptance Criteria` (the Post-Deploy rung-2 assertions), `## Verification` (the rung-2 four-step procedure) and the `## Assumptions` bullet about the dev image not being built from this repo's working tree.
- `pkg/git/git.go` — `rescueDirtyTree` (added by prompt 1): the branch name, the INFO line beginning `rescue branch pushed` with `branch` / `files` / `paths` attributes, and the ERROR line `rescue push failed, leaving repo for inspection`.
- `pkg/metrics/metrics.go` — `PullRescuesTotal` (`git_rest_pull_rescues_total`, added by prompt 2).
- `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md` — house style for prose, tables and code fences.
- `docs/dod.md` — the definition of done.

**Facts to carry verbatim into the docs (all verified 2026-09-25; do not paraphrase the commands or the paths):**

- The agent vault is a hand-written GKE StatefulSet in namespace `agent`, on both stages: `kubectldev -n agent` for dev and `kubectlprod -n agent` for prod. `docs/verifying-specs.md`'s rung-2 section names `kubectlnukedev -n dev` and the nuke-hosted vaults; it does not describe the agent vault at all. Add the agent vault; do not delete the nuke-hosted section.
- `kubectldev -n agent get statefulset vault-obsidian-agent -o jsonpath='{.spec.template.spec.containers[0].image}'` returns `europe-west3-docker.pkg.dev/smedia-octopus-dev/octopus/git-rest:dev`; the prod peer answers to `kubectlprod -n agent` with the `:master` tag.
- The repo inside the agent pod is `/data`, NOT `/data/repo`. `vault-obsidian-agent-sts.yaml` sets `REPO` to `/data` and mounts the PVC at `mountPath: /data` with `subPath: repo`; `subPath` selects a directory within the volume and is not appended to the container path, so the clone lands at `/data` and `git -C /data/repo` fails with `fatal: not a git repository` even on a correct deploy.
- The dev image is NOT built from this repo's working tree. `agent/git-rest/Dockerfile` in `seibert-data/agent` is a one-line mirror (`FROM docker.io/bborbe/git-rest:<pinned tag>`), so `BRANCH=dev make buca` there re-tags whatever that pin names, and `--build-arg BUILD_GIT_COMMIT` on that path is inert because the mirror's Dockerfile declares no `ARG`. Getting a change onto dev therefore requires publishing a new git-rest image AND moving that pin.
- The rung-2 procedure, four steps in order:
  1. In `~/Documents/workspaces/git-rest`: `VERSION=dev ALLOW_UNTAGGED_BUILD=1 make buca`. The `build` target is gated by `check-version-tag`, which refuses any `$(VERSION)` whose tag is not exactly at HEAD, so an untagged working tree needs `ALLOW_UNTAGGED_BUILD=1`. This publishes `docker.io/bborbe/git-rest:dev`.
  2. In `~/Documents/workspaces/sm-octopus/agent/git-rest`: set the `FROM` pin in its one-line `Dockerfile` to the tag published in step 1 — `FROM docker.io/bborbe/git-rest:dev` for a pre-merge check. **This is a deliberate temporary deviation from the mirror's version-pin convention and it MUST be reverted.** The same Dockerfile also serves the prod mirror (`BRANCH=master make buca`), so a pin left at `:dev` would build the prod image from the dev tag. Restoring it to a released `vX.Y.Z` tag belongs to the prod-promotion task.
  3. In the same directory: `BRANCH=dev make buca` — that Makefile includes `../Makefile.docker`, which is where `buca` lives. (`vault/obsidian-agent/`'s Makefile does NOT include `Makefile.docker`, so it carries no `buca` target; it is only where the StatefulSet manifest lives.)
  4. `kubectldev -n agent rollout restart statefulset/vault-obsidian-agent` — the StatefulSet's image is the floating `git-rest:dev` tag and sets `imagePullPolicy: Always`, so a restart suffices and no manifest edit is needed.
- The Post-Deploy assertions: after one pull interval, `kubectldev -n agent get pod vault-obsidian-agent-0 -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}'` returns `True`; `kubectldev -n agent exec vault-obsidian-agent-0 -- git -C /data rev-parse HEAD` equals `origin/master`; `kubectldev -n agent logs vault-obsidian-agent-0 --since=10m | grep -c 'rescue branch pushed'` returns at least 1; and `kubectldev -n agent get pod vault-obsidian-agent-0 -o jsonpath='{.status.startTime}'` is later than the deploy.
- The freshness anchor is the BUILD COMMIT, not the image tag, because the StatefulSet renders `image: '{{"IMAGE_PREFIX" | env}}/git-rest:{{"BRANCH" | env}}'` and dev runs the floating tag `git-rest:dev` — a tag comparison would be a check that cannot fail.
  - `deploy_check:` `kubectldev -n agent exec vault-obsidian-agent-0 -- wget -qO- http://localhost:9090/metrics | grep -m1 '^git_rest_build_info' | sed -E 's/.*commit="([^"]+)".*/\1/' | grep -E '^[0-9a-f]{7,40}$'`
  - `deploy_target:` `$(git -C ~/Documents/workspaces/git-rest rev-parse --short HEAD)`
  - The comparison is short-SHA against short-SHA: `Makefile` passes `--build-arg BUILD_GIT_COMMIT=$(git rev-parse --short HEAD)`, so the metric's `commit` label is a short SHA and a full 40-character `rev-parse HEAD` would never compare equal even on a correct deploy.
- The build identity is exposed as the gauge `git_rest_build_info{version,commit,date}` (`pkg/metrics/build_info.go`), fed by `BUILD_GIT_VERSION` / `BUILD_GIT_COMMIT` / `BUILD_DATE`. `wget` is present in the image (verified live) and the metric renders as `git_rest_build_info{commit="2b492ed",date="…",version="…"} 1`.
- The rung-1 reproduction's ordering is LOAD-BEARING: the binary boots against a clean, level clone, and the remote is advanced and the tree dirtied only afterwards. Booting with the dirty state already present lets `recoverUntracked` commit it away (`git add -A && git commit` on any `??` line, which stages tracked edits too) and `syncOnStartup` consume the divergence with a raw `git pull`, so the puller never reaches the dirty-tree fast-forward failure at all — it takes the committed-divergence route instead and the symptom does not appear.
- The recipe's assertions: `git -C "$BASE/work" status --porcelain` returns 0 lines; `git -C "$BASE/work" rev-parse HEAD` equals `git -C "$BASE/work" rev-parse origin/"$BRANCH"`; `curl -s -o /dev/null -w '%{http_code}' http://localhost:18445/readiness` returns `200`; `git -C "$BASE/work" ls-remote --heads origin 'refs/heads/rescue/*'` returns exactly 1 ref matching `| grep -Eq 'refs/heads/rescue/[0-9]{8}T[0-9]{6}Z$'`; and `curl -s localhost:18445/metrics | grep '^git_rest_pull_rescues_total'` reads `1` while the process is still alive (the counter is per-process and pre-initialised to 0 in `init()`, so the assertion is the absolute value, not a delta across restarts).
- In the rescue branch the staged deletion is captured as a tree-vs-parent diff: `git -C "$BASE/work" diff --name-status "${REF}^" "${REF}" | grep -qE '^D\tasks/doomed\.md$'` exits 0 AND `git -C "$BASE/work" ls-tree -r --name-only "${REF}" | grep -c 'tasks/doomed\.md'` returns 0 — the path is ABSENT from the rescue tree because the tree is built from a temporary index where `git add -A` applied the deletion. Asserting `git show "${REF}:tasks/doomed.md"` would fail a correct implementation. The brace form `${REF}` is required: in zsh, `"$REF:tasks/x.md"` is mangled by the `:t` modifier.
- The `.gitignore`d file is excluded: `git -C "$BASE/work" ls-tree -r --name-only "${REF}" | grep -cE 'secrets\.env$'` returns 0.
- Teardown matters: every AC re-runs the recipe on the same port, so each run must start from a clean process — a survivor keeps answering `:18445` and returns stale evidence.
- The failed-rescue-push probe needs `git -C "$BASE/origin.git" config core.hooksPath hooks`: a global `core.hooksPath` makes git ignore the per-repo `hooks/` directory entirely, so without that line the hook silently no-ops, the push succeeds, the implementation correctly resets, and the probe false-fails against correct code.
- `main.go`'s `cleanupStaleLocks` deletes every `*.lock` under `.git` at startup, so a `.git/index.lock` probe must be installed WHILE the binary runs — booting with the lock present does not work.

**Sibling prompts (do not do their work):** prompts 1-3 shipped the rescue mechanism, the counter, the tests and the `CLAUDE.md` invariants. Do NOT change any Go file, `mocks/`, `CHANGELOG.md`, or `CLAUDE.md` in this prompt.
</context>

<requirements>

## 1. Add the dirty-working-tree rung-1 recipe to `docs/verifying-specs.md`

In the `## Rung 1: local binary against a temp repo` section, add a new subsection immediately after the existing `What to assert per spec category` table. Title it:

```
### Rung 1 recipe: dirty working tree + moved remote
```

It must contain, in this order:

1. One paragraph stating what the shape is: uncommitted changes to tracked files in the served repo while the remote has moved, which makes `git merge --ff-only` refuse rather than clobber the working tree. Note that the failure is recoverable state, not a terminal one, and that a healthy puller must report ready.
2. A callout, set off as a blockquote or its own short paragraph, stating the ordering constraint as a hard requirement — **the binary boots against a clean, level clone; the remote is advanced and the tree dirtied only afterwards.** Explain why: the boot path's `recoverUntracked` runs `git add -A && git commit` on any `??` line (staging tracked edits too) and `syncOnStartup` performs a raw `git pull` before the HTTP server serves, so a tree that is already dirty at boot has the condition cleared before the puller's first cycle and the symptom never appears — the pull takes the committed-divergence route instead.
3. The recipe itself in a fenced `bash` block, condensed from spec 014's `## Reproduction` but complete enough to run: bare origin with a non-default branch name, a seed clone that tracks `tasks/x.md` and `tasks/doomed.md` plus a `.gitignore` for `secrets.env`, a work clone, booting the freshly built binary on `:18445` with `-repo` and `-pull-interval=10s` BEFORE advancing the remote, then advancing the remote so the incoming commit touches `tasks/x.md`, then dirtying the tree in all four shapes (tracked edit colliding with the incoming commit, untracked non-colliding file, staged deletion, ignored file), then waiting one pull interval and reading readiness and the counter, then teardown. Keep the spec's inline comments that explain why each step is shaped that way — in particular why `tasks/doomed.md` and `.gitignore` must be tracked at the merge-base, and why there is no commit in the dirty step.
4. A short list of the assertions, with the exact commands: clean tree, `HEAD` equal to `origin/"$BRANCH"`, readiness `200`, exactly one `refs/heads/rescue/*` ref matching `[0-9]{8}T[0-9]{6}Z`, and `git_rest_pull_rescues_total` reading `1` while the process is still alive. State explicitly that the counter is per-process and pre-initialised to 0, so the assertion is the absolute value. Derive the ref from the authoritative remote first — `REF=origin/$(git -C "$BASE/work" ls-remote --heads origin 'refs/heads/rescue/*' | awk '{print $2}' | sed 's#refs/heads/##')` — then list all four rescue-branch content assertions in the brace form: `git -C "$BASE/work" show "${REF}:tasks/x.md"` contains `LOCAL UNCOMMITTED EDIT`; `git -C "$BASE/work" show "${REF}:tasks/scratch.md"` contains `scratch`; the staged deletion is a tree-vs-parent diff — `git -C "$BASE/work" diff --name-status "${REF}^" "${REF}"` shows `D tasks/doomed.md` AND `git -C "$BASE/work" ls-tree -r --name-only "${REF}"` does not list `tasks/doomed.md` (the path is ABSENT from the rescue tree, so asserting `git show "${REF}:tasks/doomed.md"` would fail a correct implementation); and `git -C "$BASE/work" ls-tree -r --name-only "${REF}"` does not list `secrets.env`. Without these four the recipe omits the evidence that the rescue branch actually holds the dirty shapes, which is the point of the shape.
5. A short list of the three negative probes, each with its construction and its assertions: (a) clean tree with a successful fast-forward creates no rescue ref and leaves the counter at `0`; (b) a fast-forward that fails for an environmental reason (`touch "$BASE/work/.git/index.lock"` while the binary runs — it must be installed after boot because `main.go`'s `cleanupStaleLocks` removes every `*.lock` under `.git` at startup) creates no rescue ref, leaves the counter at `0`, and logs `fast-forward merge failed`; (c) `chmod 000 "$BASE/work/.git/index"` in place of the lock, same three assertions. State that probe (b) is constructible and definitively not a dirty tree, which is what pins the "every other cause" clause.
6. A short list of the two remaining negative probes: a rejected rescue push (install a `pre-receive` hook in `origin.git` that exits 1 for `refs/heads/rescue/*` AND point that repo at its own hooks directory with `git -C "$BASE/origin.git" config core.hooksPath hooks`, because a global `core.hooksPath` makes git ignore the per-repo `hooks/` directory and the hook would silently no-op) asserting the edit is still in the working tree, `HEAD` unchanged, and the log containing `rescue push failed, leaving repo for inspection`; and the committed-divergence path still merging both commits with no rescue ref.
7. A pointer to spec 014's `## Reproduction` as the authoritative form, and a line noting that `docs/verifying-specs.md`'s rung-2 section is the next rung for this shape.

Use fenced `bash` blocks for every command list, and use `${REF}` brace form in any shell snippet that appends `:path` or `:refs/...` to a variable — in zsh, `"$VAR:suffix"` is mangled by the `:t` / `:r` modifiers.

## 2. Add the GKE agent vault to the rung-2 section of `docs/verifying-specs.md`

The existing `## Rung 2: dev cluster e2e` section describes the nuke-hosted vaults only. Add a new subsection immediately after that section's existing content, and leave the nuke-hosted content exactly as it is.

Title it:

```
### Rung 2 recipe: the GKE agent vault (`vault-obsidian-agent`)
```

It must contain:

1. A sentence distinguishing the two deployments: the nuke-hosted `vault-obsidian-{openclaw,personal,trading}` vaults are described above; the agent vault is a hand-written StatefulSet in namespace `agent` on GKE, reached with `kubectldev -n agent` on dev and `kubectlprod -n agent` on prod. State that the two are different clusters and the `kubectlnuke{dev,prod}` wrappers do not reach the agent vault.
2. A note that the repo inside the agent pod is `/data`, NOT `/data/repo` — `vault-obsidian-agent-sts.yaml` sets `REPO` to `/data` and mounts the PVC at `mountPath: /data` with `subPath: repo`, and `subPath` selects a directory within the volume rather than being appended to the container path, so `git -C /data/repo` fails with `fatal: not a git repository` even on a correct deploy.
3. The four-step deploy procedure, in order, with the exact commands from the context block above: the `VERSION=dev ALLOW_UNTAGGED_BUILD=1 make buca` publish, the one-line mirror pin move, `BRANCH=dev make buca` in the mirror directory, and the `rollout restart`. Include the note that the mirror's Makefile includes `../Makefile.docker` where `buca` lives, while `vault/obsidian-agent/`'s Makefile does not and carries no `buca` target.
4. A callout that the mirror-pin step is a deliberate temporary deviation from the version-pin convention and MUST be reverted, because the same Dockerfile serves the prod mirror (`BRANCH=master make buca`) and a pin left at `:dev` would build the prod image from the dev tag. State that restoring the pin to a released `vX.Y.Z` tag belongs to the prod-promotion task, not to a pre-merge verification.
5. A sentence recording WHY the dev image cannot simply be built from the working tree: `agent/git-rest/Dockerfile` in `seibert-data/agent` is a one-line `FROM docker.io/bborbe/git-rest:<pinned tag>` mirror, so `BRANCH=dev make buca` there re-tags whatever the pin names and the `--build-arg BUILD_GIT_COMMIT` on that path is inert because the mirror's Dockerfile declares no `ARG`.
6. The Post-Deploy assertions and the build-identity check: the four `kubectldev -n agent` assertions from the context block, plus the `deploy_check` / `deploy_target` pair in a fenced `bash` block, plus the explanation that the freshness anchor is the build commit and not the image tag because dev runs the floating tag `git-rest:dev` — a tag comparison would be a check that cannot fail — and that the comparison is short-SHA against short-SHA because the image is stamped with `git rev-parse --short HEAD`.
7. One sentence stating that rung 3 (prod promotion and verifying the prod symptom) is a separate task for this spec and is deliberately not covered here.

Do NOT remove, reword or reorder the existing rung-2 or rung-3 content for the nuke-hosted vaults.

## 3. Record the stash-vs-rescue-branch rationale and the counter in `docs/deployment.md`

In `## Operational notes`, after the existing `Conflict handling` bullet, add a new `### Dirty working tree rescue` subsection. It must cover:

1. What triggers it: uncommitted changes to tracked files in the served repo while the remote has moved, which makes `git merge --ff-only` refuse rather than clobber the working tree. Note that this used to wedge the pod indefinitely — readiness reported not-ready on every cycle and every consumer of the vault stalled behind it — and that it now self-heals within one pull interval.
2. What the puller does: captures the whole working-tree state (staged, unstaged, deletions and untracked) as a commit on a `rescue/<timestamp>` branch, pushes that branch to the vault's own remote, and only after the push succeeds returns the working tree to the remote state (`git reset --hard` on the upstream tracking ref plus `git clean -fd`). State that `.gitignore`d paths are excluded by construction, so secrets and keys never leave the pod, and that the rescue branch is never force-pushed and never deleted.
3. The rationale for a rescue branch over a stash, which is the durable part: a stash is local-only and invisible to anyone but the operator holding the volume, whereas a rescue branch is recoverable from any clone of the vault's remote and survives the pod being replaced. State that an operator recovering a stuck pod by hand may still choose `git stash` or `git commit -am rescue`, but that the service now prefers the branch.
4. How to recover from a rejected rescue push: the log line `rescue push failed, leaving repo for inspection` means nothing was reset and nothing was cleaned; the operator runs `git -C <repo> push origin rescue/<ts>` (the local ref is created before the push precisely so this is possible) and confirms with `git ls-remote origin 'refs/heads/rescue/*'`. State that readiness returns to ready within one pull interval after the push succeeds.
5. How the rescue is observed: the INFO log line beginning `rescue branch pushed`, which names the branch, the file count and the captured paths — paths rather than only a count, so an unexpected publication is visible in the pod log and not only on the remote — and the counter `git_rest_pull_rescues_total`, registered and pre-initialised to zero at process start. Include one sentence on why paths are logged.

Match the surrounding style: prose paragraphs and tables, `###` heading level (the sibling `### Quarantine backlog` subsection sets the precedent), and a metric table if it reads better than prose.

## 4. Final verification

Run every command in `<verification>` from the repo root and confirm each result before finishing. Then walk each requirement above against the change: the rung-1 recipe carries the ordering constraint as a hard requirement, the rung-2 section names `kubectldev -n agent` and `/data` and records the pin step and its revert, and `docs/deployment.md` carries both the rationale and the counter.

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- This prompt is docs-only. Do NOT change any Go file, `mocks/`, `CLAUDE.md`, `CHANGELOG.md`, `helm/`, `main.go`, or `docs/api.md`. The public HTTP contract in `docs/api.md` is frozen by the spec.
- Do NOT change `docs/dod.md` or `docs/deployment.md`'s existing content beyond the addition in requirement 3 — no rewording, no reordering, no deletion.
- Do NOT remove or reword the existing rung-2 and rung-3 content for the nuke-hosted vaults in `docs/verifying-specs.md`. The agent vault is ADDED alongside them; the two are different clusters.
- Do NOT embed any `kubectl*`, `docker`, `make buca` or `git push` command in a prompt `<verification>` block — those are operator-only. They belong in the documentation prose only, where an operator reads them. (This prompt's own `<verification>` uses filesystem checks only.)
- The rung-1 recipe MUST state the boot-ordering constraint as a hard requirement, not as an incidental detail. Replaying the recipe with the dirty state present at boot produces a false negative, because the boot path clears the condition before the puller's first cycle.
- The rung-1 recipe MUST use `${REF}` brace form in shell snippets that append `:path` or `:refs/...` to a variable. In zsh, `"$VAR:suffix"` is mangled by the `:t` / `:r` modifiers.
- The rung-2 subsection MUST record the mirror-pin step AND its revert. A pin left at `:dev` would build the prod image from the dev tag; that consequence must be stated.
- The rung-2 `deploy_check` / `deploy_target` pair MUST use `rev-parse --short HEAD` on the target side, matching the short SHA baked into the image, and MUST end `deploy_check` with `grep -E '^[0-9a-f]{7,40}$'` so a missing metric makes the check exit non-zero.
- Do NOT assert facts about the agent vault beyond those listed in the context block. In particular do NOT invent a `buca` target for `vault/obsidian-agent/`, a `nuke` path for the agent vault, a rung-3 procedure, or any image tag other than the ones given.
- No new file may be created in `docs/` — the rationale goes into the existing `docs/deployment.md`.
- Markdown MUST stay lint-clean for `trivy fs --scanners secret`: do not paste real key material, tokens or credentials into the docs. The example paths (`/ssh/id_ed25519`) and commands are fine.
- `make precommit` is NOT required for this prompt — no Go file changes, so the format/generate/test/lint chain has nothing new to validate. If it is run, it must still exit 0.
</constraints>

<verification>
Run from the repo root. All checks are filesystem reads — no git, no docker, no cluster commands.

Rung-1 recipe present, with the ordering constraint:

```bash
grep -n 'Rung 1 recipe: dirty working tree + moved remote' docs/verifying-specs.md
grep -n 'clean, level clone' docs/verifying-specs.md
grep -n 'recoverUntracked' docs/verifying-specs.md
grep -n 'syncOnStartup' docs/verifying-specs.md
grep -n 'git_rest_pull_rescues_total' docs/verifying-specs.md
grep -n 'rescue/\[0-9\]{8}T\[0-9\]{6}Z' docs/verifying-specs.md
grep -n 'core.hooksPath' docs/verifying-specs.md
grep -n 'cleanupStaleLocks' docs/verifying-specs.md
grep -n 'index.lock' docs/verifying-specs.md
```

Each must print at least one match.

Brace-form check — the recipe must not use the zsh-mangled form:

```bash
grep -n '\${REF}' docs/verifying-specs.md
```

Must print at least one match. And the mangled form must be absent:

```bash
! grep -q '"\$REF:' docs/verifying-specs.md
```

Agent-vault rung-2 subsection present:

```bash
grep -n 'Rung 2 recipe: the GKE agent vault' docs/verifying-specs.md
grep -n 'kubectldev -n agent' docs/verifying-specs.md
grep -n 'kubectlprod -n agent' docs/verifying-specs.md
grep -n 'vault-obsidian-agent' docs/verifying-specs.md
grep -n 'REPO` to `/data`' docs/verifying-specs.md
grep -n 'subPath' docs/verifying-specs.md
grep -n 'ALLOW_UNTAGGED_BUILD=1' docs/verifying-specs.md
grep -n 'core.hooksPath' docs/verifying-specs.md
grep -n 'sm-octopus/agent/git-rest' docs/verifying-specs.md
grep -n 'Makefile.docker' docs/verifying-specs.md
grep -n 'rollout restart statefulset/vault-obsidian-agent' docs/verifying-specs.md
grep -n 'deploy_check' docs/verifying-specs.md
grep -n 'rev-parse --short HEAD' docs/verifying-specs.md
grep -n 'git_rest_build_info' docs/verifying-specs.md
```

Each must print at least one match.

The revert must be recorded, not just the pin step:

```bash
grep -n 'MUST be reverted' docs/verifying-specs.md
grep -n 'prod mirror' docs/verifying-specs.md
```

Both must print at least one match.

Existing nuke-hosted content must be intact:

```bash
grep -n 'kubectlnukedev -n dev' docs/verifying-specs.md
grep -n 'vault-obsidian-openclaw' docs/verifying-specs.md
grep -n '## Rung 3: prod cluster e2e' docs/verifying-specs.md
```

Each must print at least one match.

Deployment rationale present:

```bash
grep -n '### Dirty working tree rescue' docs/deployment.md
grep -n 'rescue/' docs/deployment.md
grep -n 'rescue branch pushed' docs/deployment.md
grep -n 'rescue push failed, leaving repo for inspection' docs/deployment.md
grep -n 'git_rest_pull_rescues_total' docs/deployment.md
grep -n 'stash' docs/deployment.md
grep -n 'git reset --hard' docs/deployment.md
```

Each must print at least one match.

Existing deployment content must be intact:

```bash
grep -n '### Quarantine backlog' docs/deployment.md
grep -n 'Conflict handling' docs/deployment.md
grep -n 'git_rest_quarantined_backlog' docs/deployment.md
```

Each must print at least one match.

No Go or frozen file touched by this prompt:

```bash
grep -c 'IncPullRescue' pkg/git/git.go
grep -c '^## Unreleased' CHANGELOG.md
grep -n 'Logging uses `log/slog`' CLAUDE.md
```

The first must print `1`; the second must print `1`; the third must print at least one match. All three prove this prompt changed no code, no changelog and no `CLAUDE.md`.

No new file in `docs/`:

```bash
ls docs/
```

Must list exactly `api.md`, `deployment.md`, `dod.md`, `verifying-specs.md`.

Self-check before finishing: walk each requirement above against the change — the rung-1 recipe states the boot-ordering constraint as a hard requirement and carries all three negative probes, the rung-2 subsection names `kubectldev -n agent` / `kubectlprod -n agent` / `vault-obsidian-agent` / `/data` and records the pin step with its revert, the nuke-hosted content is untouched, and `docs/deployment.md` carries both the stash-vs-rescue rationale and the counter.
</verification>
