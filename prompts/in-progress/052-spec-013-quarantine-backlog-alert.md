---
status: approved
spec: [013-quarantine-nesting-and-drain]
created: "2026-09-13T21:20:00Z"
queued: "2026-09-14T06:08:34Z"
branch: dark-factory/quarantine-nesting-and-drain
---

# Add a per-vault quarantine-backlog alert to the chart

<summary>
- Every configured vault gets an alert that fires when its quarantined-file backlog stays above zero
- The alert waits an hour before firing, so a single transient file an operator is already handling does not page anyone
- The alert is a warning, not a critical, because a quarantined file is preserved content waiting for a human decision
- The alert's description tells the operator what to do: inspect the quarantined file, repair or deliberately discard it, then remove it so the backlog is counted down
- The alert only renders when alerts are enabled, exactly like the three existing vault alerts
- A chart with no vaults configured still renders nothing — no empty or malformed alert document
- The chart's own documentation lists the new alert alongside the existing three
- No new template file, no new value, and no change to how alerts are gated
</summary>

<objective>
Give the new `git_rest_quarantined_backlog` gauge an operator-facing consumer: one additional `Alert` document per vault inside the chart's existing `range`, named `VaultObsidian<Realm>QuarantineBacklog`, expression `git_rest_quarantined_backlog{app="vault-obsidian-<name>"} > 0`, `for: 1h`, severity `warning`, with a description that names the drain path. Quarantine surfaces and preserves; the alert is the signal that a human must decide, not a remediation.
</objective>

<context>
This repo has no `CLAUDE.md`; project conventions live in `docs/dod.md`, `docs/deployment.md`, and the coding-plugin guides below.

Read these coding-plugin guides before implementing (paths inside the YOLO container):

- `/home/node/.claude/plugins/marketplaces/coding/docs/k8s-manifest-guide.md` — manifest conventions for the CR-shaped objects this chart renders
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — `## Unreleased` heading; `feat:` prefix rules

Files to read in full before editing (line numbers are hints; anchor by block name):

- `helm/templates/alerts.yaml` (57 lines) — the `{{- if .Values.alerts.enabled }}` gate, the `{{- range $vault := .Values.vaults }}` loop, the `$name` / `$camel` definitions at the top of the loop, and the three existing Alert documents: `{{ $name }}-pull-failing-warning`, `{{ $name }}-rebase-conflict-critical`, `{{ $name }}-puller-silent-critical`. The new document is a fourth sibling inside the same loop.
- `helm/templates/_helpers.tpl` — `git-rest.vaultName` produces `vault-obsidian-<name>`; the `app` label in every existing expression is that same value.
- `helm/values.yaml` — the `alerts.enabled` comment (~54) enumerates the alert set and must be kept accurate.
- `helm/README.md` — the alert list (~17) enumerates the same set.
- `CHANGELOG.md` — `## Unreleased` exists with the prompt-1 and prompt-2 bullets; append to it, do not create a second heading.

**Verified facts about the current template:**

- `$name` is `include "git-rest.vaultName" (dict "name" $vault.name)` → `vault-obsidian-<name>`; `$camel` is `$vault.name | title` → `Personal`, `Openclaw`.
- All three existing expressions filter on `app="{{ $name }}"` (double-quoted, inside the expression block scalar).
- Existing `spec.name` values follow `VaultObsidian{{ $camel }}<CamelCaseSuffix>`; existing `metadata.name` values follow `'{{ $name }}-<kebab-case-suffix>'`.
- Existing severities: one `warning` (pull-failing) and two `critical` (rebase-conflict, puller-silent).
- The template renders nothing when `alerts.enabled` is false or `vaults` is empty (`vaults: []` is the default).

**Precondition from prompt 2 (already on this branch):** the gauge `git_rest_quarantined_backlog` exists in `pkg/metrics/metrics.go`, is registered in `init()`, and is refreshed once per pull cycle from `pkg/git`. This prompt does not touch Go code.

**Environment fact:** `helm` is not installed in this container, so the rendered-manifest check is NOT runnable here; in this container the template is verified structurally, by asserting the exact lines the render is built from. That render lives on the operator ladder, pinned to a vault fixture (`helm template helm --set alerts.enabled=true --set 'vaults[0].name=personal' --set 'vaults[0].repoUrl=git@github.com:bborbe/obsidian-personal.git'`), because the chart's default `vaults: []` renders nothing and a bare render would pass vacuously. The chart must also be version-bumped and republished to OCI (see requirement 3) before any of it reaches a cluster.

