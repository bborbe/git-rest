---
status: completed
spec: [024-bug-body-conflict-hunks-quarantine]
summary: Added the replayable rung-2/rung-3 body-conflict probe recipe to docs/verifying-specs.md with a fail-loud deploy gate, baselines, probe setup, and assertions.
execution_id: git-rest-body-merge-exec-095-spec-024-deploy-verification-recipe
dark-factory-version: v0.198.7
created: "2026-11-02T18:50:00Z"
queued: "2026-11-02T18:46:44Z"
started: "2026-11-02T19:03:58Z"
completed: "2026-11-02T19:04:29Z"
branch: dark-factory/bug-body-conflict-hunks-quarantine
---

# Record the dev and prod body-conflict probe with a fail-loud deploy gate

<summary>
- The dev and prod cluster probes for the body rule become a replayable recipe in the verification guide.
- The recipe refuses to run against a pod that is not running the release under test: a version check aborts with a clear message when the deployed build differs from the newest release tag.
- The probe is identical on dev and prod; only the kubectl wrapper and namespace differ.
- The recipe records the expected merge commit, the zero-delta `_conflicts/` count, the `git_rest_body_merged_total` increment, and the unchanged quarantine backlog.
- The recipe records that `vault_api_failed` increments alongside a body resolution on rungs 2 and 3, so a reader does not mistake the expected decline-then-fallback for a regression.
- No Go code, metric, test or deployment manifest changes in this prompt.
- The recipe is evidence-gathering only: it never claims a deployed result the executor cannot observe.
</summary>

<objective>
Make the dev and prod verification of the body rule replayable: add the rung-2/rung-3 body-conflict probe recipe to `docs/verifying-specs.md`, carrying the fail-loud deploy/version gate, the baseline readings, the probe setup, and the exact assertions. This is a documentation prompt — no Go behaviour changes and no code.
</objective>

<context>
There is no repo-root `CLAUDE.md` in this worktree — do not look for one. The project conventions are restated in `<constraints>` below.

Read these coding-plugin guides before implementing (paths inside the YOLO container):

- `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md` — durable-doc expectations and prose style

Files to read in full before editing (anchor by section heading, not line number):

- `docs/verifying-specs.md` — the whole file. The new subsection goes inside `## Rung 2: dev cluster e2e`, immediately after the existing `### Rung 2 recipe: the GKE agent vault (vault-obsidian-agent)` subsection and before the `## Rung 3: prod cluster e2e` heading. Note the existing deploy-identity pattern there: it compares the pod's build commit (`git_rest_build_info{commit=…}`) against `$(git rev-parse --short HEAD)` and explains why a missing metric makes the gate exit non-zero rather than pass falsely.
- `docs/deployment.md` — the `**Vault-write mode**` bullet, for the behaviour the probe exercises (prompt 3 documents it; do not edit it here).
- `docs/api.md` and `docs/deployment.md` (`--write-coalesce*`): the `POST /api/v1/files/{path}` row says the write is **written and staged only**. A background flusher commits and pushes within the coalescing window (`--write-coalesce-delay`, default `30s`; `--write-coalesce-max-delay`, default `120s`), and a pull cycle flushes pending writes before merging (`pkg/git/git.go`). That deferred flush — not the request — is what advances the pod's local HEAD and the remote.

**Verified facts — do not re-derive, do not contradict:**

