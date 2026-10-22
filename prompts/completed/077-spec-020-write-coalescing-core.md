---
status: completed
spec: [020-commit-coalescing]
summary: 'Implemented write coalescing: stage-not-commit on the file-write path with an on-disk no-op short-circuit, a mutex-protected pending set, a timer flusher with trailing and max delay, a flush-before-pull boundary, three flags and three pre-initialised metrics, plus tests.'
execution_id: git-rest-coalesce-exec-077-spec-020-write-coalescing-core
dark-factory-version: v0.198.7
created: "2026-10-22T10:38:09Z"
queued: "2026-10-22T10:56:34Z"
started: "2026-10-22T10:56:57Z"
completed: "2026-10-22T11:04:04Z"
---

<summary>
- A POST to a file returns as soon as the bytes are written and staged; no commit and no push runs inside the request.
- A background flusher turns a burst of writes into one commit and one push.
- The flusher fires when the pending set has been quiet for the configured delay, or when the oldest pending write reaches the configured maximum delay, whichever comes first.
- Writing bytes identical to the file already on disk stages nothing and produces no commit, preserving today's no-op behaviour.
- Every pull cycle commits and pushes pending writes before it resolves upstream, so a pull never merges against a tree this process dirtied.
- A commit touching one path keeps today's message; a batch touching several paths uses a single "update N files" message.
- Three new Prometheus series make the mechanism observable, and three new flags (one boolean off-switch, two durations) control it without a redeploy.
- Setting the off-switch restores today's synchronous write-commit-push behaviour exactly.
- Claim refs and file deletes are untouched by this prompt; they keep their synchronous semantics.
</summary>

<objective>
Implement the core write-coalescing mechanism in the git layer: stage-not-commit on the file-write path, an on-disk no-op short-circuit, a mutex-protected pending set, a timer-driven flusher with trailing-delay plus max-delay, a flush-before-pull boundary, three flags, and three pre-initialised metrics. A write must return without a commit or push, and a burst of writes must land as one commit carrying the last content.
</objective>

<context>
Read the project's `CLAUDE.md` (if present) and the approved spec at `specs/in-progress/020-commit-coalescing.md` — especially § Desired Behavior, § Constraints, § Failure Modes, and the acceptance criteria named below.

Read these files fully before editing (they are the only files this prompt changes, plus tests):
- `pkg/git/git.go` — `Git` interface (line ~144), `New` (line ~175), `git` struct (line ~193), `WriteFile` (line ~668), `WriteFileIfAbsent` (line ~694), `writeFileLocked` (line ~718), `DeleteFile` (line ~772), `Pull` (line ~1864), `pushWithRetry` (line ~327), `hasRemote` (line ~2200).
- `pkg/metrics/metrics.go` — metric vars, `Metrics` interface, `prometheusMetrics` implementation, `init()` pre-initialisation.
- `pkg/factory/factory.go` — `CreateGitClient` (line ~19).
- `main.go` — `application` flags (line ~37), `Run` (line ~61), `createGitClient` (line ~360), `createGitRefresher` (line ~415), `syncOnStartup` (line ~199).
- `pkg/puller/puller.go` — the package/constructor/`Run` shape the new flusher mirrors.
- `pkg/git/git_test.go` — `initRepo`, `noopMetrics`, `runGit` helpers; `pkg/git/claim_refs_test.go` — plain-`testing` fixture style; `pkg/git/git_suite_test.go` — Ginkgo suite.
- `pkg/metrics/metrics_test.go` — pre-initialisation test helpers (`gatherCounter`, `gatherGauge`, `gatherCounterVecLabelValue`).

Coding docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-functional-options-pattern.md` — variadic options pattern used below.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md` — metric naming, `init()` pre-initialisation.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md` — interface → constructor → struct → method, `errors.Wrap`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors`, never `fmt.Errorf`, never `context.Background()` in `pkg/`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` and `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md` — coverage and test conventions.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-time-injection.md` — injected `libtime` getter for user-visible timestamps; the flusher uses monotonic `time.Now()`/`time.Since` for scheduling (see DB2/failure-mode "Clock skew").
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-factory-pattern.md` — `Create*` composition, no logic in factories.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md` — the flusher loop's non-blocking `select`.

