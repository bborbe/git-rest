---
status: completed
spec: [010-merge-with-conflict-resolver]
summary: Added ConflictResolver interface, MarkerResolver implementation, ErrConflictResolutionFailed sentinel, two new Prometheus counters with Metrics interface methods, regenerated mocks, and integration tests — all with zero compile errors and make precommit exit code 0.
container: git-rest-042-spec-010-resolver-interface
dark-factory-version: v0.156.1-1-g04f3863-dirty
created: "2026-05-12T21:00:00Z"
queued: "2026-05-12T21:14:19Z"
started: "2026-05-12T21:14:21Z"
completed: "2026-05-12T21:20:33Z"
branch: dark-factory/merge-with-conflict-resolver
---

<summary>
- A new `ConflictResolver` interface in `pkg/git/` defines the minimal seam for pluggable conflict resolution (one method: `Resolve(ctx, paths)`)
- `MarkerResolver` implements the interface by staging each conflicted file as-is — git's `<<<<<<<` / `=======` / `>>>>>>>` markers are preserved in the committed file
- `ErrConflictResolutionFailed` sentinel lets callers use `errors.Is` to distinguish resolver failure from generic merge/push errors
- Two new Prometheus counters (`git_rest_merge_outcome_total{result}` and `git_rest_conflict_paths_total`) track merge outcomes and conflict frequency per pod lifetime
- The `Metrics` interface gains two new methods; `prometheusMetrics`, `noopMetrics` in tests, and the counterfeiter mock are all updated to satisfy the new interface
- A `FakeConflictResolver` counterfeiter mock is generated so prompt 2 can wire stub resolvers for AC3 (aborted-merge) tests
- `MarkerResolver` is unit-tested against a real fixture git repo: staged after `Resolve`, markers still in file, error path when path is absent (AC4)
- Existing tests continue to compile and pass — no changes to `Pull`, `pullRebaseAndPush`, or `git.New()` in this prompt
</summary>

<objective>
Establish the foundational types for spec 010: the `ConflictResolver` interface, `MarkerResolver` default implementation, `ErrConflictResolutionFailed` sentinel, and two new Prometheus counters with matching `Metrics` interface methods. Prompt 2 builds on these by wiring them into `pullMergeAndPush`. This prompt must produce zero compile errors and all tests green before prompt 2 begins.
</objective>

<context>
Read `CLAUDE.md` for project conventions.

Read these coding guides before implementing (in `~/.claude/plugins/marketplaces/coding/docs/`):
- `go-patterns.md` — interface→struct→constructor; counterfeiter annotations; private structs
- `go-error-wrapping-guide.md` — `errors.Wrapf` from `github.com/bborbe/errors`; sentinel errors with `stderrors` alias; never `fmt.Errorf`
- `go-prometheus-metrics-guide.md` — counter naming, label pre-initialisation in `init()`, interface-based metrics
- `go-testing-guide.md` — Ginkgo/Gomega, external test packages, fixture-based integration tests
- `test-pyramid-triggers.md` — which test types to write for each code change

Key existing files — read in full before implementing:

- `pkg/git/git.go` (~640 lines): note the existing sentinel errors (`ErrNotFound`, `ErrInvalidPath`, `ErrRepoUnrecoverable`), `stderrors` import alias, and the `git` struct + `New()` constructor. You will add `ErrConflictResolutionFailed` here. Do NOT change `New()`, `Pull()`, or `pullRebaseAndPush()`.
- `pkg/metrics/metrics.go` (~82 lines): note existing counters (`HTTPRequestsTotal`, `GitOperationDuration`, `GitOperationErrors`), `init()` pre-initialisation block, and the `Metrics` interface. You will add two new counters and two new interface methods.
- `pkg/git/git_test.go` (~1000 lines): note the `noopMetrics` struct at line ~35 which implements `metrics.Metrics` — it must be updated to add the two new stub methods. Do NOT change any existing tests.
- `CHANGELOG.md` — do NOT add an entry yet; prompt 2 handles the CHANGELOG for the full spec.
</context>

<requirements>