**Sibling prompts (do not do their work):** prompts 1 and 2 shipped the guard, the `nested_source` category, and the gauge; prompt 4 refreshes `docs/deployment.md` and `docs/verifying-specs.md`. Do NOT edit any Go file or any file under `docs/` in this prompt.
</context>

<requirements>

## 1. Add the alert document to `helm/templates/alerts.yaml`

Append one more `Alert` document inside the existing `{{- range $vault := .Values.vaults }}` loop, directly after the `{{ $name }}-puller-silent-critical` document and before the closing `{{- end }}`. Follow the file's existing shape exactly (leading `---`, single-quoted `metadata.name`, `|-` block scalar for the expression, `namespace` label, camel-case `spec.name`):

```yaml
---
apiVersion: monitoring.benjamin-borbe.de/v1
kind: Alert
metadata:
  name: '{{ $name }}-quarantine-backlog-warning'
  namespace: {{ $.Release.Namespace }}
spec:
  annotations:
    summary: {{ $name }} has quarantined files waiting
    description: 'git-rest in {{ $name }} has files sitting in _conflicts/ for more than 1h — inspect _conflicts/ in the pod, repair or deliberately discard the file, then remove it so the backlog is counted down'
  expression: |-
    git_rest_quarantined_backlog{app="{{ $name }}"} > 0
  for: 1h
  labels:
    severity: warning
    namespace: {{ $.Release.Namespace }}
  name: VaultObsidian{{ $camel }}QuarantineBacklog
```

Requirements this document must satisfy:

- `spec.expression` is exactly `git_rest_quarantined_backlog{app="{{ $name }}"} > 0` — the `app` label uses the same `{{ $name }}` form as the three existing expressions.
- `for: 1h` — the window distinguishes accumulation from a single transient quarantine event an operator is already handling.
- `labels.severity: warning` — a quarantined file is preserved content awaiting a human decision, not an outage.
- `spec.annotations.description` names the resolution path (inspect `_conflicts/`, repair or deliberately discard, then remove so the file is counted down) AND states the window ("more than 1h"), so the operational knob is discoverable from the alert itself.
- `spec.name` is exactly `VaultObsidian{{ $camel }}QuarantineBacklog`.
- The literal string `QuarantineBacklog` appears exactly once in the template file (only in `spec.name`) — the `metadata.name` suffix is lowercase kebab-case.

Do NOT add a new template file, a new `values.yaml` key, a new condition, or a second `range`. The document lives inside the existing loop under the existing `alerts.enabled` gate.

## 2. Keep the chart's own documentation accurate

- `helm/values.yaml`: extend the `alerts.enabled` comment so the enumerated alert set reads `(pull-failing, rebase-conflict, puller-silent, quarantine-backlog)`.
- `helm/README.md`: extend the alert list so it reads `pull-failing, rebase-conflict, puller-silent, quarantine-backlog`.

Both are one-line edits; change nothing else in those files.

## 3. Bump the chart version

`helm/Chart.yaml`: `version: 0.1.0` → `version: 0.1.1`. Change nothing else in the file.

This is load-bearing, not cosmetic. The consumer (`~/Documents/workspaces/nuke/git-rest/Makefile`) pulls the chart from OCI pinned at `CHART_VERSION := 0.1.0`, so republishing under the same version would leave the clusters on the old chart and the new alert would never render. The version bump is what makes the republished chart distinguishable; a human republishes it afterwards with `make helm-publish` and bumps the consumer's `CHART_VERSION` in lockstep.

If a newer version than `0.1.1` already exists in `helm/Chart.yaml` when you read it, bump by one patch from that value instead — never lower it.

## 4. CHANGELOG

If `## Unreleased` is unexpectedly absent (it is created by prompt 1), create it directly under the SemVer preamble, above the newest release heading. Otherwise append to the existing section — never create a second heading:

```markdown
- feat: Add a per-vault `VaultObsidian<Realm>QuarantineBacklog` alert to the Helm chart — `git_rest_quarantined_backlog{app="vault-obsidian-<name>"} > 0` for `1h`, severity `warning`, with a description that names the drain path (inspect `_conflicts/`, repair or deliberately discard, then remove so the backlog is counted down). The window keeps a single transient quarantine event from paging an operator who is already handling it. Renders only when `alerts.enabled` is true, like the three existing vault alerts.
```

## 5. Final verification