- `docs/verifying-specs.md` has exactly these top-level sections: `## The three rungs`, `## Rung 1: local binary against a temp repo`, `## Rung 2: dev cluster e2e`, `## Rung 3: prod cluster e2e`, `## Closing the spec`, `## Rung selection by spec type`, `## Anti-patterns`, `## See also`. The rung-2 section currently holds one `###` recipe (`### Rung 2 recipe: the GKE agent vault (vault-obsidian-agent)`).
- The spec's `## Verification` gives the rung-2/3 probe in prose: bump `VERSION` in `~/Documents/workspaces/nuke/git-rest/Makefile`, `BRANCH=dev make apply`, then replay the rung-1 probe against `00 Inbox/body-probe.md` in `bborbe/obsidian-personal`; read the counter from the pod's `/metrics` and the result from a local clone. Rung 3 is the same after `BRANCH=master apply`.
- The spec's ACs 15-16 give the deploy gate commands: `kubectlnukedev -n dev get --raw "/api/v1/namespaces/dev/pods/vault-obsidian-personal-0:9090/proxy/metrics" | grep -m1 '^git_rest_build_info' | sed -E 's/.*version="([^"]+)".*/\1/' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$'` with `deploy_target: $(git describe --tags --abbrev=0)`, and the prod equivalent with `kubectlnukeprod -n prod`.
- AC 15 names the merge commit subject `merge: resolved=[00 Inbox/body-probe.md] quarantined=[]` — the rung-2/3 probe path in the personal vault. `tasks/probe.md` is the rung-1 temp-repo path and does not appear here. (An earlier draft of the spec named `tasks/probe.md` in AC 15; it has been corrected.)
- On rungs 2 and 3, `git_rest_resolver_failures_total{category="vault_api_failed"}` increments on the same pull: vault-api declines a body conflict by policy and the YAML fallback then succeeds. The spec states this is expected, not a regression.
- The spec states that the follow-up task `[[git-rest Merges Body-Conflict Hunks Instead of Quarantining Them]]` owns the dev/prod probe evidence and the 24 h watch.
- The container has no `kubectl` and no cluster credentials. Do not attempt to run any command from this recipe; the deliverable is the documented, replayable recipe. Do not fabricate probe results.
</context>

<requirements>

## 1. Add the rung-2/rung-3 recipe

In `docs/verifying-specs.md`, inside `## Rung 2: dev cluster e2e`, immediately after the `### Rung 2 recipe: the GKE agent vault (vault-obsidian-agent)` subsection and before the `## Rung 3: prod cluster e2e` heading, add a new subsection titled exactly:

```
### Rung 2/3 recipe: body-conflict auto-resolution (personal vault)
```

It must contain, in this order:

1. **Scope paragraph.** State that this is the probe for the body-only conflict rule (spec 024); rung 2 runs it on dev, rung 3 on prod, and the probe is identical apart from the kubectl wrapper and namespace. State that the probe requires the release carrying the body rule to be deployed (dev: bump `VERSION` in `~/Documents/workspaces/nuke/git-rest/Makefile`, `BRANCH=dev make apply`; prod: the same with `BRANCH=master apply` after promotion). State that the deploy gate must be run from a tree whose HEAD contains the released tag carrying the fix — the main checkout after `git fetch --tags`, or the fix worktree after merging the released master into it. `git describe --tags --abbrev=0` resolves only tags reachable from HEAD, so a fix branch forked before the release prints the previous tag and the gate aborts against a correct deploy; confirm the tag contains the fix commit with `git tag --contains <fix-commit>` before running.

2. **Fail-loud deploy gate** in a fenced `bash` block. This is the gate the recipe must pass before it touches the vault; a mismatch aborts with a non-zero exit and a message naming both versions:

   ```bash
   set -e
   DEPLOYED=$(kubectlnukedev -n dev get --raw "/api/v1/namespaces/dev/pods/vault-obsidian-personal-0:9090/proxy/metrics" \
     | grep -m1 '^git_rest_build_info' | sed -E 's/.*version="([^"]+)".*/\1/' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$')
   TARGET=$(git describe --tags --abbrev=0)
   [ "$DEPLOYED" = "$TARGET" ] || { echo "deploy gate failed: deployed=$DEPLOYED target=$TARGET" >&2; exit 1; }
   ```

   State that for rung 3 the wrapper becomes `kubectlnukeprod -n prod` and the raw path's namespace becomes `prod`, and that the trailing `grep -E` is what makes a missing or malformed metric fail the gate rather than pass it falsely.

3. **Baseline** in a fenced `bash` block, taken before the probe, with every variable defined in the block: `CLONE=${CLONE:-/tmp/obsidian-personal-probe}` — a dedicated scratch clone of `bborbe/obsidian-personal` (clone it in the baseline block), never the live Personal vault, which obsidian-git autocommits and pushes, `POD=vault-obsidian-personal-0`, `NS=dev`. `BEFORE_BODY` and `BEFORE_BACKLOG` are read from the pod: `kubectlnukedev -n "$NS" exec "$POD" -- wget -qO- http://localhost:9090/metrics | grep -m1 '^git_rest_body_merged_total' | awk '{print $NF}'` (same form for `^git_rest_quarantined_backlog`). `BEFORE_CONFLICTS` is `find "$CLONE/_conflicts" -type f 2>/dev/null | wc -l`. State that all three are read before the probe so the deltas are meaningful, and that busybox `wget` is the only HTTP client in the image — there is no `curl` in the pod.

