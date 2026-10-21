---
status: completed
spec: [017-bug-readiness-decoupled-from-sync-outcome]
summary: Added the repo-path readability readiness verdict (NewRepoReadiness) with five Ginkgo specs covering readable, cold-start, missing, ENOTDIR and reason-disclosure cases; make precommit exits 0.
execution_id: git-rest-readiness-decouple-exec-066-spec-017-serving-verdict
dark-factory-version: v0.198.7
created: "2026-10-21T15:45:28Z"
queued: "2026-10-21T15:52:42Z"
started: "2026-10-21T15:52:44Z"
completed: "2026-10-21T15:54:36Z"
branch: dark-factory/bug-readiness-decoupled-from-sync-outcome
---

# Add the serving verdict: repo-path readability replaces the pull-outcome verdict

<summary>
- A new readiness verdict reports ready when the served repository path exists and can be stat'ed.
- The verdict is independent of every pull, push, fetch and merge outcome.
- A missing repository path reports not-ready with the short reason `repo path unreadable`.
- The 503 reason never names a host path and never contains a sync word such as push, pull, sync or conflict.
- Cold start is ready: a readable path answers ready before any pull tick has run.
- Unit tests cover the readable path, the missing path, the cold-start case and the body constraints.
- The existing pull-outcome cache, handler, factory and application wiring are untouched in this prompt; the next prompt replaces them.
- No new metric, log line, alert or configuration knob is introduced.
</summary>

<objective>
Introduce the new serving verdict that `/readiness` will be built on: a `ReadinessStateReader` implementation that reports ready whenever the served repository path is readable, and not-ready with `repo path unreadable` otherwise. This prompt ships the verdict and its unit tests only; the following prompt wires it into the handler, factory and `main.go` and removes the pull-outcome cache.
</objective>

<context>
There is no repo-root `CLAUDE.md` in this worktree — do not look for one. The project conventions are restated in `<constraints>` below.

Read these coding-plugin guides before implementing (paths inside the YOLO container):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md` — public interface + private struct + `New*` constructor, interface at point of use
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 + Gomega, external test packages (`package handler_test`), coverage ≥80% for new code
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-http-handler-refactoring-guide.md` — handler-adjacent types live in `pkg/handler/`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md` — linter limits and the precommit gate
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md` — new-code coverage and verification rules

Files to read in full before editing (anchor by symbol name, not line number):

- `pkg/handler/readiness.go` — the existing `ReadinessStateReader` interface and `NewReadinessHandler`; the interface stays exactly as it is
- `pkg/handler/readiness_test.go` — the existing handler test style and suite (`handler_suite_test.go` already exists; do not add a second suite)
- `pkg/puller/pull_state.go` — the current pull-outcome verdict this one supersedes; read it for contrast, do not modify it
- `pkg/puller/pull_state_test.go` — the cold-start and stale-cache specs this change supersedes; do not modify it in this prompt

**Verified facts — do not re-derive, do not contradict:**

- `pkg/handler/readiness.go` declares exactly this interface, and the readiness handler consumes it:
  ```go
  // ReadinessStateReader is the interface the readiness handler reads from.
  // Implemented by *puller.PullStateCache.
  type ReadinessStateReader interface {
      ReadinessStatus() (bool, string)
  }
  ```