## 1. Create `pkg/git/conflict_resolver.go`

New file. Full content:

```go
// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git

import (
	"bytes"
	"context"
	"os/exec"
	"strings"

	"github.com/bborbe/errors"
)

// ConflictResolver is called by the puller when git merge produces unresolvable conflicts.
// Resolve receives the list of conflicted file paths as reported by git merge output.
// It must stage each path (git add) before returning nil so the puller can commit the merge.
// On error the puller runs git merge --abort and returns ErrConflictResolutionFailed.
//
//counterfeiter:generate -o ../../mocks/conflict_resolver.go --fake-name FakeConflictResolver . ConflictResolver
type ConflictResolver interface {
	Resolve(ctx context.Context, conflictedPaths []string) error
}

// NewMarkerResolver returns a ConflictResolver that preserves git's standard conflict markers.
// It stages each conflicted file as-is: the <<<<<<< / ======= / >>>>>>> markers written by
// git's three-way merge are left intact in the committed file. The next human or agent edit
// resolves them naturally.
func NewMarkerResolver(repoPath string) ConflictResolver {
	return &markerResolver{repoPath: repoPath}
}

type markerResolver struct {
	repoPath string
}

// Resolve stages each conflicted path without modifying its content. git add is sufficient
// because git's three-way merge already wrote the marker-annotated content to disk.
func (r *markerResolver) Resolve(ctx context.Context, conflictedPaths []string) error {
	for _, path := range conflictedPaths {
		// #nosec G204 -- git binary is hardcoded; paths come from git merge output, not user input
		cmd := exec.CommandContext(ctx, "git", "add", "--", path)
		cmd.Dir = r.repoPath
		var buf bytes.Buffer
		cmd.Stdout = &buf
		cmd.Stderr = &buf
		if err := cmd.Run(); err != nil {
			return errors.Wrapf(ctx, err, "git add %s: %s", path, strings.TrimSpace(buf.String()))
		}
	}
	return nil
}
```

## 2. Add `ErrConflictResolutionFailed` sentinel to `pkg/git/git.go`

After the `ErrRepoUnrecoverable` declaration (currently around line 42), add:

```go
// ErrConflictResolutionFailed is returned by Pull when the ConflictResolver returns an error.
// The merge is aborted before returning so the repo is left in a clean state.
// Callers detect this via errors.Is(err, ErrConflictResolutionFailed).
var ErrConflictResolutionFailed = stderrors.New("conflict resolution failed")
```

`stderrors` is already imported. Do NOT add a duplicate import.

## 3. Update `pkg/metrics/metrics.go`

### 3a. Add two new package-level counter declarations after `GitOperationErrors`:

```go
// MergeOutcomeTotal counts merge outcomes by result: clean, resolved, aborted.
var MergeOutcomeTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "git_rest_merge_outcome_total",
	Help: "Total merge outcomes by result type (clean=auto-merged, resolved=resolver succeeded, aborted=resolver failed).",
}, []string{"result"})

// ConflictPathsTotal counts total conflicted file paths passed to the resolver across pod lifetime.
var ConflictPathsTotal = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "git_rest_conflict_paths_total",
	Help: "Total count of conflicted file paths passed to the ConflictResolver across pod lifetime.",
})
```

### 3b. Update the `init()` function to register and pre-initialise the new metrics:

REPLACE the existing single `prometheus.MustRegister(...)` line (find by grepping for `prometheus.MustRegister(HTTPRequestsTotal` — there is exactly one occurrence in `pkg/metrics/metrics.go`):

```go
prometheus.MustRegister(HTTPRequestsTotal, GitOperationDuration, GitOperationErrors, MergeOutcomeTotal, ConflictPathsTotal)
```

Do NOT add a second `MustRegister` call — the existing one is the only registration site; the agent must edit it in place. Grep check after edit: `grep -c "MustRegister(" pkg/metrics/metrics.go` MUST return `1`.

After the existing pre-initialisation block (after the `HTTPRequestsTotal` loops), add:

```go
for _, result := range []string{"clean", "resolved", "aborted"} {
    MergeOutcomeTotal.WithLabelValues(result).Add(0)
}
```