4. **Probe setup** in a fenced `bash` block, with `PROBE='00 Inbox/body-probe.md'`:
   - Write the probe on the pod's side through the files API so the pod's HEAD advances with a body the remote does not have, using the in-pod busybox `wget` (the image has no `curl`): `kubectlnukedev -n "$NS" exec "$POD" -- sh -c 'printf %s "$1" > /tmp/probe.md && wget -qO- --timeout=30 --header="X-Gateway-Initator: probe" --header="X-Gateway-Secret: $GATEWAY_SECRET" --post-file=/tmp/probe.md "http://localhost:9090/api/v1/files/00%20Inbox/body-probe.md"' _ $'---\npage_type: note\n---\n\n# Heading\n\nlocal body\n'`. State that every `/api/v1/*` route on these vault pods sits behind the gateway-secret middleware (`GATEWAY_SECRET` is set from the vault Secret's `gateway-secret` key, so it is never empty), so the write must carry both `X-Gateway-Initator` and `X-Gateway-Secret`; `/metrics` and `/healthz` are unauthenticated and need neither. `kubectl exec` inherits the container env, so `$GATEWAY_SECRET` expands inside the pod and the secret never reaches the operator's shell history. State that a non-200 response means the probe was never written, and that the request only writes and stages; the next pull tick flushes the commit and push.
   - Advance the remote with a different body on the same path from the local clone: `git pull --rebase`, overwrite `$PROBE` with `---\npage_type: note\n---\n\n# Heading\n\nremote body\n`, `git add -- "$PROBE"`, commit, push. State the mechanism precisely: the clone's push must land **immediately after the write and before the pod's next pull tick** (the pull interval is `30s`), so when the pod's pull flushes the pending write the commit is made on the old base and its push is rejected as non-fast-forward; the pull then merges the diverged remote and conflicts. If the pod's flush reaches the remote first, the pod's pull is a plain fast-forward — no merge, no conflict — and the probe must be re-run with the clone's push issued inside the same window.
   - Wait one pull interval plus a margin, then **refresh the clone** (`git -C "$CLONE" pull --ff-only`) before reading anything — otherwise the clone is stale and the `_conflicts/` zero-delta assertion passes vacuously. Read the merge commit from the pod itself, then the refreshed clone's `_conflicts/` and the pod's metrics.

5. **Assertions**, as a bullet list with the exact commands:
   - The pod's last commit subject, read with `kubectlnukedev -n "$NS" exec "$POD" -- git -C /data log -1 --format=%s`, is `merge: resolved=[$PROBE] quarantined=[]`.
   - The file keeps the frontmatter and both `---` delimiters and its body is `local body`, a blank line, then `remote body`.
   - After `git -C "$CLONE" pull --ff-only`, `find "$CLONE/_conflicts" -type f 2>/dev/null | wc -l` shows a delta of 0 against `BEFORE_CONFLICTS`.
   - `git_rest_body_merged_total` equals `BEFORE_BODY + 1`.
   - `git_rest_quarantined_backlog` does not increase from `BEFORE_BACKLOG`.
   - On rungs 2 and 3, `git_rest_resolver_failures_total{category="vault_api_failed"}` increments on the same pull. State plainly that this is the normal path — vault-api declines a body conflict by policy and the YAML fallback then succeeds — and that it is not a regression. State that rung 1 does not show this increment because it wires the bare YAML resolver with no vault-api URL.

6. **Evidence ownership.** State that the follow-up task `[[git-rest Merges Body-Conflict Hunks Instead of Quarantining Them]]` owns the dev/prod probe evidence and the 24 h watch, and that this recipe is its replayable form.

7. **No fabricated evidence.** State that a run against a pod whose deploy gate fails is not evidence, and that the recipe must be re-run after the correct release is deployed.

Use fenced `bash` blocks for every command list. Use the `${VAR}` brace form in any shell snippet that appends a `:path` or `:refs/...` suffix to a variable — in zsh, `"$VAR:suffix"` is mangled by the `:t` / `:r` modifiers.

Do NOT remove, reword, reorder or condense any existing content in `docs/verifying-specs.md`, including the GKE agent-vault recipe and the rung-3 section.

## 2. Self-check before finishing

Run every command in `<verification>` from the repo root and confirm each result. Then walk each requirement above against the change: the recipe states the deploy prerequisite and the worktree requirement, carries the fail-loud deploy gate and both wrappers, baselines the three readings before the probe, sets up the probe on both sides, lists every assertion, records the expected `vault_api_failed` increment, and names the evidence owner. Confirm the GKE agent-vault recipe and the rung-3 section are byte-identical to before.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- This prompt is docs-only. Do NOT change any Go file, `mocks/`, `pkg/metrics/`, `CLAUDE.md`, `CHANGELOG.md`, `specs/`, `helm/`, `main.go`, `docs/api.md` or `docs/deployment.md`.
- No new file may be created in `docs/` — the recipe goes into the existing `docs/verifying-specs.md`.
- Do NOT remove, reword, reorder or condense the existing GKE agent-vault recipe or the `## Rung 3` section. The new recipe is ADDED alongside them.
- Do NOT attempt to run any `kubectl*`, `docker`, `make buca`, `dark-factory` or cluster command. The container has no cluster access; the deliverable is the documented recipe. Do not fabricate probe results.
- The recipe MUST state the deploy gate as a hard precondition and MUST abort non-zero on a version mismatch.
- The recipe MUST record the `vault_api_failed` increment as the expected rung-2/3 behaviour, not as a regression.
- The recipe MUST use the `${VAR}` brace form in shell snippets that append `:path` or `:refs/...` to a variable. In zsh, `"$VAR:suffix"` is mangled by the `:t` / `:r` modifiers.
- Do NOT embed any `kubectl*`, `docker`, `make buca`, `dark-factory` or `git push` command in this prompt's own `<verification>` block — those belong in the documentation prose only, where an operator reads them. This prompt's `<verification>` uses filesystem checks only.
- `make precommit` is not required for a docs-only change, because the format / generate / test / lint chain has nothing new to validate; if it is run it must still exit 0. Every other command in `<verification>` must pass.
</constraints>

<verification>
Run from the repo root. The daemon runs with `hideGit=true`, which masks `.git` inside the container — bare `git` commands die with `fatal: not a git repository`, so none appear in this block.

The new subsection is present (each `grep -n` must print at least one match):

```bash
grep -n '^### Rung 2/3 recipe: body-conflict auto-resolution (personal vault)$' docs/verifying-specs.md
grep -n 'deploy gate failed: deployed=' docs/verifying-specs.md
grep -n 'git_rest_body_merged_total' docs/verifying-specs.md
grep -n 'vault_api_failed' docs/verifying-specs.md
grep -n "00 Inbox/body-probe.md" docs/verifying-specs.md
grep -n 'merge: resolved=\[\$PROBE\] quarantined=\[\]' docs/verifying-specs.md
grep -n 'Rung 2 recipe: the GKE agent vault' docs/verifying-specs.md
grep -n '^## Rung 3: prod cluster e2e' docs/verifying-specs.md
```

The deploy gate, the baseline and the assertions are all present (each `grep -c` must print a value ≥ 1):

```bash
grep -c 'kubectlnukedev -n dev' docs/verifying-specs.md
grep -c 'kubectlnukeprod -n prod' docs/verifying-specs.md
grep -c 'BEFORE_BODY' docs/verifying-specs.md
grep -c 'BEFORE_BACKLOG' docs/verifying-specs.md
grep -c 'BEFORE_CONFLICTS' docs/verifying-specs.md
grep -c 'local body' docs/verifying-specs.md
grep -c 'remote body' docs/verifying-specs.md
```

The new subsection sits between the GKE agent-vault recipe and the rung-3 heading:

```bash
awk '/^### Rung 2\/3 recipe: body-conflict/{print "found at line " NR}' docs/verifying-specs.md
awk '/^### Rung 2 recipe: the GKE agent vault/{g=NR} /^### Rung 2\/3 recipe: body-conflict/{b=NR} /^## Rung 3: prod cluster e2e/{r=NR} END{print "gke="g" body="b" rung3="r}' docs/verifying-specs.md
```

The second command must print a line where `gke < body < rung3`, with all three non-zero.

Self-check before finishing: walk each requirement above against the change, then confirm the pre-existing sections are unchanged by eye (the diff must show additions only in the rung-2 section).
</verification>