- The handler treats a non-empty reason as the 503 body verbatim (`resp.Write([]byte(reason))`), so the verdict controls the body text.
- The readiness handler is registered unauthenticated at `/readiness` (`main.go`), so the reason must not disclose host paths.
- The spec fixes the not-ready reason to exactly `repo path unreadable` (DB4, AC2, AC3). The ready reason stays `ok` so the existing 200 body contract is unchanged.
- The repo path readability check is an `os.Stat`, never a git call (spec constraint preserving spec 005's latency guarantee).
- `make precommit` runs `generate` before `test`; it wipes and regenerates `mocks/`. This prompt adds no counterfeiter directive and no mock.
</context>

<requirements>

## 1. Add the serving verdict in a new file `pkg/handler/repo_readiness.go`

Create the file with the project license header (copy the three header lines from `pkg/handler/readiness.go` verbatim), then:

```go
package handler

import "os"

// NewRepoReadiness returns a ReadinessStateReader that reports ready whenever
// repoPath exists and can be stat'ed. It consults no pull, push, fetch or merge
// state, so a failed sync attempt never changes its verdict.
func NewRepoReadiness(repoPath string) ReadinessStateReader {
	return &repoReadiness{repoPath: repoPath}
}

type repoReadiness struct {
	repoPath string
}

// ReadinessStatus returns (true, "ok") when the served repository path is
// readable, or (false, "repo path unreadable") when it is missing or
// unreadable. The reason never contains a host path.
func (r *repoReadiness) ReadinessStatus() (bool, string) {
	if _, err := os.Stat(r.repoPath); err != nil {
		return false, "repo path unreadable"
	}
	return true, "ok"
}

var _ ReadinessStateReader = (*repoReadiness)(nil)
```

Requirements on this code:

- The not-ready reason is exactly `repo path unreadable` — no wrapping, no `err.Error()`, no path interpolation.
- The check is `os.Stat` only. Do NOT call any `git` method, do NOT take a mutex, do NOT open or read the directory contents.
- Do NOT change `ReadinessStateReader`, `NewReadinessHandler`, the handler's status codes or its `ok` body in this prompt.
- Do NOT add a constructor parameter for a clock, a threshold, a pull-state reader or a logger.

## 2. Add unit tests in a new file `pkg/handler/repo_readiness_test.go`

Use `package handler_test`, the Ginkgo v2 / Gomega style already used in `pkg/handler/readiness_test.go`, and the existing suite in `pkg/handler/handler_suite_test.go` (no new suite file). Import `os`, `path/filepath` if needed, Ginkgo/Gomega dot-imports, and `github.com/bborbe/git-rest/pkg/handler`.

Add a top-level `var _ = Describe("RepoReadiness", func() { ... })` with these specs:

1. **Readable path reports ready** — `repoDir := GinkgoT().TempDir()`; `ready, reason := handler.NewRepoReadiness(repoDir).ReadinessStatus()`; `Expect(ready).To(BeTrue())` and `Expect(reason).To(Equal("ok"))`.
2. **Cold start reports ready** — a second spec that creates a temp dir and asserts ready without any pull, clock advance or state recording ever happening. Name it so the cold-start intent is explicit, e.g. `It("reports ready before any pull has run (cold start)")`. This is the replacement for the superseded `cold start (no pull yet)` spec.
3. **Missing path reports not ready** — create a temp dir, `Expect(os.RemoveAll(repoDir)).To(Succeed())`, then assert `ready` is false and `reason` equals `"repo path unreadable"` exactly.
4. **Reason leaks neither a host path nor a sync word** — for the not-ready case assert `Expect(reason).NotTo(ContainSubstring(repoDir))` and `Expect(reason).NotTo(MatchRegexp(`(?i)push|pull|sync|conflict`))`. Do the same two assertions for the ready reason (`"ok"`).
5. **Path whose parent is a regular file reports not ready** — create `GinkgoT().TempDir()`, create a regular file `blocker` inside it, and use `filepath.Join(dir, "blocker", "repo")` as the repo path. `os.Stat` fails with `ENOTDIR`, so the verdict must be `(false, "repo path unreadable")`. This exercises the "unreadable" branch beyond a plain missing path.

Do not test `os.Chmod` permission denial: `os.Stat` does not consult the target's own permission bits (only its ancestors'), so `chmod 000` on the repo dir still stats successfully, and chmod'ing an ancestor is fragile. Requirement 2's spec 5 (regular-file ancestor → `ENOTDIR`) is the unreadable-branch coverage.

