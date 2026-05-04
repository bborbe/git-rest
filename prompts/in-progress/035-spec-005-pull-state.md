---
status: committing
spec: [005-bug-readiness-blocks-on-pull-mutex]
summary: Introduced PullStateCache in pkg/puller/pull_state.go, updated puller.New to 4-arg signature with pullTimeout and PullStateWriter, added --pull-timeout flag to main.go, created PullStateCache in Run() with 3×PullInterval threshold, generated FakePullStateWriter mock, and added comprehensive Ginkgo tests for both PullStateCache and the updated puller.
container: git-rest-035-spec-005-pull-state
dark-factory-version: v0.147.2-1-g30ba42f
created: "2026-05-04T09:15:00Z"
queued: "2026-05-04T13:48:59Z"
started: "2026-05-04T13:49:25Z"
branch: dark-factory/bug-readiness-blocks-on-pull-mutex
---

<summary>
- Bound each background pull to a configurable timeout (new flag, default 60s) so a stalled SSH connection cannot hold the git mutex indefinitely
- Record every pull's outcome (success or failure) in a small in-memory cache that the readiness handler will consume in the next prompt
- The cache reports "no successful pull yet" until the first successful pull; after a long gap it reports a "stale" reason that names the underlying error
- Time source is injected (project-wide pattern) so tests advance the clock deterministically — no wall-clock sleeps
- Existing tests continue to pass; no behavior visible at the HTTP layer changes in this prompt (that's prompt 2)
</summary>

<objective>
Introduce `PullStateCache` in `pkg/puller/pull_state.go` and update the puller to record pull outcomes and apply a per-pull timeout. Add the `--pull-timeout` flag to `main.go`. This is the infrastructure half of the readiness decoupling fix (spec 005); the readiness handler is updated in the next prompt.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Read coding guides before implementing (in `~/.claude/plugins/marketplaces/coding/docs/`):
- `go-patterns.md` — public interface + private struct + New* constructor; counterfeiter annotations
- `go-concurrency-patterns.md` — safe concurrent access patterns
- `go-testing-guide.md` — Ginkgo/Gomega suite files, counterfeiter mocks, external test packages
- `go-error-wrapping-guide.md` — never `fmt.Errorf`, never bare `return err`

Files to read in full before implementing:
- `pkg/puller/puller.go` — current `Puller` interface, `New` constructor, and `Run` loop; you will update these
- `pkg/puller/puller_test.go` — existing test suite; must stay green; update `New` calls for the new signature
- `pkg/puller/puller_suite_test.go` — Ginkgo suite boilerplate for the package; your new test file joins this suite
- `main.go` — focus on: `application` struct (~line 38), `Run()` method (~line 55), `createGitRefresher` method (~line 375); you will update these three locations
- `mocks/git.go` — FakeGit mock; pattern for how counterfeiter mocks are used in tests
</context>

<requirements>

## 1. Create `pkg/puller/pull_state.go`

The cache injects `libtime.CurrentDateTimeGetter` (the project-wide time-source pattern — see `pkg/git/git.go:65,80,163` and `go-time-injection.md`) so tests can advance the clock deterministically via `SetNow`. Never call `time.Now()` directly in `pkg/`.

Create a new file with the following exact content:

```go
// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package puller

import (
	"fmt"
	"sync"
	"time"

	libtime "github.com/bborbe/time"
)

//counterfeiter:generate -o ../../mocks/pull_state_writer.go --fake-name FakePullStateWriter . PullStateWriter

// PullStateWriter is the write side of the pull outcome cache.
// The puller calls RecordPull after every pull attempt.
type PullStateWriter interface {
	RecordPull(err error)
}

// NewPullStateCache returns a PullStateCache with the given freshness threshold.
// Threshold is typically 3 × PullInterval.
// A zero threshold disables freshness checking (always fresh after first success).
// currentDateTime is the time source — pass libtime.NewCurrentDateTime() in production.
func NewPullStateCache(currentDateTime libtime.CurrentDateTimeGetter, freshnessThreshold time.Duration) *PullStateCache {
	return &PullStateCache{
		currentDateTime:    currentDateTime,
		freshnessThreshold: freshnessThreshold,
	}
}

// PullStateCache stores the outcome of the most recent pull attempts.
// It implements both PullStateWriter (written by the puller) and
// ReadinessStatus (read by the readiness handler — see pkg/handler).
// Safe for concurrent use.
type PullStateCache struct {
	mu                 sync.RWMutex
	currentDateTime    libtime.CurrentDateTimeGetter
	lastSuccessAt      libtime.DateTime // zero = no successful pull yet
	lastErr            error            // nil = last pull succeeded
	freshnessThreshold time.Duration
}

// RecordPull records the outcome of one pull attempt at the current time.
// If err is nil the success timestamp is updated; otherwise only the error is stored.
func (c *PullStateCache) RecordPull(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil {
		c.lastSuccessAt = c.currentDateTime.Now()
	}
	c.lastErr = err
}

// ReadinessStatus returns (true, "ok") when the cache is healthy, or
// (false, reason) when the pod should not receive traffic.
//
// Rules (checked in order):
//  1. lastSuccessAt is zero → "no successful pull yet"
//  2. time since lastSuccessAt > freshnessThreshold → stale; include last error if any
//  3. Otherwise → ready
func (c *PullStateCache) ReadinessStatus() (bool, string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if time.Time(c.lastSuccessAt).IsZero() {
		return false, "no successful pull yet"
	}
	age := time.Time(c.currentDateTime.Now()).Sub(time.Time(c.lastSuccessAt))
	if c.freshnessThreshold > 0 && age > c.freshnessThreshold {
		msg := fmt.Sprintf("last successful pull stale (%v ago)", age.Round(time.Second))
		if c.lastErr != nil {
			msg += ": last pull failed: " + c.lastErr.Error()
		}
		return false, msg
	}
	return true, "ok"
}
```

External imports: stdlib (`fmt`, `sync`, `time`) plus `libtime "github.com/bborbe/time"` (already used elsewhere in the project — see `main.go:23`).

## 2. Generate the counterfeiter mock

The project uses `//counterfeiter:generate` annotations on each interface (already added in §1, line 60) plus a single `//go:generate` directive in `pkg/puller/puller_suite_test.go:16` driven by `make generate` (Makefile target).

Run mock generation with:

```bash
cd /workspace && make generate
```

This regenerates all counterfeiter mocks including `mocks/pull_state_writer.go` (FakePullStateWriter). Do NOT add a separate `//go:generate` directive to `pull_state.go` — the suite-level directive already covers the package.

Verify the mock file was generated:

```bash
ls /workspace/mocks/pull_state_writer.go
```

## 3. Update `pkg/puller/puller.go`

Change the `New` constructor to accept two additional parameters: `pullTimeout time.Duration` and `state PullStateWriter`. Update `Run` to:
1. Wrap each `g.Pull(ctx)` call in a `context.WithTimeout` derived from the loop's `ctx`. If `pullTimeout` is zero, skip the timeout wrapper.
2. Record the pull outcome into `state` immediately after every pull attempt (success or failure). The cache reads its own clock — the puller does not pass a timestamp.

Full updated `puller.go`:

```go
// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package puller

import (
	"context"
	"log/slog"
	"time"

	libtime "github.com/bborbe/time"

	"github.com/bborbe/git-rest/pkg/git"
)

//counterfeiter:generate -o ../../mocks/puller.go --fake-name FakePuller . Puller

// Puller periodically runs git pull on a repository.
type Puller interface {
	Run(ctx context.Context) error
}

// New returns a Puller that calls g.Pull on the given interval.
// pullTimeout bounds each individual pull subprocess; 0 means no timeout.
// state receives the outcome of every pull attempt.
func New(g git.Git, interval libtime.Duration, pullTimeout time.Duration, state PullStateWriter) Puller {
	return &puller{
		git:         g,
		interval:    interval,
		pullTimeout: pullTimeout,
		state:       state,
	}
}

type puller struct {
	git         git.Git
	interval    libtime.Duration
	pullTimeout time.Duration
	state       PullStateWriter
}

// Run starts the periodic pull loop. It returns when ctx is cancelled.
func (p *puller) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Duration(p.interval))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			p.runOnce(ctx)
		}
	}
}

// runOnce executes one pull attempt, bounded by pullTimeout, and records the outcome.
func (p *puller) runOnce(ctx context.Context) {
	pullCtx := ctx
	var cancel context.CancelFunc
	if p.pullTimeout > 0 {
		pullCtx, cancel = context.WithTimeout(ctx, p.pullTimeout)
		defer cancel()
	}

	err := p.git.Pull(pullCtx)
	p.state.RecordPull(err)
	if err != nil {
		slog.WarnContext(ctx, "git pull failed", "error", err)
	}
}
```

## 4. Add `PullTimeout` flag to `main.go` `application` struct

In `main.go`, add after the `PullInterval` field:

```go
PullTimeout     libtime.Duration  `required:"false" arg:"pull-timeout"      env:"PULL_TIMEOUT"      usage:"per-pull timeout; subprocess is aborted if it exceeds this duration (0 = no timeout)"    default:"60s"`
```

## 5. Update `createGitRefresher` in `main.go`

Change the method signature and body to accept and pass through the pull state:

```go
func (a *application) createGitRefresher(gitClient git.Git, state puller.PullStateWriter) run.Func {
	return func(ctx context.Context) error {
		return puller.New(gitClient, a.PullInterval, time.Duration(a.PullTimeout), state).Run(ctx)
	}
}
```

Add `"time"` to the imports in `main.go` if not already present (it already is).

## 6. Create `PullStateCache` in `Run()` and pass it to `createGitRefresher`

In `main.go`'s `Run()` method, create the cache before calling `service.Run`. The freshness threshold is `3 × PullInterval`:

```go
func (a *application) Run(ctx context.Context, sentryClient libsentry.Client) error {
	metrics.NewBuildInfoMetrics(a.BuildGitVersion, a.BuildGitCommit).SetBuildInfo(a.BuildDate)

	if err := a.bootstrap(ctx); err != nil {
		return errors.Wrap(ctx, err, "bootstrap failed")
	}

	if a.GatewaySecret == "" {
		slog.WarnContext(ctx, "gateway-secret not set", "reason", "git-rest API is unauthenticated")
	}

	gitClient, err := a.createGitClient(ctx)
	if err != nil {
		return errors.Wrap(ctx, err, "create git client failed")
	}

	pullState := puller.NewPullStateCache(libtime.NewCurrentDateTime(), 3*time.Duration(a.PullInterval))

	return service.Run(ctx,
		a.createGitRefresher(gitClient, pullState),
		a.createHTTPServer(gitClient, metrics.NewMetrics()),
	)
}
```

Note: `createHTTPServer` still receives `gitClient` (unchanged) — the readiness handler is updated in the next prompt (spec 005 prompt 2). The `pullState` is created here so it exists; prompt 2 will thread it through `createHTTPServer`.

Add import `"github.com/bborbe/git-rest/pkg/puller"` to `main.go` if not already present. It is already present (used by `createGitRefresher`).

## 7. Update `pkg/puller/puller_test.go`

The existing test calls `puller.New(fakeGit, libtime.Duration(10*time.Millisecond))` — this must be updated to the new four-arg signature. Use a `FakePullStateWriter` (generated in step 2) or a minimal inline stub. Prefer `FakePullStateWriter` if it was generated.

Update every `puller.New(...)` call in the test file to:
```go
puller.New(fakeGit, libtime.Duration(10*time.Millisecond), 0, fakePullState)
```

Where `fakePullState` is a `*mocks.FakePullStateWriter` created in `BeforeEach`.

Also add new test cases in a new `Describe("runOnce", ...)` block (or extend the existing `Describe("Run", ...)`):

1. **Records success**: when `fakeGit.Pull` returns nil, `fakePullState.RecordPullCallCount()` is 1 and `fakePullState.RecordPullArgsForCall(0)` returns `nil`.
2. **Records failure**: when `fakeGit.Pull` returns an error, `fakePullState.RecordPullCallCount()` is 1 and `fakePullState.RecordPullArgsForCall(0)` returns the original error.
3. **Timeout aborts pull**: set `pullTimeout = 10*time.Millisecond`; make `fakeGit.PullStub` block on the context (`<-ctx.Done(); return ctx.Err()`); verify the pull returns within 50ms and the recorded error is non-nil (context.DeadlineExceeded).
4. **Boundary integration**: instead of `FakePullStateWriter`, pass a real `puller.NewPullStateCache(libtime.NewCurrentDateTime(), time.Hour)` and verify that after one successful pull through `runOnce`, `cache.ReadinessStatus()` returns `(true, "ok")`. This guards the wiring between `RecordPull` and `ReadinessStatus`.

## 8. Create `pkg/puller/pull_state_test.go`

New test file for `PullStateCache` in `package puller_test`. Uses `libtime.NewCurrentDateTime()` with `SetNow` to advance the clock deterministically — no wall-clock sleeps, no flakes on slow CI:

```go
// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package puller_test

import (
	"errors"
	"time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/git-rest/pkg/puller"
)

var _ = Describe("PullStateCache", func() {
	const freshness = 90 * time.Second
	var (
		clock libtime.CurrentDateTime
		t0    libtime.DateTime
	)

	BeforeEach(func() {
		clock = libtime.NewCurrentDateTime()
		t0 = libtime.DateTime(time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC))
		clock.SetNow(t0)
	})

	Describe("cold start (no pull yet)", func() {
		It("returns not-ready with 'no successful pull yet'", func() {
			cache := puller.NewPullStateCache(clock, freshness)
			ready, reason := cache.ReadinessStatus()
			Expect(ready).To(BeFalse())
			Expect(reason).To(Equal("no successful pull yet"))
		})
	})

	Describe("after a successful pull", func() {
		It("returns ready", func() {
			cache := puller.NewPullStateCache(clock, freshness)
			cache.RecordPull(nil)
			ready, reason := cache.ReadinessStatus()
			Expect(ready).To(BeTrue())
			Expect(reason).To(Equal("ok"))
		})
	})

	Describe("after a failed pull (first ever)", func() {
		It("returns not-ready with 'no successful pull yet'", func() {
			cache := puller.NewPullStateCache(clock, freshness)
			cache.RecordPull(errors.New("ssh timeout"))
			ready, reason := cache.ReadinessStatus()
			Expect(ready).To(BeFalse())
			Expect(reason).To(Equal("no successful pull yet"))
		})
	})

	Describe("stale cache (last success beyond freshness threshold)", func() {
		It("returns not-ready with stale message", func() {
			cache := puller.NewPullStateCache(clock, freshness)
			cache.RecordPull(nil)
			// advance clock past freshness window
			clock.SetNow(libtime.DateTime(time.Time(t0).Add(2 * freshness)))
			ready, reason := cache.ReadinessStatus()
			Expect(ready).To(BeFalse())
			Expect(reason).To(ContainSubstring("last successful pull stale"))
			Expect(reason).NotTo(ContainSubstring("context canceled"))
		})

		It("includes last pull error in stale message when a later pull failed", func() {
			cache := puller.NewPullStateCache(clock, freshness)
			cache.RecordPull(nil)
			clock.SetNow(libtime.DateTime(time.Time(t0).Add(2 * freshness)))
			cache.RecordPull(errors.New("ssh: connect to host github.com port 22: Operation timed out"))
			ready, reason := cache.ReadinessStatus()
			Expect(ready).To(BeFalse())
			Expect(reason).To(ContainSubstring("last pull failed"))
			Expect(reason).To(ContainSubstring("ssh"))
			Expect(reason).NotTo(ContainSubstring("context canceled"))
		})
	})

	Describe("zero freshness threshold", func() {
		It("never expires after first success", func() {
			cache := puller.NewPullStateCache(clock, 0)
			cache.RecordPull(nil)
			clock.SetNow(libtime.DateTime(time.Time(t0).Add(24 * time.Hour)))
			ready, _ := cache.ReadinessStatus()
			Expect(ready).To(BeTrue())
		})
	})
})
```

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass — update `puller_test.go` `New` calls to match the new four-arg signature
- Use `errors.Wrap`/`errors.Wrapf` from `github.com/bborbe/errors` in Go `pkg/` code — never `fmt.Errorf`, never bare `return err`
- `context.Background()` must NOT appear in `pkg/` — only in `main.go` and test files
- No new external dependencies — only stdlib for `pull_state.go`
- The `PullStateCache` uses `sync.RWMutex` for reads (`RLock`) and writes (`Lock`) — reads are concurrent-safe
- `RecordPull` updates `lastSuccessAt` ONLY on `err == nil` — a failed pull never resets the success timestamp
- The per-pull timeout context is derived from the loop's `ctx` (not `context.Background()`) so cancelling the puller also cancels any in-flight pull
- `createHTTPServer` signature in `main.go` is NOT changed in this prompt — only `createGitRefresher` changes. The readiness handler still receives `gitClient` until prompt 2.
- The `FakePullStateWriter` mock must be in `mocks/` (same directory as `FakeGit`, `FakePuller`)
- Do NOT change `pkg/handler/readiness.go` or `pkg/factory/factory.go` in this prompt — those are updated in prompt 2
- The `pullState` variable created in `Run()` must be `*puller.PullStateCache` (concrete type) so both prompt 2 and the compiler can pass it as `handler.ReadinessStateReader` once that interface is defined
</constraints>

<verification>
```bash
cd /workspace && make test
```
Must pass — all existing tests green, new pull state and puller tests green.

Spot-check new tests:
```bash
cd /workspace && go test ./pkg/puller/... -v -run "PullStateCache|runOnce|Timeout"
```

Verify the new flag appears in help output:
```bash
cd /workspace && go run main.go --help 2>&1 | grep pull-timeout
```
Expected: line containing `--pull-timeout` and `PULL_TIMEOUT`.

Verify `make precommit` passes at end:
```bash
cd /workspace && make precommit
```
</verification>
