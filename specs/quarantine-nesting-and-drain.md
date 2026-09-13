---
status: draft
kind: bug
---

## Summary

- A file already under `_conflicts/` can be quarantined a second time, one directory level deeper, producing `_conflicts/_conflicts/...` paths.
- Nothing ever drains `_conflicts/`, and nothing reports how large it has grown — quarantined files accumulate silently.
- The nesting guard rejects an already-quarantined source, records why, and keeps the file exactly one level deep.
- A new gauge reports the live `_conflicts/` file count, and a new per-vault alert fires when that count stays above zero for an hour.
- An operator learns a file was set aside without having to go looking for it, instead of discovering it months later.

## Problem

`_conflicts/` is git-rest's fail-safe for corrupt vault files: when the conflict resolver fails on a single file, that file is moved aside and the merge continues, so one bad file no longer wedges a vault pod. Two gaps turn that fail-safe into a silent data-loss path.

**Defect A — re-quarantine nests.** The destination builder mirrors the source path under `_conflicts/` with no guard for a source that already starts with `_conflicts/`, so quarantining a previously-quarantined file deepens the tree by one level every time. The live Personal vault shows a two-generation chain on 2026-09-13: an 11:07 quarantine produced `_conflicts/25 Tasks/Daily Sentry Triage - Collector Verify - 2026-09-13.1789297678.md` (commit `52fd45732`), and a second run at 11:08 quarantined *that copy* into `_conflicts/_conflicts/25 Tasks/….1789297678.1789297701.md` (commit `12904f96a`) — committed with its conflict markers still embedded, so it also pollutes semantic search and the vault graph.

**Defect B — no drain, no signal.** Quarantine shipped as a fail-safe for sync availability and never got a drain step: no alert, no metric threshold, no periodic sweep. The OctopusAgent vault accumulated 39 quarantined files nested up to six levels deep before anyone noticed; the OpenClaw vault carried 22 files (19 distinct tasks) for three months, 15 of which had no live file left in `tasks/` and two of which were live work (`in_progress`, `ai_review`) when set aside. The only counter that exists measures lifetime quarantine *events* and never decreases when a file is removed, so it cannot express backlog at all.

## Goal

A file that already sits under `_conflicts/` is never quarantined again: the guard rejects it, records why, and the file stays exactly one level deep. The live size of `_conflicts/` is exposed as a gauge on every pod, and each vault has an alert that fires once that gauge has stayed above zero for an hour — so an operator learns a file was set aside without going to look for it, and accumulation is bounded because it is visible.

## Non-goals

- Do NOT auto-repair, auto-restore, or auto-delete quarantined content. Quarantine surfaces and preserves; the operator owns repair. The drain is a *signal*, not a remediation.
- Do NOT retroactively restore or re-import previously quarantined files. Content stays recoverable from git history.
- Do NOT delete the existing `_conflicts/` residue in any vault from code. Clearing residue is a one-time operator action with its own commit, per the precedent set when the 2026-09-13 nested Personal artifact was cleared.
- Do NOT change the `ConflictResolver` interface, `MarkerResolver`, or `YAMLMergeResolver` internals.
- Do NOT change the `merge: resolved=[…] quarantined=[…]` commit-message format established by spec 012.
- Do NOT add a per-repo label to any metric — one pod serves one repo, so the label would have cardinality 1; the pod identity already carries the repo via the `app` label at scrape time.
- Do NOT add a quarantine-size cap or retention policy. Visibility bounds accumulation; deliberate truncation does not.
- Do NOT suppress the abort for an all-rejected merge. Calling a guard-rejected nested file "handled" so the merge could commit would either stage a conflicted file with its markers intact — the exact harm the Problem section calls out — or require a new commit-with-unmerged-entries path that git refuses. The wedge is the correct trade: it surfaces the conflict as a readiness 503 within one pull interval instead of silently deepening the tree, and it is the same floor the pod already applies whenever no path can be resolved.

## Reproduction