### 3c. Add two new methods to the `Metrics` interface:

```go
// IncMergeOutcome records a merge outcome. result must be "clean", "resolved", or "aborted".
IncMergeOutcome(result string)
// IncConflictPaths records n conflicted paths passed to the resolver in one merge cycle.
IncConflictPaths(n int)
```

### 3d. Add implementations on `prometheusMetrics`:

```go
func (p *prometheusMetrics) IncMergeOutcome(result string) {
	MergeOutcomeTotal.WithLabelValues(result).Inc()
}

func (p *prometheusMetrics) IncConflictPaths(n int) {
	ConflictPathsTotal.Add(float64(n))
}
```

## 4. Update `noopMetrics` stub in `pkg/git/git_test.go`

The `noopMetrics` struct (defined around line 36) must implement the two new `Metrics` interface methods. Add after the existing `IncRebaseConflict` stub:

```go
func (n *noopMetrics) IncMergeOutcome(_ string) {}

func (n *noopMetrics) IncConflictPaths(_ int) {}
```

Do NOT change any other part of `git_test.go`.

## 5. Regenerate mocks

Run:
```bash
cd /workspace && make generate
```

This regenerates all counterfeiter mocks. It will:
- Update `mocks/metrics.go` to include `IncMergeOutcome` and `IncConflictPaths`
- Create `mocks/conflict_resolver.go` with `FakeConflictResolver`

Verify the new files exist:
```bash
ls /workspace/mocks/conflict_resolver.go /workspace/mocks/metrics.go
```

## 6. Create `pkg/git/conflict_resolver_test.go` (AC4 tests)

New file. These are integration tests because `MarkerResolver.Resolve` invokes a real `git` subprocess.

