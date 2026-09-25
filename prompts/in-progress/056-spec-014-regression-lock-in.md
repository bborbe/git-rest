---
status: approved
spec: [014-bug-pull-cannot-recover-from-dirty-working-tree]
created: "2026-09-25T10:39:18Z"
queued: "2026-09-25T11:34:36Z"
branch: dark-factory/bug-pull-cannot-recover-from-dirty-working-tree
---

# Lock in the untouched pull paths and the undocumented invariants

<summary>
- A clean fast-forward still fast-forwards, and provably creates no rescue branch
- The push path, the no-op path and the existing committed-divergence merge all still behave exactly as they did, each provably creating no rescue branch
- A fast-forward that fails for a reason other than a dirty tree still returns the merge's own error and still rescues nothing
- A `git status` that itself fails does not short-circuit the merge — the merge is still attempted and its own error surfaces
- Three conventions that already hold in the code are written down, so the next change does not have to rediscover them
- Nothing in the rescue mechanism or the new counter changes
</summary>

<objective>
Add the regression specs that pin the pull paths this spec must NOT change (clean fast-forward, push, no-op, committed-divergence merge, non-dirty fast-forward failure, failing `git status`), and record the three conventions that hold in the code but are absent from `CLAUDE.md`: `log/slog` for logging, injected `libtime` timestamps instead of `time.Now()`, and pre-initialised Prometheus counters in `init()`.
</objective>

<context>
Read `CLAUDE.md` at the repo root for project conventions — its `## Key Design Decisions` list is where the three missing invariants go.

Read these coding-plugin guides before implementing (paths inside the YOLO container):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 + Gomega; external test packages; real `git` via `os/exec` against a temp working tree
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md` — the pre-initialisation rule the third `CLAUDE.md` invariant records
- `/home/node/.claude/plugins/marketplaces/coding/docs/claude-md-guide.md` — what belongs in a project `CLAUDE.md` and at what grain

Files to read in full before editing (line numbers are hints; anchor by symbol name):

- `pkg/git/git_test.go` — the `Pull state machine` Describe (~1006) with its `Context` blocks for "local clean, remote unchanged (no-op)", "local clean, remote has new commits (fast-forward)", "local ahead, remote unchanged (push)" and "diverged, no content conflict (merge+push)"; the `Dirty working tree rescue` Describe added by prompt 1; the helpers `setupPullFixture` (~689), `gitOutputStr` (~750), `runGit` (~29), `captureSlogLogs` (~1418) and `gatherPullRescues` (added by prompt 2)
- `pkg/git/git.go` — `syncWithUpstream`, `fastForwardOrRescue`, `workingTreeDirty` and `rescueDirtyTree` (all added by prompt 1), so the new specs assert against the real branches
- `CLAUDE.md` — the `## Key Design Decisions` bullet list
- `docs/dod.md` — the project's definition of done

**Preconditions from prompts 1 and 2 (already on this branch):**

- `pkg/git/git.go` has `workingTreeDirty`, `createRescueCommit`, `rescueDirtyTree`, `syncWithUpstream` and `fastForwardOrRescue`. `workingTreeDirty` logs the WARN `git-rest: git status failed before fast-forward; treating tree as clean` when `git status --porcelain` fails, and returns false in that case. `fastForwardOrRescue` inspects the tree BEFORE the merge and returns the merge's own error wrapped as `fast-forward merge failed` when the tree is not dirty.
- `pkg/metrics/metrics.go` has `PullRescuesTotal` (`git_rest_pull_rescues_total`), pre-initialised to 0 in `init()`, and the `Metrics` interface has `IncPullRescue()`.
- `pkg/git/git_test.go` has `gatherPullRescues() float64` and the `Dirty working tree rescue` Describe, whose specs read the counter as a delta against a `before` read.
- `pkg/git/git_test.go` has `setupRescueFixture() (workDir string, advanceRemote func(), dirtyTree func(), remoteDir string, cleanup func())`.

**Verified facts about the current code:**