Defect A, against a fresh binary at the pre-fix commit `786b885`. The trigger is a merge in which the conflict resolver fails on a path that already lives under `_conflicts/`. Two flags are load-bearing and each is sufficient to break the repro if omitted: `-vault-write=true` selects the `YAMLMergeResolver` (the default `MarkerResolver` resolves invalid YAML without quarantining), and a short `-pull-interval` is required because the puller uses a plain ticker with no immediate first tick — the startup pull bypasses the resolver entirely.

```bash
# 1. Build a fresh binary from current source (never the installed one)
cd ~/Documents/workspaces/git-rest-quarantine-drain
go build -o /tmp/git-rest-verify .

# 2. Bare origin so pushes to it are accepted (a non-bare origin rejects them)
ORIGIN=/tmp/gr-origin.git && rm -rf "$ORIGIN" && git init -q --bare -b main "$ORIGIN"
SEED=/tmp/gr-seed && rm -rf "$SEED" && mkdir -p "$SEED" && cd "$SEED"
git init -q -b main && git config user.email v@local && git config user.name v
mkdir -p "_conflicts/25 Tasks"
printf -- '---\ntitle: a\n---\nbody\n' > "_conflicts/25 Tasks/Prev A.md"
git add -A && git commit -q -m init && git remote add origin "$ORIGIN" && git push -q origin main

# 3. Clone; diverge both sides on the quarantined path
WORK=/tmp/gr-work && rm -rf "$WORK" && git clone -q "$ORIGIN" "$WORK" && cd "$WORK"
git config user.email v@local && git config user.name v
printf -- '---\ntitle: a\n---\nLOCAL CHANGE\n' > "_conflicts/25 Tasks/Prev A.md"
git commit -q -am local
# Corrupt-frontmatter change on the remote side of the same path
cd "$SEED"
printf -- '---\ntitle: [unclosed\n---\nREMOTE CHANGE\n' > "_conflicts/25 Tasks/Prev A.md"
git commit -q -am remote && git push -q origin main
cd "$WORK"

# 4. Serve the clone with vault-write AND a short pull interval, then wait one tick
/tmp/git-rest-verify -listen=:18445 -repo="$WORK" -pull-interval=3s -vault-write=true &
sleep 8
curl -s -w 'HTTP %{http_code}\n' -o /dev/null http://localhost:18445/readiness
kill %1 2>/dev/null

# 5. Inspect the resulting tree and log
find "$WORK/_conflicts" -type f | sed "s|$WORK/||"
git -C "$WORK" log --oneline -3
```

Observed against the pre-fix commit — the nested second level is the defect. The source is removed by the move, so the tree holds exactly one file, and the quarantine line is logged at WARN:

```
$ find "$WORK/_conflicts" -type f | sed "s|$WORK/||"
_conflicts/_conflicts/25 Tasks/Prev A.1789326700.md          ← nested one level deeper; the source is gone
$ git -C "$WORK" log --oneline -3
652a727 merge: resolved=[] quarantined=[_conflicts/25 Tasks/Prev A.md]
$ # pod log
W0913 20:31:47.506440   1 git.go:925] WARN git-rest: quarantined conflicted file path="_conflicts/25 Tasks/Prev A.md" dest="_conflicts/_conflicts/25 Tasks/Prev A.1789326700.md"
```

The commit line differs per run (timestamps); the shape does not.

With the fix the tree holds the same single file but no second `_conflicts/` level, and the log carries a WARN naming the nested source plus a `nested_source` counter increment in place of the quarantine WARN. With the fix, a merge containing a nested conflicted path is rejected up front and aborts — see Desired Behavior 2 and 3. The reproduction therefore stops at the `nested_source` WARN and a clean worktree rather than producing a commit, and the assertion that a merge *without* a nested path still commits is carried by the ordinary quarantine specs in `pkg/git/git_test.go`.

Defect B is observed with no merge at all — on any vault that has ever quarantined:

```bash
find _conflicts -type f | wc -l                                   # → 39 in OctopusAgent; nested up to six levels
curl -s localhost:9090/metrics | grep -c '^git_rest_quarantined_files_total'
# → 1 series: a monotonic counter over lifetime events, not a backlog.
# (A bare `grep -c quarantined` returns 3 — HELP, TYPE, and sample lines.)
```

The counter cannot report the 39. No alert references it: the chart's three vault alerts cover pull failures, rebase conflicts, and a silent puller — none cover quarantine.

## Expected vs Actual

**Expected.** Quarantining a path that already starts with `_conflicts/` leaves the tree exactly one level deep, and the rejection is recorded. The live `_conflicts/` file count is exposed as a gauge, and an alert named `VaultObsidian<Realm>QuarantineBacklog` fires for a vault whose gauge stays above zero.

**Actual.** `quarantineDestPath` joins `"_conflicts"` onto a source directory that is itself `_conflicts/<dir>`, yielding `_conflicts/_conflicts/<dir>/…`. Each re-quarantine deepens the tree. No gauge exists; the only related series is a monotonic counter over lifetime events; no alert references it.

## Why this is a bug

Spec 012 documents the quarantine contract as "moved within the working tree from `<path>` to `_conflicts/<path>.<unix-timestamp-seconds>.md`" — a *one-level* mirror. Defect A violates that contract for any path already under `_conflicts/`. Spec 012 also asserts the growth bound explicitly: "growth is bounded by the rate of corrupt files (currently rare; quarantine count is observable on the new counter)". The live evidence disproves both halves — a re-quarantined file grows the tree on its own, and the counter expresses events rather than backlog. Defect B is the missing half of a fail-safe whose stated purpose is that a bad file is "preserved so an operator can inspect or replay it": a file nobody can see is not preserved in any useful sense.

## Acceptance Criteria