Also read the project's own `docs/dod.md` (the daemon's validation prompt). **Two of its criteria are intentionally deviated from here**: the Testing criterion uses plain `testing` for the `pkg/git` tests because Ginkgo's single `TestSuite` entry point (`pkg/git/git_suite_test.go:18`) cannot be selected by `go test -run` (the `main_test.go` spec in 8.5 stays Ginkgo), and the `## Unreleased` CHANGELOG entry it asks for is delivered by prompt 3 in this batch — do not edit `CHANGELOG.md` here and do not report either as a blocker.
</context>

<requirements>
## 1. Metrics (`pkg/metrics/metrics.go`)

1.1 Add three package-level vars next to the existing ones:

```go
// WriteFlushesTotal counts coalesced flush attempts by outcome ("ok" or "error").
var WriteFlushesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "git_rest_write_flushes_total",
	Help: "Total write-coalescing flush attempts by outcome.",
}, []string{"outcome"})

// WriteCoalescedTotal counts file writes absorbed into a coalesced commit batch.
var WriteCoalescedTotal = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "git_rest_write_coalesced_total",
	Help: "Total file writes absorbed into a coalesced commit batch.",
})

// WritePending reports the number of paths currently staged and waiting for a coalesced commit.
var WritePending = prometheus.NewGauge(prometheus.GaugeOpts{
	Name: "git_rest_write_pending",
	Help: "Number of paths currently staged and waiting for a coalesced commit.",
})
```

1.2 Add three methods to the `Metrics` interface (after `IncPushRetry()`), each with a GoDoc comment:

```go
// IncWriteFlush records one coalesced flush attempt. outcome is "ok" or "error".
IncWriteFlush(outcome string)
// IncWriteCoalesced records one file write absorbed into a coalesced batch.
// It increments once per staged write, so a three-write burst increments it by 3.
IncWriteCoalesced()
// SetWritePending records the current pending-path count. It is a setter so a
// successful flush returns the gauge to 0.
SetWritePending(count int)
```

1.3 Implement them on `prometheusMetrics` using `WriteFlushesTotal.WithLabelValues(outcome).Inc()`, `WriteCoalescedTotal.Inc()`, `WritePending.Set(float64(count))`.

1.4 In `init()`: register all three with `prometheus.MustRegister`, then pre-initialise `WriteFlushesTotal.WithLabelValues("ok").Add(0)`, `WriteFlushesTotal.WithLabelValues("error").Add(0)`, `WriteCoalescedTotal.Add(0)`, `WritePending.Set(0)`. Mirror the existing `QuarantinedBacklog` comment style explaining the explicit pre-initialisation.

1.5 Update every non-generated `metrics.Metrics` implementation in tests so the package compiles: `pkg/git/git_test.go` (`noopMetrics`) and `pkg/git/resolve_conflict_merge_test.go` (`unsafeTestMetrics`). Regenerate `mocks/metrics.go` with `go generate ./...` — do not hand-edit generated files.

1.6 Extend `pkg/metrics/metrics_test.go`: assert `git_rest_write_flushes_total{outcome="ok"}` and `{outcome="error"}` are present at 0 at `init()` time, that `git_rest_write_coalesced_total` is present at 0, that `git_rest_write_pending` is present at 0, and that each interface method moves its series (`IncWriteFlush`, `IncWriteCoalesced`, `SetWritePending`). Use `gatherCounterPresent` for `git_rest_write_coalesced_total` and `gatherGauge` (which returns `(value, present)`) for `git_rest_write_pending` — **`gatherCounter` and `gatherCounterVecLabelValue` return 0 for an absent series too, so they cannot distinguish "registered at 0" from "not registered"** (see their own doc comment at `pkg/metrics/metrics_test.go:95-98`), and a presence assertion built on them would pass even if `MustRegister` were deleted. For the two `{outcome=...}` series, add a presence check that scans `prometheus.DefaultGatherer.Gather()` for the family and the label value.

## 2. Coalescing configuration on the git client (`pkg/git/git.go`)

2.1 Add the config and option types above `New`:

```go
// CoalesceConfig controls write coalescing. Enabled=false restores the
// synchronous write-commit-push path used before coalescing existed.
type CoalesceConfig struct {
	Enabled  bool
	Delay    time.Duration
	MaxDelay time.Duration
}

// GitOptions holds optional configuration for New.
type GitOptions struct {
	Coalesce CoalesceConfig
}

// GitOption modifies GitOptions.
type GitOption func(*GitOptions)

// WithCoalesce sets the write-coalescing configuration.
func WithCoalesce(config CoalesceConfig) GitOption {
	return func(opts *GitOptions) {
		opts.Coalesce = config
	}
}
```

2.2 Change `New` to accept variadic options — the zero value must leave coalescing disabled, so every existing caller keeps compiling and keeps today's synchronous behaviour:

```go
func New(
	repoPath string,
	m metrics.Metrics,
	currentDateTimeGetter libtime.CurrentDateTimeGetter,
	sshKeyPath SSHKeyPath,
	resolver ConflictResolver,
	options ...GitOption,
) Git {
	gitOptions := GitOptions{}
	for _, option := range options {
		option(&gitOptions)
	}
	g := &git{
		repoPath:              repoPath,
		metrics:               m,
		currentDateTimeGetter: currentDateTimeGetter,
		sshKeyPath:            sshKeyPath,
		resolver:              resolver,
		coalesce:              gitOptions.Coalesce,
		pending:               make(map[string]pendingWrite),
	}
	g.origin = &gitOrigin{g: g}
	return g
}
```

2.3 Add these fields to the `git` struct: `coalesce CoalesceConfig` and `pending map[string]pendingWrite`. Define:

```go
// pendingWrite records one staged-but-uncommitted path. created is the
// create-vs-update decision captured at the first write of the batch.
type pendingWrite struct {
	created      bool
	firstSeen    time.Time
	lastModified time.Time
}
```

## 3. Stage-not-commit on the file-write path

3.1 Add two methods to the `Git` interface (with GoDoc comments):

```go
// FlushPending commits and pushes the pending write set. A no-op when the set is empty.
FlushPending(ctx context.Context) error
// FlushPendingIfDue flushes only when the pending set is non-empty and either
// the trailing delay or the max-delay has elapsed.
FlushPendingIfDue(ctx context.Context) error
```

3.2 Replace the body of `writeFileLocked` with this shape, keeping every existing error-wrap and `IncGitOperationError("write_file")` call:

```go
func (g *git) writeFileLocked(
	ctx context.Context,
	path string,
	content []byte,
	fileExists bool,
) error {
	fullPath := filepath.Join(g.repoPath, path)

	// Compare against the file on disk, NOT the index against HEAD: after the
	// first write of a burst the index already differs from HEAD, so an
	// index-vs-HEAD check would report "changed" for every later identical write.
	if existing, err := os.ReadFile(fullPath); err == nil && bytes.Equal(existing, content) {
		slog.InfoContext(ctx, "write file: no changes to commit (content unchanged)", "path", path)
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(fullPath), 0750); err != nil {
		g.metrics.IncGitOperationError("write_file")
		return errors.Wrapf(ctx, err, "create directories for %s", path)
	}
	if err := os.WriteFile(fullPath, content, 0600); err != nil { //nolint:gosec
		g.metrics.IncGitOperationError("write_file")
		return errors.Wrapf(ctx, err, "write file %s", path)
	}
	if err := g.runCmd(ctx, g.repoPath, "add", path); err != nil {
		g.metrics.IncGitOperationError("write_file")
		return errors.Wrapf(ctx, err, "git add %s", path)
	}

	if g.coalesce.Enabled {
		g.enqueuePendingLocked(path, !fileExists)
		return nil
	}

	// Coalescing disabled: keep today's synchronous commit + push exactly.
	// (existing commit-message selection, commit, "nothing to commit" handling,
	//  hasRemote guard and pushWithRetry stay verbatim)
}
```

The exact log string `write file: no changes to commit (content unchanged)` is load-bearing — the operator acceptance criterion greps it.

3.3 Add:

```go
// enqueuePendingLocked records path in the pending set and updates the gauges.
// The caller holds g.mu.
func (g *git) enqueuePendingLocked(path string, created bool) {
	now := time.Now()
	entry, ok := g.pending[path]
	if !ok {
		entry = pendingWrite{created: created, firstSeen: now}
	}
	entry.lastModified = now
	g.pending[path] = entry
	g.metrics.IncWriteCoalesced()
	g.metrics.SetWritePending(len(g.pending))
}
```