- `setupPullFixture() (workDir string, externalPush func(file, content string), cleanup func())` creates a temp repo backed by a local bare remote whose initial commit is EMPTY, with upstream set. Its `externalPush(file, content)` clones the bare remote into a temp dir, writes `file`, commits and pushes.
- `writeLocalCommit(workDir, file, content)` writes a file, stages it and commits it locally without pushing.
- `gitOutputStr(dir string, args ...string) string` runs git and PANICS on a non-zero exit, returning combined output.
- `runGit(dir string, args ...string)` is the package-level helper that panics on error.
- `captureSlogLogs() (*bytes.Buffer, func())` swaps `slog.Default()` for a buffer-backed text handler at `slog.LevelInfo`, so WARN lines are captured.
- The existing `Pull state machine` Contexts already assert the happy behaviour of the no-op, fast-forward, push and diverged-merge paths. This prompt ADDS the negative rescue-branch assertions to them; it does not replace or rewrite them.
- `pkg/git/git_test.go` is `package git_test` and already imports `os`, `os/exec`, `path/filepath`, `strings`, `bytes`, `log/slog`, Ginkgo, Gomega, `prometheus`, `libtime "github.com/bborbe/time"`, `github.com/bborbe/git-rest/mocks`, `github.com/bborbe/git-rest/pkg/git` and `github.com/bborbe/git-rest/pkg/metrics`.
- `git for-each-ref --format=%(refname) refs/heads/rescue/` exits 0 with empty output when no ref matches — verified live. `gitOutputStr` therefore returns `""` and does not panic.
- With `.git/index.lock` present, `git status --porcelain` exits 0 with empty output (the tree reads as clean) while `git merge --ff-only` fails with `Unable to create '.../index.lock': File exists.` — verified live. This is the deterministic construction for a non-dirty fast-forward failure.
- Under uid 0 a `0000` mode file stays readable, so the failing-`git status` spec must skip itself when running as root — the same guard the `Quarantine backlog gauge` AC11 spec uses.
- `DeferCleanup` is available because the file dot-imports Ginkgo v2.

**Sibling prompts (do not do their work):**

- Prompt 1 shipped the rescue mechanism, the two log lines, the 006 constraint amendment and the `fix:` CHANGELOG bullet. Do NOT change any of them.
- Prompt 2 shipped the counter, its interface method, the regenerated `mocks/metrics.go`, the two hand-written fakes and the `feat:` CHANGELOG bullet. Do NOT change any of them.
- Prompt 4 rewrites `docs/verifying-specs.md` and `docs/deployment.md`. Do NOT touch `docs/`.
- The CHANGELOG needs no new bullet in this prompt: prompts 1 and 2 already wrote the two bullets that describe the whole change, and this prompt adds tests and docs only.
</context>

<requirements>

## 1. Add the rescue-branch-absence assertion to the no-op path

In the `Pull state machine` Describe, inside the existing `Context("local clean, remote unchanged (no-op)")`, add one spec. Do not modify the existing spec there.

```go
		It("AC6: the localSHA == remoteSHA no-op path creates no rescue branch", func() {
			before := gatherPullRescues()
			Expect(pg.Pull(ctx)).To(BeNil())
			Expect(strings.TrimSpace(gitOutputStr(
				workDir, "for-each-ref", "--format=%(refname)", "refs/heads/rescue/",
			))).To(BeEmpty())
			Expect(gatherPullRescues() - before).To(Equal(0.0))
		})
```

## 2. Add the rescue-branch-absence assertion to the fast-forward path

In the existing `Context("local clean, remote has new commits (fast-forward)")`, add one spec. Do not modify the two existing specs there.

```go
		It("AC4a: a clean fast-forward creates no rescue branch and counts no rescue", func() {
			before := gatherPullRescues()
			Expect(pg.Pull(ctx)).To(BeNil())
			Expect(strings.TrimSpace(gitOutputStr(
				workDir, "for-each-ref", "--format=%(refname)", "refs/heads/rescue/",
			))).To(BeEmpty(), "a clean tree must never be rescued")
			Expect(gatherPullRescues() - before).To(Equal(0.0))
		})
```

## 3. Add the rescue-branch-absence assertion to the push path

In the existing `Context("local ahead, remote unchanged (push)")`, add one spec. Do not modify the existing spec there.