- [ ] The destination builder maps an already-`_conflicts/`-prefixed source to a destination with exactly one `_conflicts/` level — evidence: Ginkgo assertion in `pkg/git` that `strings.Count(quarantineDestPath("_conflicts/dir/b.md", ts), "_conflicts/") == 1` and `strings.Count(quarantineDestPath("dir/b.md", ts), "_conflicts/") == 1` (both inputs, so the normalization is total rather than special-cased).
- [ ] A source path already under `_conflicts/` does not deepen the tree, and when it is the only conflicted path the merge aborts — evidence: Ginkgo spec in `pkg/git/git_test.go` with a diverged `_conflicts/25 Tasks/Prev A.md` whose remote side is invalid YAML; after `g.Pull(ctx)`, `errors.Is(err, ErrConflictResolutionFailed)` is true, `os.Stat(filepath.Join(repo, "_conflicts", "_conflicts"))` returns an error satisfying `os.IsNotExist`, the source file’s content and mode are byte-identical to their pre-pull state, and `git diff --cached --name-only` does not list it (negative evidence: the rejected path is neither moved nor staged).
- [ ] A merge containing a nested conflicted path aborts even when another path in the same merge resolves — evidence: Ginkgo spec with two diverged files, one nested (invalid YAML on the remote side) and one ordinary valid-frontmatter path the resolver merges cleanly; `g.Pull(ctx)` returns a non-nil error satisfying `errors.Is(err, ErrConflictResolutionFailed)`, `git status --porcelain` is empty (the abort restored the worktree), and `git log` shows no new commit.
- [ ] The rejected re-quarantine is recorded by category — evidence: `git_rest_resolver_failures_total{category="nested_source"}` is present with value 0 immediately after `init()`, and after the same spec its value has increased by exactly 1 relative to the reading taken before `g.Pull` was called (a delta, not an absolute — the counter lives in the process-global registry and other specs in the same binary also increment it).
- [ ] The rejection is visible to an operator in the pod log — evidence: the spec’s captured `slog` output contains one WARN record whose message names the nested source path and contains the substring `nested`.
- [ ] The existing quarantine counter is not incremented by a rejected re-quarantine — evidence: after the same spec, `git_rest_quarantined_files_total` equals 0 (negative evidence: the guard rejects rather than quarantines, so no quarantine event was recorded).
- [ ] The frozen `ConflictResolver` surface is byte-identical — evidence: `git log --oneline origin/master..HEAD -- pkg/git/conflict_resolver.go mocks/conflict_resolver.go` returns 0 lines (a branch-relative commit check, not `git diff` — the prompt commits its work, so a worktree-vs-index diff would read empty even if these files had been modified).
- [ ] The backlog gauge reports the true file count and is a gauge, not a counter — evidence: with 3 files seeded under `_conflicts/`, after a refresh an in-process gather returns `git_rest_quarantined_backlog` 3; after deleting one file and refreshing it returns 2 — the decrease is the delta assertion that distinguishes a gauge from a counter.
- [ ] The gauge counts nested files, so legacy residue is not invisible — evidence: with files at both `_conflicts/a.md` and `_conflicts/_conflicts/b.md`, a refresh returns 2.
- [ ] The gauge is pre-initialised so `/metrics` exposes the series before any quarantine occurs — evidence: in-process gather immediately after `init()` on a repo with no `_conflicts/` directory returns 0 for `git_rest_quarantined_backlog`.
- [ ] An unreadable `_conflicts/` directory degrades to 0 and logs, without failing the pull — evidence: Ginkgo spec running only when `os.Geteuid() != 0` (root reads a `0000` directory successfully, which would make the case pass vacuously) sets `_conflicts` to mode `0000`, asserts `g.Pull(ctx)` returns nil, asserts the gauge reads 0, and asserts one WARN record names the unreadable directory.
- [ ] A per-vault alert renders on a configured vault — evidence: `helm template helm --set alerts.enabled=true --set 'vaults[0].name=personal' --set 'vaults[0].repoUrl=git@github.com:bborbe/obsidian-personal.git'` renders exactly one document whose `spec.name` is `VaultObsidianPersonalQuarantineBacklog`, whose `spec.expression` contains `git_rest_quarantined_backlog`, whose `spec.labels.severity` is `warning`, and whose `spec.annotations.description` names the resolution path and states the window; `grep -c 'QuarantineBacklog'` returns 1. (The default `vaults: []` renders nothing, so the fixture is what makes the assertion bite.)
- [ ] **Post-Deploy (Rung-2):** with ≥1 file seeded under `_conflicts/` on the dev vault pod, the gauge reports it — evidence: `kubectlnukedev -n dev exec vault-obsidian-personal-0 -- wget -qO- http://localhost:9090/metrics | grep '^git_rest_quarantined_backlog'` returns a value equal to the seeded count; the raw line is captured verbatim in `# Results`.
  - `deploy_check:` `kubectlnukedev -n dev get pod vault-obsidian-personal-0 -o jsonpath='{.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(git describe --tags --abbrev=0)`
- [ ] **Post-Deploy (Rung-2):** the gauge returns to zero after the seed is removed, proving it tracks deletion as well as creation — evidence: the same command returns `git_rest_quarantined_backlog 0` after the seeded file is removed and one pull interval elapses.
  - `deploy_check:` `kubectlnukedev -n dev get pod vault-obsidian-personal-0 -o jsonpath='{.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(git describe --tags --abbrev=0)`
- [ ] **Post-Deploy (Rung-2):** the released image no longer nests a re-quarantine — evidence: copy the `## Reproduction` fixture so the clone’s origin is reachable from inside the container, then run the dev image against it: `cp -r /tmp/gr-work /tmp/gr-container && docker run --rm -v /tmp/gr-container:/data -v /tmp/gr-origin.git:/tmp/gr-origin.git <dev-image> -repo=/data -pull-interval=3s -vault-write=true`; then `find /tmp/gr-container/_conflicts -type d -name _conflicts` returns 0 lines (the nested level) while `find /tmp/gr-container/_conflicts -type f` returns exactly 1, and the container log carries a WARN containing `nested` — the WARN assertion is what proves the replay actually fired rather than silently no-op’d on an unreachable origin. The replay is deliberately **not** run inside the dev vault pod: the fixture wedges its pod (see Desired Behavior 3), which would block the shared dev vault for other consumers. A dev pod whose repo already carries nested residue is confirmed clean separately by the Rung-3 `git ls-files` ACs.
  - `deploy_check:` `kubectlnukedev -n dev get pod vault-obsidian-personal-0 -o jsonpath='{.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(git describe --tags --abbrev=0)`