`IncWriteCoalesced` is called once per staged write, including the first of a burst — a three-write burst increments it by 3, which is what the acceptance criterion asserts. A no-op write (bytes identical on disk) returns before this and must not increment.

3.4 `WriteFileIfAbsent` keeps its `ErrFileExists` behaviour and calls `writeFileLocked(ctx, path, content, false)`; coalescing applies to it too (the create-only path is still a file write).

3.5 Update the `WriteFile` and `WriteFileIfAbsent` GoDoc comments: they now stage the content; a background flusher commits and pushes it.

## 4. The flush (`pkg/git/git.go`)

4.1 Add:

```go
// FlushPending commits and pushes the pending write set.
func (g *git) FlushPending(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.flushPendingLocked(ctx)
}

// FlushPendingIfDue flushes when the trailing delay or max-delay has elapsed.
func (g *git) FlushPendingIfDue(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.pending) == 0 {
		return nil
	}
	oldest, latest := g.pendingBoundsLocked()
	now := time.Now()
	trailingDue := now.Sub(latest) >= g.coalesce.Delay
	maxDue := g.coalesce.MaxDelay > 0 && now.Sub(oldest) >= g.coalesce.MaxDelay
	if !trailingDue && !maxDue {
		return nil
	}
	return g.flushPendingLocked(ctx)
}

// pendingBoundsLocked returns the oldest firstSeen and the most recent
// lastModified in the pending set. The caller holds g.mu.
func (g *git) pendingBoundsLocked() (oldest, latest time.Time) {
	for _, entry := range g.pending {
		if oldest.IsZero() || entry.firstSeen.Before(oldest) {
			oldest = entry.firstSeen
		}
		if entry.lastModified.After(latest) {
			latest = entry.lastModified
		}
	}
	return oldest, latest
}
```

`time.Now()` carries a monotonic reading, so a skewed wall clock changes no scheduling decision. Do not use the injected `libtime` getter for this scheduling.

4.2 Implement `flushPendingLocked` (caller holds `g.mu`):

```go
func (g *git) flushPendingLocked(ctx context.Context) error {
	if len(g.pending) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return errors.Wrap(ctx, err, "flush pending writes")
	}

	paths := make([]string, 0, len(g.pending))
	for path := range g.pending {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	message := g.flushCommitMessageLocked(paths)

	preFlushHEAD, headErr := g.runCmdOutput(ctx, g.repoPath, "rev-parse", "HEAD")
	preFlushSHA := strings.TrimSpace(string(preFlushHEAD))

	commitOut, err := g.runCmdRaw(ctx, g.repoPath, "commit", "-m", message)
	if err != nil && !strings.Contains(string(commitOut), "nothing to commit") {
		g.metrics.IncWriteFlush("error")
		return errors.Wrapf(ctx, err, "git commit: %s", strings.TrimSpace(string(commitOut)))
	}

	pushOutcome := "skipped"
	if g.hasRemote(ctx) {
		if err := g.pushWithRetry(ctx, ""); err != nil {
			g.restorePendingIndexLocked(ctx, preFlushSHA, headErr)
			g.metrics.IncWriteFlush("error")
			return errors.Wrap(ctx, err, "git push")
		}
		pushOutcome = "ok"
	}

	g.pending = make(map[string]pendingWrite)
	g.metrics.SetWritePending(0)
	g.metrics.IncWriteFlush("ok")
	slog.InfoContext(ctx, "git-rest: flushed pending writes", "paths", len(paths), "push", pushOutcome)
	return nil
}
```

4.3 Commit-message rule (DB3):

```go
// flushCommitMessageLocked builds the batch commit message. The git-rest:
// prefix is load-bearing: the rate measurement greps ^git-rest:.
func (g *git) flushCommitMessageLocked(paths []string) string {
	if len(paths) == 1 {
		if g.pending[paths[0]].created {
			return "git-rest: create " + paths[0]
		}
		return "git-rest: update " + paths[0]
	}
	return "git-rest: update " + strconv.Itoa(len(paths)) + " files"
}
```