Run every command in `<verification>` from the repo root and confirm each result before finishing.

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT edit any Go file. This prompt is chart-only plus the CHANGELOG bullet.
- Do NOT edit `docs/deployment.md` or `docs/verifying-specs.md` — that is prompt 4's scope.
- Do NOT add a new template file, a new chart value, or a new `alerts.enabled` gate. The new document is one more item inside the existing `range`.
- Do NOT change the three existing Alert documents, their names, expressions, severities, or ordering relative to each other. The new document goes last inside the loop.
- Do NOT add a `repo` label to the expression — one pod serves one repo, so the label would have cardinality 1; the `app` label already carries the repo.
- The expression MUST filter on `app="{{ $name }}"` (the `vault-obsidian-<name>` form), matching the file's existing expressions.
- The alert MUST be `severity: warning` and MUST use `for: 1h`. Do not shorten the window and do not make it critical.
- The description MUST name the resolution path and the window; it MUST NOT suggest deleting quarantined content silently or imply the alert repairs anything. Quarantine surfaces and preserves; the operator owns repair.
- The `metadata.name` suffix MUST stay lowercase kebab-case so the literal `QuarantineBacklog` occurs exactly once in the file.
- Do NOT run `helm template`, `helm lint`, or `helm package` — `helm` is not installed in this container and a missing binary would look like a pass. The rendered-manifest check belongs to the spec's verification ladder.
- The container's `.git` is masked: do not run `git diff`, `git log`, or `git status` against the project repo as verification.
- `make precommit` from the repo root MUST exit 0 (it does not lint the chart, so the structural assertions below are the real check).
</constraints>

<verification>
Run from the repo root.

```bash
make precommit
```
Must exit 0.

Structural assertions on the template (fixed-string greps; `$name` must not be shell-expanded, so keep single quotes):

```bash
grep -c 'QuarantineBacklog' helm/templates/alerts.yaml                 # must print 1 (spec.name only)
grep -nF 'git_rest_quarantined_backlog{app="{{ $name }}"} > 0' helm/templates/alerts.yaml   # exactly 1 match
grep -c 'for: 1h' helm/templates/alerts.yaml                           # must print 1
grep -c 'severity: warning' helm/templates/alerts.yaml                 # must print 2 (pull-failing + quarantine-backlog)
grep -c 'kind: Alert' helm/templates/alerts.yaml                       # must print 4
grep -c 'app="{{ $name }}"' helm/templates/alerts.yaml                 # must print 4
grep -c "name: '{{ \$name }}-" helm/templates/alerts.yaml              # must print 4 (four per-vault metadata names)
grep -nF 'more than 1h' helm/templates/alerts.yaml                     # exactly 1 match (the window in the description)
grep -nF 'repair or deliberately discard' helm/templates/alerts.yaml   # exactly 1 match (the drain path in the description)
grep -nF 'VaultObsidian{{ $camel }}QuarantineBacklog' helm/templates/alerts.yaml   # exactly 1 match
```

Gate and loop preserved:

```bash
grep -c 'alerts.enabled' helm/templates/alerts.yaml    # must print 1 (the gate, unchanged)
grep -c 'range \$vault' helm/templates/alerts.yaml     # must print 1 (the single loop, unchanged)
grep -cF '{{- end }}' helm/templates/alerts.yaml       # must print 2 (loop close + gate close)
```
The gate, the range, and the closing `{{- end }}` lines are unchanged in count and position; the new document sits between the `puller-silent-critical` document and the closing `{{- end }}`.

Chart documentation:

```bash
grep -n 'quarantine-backlog' helm/values.yaml      # ≥1 match
grep -n 'quarantine-backlog' helm/README.md        # ≥1 match
grep -nF 'pull-failing, rebase-conflict, puller-silent, quarantine-backlog' helm/README.md   # ≥1 match
```

CHANGELOG checks:

```bash
grep -c '^## Unreleased' CHANGELOG.md                        # must print 1
grep -n 'QuarantineBacklog' CHANGELOG.md                     # ≥1 match inside ## Unreleased
grep -c 'git_rest_quarantined_backlog' CHANGELOG.md          # must print ≥2 (prompt 2's gauge bullet + this prompt's alert bullet)
```

Metric-name cross-check — the expression must name the gauge prompt 2 registered. A one-character drift here produces an alert that silently never fires, and nothing else in this repo would catch it:

```bash
grep -nF 'git_rest_quarantined_backlog' pkg/metrics/metrics.go     # ≥1 match
```

If this returns nothing, the prompt-2 precondition did not land: STOP and report. Do NOT add the metric here — this prompt does not touch Go code.

Self-check before finishing: re-run `make precommit`, then walk each requirement above against the change — the new document is inside the existing loop under the existing gate, its expression/severity/window/name are exact, its description names the drain path and the window, and the three existing alerts are untouched.
</verification>
