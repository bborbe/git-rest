---
status: completed
spec: [011-yaml-merge-resolver]
summary: Wire VaultWrite flag to select YAMLMergeResolver vs MarkerResolver across all four factory.CreateGitClient call sites
container: git-rest-yaml-merge-resolver-exec-045-spec-011-wiring-and-docs
dark-factory-version: v0.173.0
created: "2026-06-02T10:00:00Z"
queued: "2026-06-02T09:40:06Z"
started: "2026-06-02T10:36:11Z"
completed: "2026-06-02T10:40:11Z"
---

<summary>
- A new boolean process flag selects which resolver runs in a given pod: off by default (existing marker behavior), on for vault-write pods
- Every place in the boot path that constructs a git client honors the same flag — there are four such places in main.go; missing one breaks the build
- A test asserts the flag actually changes which resolver type is used, so this can't silently regress
- The CHANGELOG gains an entry under a new Unreleased heading describing the new resolver and the new flag/env
- The deployment guide gets a short section explaining when to set `VAULT_WRITE_MODE=true`
- Prompt 1 must already be complete and `make precommit` green before this prompt starts — this prompt only WIRES the resolver that prompt 1 added
</summary>

<objective>
Wire selection of `YAMLMergeResolver` vs `MarkerResolver` in `main.go` via a new boolean field (`--vault-write` flag / `VAULT_WRITE_MODE` env, default false). Update ALL FOUR `factory.CreateGitClient` call sites in `main.go`. Add a Ginkgo test that asserts the constructed resolver concrete type matches the flag. Add a CHANGELOG entry under `## Unreleased` and a short deployment-doc section. Prompt 1 (`1-spec-011-yaml-merge-resolver.md`) must be complete and `make precommit` passing before this prompt begins.
</objective>

<context>
Read `CLAUDE.md` at the repo root for project conventions.

Read these coding-plugin guides (paths inside the YOLO container):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-cli-guide.md` — `bborbe/argument/v2` `arg:` / `env:` struct tag conventions; required vs optional fields; default values
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — `## Unreleased` heading convention; bullet-point entries; what to include
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo external test packages; type assertion in tests

Files to read in full before editing:

- `main.go` (full, ~430 lines). The four `factory.CreateGitClient(...)` call sites live in:
  - `initIfNeeded()` (around line 307)
  - `cloneIfNeeded()` (around line 331)
  - `configureUserIfSet()` (around line 352)
  - `createGitClient()` (around line 376)

  All four currently pass `git.NewMarkerResolver(a.Repo)` as the 5th argument. They MUST all be updated in lockstep — partial edits break compilation.

- `pkg/git/yaml_merge_resolver.go` (from prompt 1) — exposes `NewYAMLMergeResolver(repoPath string, m metrics.Metrics) ConflictResolver`. Note that this constructor takes a `metrics.Metrics`, whereas `NewMarkerResolver(repoPath string)` does not. The wiring helper must thread metrics through for the YAML case.

- `pkg/git/conflict_resolver.go` — unchanged from before prompt 1.

- `docs/deployment.md` — existing operational doc, currently around 170 lines; the new section appends to the "Operational notes" section near the bottom.

- `CHANGELOG.md` — current top is `## v0.20.1`. Add a new `## Unreleased` heading directly under the `# Changelog` title and the introductory blurb, ABOVE `## v0.20.1`.

**Cross-prompt note:** This prompt depends entirely on prompt 1. Do not start until prompt 1 is complete and `make precommit` passes on the resulting code.

**Application struct tag style (verified from `main.go` lines 38-54):** existing fields use aligned `required:` `arg:` `env:` `usage:` `default:` tags. The new field follows the same alignment style. For booleans parsed by `bborbe/argument/v2`, the `default:` value is the string `"false"` (the parser converts it).
</context>

<requirements>

## 1. Add the `VaultWrite` field to the `application` struct in `main.go`

In the `application` struct (around lines 38-54), add a new field after `GatewaySecret`:

```go
VaultWrite bool `required:"false" arg:"vault-write" env:"VAULT_WRITE_MODE" usage:"When true, use YAMLMergeResolver for merge conflicts (deep-merges YAML frontmatter). Default false uses MarkerResolver (preserves <<<<<<< markers)." default:"false"`
```

Keep struct-tag alignment consistent with the surrounding fields — `goimports` / `gofmt` may rewrap.

## 2. Add a small helper that selects the resolver

Add this private helper somewhere in `main.go` near the other small helpers (e.g. just below `resolveGitSSHCommand` around line 268):

```go
// selectResolver returns the ConflictResolver this pod should use.
// Vault-write pods get the YAMLMergeResolver (deep-merges YAML frontmatter on
// conflict); all other pods keep the MarkerResolver (commits with <<<<<<< /
// ======= / >>>>>>> markers intact). One resolver per process — never per-request.
func (a *application) selectResolver(m metrics.Metrics) git.ConflictResolver {
	if a.VaultWrite {
		return git.NewYAMLMergeResolver(a.Repo, m)
	}
	return git.NewMarkerResolver(a.Repo)
}
```

## 3. Update ALL FOUR `factory.CreateGitClient` call sites in `main.go`

Find them with:

```bash
grep -n "factory.CreateGitClient" main.go
```

Expected: exactly 4 matches. ALL FOUR must be updated to call `a.selectResolver(...)` instead of the hardcoded `git.NewMarkerResolver(a.Repo)`. Use the same `metrics.NewMetrics()` instance that is already constructed for that call site.

Pattern transformation — apply to each of the four call sites:

```go
// BEFORE (current code):
factory.CreateGitClient(
    a.Repo,
    metrics.NewMetrics(),
    libtime.NewCurrentDateTime(),
    a.GitSSHKey,
    git.NewMarkerResolver(a.Repo),
)

// AFTER (assignment example — preserve the surrounding return / assignment exactly):
m := metrics.NewMetrics()
tmpGit := factory.CreateGitClient(
    a.Repo,
    m,
    libtime.NewCurrentDateTime(),
    a.GitSSHKey,
    a.selectResolver(m),
)
```

Only `createGitClient()` returns `(git.Git, error)`; the other three sites assign to a local. Follow the per-site example for each. The per-site example below is the authoritative pattern; this generic block is illustrative only.

For each call site, choose the smallest local refactor that keeps the surrounding control flow correct. For example, `initIfNeeded()` currently looks like:

```go
tmpGit := factory.CreateGitClient(
    a.Repo,
    metrics.NewMetrics(),
    libtime.NewCurrentDateTime(),
    a.GitSSHKey,
    git.NewMarkerResolver(a.Repo),
)
if err := tmpGit.Init(ctx); err != nil { ... }
```

Becomes:

```go
m := metrics.NewMetrics()
tmpGit := factory.CreateGitClient(
    a.Repo,
    m,
    libtime.NewCurrentDateTime(),
    a.GitSSHKey,
    a.selectResolver(m),
)
if err := tmpGit.Init(ctx); err != nil { ... }
```

Apply the same pattern to `cloneIfNeeded`, `configureUserIfSet`, and `createGitClient`.

**Grep checks after editing:**

```bash
grep -c "factory.CreateGitClient" main.go      # must still equal 4
grep -c "git.NewMarkerResolver" main.go        # must equal 0 (selectResolver replaces it)
grep -c "a.selectResolver" main.go             # must equal 4
```

## 4. Add a selection-wiring test

Create `main_resolver_selection_test.go` at the repo root (alongside `main.go`) — keep it in `package main` to access the unexported `application` struct and `selectResolver` method.

