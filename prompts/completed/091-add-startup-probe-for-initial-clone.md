---
status: completed
summary: Added a per-vault startupProbe giving the initial clone ~5 minutes, bumped the chart to 0.1.3, added a CHANGELOG fix entry, and reconciled the deployment docs and chart README.
execution_id: git-rest-exec-091-add-startup-probe-for-initial-clone
dark-factory-version: v0.198.7
created: "2026-10-31T14:42:56Z"
queued: "2026-10-31T14:56:04Z"
started: "2026-10-31T17:22:51Z"
completed: "2026-10-31T17:25:34Z"
---

# Give the initial clone a startup-probe budget

<summary>
- A freshly wiped volume can now finish its first clone before Kubernetes kills the container
- The container gets about five minutes for the initial clone instead of roughly eighty seconds
- Liveness and readiness checks only begin once the initial startup phase has finished, so a slow clone no longer looks like a hung process
- An existing, already-cloned volume behaves exactly as before — no change to restart behaviour in steady state
- The same change applies to every vault the chart deploys, because they all come from one template
- The chart version moves up one patch so the published chart carries the change
- The deployment documentation describes the new startup phase instead of telling operators to pad the liveness delay
- No configuration knob is added: the timings are fixed in the template, like the probes beside them
- No application code changes; the fix is entirely in how the cluster waits for the process
</summary>

<objective>
Stop the kubelet from killing a git-rest container mid-clone, so a fresh PVC can reach a ready pod. A 483 MB clone on `vault-obsidian-openclaw` outlived the liveness probe's ~80 s budget, the container was killed mid-clone, the partial `.git` left on the PVC made every later start skip the clone, and the pod then ran forever against a repository with no branch — a self-perpetuating state that a startup probe prevents by withholding liveness until startup has succeeded.
</objective>

<context>
Read `CLAUDE.md` first — it holds the dark-factory workflow rules and the mandatory spec-verification ladder. Then `docs/dod.md` (Definition of Done), `docs/deployment.md`, and `docs/verifying-specs.md` (its rung-selection table classifies a StatefulSet template change as requiring rung 2).

This chart is what the `nuke/git-rest` deploy applies; a chart version bump alone does not reach a cluster — the chart must be republished and consumed by that deploy.

Read these coding-plugin guides before implementing (paths inside the YOLO container):

- `/home/node/.claude/plugins/marketplaces/coding/docs/k8s-manifest-guide.md` — StatefulSet/PVC and manifest conventions (this change is a Helm template, so the `k8s/` layout rules do not apply)
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — `## Unreleased` heading; `fix:` prefix rules

Files to read in full before editing (line numbers are hints; anchor by block name):

- `helm/templates/statefulset.yaml` — the single per-vault `StatefulSet`. The whole file is wrapped in one `{{- range $vault := .Values.vaults }}` … `{{- end }}`, so anything added inside it renders once per configured vault. The container's `livenessProbe` / `readinessProbe` sit after `ports` and before `resources`.
- `helm/values.yaml` — confirms no probe keys exist today: no `Probe` substring anywhere, and probe timings are hardcoded in the template.
- `main.go` — `func (a *application) bootstrap(ctx context.Context) error` runs `a.cloneIfNeeded(ctx)` and the HTTP server is only created afterwards in `func (a *application) Run`, so `/healthz` is not listening at all during the initial clone. `func (a *application) cloneIfNeeded(ctx context.Context) error` returns early when `filepath.Join(a.Repo, ".git")` exists — which is why a killed clone is never retried.
- `docs/deployment.md` § Health & readiness and the manifest example above it.
- `helm/README.md` — the per-vault StatefulSet bullet names the probes.

Measured incident (2026-10-31, dev cluster, `vault-obsidian-openclaw`): `livenessProbe` is `httpGet /healthz`, `initialDelaySeconds: 30`, default `periodSeconds` of 10, `failureThreshold: 5` — the container dies at roughly 80 s. The clone of the 483 MB `bborbe/obsidian-openclaw` repo takes longer. The partial `.git` survives, `cloneIfNeeded` returns early on every later start, and the log repeats `git pull failed ... fatal: HEAD does not point to a branch`. The defect was invisible for 72 days because the existing volume already held a complete clone.

Kubernetes semantics this change relies on: **when a pod defines a `startupProbe`, the kubelet does not run the liveness or readiness probe until the startup probe has succeeded.** Adding the startup probe therefore re-times the existing liveness probe for free — no edit to `livenessProbe` is needed or wanted.
</context>

<requirements>
## 1. Add the startup probe (`helm/templates/statefulset.yaml`)

1.1 Immediately before the existing `livenessProbe:` key inside the `service` container (the block at 10-space indentation, following `ports:`), insert:

```yaml
          startupProbe:
            httpGet:
              path: /healthz
              port: 9090
              scheme: HTTP
            initialDelaySeconds: 10
            periodSeconds: 10
            timeoutSeconds: 5
            failureThreshold: 30      # 10 + 30*10 = ~5 min for the initial clone
            successThreshold: 1
```

1.2 Match the surrounding YAML style exactly: `startupProbe` at 10 spaces, `httpGet` at 12, `path` / `port` / `scheme` at 14, the timing keys at 12. The `httpGet` body is identical to the liveness probe's (`/healthz`, port `9090`, scheme `HTTP`). Field order is exactly as written above: `initialDelaySeconds`, `periodSeconds`, `timeoutSeconds`, `failureThreshold`, `successThreshold`. The liveness probe has no `periodSeconds`, so this is the one place the order differs from it.

1.3 The comment on `failureThreshold` stays. It is the arithmetic that makes the budget legible: `10 + 30*10 = ~5 min`.

## 2. Leave the sibling probes untouched (`helm/templates/statefulset.yaml`)

2.1 Change nothing in `livenessProbe` (`initialDelaySeconds: 30`, `timeoutSeconds: 5`, `failureThreshold: 5`, `successThreshold: 1`) and nothing in `readinessProbe` (`initialDelaySeconds: 15`, `timeoutSeconds: 5`). Do not add `periodSeconds` to either, do not raise the liveness `initialDelaySeconds`, and do not reorder them.

2.2 Do not add any new key to `helm/values.yaml` and do not templatize the probe timings. The probes beside this one are hardcoded and this one stays hardcoded; the chart has no probe values today and this change introduces none.

## 3. Bump the chart version (`helm/Chart.yaml`)

3.1 Change `version: 0.1.2` to `version: 0.1.3`. Change nothing else in the file, including `appVersion`.

## 4. CHANGELOG entry (`CHANGELOG.md`)

4.1 Add one `fix:` bullet to the end of the `## Unreleased` list. It must state: a per-vault `startupProbe` gives the initial clone ~5 minutes before the container is restarted; liveness and readiness are withheld until it succeeds, so a slow first clone no longer kills the pod mid-clone and no longer strands a partial `.git` that blocks every later clone; liveness and readiness timings are unchanged.

4.2 Do not create a new version heading and do not touch any released section.

## 5. Reconcile the documentation

5.1 `docs/deployment.md`: in the manifest example, add the same `startupProbe` block (same values, same indentation relative to the neighbouring `livenessProbe`) immediately before `livenessProbe`. Then in § Health & readiness, replace the closing sentence about keeping `initialDelaySeconds` on liveness generous for large-repo first boots with an accurate statement: a `startupProbe` on `/healthz` gives the initial clone ~5 minutes (`initialDelaySeconds: 10` + `failureThreshold: 30` × `periodSeconds: 10`), and Kubernetes does not run the liveness or readiness probe until it passes — so the liveness `initialDelaySeconds` is not the knob for a slow first clone.

5.2 `helm/README.md`: in the per-vault StatefulSet bullet, name the startup probe alongside liveness and readiness, using the literal key name `startupProbe` (e.g. `startupProbe` `/healthz`, liveness `/healthz`, readiness `/readiness`) — `<verification>` greps for `startupProbe`. Do not otherwise restructure that section and do not add a values-table row — there is no new value.

5.3 Change nothing else in either document.

## 6. Self-check before finishing

6.1 Re-run every command in `<verification>` and confirm each printed count matches the stated value.

6.2 Walk the change against the incident: the startup probe is emitted for every vault because it sits inside the existing `range`; it polls `/healthz`, which is not listening until `bootstrap` returns, so it fails for the duration of the clone and then passes on the first successful poll; liveness cannot restart the container before that because Kubernetes withholds it.

6.3 Do not describe a passing test or a rendered manifest as proof that the incident is fixed. The behavioural proof is a wiped PVC reaching a ready pod, which this container cannot exercise.
</requirements>

<constraints>
- Chart, docs, and CHANGELOG only. Do NOT edit any Go file. In particular, do NOT change `cloneIfNeeded` or add a guard for a partial `.git` — recovering from a half-clone is a separate change and out of scope here.
- Do NOT change the `livenessProbe` or `readinessProbe` values. The behaviour change comes solely from Kubernetes withholding liveness and readiness until the startup probe succeeds.
- Do NOT add a `startupProbe`-related key to `helm/values.yaml` and do NOT make the probe timings configurable. No new values keys at all.
- Do NOT restructure the template, change the `range`, add a template file, or touch the `Service`, `Secret`, or `Alert` templates.
- Do NOT change `appVersion` in `helm/Chart.yaml`.
- Do NOT claim the fix is verified end-to-end: no test in this repo can prove a wiped PVC reaches a ready pod. The render check only proves the block is emitted.
- `helm` may not be installed in this container (a previous prompt in this repo recorded exactly that). If `helm template` cannot run, do not substitute a fake pass — report that the render check could not run and rely on the structural assertions in `<verification>`.
- Do NOT run `make helm-publish` (or `helm push`). Republishing the chart to OCI and bumping the consumer's `CHART_VERSION` pin in the `nuke/git-rest` deploy repo are operator steps after this prompt; the `helm/Chart.yaml` version bump is all this prompt does.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run every command from the repo root and confirm each result before finishing.