## 3. Self-check before finishing

Run every command in `<verification>` from the repo root. Confirm the new verdict answers ready for a readable directory and not-ready for a missing or `ENOTDIR` path, that the reason is exactly `repo path unreadable`, and that nothing in `pkg/puller`, `pkg/factory`, `main.go`, `export_test.go` or the docs changed in this prompt.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT modify `pkg/handler/readiness.go` — the interface and the handler stay byte-identical in this prompt.
- Do NOT modify `pkg/puller/pull_state.go` or `pkg/puller/pull_state_test.go` — the next prompt removes them.
- Do NOT modify `pkg/factory/factory.go`, `main.go`, `export_test.go` or `main_test.go` — wiring belongs to the next prompt.
- Do NOT modify `docs/api.md`, `README.md`, `docs/deployment.md`, `helm/templates/alerts.yaml` or `CHANGELOG.md` — prose correction is a separate prompt.
- Do NOT add a new metric, gauge, alert, log line, environment variable, CLI flag or configuration field.
- Do NOT add a scenario file under `scenarios/`.
- Do NOT edit `specs/`.
- Do NOT run `go mod vendor`, and do NOT write `-mod=vendor` in any command — this repo does not commit `vendor/`.
- Errors, if any arise, via `github.com/bborbe/errors` — no `fmt.Errorf`. The verdict itself returns no error; it maps a stat failure to the fixed reason.
- Interface → Constructor → Struct → Method; keep `repoReadiness` private and return the `ReadinessStateReader` interface from `NewRepoReadiness`.
- New code needs ≥80% statement coverage; every branch of `ReadinessStatus` is covered by requirement 2.
- Files must carry the license header or `addlicense` fails `make precommit`.
- Tests use Ginkgo v2 / Gomega; `make precommit` is the gate.
</constraints>

<verification>
Run from the repo root. The daemon runs with `workflow: direct` and does not mask `.git`, but do not put git commands in this block — nothing here needs them.

Focused run while implementing:

```bash
go test ./pkg/handler/... -mod=mod -ginkgo.v -ginkgo.focus='RepoReadiness' -ginkgo.fail-on-empty
```

Must exit 0 and print `Ran 5 of <N> Specs`, listing all five `RepoReadiness` spec names. `-ginkgo.fail-on-empty` turns a zero-spec match into a non-zero exit, so a typo'd focus — or an empty `Describe` with no `It` blocks — cannot pass. Without it, Ginkgo exits 0 on zero matches and this check could not fail.

The verdict exists with the fixed reason and no git dependency (each `grep -n` must print at least one match):

```bash
grep -n 'func NewRepoReadiness' pkg/handler/repo_readiness.go
grep -n 'repo path unreadable' pkg/handler/repo_readiness.go
grep -n 'os.Stat' pkg/handler/repo_readiness.go
grep -n 'RepoReadiness' pkg/handler/repo_readiness_test.go
```

The verdict does not import git and does not consult pull state (the file must exist first — `! grep -q` also exits 0 when the file is missing — then each check must pass):

```bash
test -f pkg/handler/repo_readiness.go
! grep -q 'pkg/git' pkg/handler/repo_readiness.go
! grep -q 'PullState\|ReadinessStatus.*lastSuccess\|freshness' pkg/handler/repo_readiness.go
```

Frozen surfaces — this prompt changes no existing file (positive anchors first, then the absence checks):

```bash
grep -q 'ReadinessStatus() (bool, string)' pkg/handler/readiness.go
grep -q 'func NewReadinessHandler' pkg/handler/readiness.go
grep -q 'resp.Write(\[\]byte("ok"))' pkg/handler/readiness.go
! grep -q 'NewRepoReadiness' pkg/handler/readiness.go
! grep -q 'NewRepoReadiness' main.go
```

Final gate:

```bash
ROOTDIR=/workspace make precommit
```

Must exit 0.
</verification>