- [ ] **Post-Deploy (Rung-3):** on prod, no path matching `_conflicts/_conflicts/` exists in the Personal or OpenClaw vault — evidence: `kubectlnukeprod -n prod exec vault-obsidian-personal-0 -- sh -c 'cd /data && git ls-files | grep -c "_conflicts/_conflicts/"'` prints `0` **after at least one pull interval has elapsed since the rollout** (so the probe covers newly nested paths, not merely residue an operator already cleared), and the same command on `vault-obsidian-openclaw-0` prints `0`.
  - `deploy_check:` `kubectlnukeprod -n prod get pod vault-obsidian-personal-0 -o jsonpath='{.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(git describe --tags --abbrev=0)`
- [ ] **Post-Deploy (Rung-3):** the prod alert is live and its expression evaluates against a real scrape — evidence: `VaultObsidianPersonalQuarantineBacklog` is present in prod and the query `git_rest_quarantined_backlog{app="vault-obsidian-personal"}` returns exactly one series; the raw query, its output, and the resolved alert state are captured in `# Results`.
  - `deploy_check:` `kubectlnukeprod -n prod get pod vault-obsidian-personal-0 -o jsonpath='{.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(git describe --tags --abbrev=0)`
- [ ] **Post-Deploy (Rung-3):** the shipped version equals the verified one — evidence: the `VERSION` pin in `~/Documents/workspaces/nuke/git-rest/Makefile` and the tag rendered by `kubectlnukeprod -n prod get pod vault-obsidian-personal-0 -o jsonpath='{.spec.containers[0].image}'` are equal, and both equal the released git-rest tag; captured as two raw strings that match.
  - `deploy_check:` `kubectlnukeprod -n prod get pod vault-obsidian-personal-0 -o jsonpath='{.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(git describe --tags --abbrev=0)`
- [ ] `make precommit` exits 0 from the repo root — evidence: exit code 0.
- [ ] `CHANGELOG.md` carries the change under `## Unreleased` — evidence: `grep -n 'quarantine' CHANGELOG.md` returns a line inside the `## Unreleased` section.

Scenario coverage — none. Every behavioral assertion above is reachable through in-process integration tests against the real `git` binary in a temp working tree, plus helm-template assertions for the alert and cluster reads for the deploy rungs. No multi-service interaction is load-bearing, so a scenario would add ceremony without signal.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

```
make precommit
make test
grep -n 'git_rest_quarantined_backlog' pkg/metrics/metrics.go
grep -n 'nested_source' pkg/metrics/metrics.go
grep -n 'QuarantineBacklog' helm/templates/alerts.yaml
helm template helm --set alerts.enabled=true --set 'vaults[0].name=personal' --set 'vaults[0].repoUrl=git@github.com:bborbe/obsidian-personal.git'
git log --oneline origin/master..HEAD -- pkg/git/conflict_resolver.go mocks/conflict_resolver.go
```

All commands exit 0; each `grep` returns ≥1 match; the `helm template` output contains `VaultObsidianPersonalQuarantineBacklog`; the `git log` returns 0 lines (the frozen files are untouched on this branch).

### Operator-executable (runs on the host, spec verification ladder)