```go
		It("AC6: the remoteSHA == baseSHA push path creates no rescue branch", func() {
			before := gatherPullRescues()
			Expect(pg.Pull(ctx)).To(BeNil())
			Expect(strings.TrimSpace(gitOutputStr(
				workDir, "for-each-ref", "--format=%(refname)", "refs/heads/rescue/",
			))).To(BeEmpty())
			Expect(gatherPullRescues() - before).To(Equal(0.0))
		})
```

## 4. Add the rescue-branch-absence assertion to the committed-divergence path

In the existing `Context("diverged, no content conflict (merge+push)")`, add one spec. The three existing specs there already assert the merge commit and both subjects in the log — do not duplicate those assertions, and do not modify the existing specs.

```go
		It("AC6: spec 006's committed-divergence merge creates no rescue branch", func() {
			before := gatherPullRescues()
			Expect(pg.Pull(ctx)).To(BeNil())
			Expect(strings.TrimSpace(gitOutputStr(
				workDir, "for-each-ref", "--format=%(refname)", "refs/heads/rescue/",
			))).To(BeEmpty())
			Expect(gatherPullRescues() - before).To(Equal(0.0))
		})
```

## 5. Add the non-dirty fast-forward failure context

Inside the `Pull state machine` Describe, add a new `Context` after `Context("HEAD has no upstream tracking ref")`. It uses the existing `setupPullFixture`-backed `workDir` and `externalPush` variables from the Describe's `BeforeEach`.

```go
	Context("fast-forward fails for a reason other than a dirty tree", func() {
		BeforeEach(func() {
			externalPush("remote.txt", "from remote\n")
			// An index.lock makes `git merge --ff-only` fail while the working tree
			// itself is clean, so this pins DB1's "every other cause" clause.
			Expect(os.WriteFile(
				filepath.Join(workDir, ".git", "index.lock"), []byte{}, 0o600,
			)).To(Succeed())
			DeferCleanup(func() {
				_ = os.Remove(filepath.Join(workDir, ".git", "index.lock"))
			})
		})

		It("AC4b: returns the merge's own error and rescues nothing", func() {
			before := gatherPullRescues()

			logs, restore := captureSlogLogs()
			defer restore()

			err := pg.Pull(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("fast-forward merge failed"))
			Expect(logs.String()).NotTo(ContainSubstring("rescue branch pushed"))

			Expect(strings.TrimSpace(gitOutputStr(
				workDir, "for-each-ref", "--format=%(refname)", "refs/heads/rescue/",
			))).To(BeEmpty())
			Expect(gatherPullRescues() - before).To(Equal(0.0))
		})
	})
```

The remote MUST be ahead in this context (that is what `externalPush` does): with `localSHA == remoteSHA` the state machine returns at the first case and no merge is attempted at all, so the spec would pass vacuously.

## 6. Add the failing-`git status` context

Inside the `Pull state machine` Describe, add a second new `Context` after the one from requirement 5.

```go
	Context("git status itself fails", func() {
		BeforeEach(func() {
			externalPush("remote.txt", "from remote\n")
			if os.Geteuid() == 0 {
				Skip("running as root: a 0000 file stays readable, so the case would pass vacuously")
			}
			Expect(os.Chmod(filepath.Join(workDir, ".git", "index"), 0o000)).To(Succeed())
			DeferCleanup(func() {
				_ = os.Chmod(filepath.Join(workDir, ".git", "index"), 0o600)
			})
		})

		It("AC4c: still attempts the merge and surfaces the merge's error", func() {
			before := gatherPullRescues()

			logs, restore := captureSlogLogs()
			defer restore()

			err := pg.Pull(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("fast-forward merge failed"))
			Expect(logs.String()).
				To(ContainSubstring("git status failed before fast-forward"))

			Expect(strings.TrimSpace(gitOutputStr(
				workDir, "for-each-ref", "--format=%(refname)", "refs/heads/rescue/",
			))).To(BeEmpty())
			Expect(gatherPullRescues() - before).To(Equal(0.0))
		})
	})
```