A single path uses today's message verbatim (`git-rest: create <path>` / `git-rest: update <path>`); more than one path uses `git-rest: update <n> files`.

4.4 A failed push must never drop the batch, and the index must still carry the staged paths afterwards (the operator criterion for the SIGTERM deadline asserts `git diff --cached --name-only` lists the path). Add:

```go
// restorePendingIndexLocked moves HEAD back to preFlushSHA while keeping the
// committed tree in the index, so a failed push leaves the paths staged and the
// pending set durable. The reset runs on a context without cancellation: the
// shutdown flush may already be past its deadline and the restore is a fast
// local operation.
func (g *git) restorePendingIndexLocked(ctx context.Context, preFlushSHA string, headErr error) {
	if headErr != nil || preFlushSHA == "" {
		return
	}
	if err := g.runCmd(context.WithoutCancel(ctx), g.repoPath, "reset", "--soft", preFlushSHA); err != nil {
		slog.WarnContext(ctx, "git-rest: restore pending index after failed flush", "error", err)
	}
}
```

The pending set is cleared only after the push returns nil. On a commit or push error the set stays intact and the next tick retries.

4.5 `git_rest_write_pending` is set to `len(g.pending)` when a write joins the set and to `0` after a successful flush. A failed flush leaves the previous value in place.

## 5. Flush before pull (DB4)

In `Pull` (`pkg/git/git.go`), after `g.mu.Lock()` / `defer g.mu.Unlock()` and the `defer g.refreshQuarantinedBacklog(ctx)`, and BEFORE the `if !g.hasRemote(ctx)` early return and before `rev-parse @{u}`, insert:

```go
// Flush pending writes before resolving upstream or merging, so a pull never
// merges against a tree this process dirtied.
if err := g.flushPendingLocked(ctx); err != nil {
	slog.WarnContext(ctx, "git-rest: flush pending writes before pull failed", "error", err)
}
```

A flush failure must not abort the pull — the pull's existing rescue path (spec 019) covers a tree that is still dirty, and the next tick retries the flush.

## 6. Flusher package (`pkg/flusher/flusher.go`, new)

Create a package mirroring `pkg/puller`:

```go
package flusher

import (
	"context"
	"log/slog"
	"time"

	libtime "github.com/bborbe/time"

	"github.com/bborbe/git-rest/pkg/git"
)

// Flusher periodically flushes due pending writes on a git client.
type Flusher interface {
	Run(ctx context.Context) error
}

// New returns a Flusher that ticks on tickInterval and flushes when the git
// client reports the pending set is due.
func New(g git.Git, tickInterval libtime.Duration) Flusher {
	return &flusher{git: g, tickInterval: tickInterval}
}

type flusher struct {
	git          git.Git
	tickInterval libtime.Duration
}

// Run starts the flush loop. It returns when ctx is cancelled.
func (f *flusher) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Duration(f.tickInterval))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := f.git.FlushPendingIfDue(ctx); err != nil {
				slog.WarnContext(ctx, "git-rest: flush pending writes failed", "error", err)
			}
		}
	}
}
```

No counterfeiter annotation — no consumer injects a fake `Flusher`. The due decision lives in the git client under the package mutex, so the flusher holds no state.

## 7. Factory and main wiring

7.1 `pkg/factory/factory.go` — make `CreateGitClient` variadic so existing call sites keep compiling:

```go
func CreateGitClient(
	repoPath string,
	m metrics.Metrics,
	currentDateTimeGetter libtime.CurrentDateTimeGetter,
	sshKeyPath git.SSHKeyPath,
	resolver git.ConflictResolver,
	options ...git.GitOption,
) git.Git {
	return git.New(repoPath, m, currentDateTimeGetter, sshKeyPath, resolver, options...)
}
```

7.2 `main.go` — add three flags to the `application` struct, following the existing `arg:` / `env:` convention and `libtime.Duration` for durations:

```go
WriteCoalesce bool `required:"false" arg:"write-coalesce" env:"WRITE_COALESCE" usage:"Coalesce file writes into one commit and one push per window. false restores synchronous write-commit-push." default:"true"`
WriteCoalesceDelay libtime.Duration `required:"false" arg:"write-coalesce-delay" env:"WRITE_COALESCE_DELAY" usage:"Flush after the pending set has been quiet for this long." default:"30s"`
WriteCoalesceMaxDelay libtime.Duration `required:"false" arg:"write-coalesce-max-delay" env:"WRITE_COALESCE_MAX_DELAY" usage:"Flush when the oldest pending write is this old, even under continuous writes." default:"120s"`
```

Disabling is the boolean, NOT a zero duration. Do not add any other flag or knob.

7.3 Add near `syncOnStartupTimeout`:

```go
// flushTickInterval is how often the flusher asks the git client whether the
// pending set is due. The due decision itself uses the configured delays.
const flushTickInterval = time.Second
```

7.4 Add:

```go
func (a *application) coalesceConfig() git.CoalesceConfig {
	return git.CoalesceConfig{
		Enabled:  a.WriteCoalesce,
		Delay:    time.Duration(a.WriteCoalesceDelay),
		MaxDelay: time.Duration(a.WriteCoalesceMaxDelay),
	}
}

func (a *application) createWriteFlusher(gitClient git.Git) run.Func {
	return func(ctx context.Context) error {
		return flusher.New(gitClient, libtime.Duration(flushTickInterval)).Run(ctx)
	}
}
```

7.5 In `createGitClient`, pass the option: `factory.CreateGitClient(a.Repo, m, libtime.NewCurrentDateTime(), a.GitSSHKey, a.selectResolver(m), git.WithCoalesce(a.coalesceConfig()))`. Leave the three bootstrap clients (`initIfNeeded`, `cloneIfNeeded`, `configureUserIfSet`) without the option — they perform no file writes. Add `a.createWriteFlusher(gitClient)` to the `service.Run(...)` argument list, after `a.createGitRefresher(gitClient)`.

7.6 Import `"github.com/bborbe/git-rest/pkg/flusher"`.

## 8. Tests (plain `testing`, package `git_test`, new file `pkg/git/coalesce_test.go`)

The spec's container verification runs `go test ./pkg/git/... -run '<Name>'`. Ginkgo's `-run` filters Go test functions, so write these as plain `func TestXxx(t *testing.T)` (the repo already does this in `pkg/git/claim_refs_test.go`), not as Ginkgo specs. Use `mocks.FakeMetrics` to observe the new metric calls.

Add a fixture helper in the new file:

```go
// newCoalesceFixture builds a bare origin, a seed with tasks/x.md and
// tasks/y.md containing "v0\n", a work clone, and a git client configured with
// the given coalescing options. It returns the client, the work clone path and
// the bare origin path.
func newCoalesceFixture(t *testing.T, config git.CoalesceConfig) (git.Git, string, string)
```

Use `libtime.NewCurrentDateTime()`, `""` for the SSH key path, and `git.NewMarkerResolver(work)`. Push the seed to the bare origin and set the bare origin's `HEAD` to the seeded branch, mirroring `newClaimFixture`.