```go
// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"reflect"
	"testing"

	"github.com/bborbe/git-rest/pkg/metrics"
)

func TestSelectResolver_Default_Uses_MarkerResolver(t *testing.T) {
	app := &application{Repo: "/tmp/repo-default", VaultWrite: false}
	r := app.selectResolver(metrics.NewMetrics())
	got := reflect.TypeOf(r).String()
	if got != "*git.markerResolver" {
		t.Fatalf("expected *git.markerResolver, got %s", got)
	}
}

func TestSelectResolver_VaultWrite_Uses_YAMLMergeResolver(t *testing.T) {
	app := &application{Repo: "/tmp/repo-vault", VaultWrite: true}
	r := app.selectResolver(metrics.NewMetrics())
	got := reflect.TypeOf(r).String()
	if got != "*git.yamlMergeResolver" {
		t.Fatalf("expected *git.yamlMergeResolver, got %s", got)
	}
}
```

Notes for the implementer:

- This is a plain `testing.T` test (not Ginkgo). `main_test.go` already exists in the repo (verify with `ls main_*test.go`); this file sits alongside.
- The exact concrete type names `*git.markerResolver` and `*git.yamlMergeResolver` come from the unexported struct names in `pkg/git/conflict_resolver.go` and `pkg/git/yaml_merge_resolver.go`. If `make test` reports a mismatch, the names changed — update the assertion to whatever `reflect.TypeOf(r).String()` actually returns and keep the structural intent (`Default → marker`, `VaultWrite → yaml`).
- Do NOT use `errors.Is` here — we are not asserting an error chain. We are asserting concrete type identity, which is the only way to confirm the selection actually picks the right resolver.

## 5. Add the `## Unreleased` CHANGELOG entry

Edit `CHANGELOG.md`. Insert a new section directly between the introductory `All notable changes...` line and the existing `## v0.20.1` heading:

```markdown
## Unreleased

- feat: Add `YAMLMergeResolver` for conflict resolution on markdown files with YAML frontmatter. Deep-merges frontmatter keys (theirs wins on overlap), concatenates bodies, and stages the result. On YAML parse failure, missing frontmatter delimiter, file-write error, or `git add` failure, returns the existing `ErrConflictResolutionFailed` sentinel so the puller aborts the merge (no invalid file is ever committed).
- feat: Add `--vault-write` flag / `VAULT_WRITE_MODE` env (default `false`). When `true`, the pod uses `YAMLMergeResolver`; when `false`, behavior is unchanged (`MarkerResolver`). Selection is per-process; non-vault pods are unaffected.
- feat: Add `git_rest_resolver_failures_total{category}` Prometheus counter with the four labels `yaml_parse_failed`, `no_frontmatter`, `write_failed`, `git_add_failed` pre-initialised to zero. Operators can distinguish resolver failure modes without log scraping.
- Fixes agent vault scanner skipping markdown files whose frontmatter was clobbered by `<<<<<<<` markers (157 skips observed in prod the first minutes after `agent_controller_vault_scanner_skipped_files_total{reason="duplicate_frontmatter_invalid"}` went live).
```

The existing `## v0.20.1` and below stay untouched.

## 6. Add the deployment-doc section

Open `docs/deployment.md`. In the "Operational notes" section near the bottom (currently around line 166), append a new bullet AFTER the existing "Conflict handling" bullet:

```markdown
- **Vault-write mode**: set `VAULT_WRITE_MODE=true` (or `--vault-write`) on pods that serve agent vault writes. The pod then uses `YAMLMergeResolver`: on a merge conflict in a markdown file with YAML frontmatter, the resolver deep-merges frontmatter keys (theirs wins on overlap) and combines bodies, producing a syntactically valid file. On YAML parse failure or missing frontmatter delimiters, the resolver returns `ErrConflictResolutionFailed` and the puller aborts the merge (same blast radius as today's `MarkerResolver` failure). Leave `VAULT_WRITE_MODE` unset (or `false`) on pods serving human-touched repos — they continue using `MarkerResolver`. Watch `git_rest_resolver_failures_total{category}` on `/metrics` to distinguish failure modes (`yaml_parse_failed`, `no_frontmatter`, `write_failed`, `git_add_failed`).
```

Also add the env var to the `StatefulSet essentials` YAML example near the existing `env:` block:

```yaml
            - name: VAULT_WRITE_MODE
              value: 'true'  # set on vault-write pods; omit (or 'false') for human-touched repo pods
```

Place it immediately after the existing `PULL_INTERVAL` entry.

## 7. Final verification

Run from the repo root:

```bash
make precommit
```

Must exit 0. The wiring test from requirement 4 runs as part of `make test`, which `precommit` invokes.

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT change the `ConflictResolver` interface, `MarkerResolver`, or `YAMLMergeResolver` implementation files. This prompt only WIRES selection; the resolver code is owned by prompt 1.
- Do NOT add a per-request resolver-routing mechanism — spec Non-goals: selection is per-process only.
- Do NOT add a "disable YAMLMergeResolver on vault-write pods" knob — spec Non-goals: invariant.
- ALL FOUR `factory.CreateGitClient(...)` call sites in `main.go` MUST be updated, or `go build ./...` fails. Use the grep checks in requirement 3 BEFORE running `make precommit` — a missed call site is the most common breakage in multi-call-site wiring changes.
- Default of `VaultWrite` MUST be `false` — non-vault pods must be byte-for-byte unchanged in behavior. Verified by the `TestSelectResolver_Default_Uses_MarkerResolver` test in requirement 4.
- The CHANGELOG entry MUST live under `## Unreleased`, NOT under a freshly-invented version heading. dark-factory's release flow promotes `## Unreleased` to the next version when it cuts a release.
- The deployment-doc YAML snippet MUST keep the existing `name:` / `value:` style (no `valueFrom: configMapKeyRef:` — env-var injection in this project uses literal `value:` for booleans).
- Existing tests must still pass — the only NEW test added is `main_resolver_selection_test.go`. No existing test file is edited.
- Do NOT change the public HTTP contract (`/api/v1/files/*`, `/readiness`, `/healthz`, `/metrics`).
- Errors MUST be wrapped with `errors.Wrap` / `errors.Wrapf` from `github.com/bborbe/errors` — none of the wiring changes introduce new error paths, but if you happen to refactor anything that returns an error, keep this rule.
</constraints>

<verification>
Run from the repo root:

```bash
make precommit
```

Must exit 0.

Selection wiring grep checks:

```bash
grep -c "factory.CreateGitClient" main.go         # must equal 4
grep -c "a.selectResolver" main.go                # must equal 4
grep -c "git.NewMarkerResolver" main.go           # must equal 0 (replaced by selectResolver)
grep -n "VAULT_WRITE_MODE" main.go                # ≥1 match (the arg tag)
grep -n "VaultWrite" main.go                      # ≥2 matches (field + selectResolver consumer)
```

Selection tests:

```bash
go test . -run TestSelectResolver -v
```

Both `TestSelectResolver_Default_Uses_MarkerResolver` and `TestSelectResolver_VaultWrite_Uses_YAMLMergeResolver` must pass.

CHANGELOG entry:

```bash
grep -n "YAMLMergeResolver" CHANGELOG.md          # ≥1 match in the ## Unreleased section
grep -n "^## Unreleased" CHANGELOG.md             # exactly 1 match, line number above the ## v0.20.1 line
```

Deployment doc:

```bash
grep -n "VAULT_WRITE_MODE" docs/deployment.md     # ≥3 matches (operational notes bullet + `name: VAULT_WRITE_MODE` env line + the "omit (or 'false') for ..." comment)
grep -n "YAMLMergeResolver" docs/deployment.md    # ≥1 match
```

Final spec-AC sweep (matches the spec's Verification block):

```bash
grep -n 'NewYAMLMergeResolver' pkg/git/yaml_merge_resolver.go
grep -E 'func NewYAMLMergeResolver.*ConflictResolver' pkg/git/yaml_merge_resolver.go
git diff pkg/git/conflict_resolver.go pkg/git/conflict_resolver_test.go mocks/conflict_resolver.go
grep -n 'YAMLMergeResolver' CHANGELOG.md
```

All grep lines return ≥1 match; the `git diff` returns zero changes.
</verification>