- `go build -o /tmp/git-rest-verify .` — fresh binary from current source, then replay `## Reproduction` locally against the temp fixture; no second `_conflicts/` level may appear
- `docker run --rm -v /tmp/gr-container:/data <built-image> -repo=/data -pull-interval=3s -vault-write=true` — the released image against a local fixture, which is how the dev replay is exercised *without* wedging the shared dev vault pod
- `cd ~/Documents/workspaces/nuke && git pull && cd git-rest && BRANCH=dev make apply` — mirror + Helm upgrade to dev
- `kubectlnukedev -n dev rollout restart statefulset/vault-obsidian-personal && kubectlnukedev -n dev rollout status statefulset/vault-obsidian-personal --timeout=120s`
- `kubectlnukedev -n dev exec vault-obsidian-personal-0 -- wget -qO- http://localhost:9090/metrics | grep git_rest_quarantined_backlog` — dev observable
- prod promotion via `[[git-rest - Deploy New Version]]` (`BRANCH=master make apply`), then the same probes on prod with `kubectlnukeprod -n prod`

## Desired Behavior

1. `quarantineDestPath` returns a destination containing exactly one `_conflicts/` segment for any input, including an input that already starts with `_conflicts/`. For an already-quarantined source the destination applies the new timestamp in place rather than deepening the tree. This keeps the documented one-level contract total, so the function is correct even though the guard below means production no longer reaches it with a nested input.
2. The guard is a **pre-flight** over the whole conflict list, in the same ordering slot as the existing `validateConflictPathsSafe` call and before `ensureConflictsDir` touches disk. A conflicted path that already begins with `_conflicts/` is not quarantined again: the guard records one increment of `git_rest_resolver_failures_total{category="nested_source"}`, emits one `slog.WarnContext` line whose message contains the substring `nested` and names both the path and the reason, and then rejects the merge.
3. A merge containing a nested conflicted path therefore **aborts**: `git merge --abort`, `Pull` returns a wrapped `ErrConflictResolutionFailed`, and the worktree returns to its pre-merge state. This holds whether the nested path is the only conflict or one of several. There is no partial case, because the rejected path stays unmerged in the index and `git commit` refuses to commit while any unmerged entry exists — so a merge that quarantined its other paths could not be committed either way. Rejecting up front makes that outcome deliberate and deterministic instead of incidental.
4. `nested_source` is added to the documented `ResolverFailuresTotal` category list and pre-initialised to 0 in `init()` alongside the existing label values.
5. A new gauge `git_rest_quarantined_backlog` exposes the number of regular files currently under `_conflicts/` in the served repo. It counts recursively, so a nested path left by an older binary is counted rather than hidden. It is registered in `init()` and pre-initialised to 0.
6. The gauge is refreshed once per pull cycle after the merge attempt resolves. The `Metrics` interface gains exactly one method, and it is a **setter** that receives the count — the directory walk lives in `pkg/git`, which is the only caller, so `pkg/metrics` stays free of filesystem access. Setting rather than incrementing is what lets deletion reduce the number. A missing `_conflicts/` directory reports 0. An unreadable `_conflicts/` directory reports 0 and logs one WARN — it must never fail the pull.
7. `helm/templates/alerts.yaml` gains one further `Alert` per vault inside the existing `range`, named `VaultObsidian<Realm>QuarantineBacklog`, with `expression` `git_rest_quarantined_backlog{app="vault-obsidian-<name>"} > 0`, `for: 1h`, `severity: warning`, and a description naming the resolution path (inspect `_conflicts/`, repair or deliberately discard, then remove so the file is counted down). The `app` label matches the form the file's three existing expressions already use. It renders under the existing `alerts.enabled` gate and follows the file's existing `monitoring.benjamin-borbe.de/v1` shape.
8. The `for: 1h` window distinguishes accumulation from a single transient quarantine event an operator is already handling. It is an operational knob documented in the alert description rather than hidden — the rendered `spec.annotations.description` names the resolution path (inspect `_conflicts/`, repair or deliberately discard, then remove so the file is counted down) and states the window, so the knob is discoverable from the alert itself and not only from this spec.
9. The existing `git_rest_quarantined_files_total` counter, the `merge: resolved=[…] quarantined=[…]` commit format, and every existing label value keep their current semantics unchanged.

