---
status: completed
summary: Added strict lsof -t output parsing so busybox-style listings are treated as undetermined, letting the /proc probe answer and stale .git/*.lock files be cleaned again
execution_id: git-rest-lockprobe-exec-101-lsof-terse-pid-parse
dark-factory-version: v0.198.17
created: "2026-11-05T20:16:49Z"
queued: "2026-11-05T20:16:49Z"
started: "2026-11-05T22:12:34Z"
completed: "2026-11-05T22:18:29Z"
---

# Treat an `lsof -t` listing that is not a bare pid list as no evidence

<summary>
- Stale git lock files are cleaned again inside the agent image, whose `lsof` is the busybox stand-in rather than the real tool.
- The busybox stand-in ignores the "pids only" flag and prints a full listing with success; that output is no longer read as "the lock is held".
- Output that is not one bare pid per line now falls through to the process-table probe instead of ending the decision.
- Real `lsof` on macOS is unaffected: a bare pid list still decides the question, and exit 1 with empty stderr still means free.
- The three failing `Per-cycle stale lock cleanup` specs pass again in the agent image.
- No behaviour change on any platform where `lsof` is real.
</summary>

<objective>
Stop `lockHeldByLsof` from reporting a free lock as held when the binary named `lsof` on `PATH` does not honour `-t`. In `bborbe/github-update-go-agent:v0.21.11` the binary is a symlink to busybox, whose `lsof` applet ignores every flag and prints a full open-file listing with exit 0 — so every lock, including a freshly created free one, is reported held, the per-cycle cleanup never removes a stale lock, and `make precommit` fails on three `pkg/git` specs, which parks the repo's Go bump at `human_review` forever.
</objective>

<context>
Measured in `bborbe/github-update-go-agent:v0.21.11` (the image the `github-update-go` agent runs), on a clean clone of `origin/master`:

```
$ ls -l /usr/bin/lsof
lrwxrwxrwx 1 root root 12 /usr/bin/lsof -> /bin/busybox
$ : > /tmp/free.lock && lsof -t /tmp/free.lock
1	/bin/busybox	0	/dev/null
1	/bin/busybox	1	pipe:[2033249]
1	/bin/busybox	2	pipe:[2033250]
$ echo $?
0
```

`lsof -t /tmp/free.lock` must print bare pids and nothing else, and must exit 1 when no process holds the file. This busybox applet does neither: `-t`, `-F` and `-v` are all ignored and the full listing is printed with exit 0.

Consequence in `lockHeldByLsof`: `err == nil` and the output is non-empty, so the function returns `(true, true)` — "held, determined" — for a file nothing holds. `lockHeldByProcess` therefore never reaches `lockHeldByProc`, `cleanupStaleLocks` leaves every lock in place, and `Pull` fails with `cannot lock ref 'HEAD': Unable to create '…/HEAD.lock': File exists`. Three specs fail, reproduced on `origin/master` at `16ad07a`:

```
[FAIL] Per-cycle stale lock cleanup [It] AC4: removes a lock created after boot and completes the fast-forward
[FAIL] Per-cycle stale lock cleanup [It] AC6: covers every *.lock under .git and touches no non-lock file
[FAIL] Per-cycle stale lock cleanup [It] probes a free lock and a live file-descriptor holder
Ran 6 of 248 Specs — 3 Passed | 3 Failed
```

The `BeforeEach` skip guard does not fire: `LockHeldByProcess` answers `determined == true` (wrongly), so `Skip` is never reached. The failing assertion in the third spec is `Expect(held).To(BeFalse())`.

Read these before changing anything.

- `pkg/git/git.go` — the probe chain:
  - `lockHeldByProcess` — stats the path first (a missing path is a determined "free"), then tries `lockHeldByLsof`, then `lockHeldByProc`.
  - `lockHeldByLsof` — **the function being fixed**. Note it currently returns `strings.TrimSpace(string(out)) != "", true` on any error-free exit.
  - `lockHeldByProc` / `scanProcFDs` / `procFDScan` — the `/proc` fallback. **Do not change these.** They answer correctly in the agent image: with a free lock the scan finds no match, sees `readable > 0`, `unreadable == 0`, and returns `(false, true)`.
  - `cleanupStaleLocks` — the per-cycle walk; it only removes a lock whose probe says `determined && !held`.
- `pkg/git/git_test.go` — `Describe("Per-cycle stale lock cleanup", …)`, whose `BeforeEach` holds the skip guard and whose six specs pin the behaviour. `It("leaves a lock in place when liveness cannot be determined")` and `It("treats a vanished lock as free and a missing .git as a no-op")` must keep passing unchanged.
- `pkg/git/export_test.go` — where the external test package reaches unexported helpers; the new parser needs an export here.
- `CHANGELOG.md` — there is no `## Unreleased` heading yet; the newest released heading (`## v0.40.5` at the time of writing — read the file, it may have moved) sits directly under the SemVer preamble, so the new heading goes above it.

Coding guides (in-container paths):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors` only, never `fmt.Errorf`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 / Gomega, external test package `git_test`.
</context>

<requirements>

## 1. Parse `lsof -t` output strictly (`pkg/git/git.go`)

1.1 Add:

```go
func parseLsofPIDList(out string) (pids []string, ok bool)
```

Contract:

- Split on `"\n"`, trim whitespace from each line, drop empty lines.
- Every remaining line must be a bare decimal integer: ASCII digits `0-9` only (no sign, no space, no separator — `strconv.Atoi` alone is not enough, it accepts `+1234`), and the value parsed with `strconv.Atoi` must be greater than zero. Anything else — a tab-separated field, a path, a header, a warning — makes the whole call return `nil, false`.
- No pid lines at all (empty or whitespace-only output) returns `nil, false`.
- Order is preserved as printed.
- Pure function: no I/O, no `exec`, no context.

1.2 `lockHeldByLsof` must consult it. Replace the `err == nil` branch with:

- Parse the output with `parseLsofPIDList`.
- `ok == false` → return `false, false`. The binary does not honour `-t`, so its output is not evidence of anything; returning undetermined is what lets `lockHeldByProcess` fall through to `lockHeldByProc`.
- `ok == true` → return `len(pids) > 0, true`.

The `exitErr.ExitCode() == 1 && len(exitErr.Stderr) == 0` branch and the final `return false, false` stay exactly as they are.

1.3 Rewrite the function's doc comment. It currently says lsof "exists on macOS and not in the alpine image", which is false — the image ships one, it is just not real lsof. State the two things the function now relies on: a real `lsof -t` prints one bare pid per line and exits 1 when nothing holds the file; a binary that prints anything else (measured: busybox's applet at `/usr/bin/lsof` in `bborbe/github-update-go-agent`, which ignores `-t` and exits 0) is treated as undetermined so the `/proc` probe answers instead.

1.4 Do not change `lockHeldByProcess`, `lockHeldByProc`, `scanProcFDs`, `procFDScan`, `cleanupStaleLocks`, or any caller. The fix is confined to `lockHeldByLsof` plus the new pure helper.

## 2. Export and tests

2.1 In `pkg/git/export_test.go`, beside `LockHeldByProcess`, add (the `LockHeldByLsof` export in 2.3 goes beside it too):

```go
// ParseLsofPIDList exposes the lsof -t output parser for direct tests.
// Test builds only.
var ParseLsofPIDList = parseLsofPIDList
```

2.2 In `pkg/git/git_test.go`, add a new `Describe("lsof -t output parsing", …)` block directly after the `Per-cycle stale lock cleanup` block. No fixture is needed — each spec drives `git.ParseLsofPIDList` with a literal.

Specs:

- **Real lsof output is evidence.** Input `"1234\n5678\n"` → `ok` true, `pids` equals `[]string{"1234", "5678"}`.
- **The busybox listing is not evidence.** Use the measured bytes exactly:
  `"1\t/bin/busybox\t0\t/dev/null\n1\t/bin/busybox\t1\tpipe:[2033249]\n1\t/bin/busybox\t2\tpipe:[2033250]\n"` → `ok` false, `pids` empty. This is the regression guard: before the fix this shape made `lockHeldByLsof` report every free lock as held.
- **Empty output is not evidence.** `""` and `"  \n\n "` → `ok` false.
- **A non-pid token is not evidence.** `"1234\nnot-a-pid\n"` → `ok` false, `pids` empty — a partial parse must not be accepted.
- **Zero is not a pid.** `"0\n"` → `ok` false.
- **A signed number is not a pid.** `"+1234\n"` → `ok` false.

2.3 Test the wiring, not only the parser. Requirement 1.2 is the behaviour change; the parser specs alone would pass even if `lockHeldByLsof` never called the parser. In `pkg/git/export_test.go` add, beside `LockHeldByProcess`:

```go
// LockHeldByLsof exposes the lsof backend for direct tests. Test builds only.
var LockHeldByLsof = lockHeldByLsof
```

In the same `Describe("lsof -t output parsing", …)` block, add a `Context("lockHeldByLsof with a stub lsof on PATH", …)` that, per spec:

- creates a temp dir with `GinkgoT().TempDir()`, writes an executable `lsof` shell script into it (`0o755`, first line `#!/bin/sh` — without it exec fails with "exec format error", which is not an `ExitError`; model it on the script-writing spec near `pkg/git/git_test.go:3439`), and prepends that dir to `PATH` with `GinkgoT().Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))` so the change is restored automatically;
- creates a real lock file with `os.WriteFile` in another temp dir and passes its path.

Specs:

- **A busybox-shaped listing is undetermined.** The stub prints only the one `/dev/null` line of the busybox listing (`printf '1\t/bin/busybox\t0\t/dev/null\n'` — not the `pipe:[…]` lines, which would break the `pipe:[2033249]` count in `<verification>`) and exits 0 → `LockHeldByLsof` returns `held == false`, `determined == false`. This is the regression guard for the wiring: before the fix it returned `true, true`.
- **A bare pid list is a determined "held".** The stub prints `1234` and exits 0 → `held == true`, `determined == true`.
- **Exit 1 with empty stderr is a determined "free".** The stub prints nothing and `exit 1` → `held == false`, `determined == true` — the unchanged branch, pinned so the fix cannot regress it.

The parser specs in 2.2 are the platform-independent guard; these three pin the wiring on any platform with `/bin/sh`.

2.4 Do not modify any existing spec's assertions or fixtures, and do not weaken the `BeforeEach` skip guard — it is still the correct behaviour for an environment where neither backend can answer.

## 3. Changelog (`CHANGELOG.md`)

3.1 If `CHANGELOG.md` already has a `## Unreleased` heading, append the bullet to the end of that section. Otherwise create `## Unreleased` directly above the first `## v` heading (the newest release). Then add exactly one bullet under it:

```
- fix: Treat an `lsof -t` listing that is not a bare pid per line as no evidence, so the busybox `lsof` applet in the agent image no longer reports every free lock as held and stale `.git/*.lock` files are cleaned again
```

Do not add a second bullet and do not touch a released section. Add exactly one `## Unreleased` heading.

## 4. Self-check before finishing

4.1 Re-run `<verification>` and confirm every line passes.

4.2 Walk each `<summary>` bullet against the change. In particular confirm that a real `lsof` on macOS still decides the question in both directions — the free-lock spec and the held-lock spec in `Per-cycle stale lock cleanup` are the two halves of that.
</requirements>

<constraints>
- The fix must not weaken lock safety: an undetermined answer must never delete a lock. Only `determined && !held` removes one, and that path is unchanged.
- `lockHeldByProc` and the `/proc` scan must not change — they answer correctly in the agent image.
- No new sentinel, configuration field, flag, metric or label. No change to `lockHeldByProcess`'s signature or to any caller.
- Errors are wrapped with `github.com/bborbe/errors`, never `fmt.Errorf`.
- Non-test `pkg/git` code must not call `context.Background()`; contexts are passed in.
- Tests are Ginkgo v2 / Gomega, external test package `git_test`.
- Precommit budgets: funlen 80/50, gocognit 20, nestif 4, maintidx 20, dupl, golines 100. `lockHeldByLsof` must stay under these; if it does not, extract a named helper rather than nesting.
- `.dark-factory.yaml` sets `workflow: direct`, `pr: false`, `autoRelease: false`: dark-factory commits to the branch directly; no PR or auto-release is expected.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run inside the container, from the repository root:

```
GOFLAGS=-buildvcs=false go build ./...
GOFLAGS=-buildvcs=false go test ./pkg/git/ -count=1
make test
make precommit
```

All must exit 0. `make precommit` is the authoritative gate; if it fails, fix the failure and re-run only the failing target until it passes, then run `make precommit` once more.

Then confirm the observable outcomes — every line must print `ok`:

```
test "$(grep -cF 'func parseLsofPIDList(' pkg/git/git.go)" = "1" && echo ok
test "$(grep -cF 'var ParseLsofPIDList = parseLsofPIDList' pkg/git/export_test.go)" = "1" && echo ok
test "$(grep -cF 'var LockHeldByLsof = lockHeldByLsof' pkg/git/export_test.go)" = "1" && echo ok
test "$(grep -cF 'lockHeldByLsof with a stub lsof on PATH' pkg/git/git_test.go)" = "1" && echo ok
test "$(grep -cF 'Describe("lsof -t output parsing"' pkg/git/git_test.go)" = "1" && echo ok
test "$(grep -cF 'pipe:[2033249]' pkg/git/git_test.go)" = "1" && echo ok
test "$(grep -c '^## Unreleased' CHANGELOG.md)" = "1" && echo ok
! grep -qF 'fmt.Errorf' pkg/git/git.go && echo ok
! grep -qF 'context.Background()' pkg/git/git.go && echo ok
```

And confirm the three previously failing specs now pass:

```
GOFLAGS=-buildvcs=false go test ./pkg/git/ -count=1 -args -ginkgo.focus="Per-cycle stale lock cleanup" 2>&1 | tail -5
```

Expected: no `[FAIL]` line, `0 Failed`, and `SUCCESS!`. If the six specs report `Skipped` (the probe is inconclusive in this container), that is acceptable — the 2.3 stub specs are the guard that runs here; do not weaken the skip guard to force them to run.
</verification>