`make precommit` must exit 0 (it does not lint the chart, so the assertions below are the real container-side check):

```bash
make precommit
```

Structural assertions on the template (fixed-string greps; `$` in the template must not be shell-expanded, so keep single quotes):

```bash
grep -c 'startupProbe:' helm/templates/statefulset.yaml              # must print 1
grep -c 'failureThreshold: 30' helm/templates/statefulset.yaml       # must print 1
grep -c '^          startupProbe:$' helm/templates/statefulset.yaml  # must print 1 (10-space indent, sibling of livenessProbe)
grep -c '^            httpGet:$' helm/templates/statefulset.yaml     # must print 3 (startup + liveness + readiness)
grep -n '10 + 30\*10 = ~5 min' helm/templates/statefulset.yaml       # exactly 1 match (the budget comment)
```

Sibling probes unchanged:

```bash
grep -c 'livenessProbe:' helm/templates/statefulset.yaml             # must print 1
grep -c 'readinessProbe:' helm/templates/statefulset.yaml            # must print 1
grep -c 'initialDelaySeconds: 30' helm/templates/statefulset.yaml    # must print 1 (liveness, unchanged)
grep -c 'initialDelaySeconds: 15' helm/templates/statefulset.yaml    # must print 1 (readiness, unchanged)
grep -c 'failureThreshold: 5' helm/templates/statefulset.yaml        # must print 1 (liveness, unchanged)
grep -c 'periodSeconds' helm/templates/statefulset.yaml              # must print 1 (startup only)
```

No new configuration surface:

```bash
! grep -q 'Probe' helm/values.yaml                                   # absence: no probe values were added
grep -c 'range \$vault' helm/templates/statefulset.yaml              # must print 1 (the single loop, unchanged)
```

Placement inside the loop — the block must render once per vault, not once per chart:

```bash
awk '/\{\{- range \$vault/{r=NR} /startupProbe:/{p=NR} /^\{\{- end \}\}/{e=NR} END{print (r>0 && p>r && (e==0 || p<e)) ? "inside range" : "OUTSIDE range"}' helm/templates/statefulset.yaml
```

Must print `inside range`.

The block's own timing keys and path — a typo here would pass every other assertion:

```bash
grep -c 'initialDelaySeconds: 10' helm/templates/statefulset.yaml   # must print 1 (startup only)
grep -c 'periodSeconds: 10' helm/templates/statefulset.yaml         # must print 1 (startup only)
grep -c '^              path: /healthz$' helm/templates/statefulset.yaml   # must print 2 (startup + liveness)
```

Chart version and CHANGELOG:

```bash
grep -c '^version: 0.1.3$' helm/Chart.yaml                          # must print 1
grep -c '^## Unreleased' CHANGELOG.md                               # must print 1
awk '/^## /{sec=$0} /startupProbe/{print "sits under: " sec}' CHANGELOG.md   # must print `sits under: ## Unreleased`
```

Documentation reconciled:

```bash
grep -c '^          startupProbe:$' docs/deployment.md                # must print 1 (the example manifest, 10-space indent)
grep -c 'startupProbe' helm/README.md                                # must print 1
! grep -q 'initialDelaySeconds` on liveness should be generous' docs/deployment.md
grep -c 'startupProbe' docs/deployment.md                            # must print 2 (example block + the replacement Health & readiness sentence)
```

Rendered-manifest check — NOT runnable in this container. `helm` is not installed here (recorded by `prompts/completed/052-spec-013-quarantine-backlog-alert.md`), and a missing binary would look like a pass: the command fails, the redirect leaves an empty file, and every `grep -c` prints 0. Do not attempt `helm template`, `helm lint` or `helm package`.

The per-vault render belongs to the operator verification ladder (`docs/verifying-specs.md`, rung 2 for a StatefulSet template change). Its shape, for that ladder entry and not for this prompt: `helm template git-rest helm/` with three vault fixtures, expecting 3 `startupProbe:`, 3 `failureThreshold: 30` and 3 `failureThreshold: 5`.

**The real proof is behavioural and lives in the incident**: a wiped PVC must reach a ready pod with a complete clone. That requires a cluster, a wiped volume, and a large repository — nothing in this repo proves it. State the render result as what it is: the block is emitted for every vault.
</verification>