8.1 `TestCoalesc` — three writes to `tasks/x.md` with content `A`, `B`, `C` (no sleeps), then `FlushPending`:
- with `CoalesceConfig{Enabled: true, Delay: 20 * time.Millisecond, MaxDelay: time.Second}`: exactly one commit on origin touching `tasks/x.md`, subject `git-rest: update tasks/x.md`, and `git show <origin-branch>:tasks/x.md` is `C`; `FakeMetrics.IncWriteCoalescedCallCount()` is exactly **3** — DB7 counts writes absorbed, not batches merged, so a per-flush increment must fail this. Capture `slog` into a `bytes.Buffer` via `slog.NewTextHandler` (`slog.SetDefault`, restore with `defer`) and assert the buffer contains `git-rest: flushed pending writes` — AC5 and AC15 both grep that exact prefix and it is the Detection cell for "Pull arrives with writes pending", so a typo in it would otherwise pass every container test.
- with `CoalesceConfig{Enabled: false}`: three commits with subject `git-rest: update tasks/x.md`, final content `C`.
- with `CoalesceConfig{Enabled: true}`, `WriteFileIfAbsent` on a path that does not exist (`tasks/new.md`), then `FlushPending`: exactly one commit with subject `git-rest: create tasks/new.md` — **the only test of the `created` branch in `flushCommitMessageLocked` anywhere in the batch** (8.1 writes to an existing file, and prompt 2's recovery tests stage modifications only).

8.2 `TestUnchangedWriteStagesNothing` — write `A` three times, `FlushPending`:
- exactly one `git-rest: update tasks/x.md` commit on origin;
- `FakeMetrics.IncWriteCoalescedCallCount()` is 1;
- capture `slog` output with a `slog.NewTextHandler` on a `bytes.Buffer` (`slog.SetDefault`, restore the previous logger with `defer`), and assert the buffer contains the exact string `write file: no changes to commit (content unchanged)` twice.

8.3 `TestFlushBeforePull` — write `tasks/x.md`, then call `Pull(ctx)` directly:
- `git status --porcelain` in the work clone is empty;
- the origin's branch log for `tasks/x.md` contains at least one `^git-rest:` subject;
- no `rescue/` branch exists on the origin (a dirty tree would have produced one).

8.4 `TestFlushTimerFires` — `CoalesceConfig{Enabled: true, Delay: 50 * time.Millisecond, MaxDelay: time.Second}`; write once, then start `flusher.New(g, libtime.Duration(10*time.Millisecond)).Run(ctx)` with a cancelable context. Poll for up to 2 s until the origin has a `git-rest:` commit for `tasks/x.md`. Never call `Pull`. This proves the timer, not the pull boundary, did the flush.

8.6 `TestFlushMaxDelayCeiling` — `CoalesceConfig{Enabled: true, Delay: time.Hour, MaxDelay: 10 * time.Millisecond}`; write once, sleep 20 ms, then call `FlushPendingIfDue` and assert it returns nil and the origin now has one `git-rest: update tasks/x.md` commit. **The trailing delay cannot fire (an hour), so only the max-delay branch can produce that commit** — this is the branch AC13's second clause exists to catch, and nothing else in the batch exercises it (8.1 calls `FlushPending` directly, 8.4 uses `Delay: 50ms` / `MaxDelay: 1s` so the trailing timer fires first).

Add a small `waitFor(t *testing.T, timeout time.Duration, condition func() bool)` polling helper in the file.

8.5 `TestWriteCoalesceFlags` (in `main_test.go`, package `main_test`, reusing the suite's `gexec.Build`) — **the three flags exist only in the `arg:` / `env:` struct tags, so only a run through the real parser can catch a typo in them.** A misspelled `arg:"write-coalesce"` produces no compile error and is rejected only when an operator runs a recipe, i.e. after merge. Build the binary, then start it against a `git init` temp repo with `--write-coalesce=false --write-coalesce-delay=1ms --write-coalesce-max-delay=2ms` (the suite has no binary-start helper today — pass `--repo=<t.TempDir()>` and `--listen=127.0.0.1:0` yourself and capture stderr into a buffer), assert it is still running after ~1 s and its stderr contains no `flag provided but not defined` / `unknown flag`, then kill it. Repeat once with `WRITE_COALESCE=false` and no flags. **That alone proves only that the variable is accepted, not that the `env:` tag is spelled correctly** — a misspelled tag is silently ignored, the flag keeps its default, and the binary starts either way, so the rollback lever could ship broken. To actually traverse the `env:` boundary, run once more with `WRITE_COALESCE_DELAY=bogus` and no flags and assert the process **exits non-zero** (verified on this repo: exit 4) with stderr containing `parse app failed` — `github.com/bborbe/argument` rejects the invalid duration and `service.Main` returns before `Run`, while a misspelled `env:` tag ignores the variable, the process starts normally, and this assertion fails.

## 9. Reference — acceptance criteria this prompt implements (operator-executed Rung 1, not run in the container)

These are copied from the spec so the behaviour you implement is unambiguous. They are verified on the host after merge against recipe binaries; do not attempt to run them here and do not put them in the verification block.

- **AC1** — a burst to one path lands as one commit carrying the last content: `git -C "$BASE/work" log --format='%s' -- tasks/x.md | grep -c '^git-rest: update tasks/x.md$'` returns `1`; `git show HEAD:tasks/x.md` prints `C`.
- **AC2** — the same recipe with `--write-coalesce=false` returns `3` for the same grep, content `C` at HEAD.
- **AC3** — a burst spanning two paths lands as one commit: `git log -1 --format='%s'` matches `^git-rest: update 2 files$`; `git show --name-only --format='' HEAD` lists exactly `tasks/x.md` and `tasks/y.md`.
- **AC4** — write latency leaves the request path: recipe (c) (a slow `pre-receive` hook making every push ≥ 500 ms) issues 200 sequential POSTs and the p50 client duration is ≤ 250 ms; the `--write-coalesce=false` comparator is ≥ 500 ms. The slow hook is part of the recipe, not optional.
- **AC5** — every pull cycle starts clean: `git status --porcelain` returns 0 lines after a pull; the pull flushed a pending write (`grep -c 'git-rest: flushed pending writes'` ≥ 1) and produced no `rescue branch pushed` line.
- **AC9** — unchanged content still produces no commit and no push: three posts of `A` produce exactly one `git-rest: update tasks/x.md` commit and `grep -c 'write file: no changes to commit (content unchanged)'` returns `2`.
- **AC10** — the flush is observable: `git_rest_write_flushes_total{outcome="ok"}` ≥ 1; `git_rest_write_coalesced_total` ≥ 3 after a three-write burst; `git_rest_write_pending` is exposed.
- **AC13** — the flusher is a timer, not the pull boundary: with `--pull-interval=10m --write-coalesce-delay=5s` a single post flushes within 12 s with no pull possible; with `--pull-interval=10m --write-coalesce-delay=300s --write-coalesce-max-delay=20s` and distinct content every 2 s for 60 s, at least 3 commits land (only the 20 s ceiling can produce them).

Before finishing, re-run the verification block and confirm every command passes, then walk requirement sections 1–7 against the change. The ACs above are operator-executed on the host and are deliberately out of scope here — do not run them.
</requirements>

<constraints>
- The buffer is scoped to the file-write path only. `/api/v1/refs` PUT/DELETE under `refs/claims/*` must never enter it; a deferred claim push would break exactly-once claim scheduling. Do not change claim-ref code in this prompt.
- The unchanged-content short-circuit compares the new bytes to the file on disk, not the index to `HEAD`. The exact log message `write file: no changes to commit (content unchanged)` is kept.
- A failed push must never drop the batch. The pending set is cleared only after the push returns nil; on failure the index is restored so the paths stay staged and the next tick retries.
- The dirty-tree window is closed at every pull boundary: `Pull` flushes before resolving upstream or merging.
- The timer is not optional and is not the pull boundary. A pull-only flush is a failure.
- Flags follow the repo's `arg:` / `env:` convention. Disabling is the boolean `--write-coalesce`, never a zero duration (`--pull-timeout` documents `0 = no timeout`, so a zero sentinel would invert the intent).
- Preserve repo invariants: errors wrapped with `github.com/bborbe/errors` (never `fmt.Errorf`), no `context.Background()` in `pkg/`, `log/slog` for logging, metrics under `git_rest_` and pre-initialised in `init()`, interface → constructor → struct → method, factory functions are pure composition.
- Do NOT add any knob the spec did not ask for: no per-path configuration, no refresh interval, no extra metrics, no config file.
- Do NOT edit `docs/api.md`, `docs/deployment.md` or `CHANGELOG.md` — prompt 3 owns those.
- Do NOT implement SIGTERM flushing, boot recovery, or the delete flush-first ordering — prompt 2 owns those.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass. New code needs ≥ 80% statement coverage; changed code needs its changed paths tested.
- The container has no view of `origin/master` and no operator tooling: do not add `git`, `docker`, `kubectl`, `dark-factory` or `scripts/*` commands to the verification block.
</constraints>

<verification>
Run these from the repo root. Iterate with `make test`; run `make precommit` once at the end.

```
go generate ./...
go test -mod=mod ./pkg/git/... -run 'Coalesc|FlushBeforePull|FlushTimerFires|UnchangedWriteStagesNothing' -v
go test -mod=mod ./pkg/metrics/... -v
make test
make precommit
```

`go generate ./...` must regenerate `mocks/git.go` and `mocks/metrics.go` without error. `make precommit` must exit 0 (format, generate, test, lint, license).
</verification>
