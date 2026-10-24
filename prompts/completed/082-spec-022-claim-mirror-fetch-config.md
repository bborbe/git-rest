---
status: completed
spec: [022-claim-ref-local-mirror]
summary: Added an idempotent startup step configuring the origin fetch to mirror refs/claims/* into refs/claim-mirror/* with pruning, wired into createGitClient before the boot sync, with tests for idempotent success and failure-keeps-serving.
execution_id: git-rest-claim-mirror-exec-082-spec-022-claim-mirror-fetch-config
dark-factory-version: v0.198.7
created: "2026-10-24T15:44:42Z"
queued: "2026-10-24T15:55:49Z"
started: "2026-10-24T15:55:51Z"
completed: "2026-10-24T15:59:04Z"
branch: dark-factory/claim-ref-local-mirror
---

<summary>
- A served clone's origin fetch is configured, idempotently, to carry the claim namespace into a local mirror namespace.
- The configuration runs at startup before the boot-time sync, so the first request already has a populated mirror to read from.
- Running the configuration twice leaves exactly two fetch refspecs and no duplicate entry.
- Pruning is enabled for the origin, so claims deleted upstream disappear locally after a fetch.
- A configuration failure never aborts startup and never crashloops the pod: the client keeps serving and every claim read keeps using the live origin.
- A test-visible constructor lets tests drive the claim read path with a real repository and a fake origin.
</summary>

<objective>
Add the startup step that makes the puller's existing `git fetch` also carry `refs/claims/*` into `refs/claim-mirror/*`, and enable `remote.origin.prune`. The step must be idempotent and must never abort startup on failure. Wire it into `createGitClient` so it runs before `syncOnStartup`, and prove both the idempotent-success and the failure-keeps-serving behaviours with tests.
</objective>

<context>
Read the approved spec at `specs/in-progress/022-claim-ref-local-mirror.md` — especially § Desired Behavior 1, § Acceptance Criteria AC1, § Assumptions, and § Constraints.

Read these files fully before editing:
- `pkg/git/git.go` — the `git` struct, `New`, `GitOptions`/`GitOption`/`WithCoalesce`, `runCmd`, `runCmdOutput`, `runCmdRaw`, `ConfigureUser`, `ConfigurePackMemory`. The new configuration step follows the `ConfigurePackMemory` shape.
- `pkg/git/claim_refs.go` — `claimKeyPrefix`, `ClaimOrigin`, and the existing claim-ref code. New constants live next to these.
- `pkg/git/export_test.go` — `NewGitWithClaimOrigin` shows the test-only constructor pattern; add a sibling, do not change the existing one.
- `pkg/git/claim_refs_test.go` — `newClaimFixture(t)`, `mustGit(t, dir, args...)`, `configureClaimIdentity(t, dir)`, `newClaimClient(repoPath)`. Reuse them; do not duplicate fixture code.
- `pkg/git/git_test.go` — `noopMetrics`.
- `main.go` lines 68–97 (`Run`), 392–412 (`createGitClient`). Note `createGitClient` is called at line 79, before `syncOnStartup` at line 88.
- `pkg/factory/factory.go` — `CreateGitClient` passes its variadic options through to `git.New`.

Coding docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md` — interface + constructor conventions, counterfeiter.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `errors.Wrap(ctx, err, "…")`, never `fmt.Errorf`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — plain `testing` functions in `package git_test` (the Ginkgo `TestSuite` entry point cannot be selected with `-run`).
</context>

<requirements>
## 1. New file `pkg/git/claim_mirror.go`

Start with the repo's three-line license header (copy it from `pkg/git/claim_refs.go`). `package git`.

Add these constants:

```go
// claimMirrorRefspec carries the claim namespace into a mirror namespace the
// default heads refspec cannot reach.
const claimMirrorRefspec = "+refs/claims/*:refs/claim-mirror/*"

// claimMirrorNamespace is the local namespace the claims refspec writes.
const claimMirrorNamespace = "refs/claim-mirror/"
```

Add:

```go
// ConfigureClaimMirror makes the clone's origin fetch carry the claim namespace
// into refs/claim-mirror/* and enables pruning, so the puller's existing fetch
// refreshes the mirror. It is idempotent: a second call adds no duplicate entry.
// A failure is returned to the caller, which logs it and keeps serving.
func (g *git) ConfigureClaimMirror(ctx context.Context) error
```

Implementation, in this order:

1. Read the current entries with `g.runCmdRaw(ctx, g.repoPath, "config", "--get-all", "remote.origin.fetch")`. `git config --get-all` exits non-zero when the key is absent; treat that as "no entries" and continue (do not return on this read).
2. If the output does not already contain `claimMirrorRefspec` as a whole line, run `g.runCmd(ctx, g.repoPath, "config", "--add", "remote.origin.fetch", claimMirrorRefspec)`. On error return `errors.Wrap(ctx, err, "add claim mirror fetch refspec")`. `git config --add` is not idempotent, which is exactly why this presence test is mandatory — never call `--add` unconditionally.
3. Run `g.runCmd(ctx, g.repoPath, "config", "remote.origin.prune", "true")`. On error return `errors.Wrap(ctx, err, "enable remote.origin.prune")`. This write is a replace, so it is naturally idempotent.
4. Return nil.

Add an unexported helper used by this file and by later prompts:

```go
// hasConfigLine reports whether output contains want as a whole line.
func hasConfigLine(output []byte, want string) bool
```

Split on `"\n"`, compare each line exactly (no trimming of internal whitespace; trim a trailing `\r` only).

## 2. Add the method to the `Git` interface (`pkg/git/git.go`)

In the `Git` interface, immediately after `ConfigurePackMemory(ctx context.Context) error`, add:

```go
// ConfigureClaimMirror configures the clone's origin fetch to carry the claim
// namespace into refs/claim-mirror/* and enables remote.origin.prune. It is
// idempotent and must never abort startup on failure.
ConfigureClaimMirror(ctx context.Context) error
```

Do not hand-edit `mocks/git.go`. After the interface edit run:

```
go generate -mod=mod ./...
```

and confirm `mocks/git.go` now contains `ConfigureClaimMirror`. The directive in `pkg/git/git_suite_test.go` regenerates all fakes.

## 3. Wire the step into `createGitClient` (`main.go`)

Change `createGitClient` so the constructed client is a local variable, the configuration step runs against it, and a failure is warn-logged without changing the returned error:

```go
gitClient := factory.CreateGitClient(
    a.Repo,
    m,
    libtime.NewCurrentDateTime(),
    a.GitSSHKey,
    a.selectResolver(m),
    git.WithCoalesce(a.coalesceConfig()),
)
if err := gitClient.ConfigureClaimMirror(ctx); err != nil {
    slog.WarnContext(
        ctx,
        "git-rest: claim mirror configuration failed; claim reads will use the live origin",
        "error",
        err,
    )
}
return gitClient, nil
```

Keep the existing `.git`-exists and SSH-key checks above it. Do NOT change `initIfNeeded`, `cloneIfNeeded`, or `configureUserIfSet` — they build throwaway clients and do not need the step. This is the only `createGitClient` in the repo (single binary, no `cmd/` sub-binaries).

## 4. Test-only constructor (`pkg/git/export_test.go`)

Add:

```go
// NewGitWithClaimMirrorForTest returns a Git backed by repoPath whose
// origin-contacting operations go to origin. Test builds only.
func NewGitWithClaimMirrorForTest(repoPath string, origin ClaimOrigin) Git {
    return &git{repoPath: repoPath, origin: origin}
}
```

Do not modify `NewGitWithClaimOrigin`.

## 5. Tests (`pkg/git/claim_mirror_test.go`, new file, `package git_test`)

Start with the license header. Both tests are top-level `func TestClaimMirror…(t *testing.T)` functions so `go test -run 'TestClaimMirror'` selects them.

### 5.1 `TestClaimMirrorConfigureFetch`

- `fixture := newClaimFixture(t)`.
- `client := newClaimClient(fixture.cloneA)`.
- Call `client.ConfigureClaimMirror(context.Background())` twice; both must return nil.
- Read `out := mustGit(t, fixture.cloneA, "config", "--get-all", "remote.origin.fetch")`; split into non-empty lines and assert there are exactly **two**: `+refs/heads/*:refs/remotes/origin/*` and `+refs/claims/*:refs/claim-mirror/*` (compare as a set so the assertion does not depend on order, but assert the count so a duplicate fails).
- Assert `strings.TrimSpace(mustGit(t, fixture.cloneA, "config", "--get", "remote.origin.prune")) == "true"`.

### 5.2 `TestClaimMirrorConfigFailureKeepsServing`

The write must fail deterministically. Making `.git/config` read-only is not enough: git replaces the file through a lockfile + rename, which only needs write permission on the directory. Make the **directory** read-only instead.

- `fixture := newClaimFixture(t)`.
- `gitDir := filepath.Join(fixture.cloneA, ".git")`.
- `os.Chmod(gitDir, 0o500)` and register `t.Cleanup(func() { _ = os.Chmod(gitDir, 0o700) })`.
- Guard against running as root: try `os.WriteFile(filepath.Join(gitDir, ".probe"), []byte("x"), 0o600)`; if it succeeds, remove the probe and `t.Skip("running as root: cannot enforce a read-only .git directory")`.
- Build `g := git.NewGitWithClaimMirrorForTest(fixture.cloneA, fake)` where `fake := &mocks.FakeClaimOrigin{}` with `ListRefsStub = func(...) ([]git.RemoteRef, error) { return nil, nil }`.
- `err := g.ConfigureClaimMirror(context.Background())` must be non-nil. Assert no panic (the call simply returning is the proof).
- Then `claims, err := g.ListClaimRefs(context.Background(), "claims/")`: err must be nil, `claims` must be a non-nil empty slice, and `fake.ListRefsCallCount()` must be 1 — the client still serves and takes the live path.
</requirements>

<constraints>
- The `GET /api/v1/refs` response body, key set, ordering and status codes are unchanged; the `400` for a glob-bearing or invalid prefix is unchanged.
- No new metric, no new endpoint, no new query parameter, no new CLI flag.
- The mirror lives in `refs/claim-mirror/*`; a ref operation never writes configuration and never writes `refs/remotes/origin/*`.
- Git is driven by the system `git` binary, matching the existing client; every git subprocess on the request path is bound by the request context.
- The read path must not take `g.mu`.
- Do not change the executor's 30 s caller cap and do not add a git-rest-side request deadline.
- `autoRelease: false` — the release tag is cut manually after merge.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
```
set -e
go generate -mod=mod ./...
GOFLAGS=-buildvcs=false go test -mod=mod -count=1 -run 'TestClaimMirror(ConfigureFetch|ConfigFailureKeepsServing)$' -v ./pkg/git/
GOFLAGS=-buildvcs=false go test -mod=mod -count=1 ./pkg/git/... ./pkg/puller/... ./pkg/handler/...
make test
```
All must exit 0. If `go generate` is unavailable offline, report the failure instead of hand-editing `mocks/git.go`.
</verification>