```go
// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/git-rest/pkg/git"
)

// setupConflictFixture creates a pair of repos with a genuine same-line conflict.
// Returns workDir (has .git/MERGE_HEAD after setup), and a cleanup func.
// The conflict is on "shared.txt": remote wrote "remote\n", local wrote "local\n".
func setupConflictFixture() (workDir string, cleanup func()) {
	workDir, externalPush, c := setupPullFixture()

	// External writer commits "remote\n" to shared.txt and pushes.
	externalPush("shared.txt", "remote\n")

	// Local writer commits "local\n" to the same file (creates divergence + conflict).
	writeLocalCommit(workDir, "shared.txt", "local\n")

	// Fetch remote so FETCH_HEAD / tracking ref is up to date.
	runGit(workDir, "fetch")

	// Derive upstream tracking ref without hardcoding branch name.
	upstream := strings.TrimSpace(gitOutputStr(workDir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"))

	// Run merge — expected to fail with a conflict on shared.txt.
	cmd := exec.Command("git", "merge", "--no-edit", upstream)
	cmd.Dir = workDir
	_ = cmd.Run() // non-zero exit expected; working tree now has conflicted shared.txt

	_, statErr := os.Stat(filepath.Join(workDir, ".git", "MERGE_HEAD"))
	if statErr != nil {
		panic("setupConflictFixture: expected .git/MERGE_HEAD to exist after failed merge")
	}

	return workDir, c
}

var _ = Describe("MarkerResolver", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	Describe("Resolve", func() {
		Context("AC4 happy path: conflicted file staged after Resolve, markers preserved", func() {
			It("stages the conflicted file and leaves content with conflict markers", func() {
				workDir, cleanup := setupConflictFixture()
				defer cleanup()

				resolver := git.NewMarkerResolver(workDir)
				err := resolver.Resolve(ctx, []string{"shared.txt"})
				Expect(err).NotTo(HaveOccurred())

				// File must no longer appear as unmerged.
				unmerged := strings.TrimSpace(gitOutputStr(workDir, "diff", "--name-only", "--diff-filter=U"))
				Expect(unmerged).To(BeEmpty(), "shared.txt should not be unmerged after Resolve")

				// Content must still contain conflict markers (not cleaned up by resolver).
				content, readErr := os.ReadFile(filepath.Join(workDir, "shared.txt"))
				Expect(readErr).NotTo(HaveOccurred())
				Expect(string(content)).To(ContainSubstring("<<<<<<<"), "conflict markers must be preserved")
				Expect(string(content)).To(ContainSubstring("======="), "conflict markers must be preserved")
				Expect(string(content)).To(ContainSubstring(">>>>>>>"), "conflict markers must be preserved")

				// Both versions must be in the file.
				Expect(string(content)).To(ContainSubstring("remote"), "remote version must be preserved")
				Expect(string(content)).To(ContainSubstring("local"), "local version must be preserved")
			})
		})

		Context("AC4 error path: non-existent path returns error", func() {
			It("returns a wrapped error when the path does not exist", func() {
				workDir, cleanup := setupConflictFixture()
				defer cleanup()

				resolver := git.NewMarkerResolver(workDir)
				err := resolver.Resolve(ctx, []string{"does-not-exist.txt"})
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("does-not-exist.txt"))
			})
		})
	})
})
```

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Do NOT change `New()`, `Pull()`, `pullRebaseAndPush()`, or any existing test assertions — prompt 2 handles those
- `context.Background()` MUST NOT appear in `pkg/` — only in `main.go` and test files
- Errors MUST be wrapped with `errors.Wrapf` from `github.com/bborbe/errors` — never `fmt.Errorf`, never bare `return err`
- `ErrConflictResolutionFailed` MUST use `stderrors.New(...)` (not `errors.New`) so it is a comparable sentinel — the `stderrors` alias is already imported in `pkg/git/git.go`
- The `Metrics` interface MUST declare the two new methods (`IncMergeOutcome`, `IncConflictPaths`) BEFORE the implementations so `make generate` picks up the correct interface
- `prometheus.MustRegister` MUST include both `MergeOutcomeTotal` and `ConflictPathsTotal` in the same call (or a second call) — do NOT leave them unregistered
- `MergeOutcomeTotal` MUST be pre-initialised for all three result labels ("clean", "resolved", "aborted") in `init()` — pre-init prevents gaps in first-scrape responses
- `ConflictPathsTotal` is a plain `Counter` (no labels) — do NOT make it a `CounterVec`
- The counterfeiter annotation `//counterfeiter:generate ... . ConflictResolver` MUST appear on the `ConflictResolver` interface directly, NOT on a separate line in another file
- `noopMetrics` stubs MUST use blank-identifier params (`_ string`, `_ int`) — the struct methods produce no output and have no logic
- The `conflict_resolver_test.go` file MUST be in `package git_test` (external) and MUST reuse `setupPullFixture`, `writeLocalCommit`, `runGit`, and `gitOutputStr` from `git_test.go` — do NOT redefine them
- Existing tests must still pass
</constraints>

<verification>
Run tests (iterative — repeat after each meaningful change):
```bash
cd /workspace && make test
```

Verify new sentinel is declared:
```bash
grep -n "ErrConflictResolutionFailed" /workspace/pkg/git/git.go
```
Expected: one `var` declaration.

Verify new counter names are registered:
```bash
grep -n "git_rest_merge_outcome_total\|git_rest_conflict_paths_total" /workspace/pkg/metrics/metrics.go
```
Expected: two matches (one per counter declaration).

Verify `FakeConflictResolver` mock was generated:
```bash
grep -n "FakeConflictResolver" /workspace/mocks/conflict_resolver.go | head -5
```
Expected: struct definition present.

Verify `FakeMetrics` mock has new methods:
```bash
grep -n "IncMergeOutcome\|IncConflictPaths" /workspace/mocks/metrics.go
```
Expected: both method names present.

Verify `noopMetrics` compiles with new methods:
```bash
grep -n "IncMergeOutcome\|IncConflictPaths" /workspace/pkg/git/git_test.go
```
Expected: two stub definitions present.

Spot-check MarkerResolver tests:
```bash
cd /workspace && go test ./pkg/git/... -v -run "MarkerResolver"
```
Expected: both It blocks pass.

Final validation:
```bash
cd /workspace && make precommit
```
Must pass with exit code 0.
</verification>