The WARN assertion is what makes this spec discriminating: without it, a `git status` that silently returned clean would pass the spec for the wrong reason. The `Skip` guard is required — under uid 0 a `0000` file stays readable, so the spec would otherwise pass without exercising the branch.

Note on the error message: with the index unreadable, both `git status --porcelain` and `git merge --ff-only` fail. The assertion that the returned error contains `fast-forward merge failed` is exactly the AC4(c) contract — a failing `git status` must NOT short-circuit the merge.

## 7. Record the three undocumented invariants in `CLAUDE.md`

Append three bullets to the existing `## Key Design Decisions` bullet list. Do not create a new section, do not reorder or reword any existing bullet, and do not add anything beyond these three:

```markdown
- Logging in new code uses `log/slog` (`slog.InfoContext` / `slog.WarnContext` / `slog.ErrorContext`) — no `fmt.Print*`. `pkg/git/yaml_merge_resolver.go` is the one legacy `github.com/golang/glog` holdout: keep `glog` there, and never mix the two loggers in one file.
- Timestamps come from the injected `libtime` getter (`currentDateTimeGetter` in `pkg/git`), never `time.Now()` inside `pkg/`
- Prometheus counters and gauges are pre-initialised in `init()` (`pkg/metrics/metrics.go`) so every series is visible on `/metrics` before its first event
```

These hold in the code for new code — `pkg/puller/puller.go` uses `log/slog` (`pkg/git/yaml_merge_resolver.go` is the one legacy `glog` holdout), `pkg/git/git.go` uses `g.currentDateTimeGetter.Now()` and `time.Now()` appears nowhere in `pkg/`, and `pkg/metrics/metrics.go` pre-initialises every series in `init()`. The bullets make them discoverable rather than something the next change has to rediscover. Do not restate or contradict the three invariants already documented in `CLAUDE.md` (errors wrapped with `github.com/bborbe/errors`, metrics under the `git_rest_` prefix, no `context.Background()` in `pkg/`).

## 8. Final verification

Run every command in `<verification>` from the repo root and confirm each result before finishing. Then walk each requirement above against the change: the four untouched paths each assert an empty rescue-ref list and a zero counter delta, the non-dirty fast-forward failure returns `fast-forward merge failed` with no rescue, the failing `git status` still attempts the merge and logs the WARN, and `CLAUDE.md` gained exactly three bullets.

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT change the rescue mechanism, the log messages, the ref naming, the temporary-index construction, the reset/clean gating, or the counter. Prompts 1 and 2 shipped those and they must stay byte-identical.
- Do NOT modify or rewrite any existing spec in `pkg/git/git_test.go`. This prompt only ADDS specs and adds one new `Context`-pair. Every existing `It` stays byte-identical.
- Do NOT add a CHANGELOG bullet — prompts 1 and 2 already wrote the two bullets that describe the whole change. Verify both are still present, do not rewrite them.
- Do NOT add a rescue-branch assertion that is vacuous. Each untouched-path spec must run against a fixture whose state actually exercises that path (the no-op path needs local == remote; the fast-forward path needs remote ahead and a clean tree; the push path needs local ahead; the divergence path needs both ahead).
- The non-dirty fast-forward failure context MUST have the remote ahead, or `localSHA == remoteSHA` returns before any merge is attempted and the spec proves nothing.
- The failing-`git status` spec MUST keep the `os.Geteuid() == 0` Skip guard, and MUST restore the index mode via `DeferCleanup`. Without the guard the spec passes vacuously under uid 0.
- The `pkg/git` counter assertions MUST be deltas against a `before` read. The counter is process-global and shared by every spec in that test binary, so an absolute-value assertion is wrong here.
- Do NOT add a scenario file under `scenarios/`. The behaviour is fully reachable from the Go integration suite that drives real `git` against a temp working tree, so a scenario prompt is not warranted for this spec.
- Do NOT touch `pkg/git/git.go`, `pkg/metrics/metrics.go`, `pkg/metrics/metrics_test.go`, `mocks/`, or `main.go` in this prompt. It adds specs and a doc list, nothing else.
- Do NOT touch `docs/verifying-specs.md` or `docs/deployment.md` — prompt 4 owns them. Do NOT touch `docs/api.md` — the public HTTP contract is frozen.
- The three `CLAUDE.md` bullets MUST be appended to `## Key Design Decisions`, in the order given, with no extra section heading and no rewording of existing bullets.
- Tests MUST use Ginkgo v2 + Gomega in the external `package git_test`, matching the file they are added to.
- `make precommit` from the repo root MUST exit 0.
- Existing tests must still pass, including every spec in the `Pull state machine`, `Entry-state recovery`, `Pull nested quarantine guard`, `Quarantine backlog gauge` and `Dirty working tree rescue` Describe blocks.
</constraints>