## Constraints

- `quarantineDestPath` and the guard are unexported. Pure-function cases live in the internal `package git` test file and call the helpers directly, matching the existing `TestQuarantineDestPath` in `pkg/git/resolve_conflict_merge_test.go`. The merge-level spec lives in `pkg/git/git_test.go` (Ginkgo v2 + Gomega, `package git_test`, `mocks.FakeMetrics`), beside the existing quarantine spec from spec 012, which it extends rather than replaces.
- The `Metrics` interface gains exactly one method for the backlog refresh. Its counterfeiter directive is at `pkg/metrics/metrics.go`; `mocks/metrics.go` is regenerated, and the two hand-written fakes in `pkg/git/git_test.go` and `pkg/git/resolve_conflict_merge_test.go` gain the method too.
- The backlog and category ACs assert against the **process-global** registry (`prometheus.DefaultGatherer`), so the merge-level spec must construct the real `metrics.NewMetrics()` rather than `mocks.FakeMetrics` — the `mocks.FakeMetrics` harness used by the existing quarantine specs cannot back a gather assertion. Spec 012's merge-level specs set the same precedent.
- Errors via `github.com/bborbe/errors` — no `fmt.Errorf`, no bare `return err`.
- Logging via `log/slog` (`slog.WarnContext` / `slog.ErrorContext`), matching the existing quarantine call sites.
- Metrics follow `pkg/metrics/metrics.go` conventions: a `prometheus.NewGauge` for the backlog, registered in `init()`, with the explicit `.Add(0)` visible alongside the labelled pre-initialisations. See `~/Documents/workspaces/coding/docs/go-prometheus-metrics-guide.md` for the Counter-vs-Gauge rule and the pre-initialisation requirement.
- Tests are Ginkgo v2 + Gomega for the merge-level specs; the pure-function cases stay in the existing internal `testing` file. See `~/Documents/workspaces/coding/docs/go-testing-guide.md`.
- The merge-level spec uses real `git` via `os/exec` against a temp working tree with no network.
- `pkg/git/conflict_resolver.go` and `mocks/conflict_resolver.go` are frozen — the `ConflictResolver` interface signature and its counterfeiter directive must not change.
- Existing test files stay intact except where this spec adds a case: `pkg/git/conflict_resolver_test.go` and `pkg/git/yaml_merge_resolver_test.go` are unchanged, and `pkg/git/git_test.go` is extended (its `noopMetrics` fake gains the new interface method; its existing specs keep passing).
- Build via `make precommit` from the repo root.
- The alert is one additional document inside the existing `range` loop — no new template file, no change to the `alerts.enabled` gate.
- `docs/deployment.md` documents the resolver failure categories and currently lists four, with a stale claim that a YAML-parse failure aborts the merge. Update it to the real category set including `nested_source`, and document the backlog gauge and the alert alongside it.
- `docs/verifying-specs.md` is stale on the same axis the deploy references were: its rung-2/3 sections still target `kubectlquant`, `trading-dev`, `shared/base`, and `vault-obsidian-{openclaw,trading}`. Correct it to the `nuke/git-rest/` deploy source and the `kubectlnuke{dev,prod}` wrappers in the same prompt, so the next spec does not inherit the same drift.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection | Reversibility | Concurrency |
|---|---|---|---|---|---|
| Resolver fails on a path already under `_conflicts/` | Pre-flight guard rejects the merge; the file stays one level deep and untouched; WARN names the path | Operator repairs or deliberately discards the file, then the next pull has no nested conflict | `git_rest_resolver_failures_total{category="nested_source"}` increments; WARN log line | Reversible — the file is untouched | Pull loop is single-threaded per pod |
| `_conflicts/` unreadable or absent at gauge-refresh time | Gauge reports 0; one WARN; the pull is unaffected | Operator runs `ls -la <repo>/_conflicts` in the pod and checks ownership/mode on the PVC | WARN log line; gauge reads 0 while files demonstrably exist | Reversible | Single-threaded refresh after the merge |
| `_conflicts/` holds legacy nested residue | Gauge counts nested files, so they are visible rather than hidden | Operator clears residue in one deliberate commit | Gauge value equals `find _conflicts -type f \| wc -l` | Reversible | N/A |
| Pod crashes between the merge commit and the gauge refresh | Gauge holds its previous value until the next pull cycle, so it can read stale by at most one interval | None needed — the next pull refreshes; the alert window is 1h, far longer than one interval | Gauge value lags the directory for one interval | Reversible | Next pull is single-threaded |
| `_conflicts/` grows very large | The walk runs once per pull cycle and is bounded by directory size; cost grows linearly with the backlog the gauge exists to surface | Operator clears the residue the gauge surfaced | Gauge value and pull-cycle duration | Reversible | Single-threaded |
| Any merge contains a conflicted path already under `_conflicts/` | The pre-flight guard rejects the whole merge; `Pull` returns `ErrConflictResolutionFailed`; readiness 503 until the conflict is cleared, and the `nested_source` counter increments once per retry interval | Operator edits the `_conflicts/` file on the remote (or removes it) so the next pull has no conflict on it — the runbook's `[[Resolve Obsidian Vault Conflicts]]` path | Readiness 503 in the pod; `git_rest_merge_outcome_total{result="aborted"}` increments; one WARN per retry naming the nested path | Reversible — nothing was moved or staged | Pull loop is single-threaded; abort leaves the worktree clean for the next attempt |
| Alert fires for a single transient file an operator is already handling | Alert is `warning`, not `critical`, and waits `for: 1h` | Operator resolves the file; gauge drops; alert clears | Alert state in Alertmanager | Reversible | N/A |
| Alert expression references a metric the cluster does not scrape | Alert silently never fires | Operator verifies one real scrape during the rung-2 post-deploy check | Rung-2 evidence captures the raw `/metrics` line and the expression result | Reversible | N/A |