<verification>
Run from the repo root.

```bash
make precommit
```

Must exit 0.

Iterative runs while implementing (use these after each meaningful change, before the final `make precommit`):

```bash
make test
go test ./pkg/git/... -ginkgo.focus='Pull state machine'
go test ./pkg/git/... -ginkgo.focus='fast-forward fails for a reason other than a dirty tree'
go test ./pkg/git/... -ginkgo.focus='git status itself fails'
```

All pass. (The `git status itself fails` context skips itself when the test process runs as root; that is expected, not a failure.)

Focused evidence that the new negative specs are real, not vacuous:

```bash
go test ./pkg/git/... -ginkgo.focus='AC4a' -v
go test ./pkg/git/... -ginkgo.focus='AC4b' -v
go test ./pkg/git/... -ginkgo.focus='AC4c' -v
go test ./pkg/git/... -ginkgo.focus='AC6' -v
```

Each focused run passes, and the `AC4b` run prints a non-zero number of specs that ran (a `0 of 0` result means the focus string matched nothing — fix the focus, do not accept it).

Spec-count check — the new specs are present and no existing spec was removed:

```bash
grep -c 'AC4a:' pkg/git/git_test.go
grep -c 'AC4b:' pkg/git/git_test.go
grep -c 'AC4c:' pkg/git/git_test.go
grep -c 'AC6:' pkg/git/git_test.go
grep -c 'rescue branch pushed' pkg/git/git_test.go
```

The first three must each print at least `1`; the `AC6` grep must print at least `3` (the no-op, push and committed-divergence paths); the last must print at least `1` (the `AC4b` absence assertion).

New-context check:

```bash
grep -n 'fast-forward fails for a reason other than a dirty tree\|git status itself fails' pkg/git/git_test.go
grep -n 'git status failed before fast-forward' pkg/git/git_test.go
```

Both must print matches.

CLAUDE.md checks:

```bash
grep -n 'Logging in new code uses `log/slog`' CLAUDE.md
grep -n 'currentDateTimeGetter' CLAUDE.md
grep -n 'pre-initialised in `init()`' CLAUDE.md
grep -n '^## Key Design Decisions' CLAUDE.md
```

All four must print matches, and the three new bullets must appear after the `## Key Design Decisions` heading.

No-rewrite check — the existing invariants are still documented:

```bash
grep -n 'github.com/bborbe/errors' CLAUDE.md
grep -n 'git_rest_' CLAUDE.md
grep -n 'context.Background()' CLAUDE.md
```

Each must print at least one match.

CHANGELOG untouched check:

```bash
grep -c '^## Unreleased' CHANGELOG.md
grep -n 'rescue/' CHANGELOG.md
grep -n 'git_rest_pull_rescues_total' CHANGELOG.md
```

The first must print `1`; the second and third must each print at least one match — prompts 1 and 2's bullets are still present and unmodified.

Out-of-scope-files check:

```bash
grep -c 'IncPullRescue' pkg/git/git.go
```

Must still print `1` — this prompt did not add or move the counter call site.

Self-check before finishing: re-run `make precommit`, then walk each requirement above against the change — the four untouched paths each assert an empty rescue-ref list and a zero counter delta, the non-dirty fast-forward failure returns `fast-forward merge failed` and logs no `rescue branch pushed`, the failing-`git status` spec asserts the WARN and still expects the merge's error, and `CLAUDE.md` gained exactly three bullets appended to `## Key Design Decisions`.
</verification>