## Security / Abuse Cases

- The guard introduces no new input path: the source path still comes from `git merge` output, already validated by the existing containment check before the quarantine flow. The guard reads a prefix and rejects; it constructs no path.
- The gauge walk is a bounded read of one directory tree inside the repo root. It follows no symlink out of the root, opens no files, and never writes. A symlinked directory under `_conflicts/` is counted as one entry or skipped — either way it cannot escape the root.
- No new HTTP surface and no new user input crosses a trust boundary; the gauge is scrape-only.
- Quarantined content remains committed to git history and pushed to the remote, unchanged from today. The alert description states that removing a file is a deliberate, recorded act, so the drain does not become a silent deletion path.

## Suggested Decomposition

Prompts should be generated in this order — each row is a single prompt with a clear scope.

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Non-nesting destination + nested-source guard + abort-floor accounting, WARN + `nested_source` category | 1, 2, 3, 4, 9 | 1, 2, 3, 4, 5, 6, 7, 19 | — |
| 2 | Backlog gauge + `Metrics` setter method + per-pull refresh + unreadable-dir resilience + mocks | 5, 6, 9 | 8, 9, 10, 11, 19 | prompt 1 (shares the pull path) |
| 3 | Per-vault `QuarantineBacklog` alert in the chart | 7, 8 | 12, 19 | prompt 2 (the expression needs the gauge name) |
| 4 | CHANGELOG entry under `## Unreleased` + `docs/deployment.md` refresh | — | 19, 20 | prompts 1–3 |

Every prompt carries AC 19 (`make precommit` exits 0) because each one changes the tree; prompt 4's diff is documentation-only. The six post-deploy ACs (13–18) are operator-verified against dev and prod after the PR merges, not by a prompt — they have no row by design.

Rationale: prompt 1 fixes the tree-shape defect and is independently shippable; prompt 2 adds the observable and touches the same pull path, so it follows rather than races; prompt 3 is chart-only and depends only on the gauge name existing; prompt 4 closes the release and documentation loop.
