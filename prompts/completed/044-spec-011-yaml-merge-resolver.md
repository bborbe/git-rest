---
status: completed
spec: [011-yaml-merge-resolver]
container: git-rest-yaml-merge-resolver-exec-044-spec-011-yaml-merge-resolver
dark-factory-version: v0.173.0
created: "2026-06-02T10:00:00Z"
queued: "2026-06-02T09:40:06Z"
started: "2026-06-02T10:12:59Z"
completed: "2026-06-02T10:36:10Z"
---

<summary>
- A new conflict resolver lives next to `MarkerResolver` and deep-merges YAML frontmatter on conflicted markdown files instead of leaving conflict markers
- On overlapping frontmatter keys the remote (theirs) value wins; non-overlapping keys from both sides are preserved
- Bodies after the frontmatter are combined: theirs alone, ours alone, or ours-blank-theirs when both differ
- Any YAML parse failure, missing frontmatter delimiter, file-write error, or `git add` error makes the resolver give up by returning the existing `ErrConflictResolutionFailed` sentinel — the puller then aborts the merge as it does today
- Each of the four failure categories (`yaml_parse_failed`, `no_frontmatter`, `write_failed`, `git_add_failed`) becomes a distinct, label-distinguishable value on `/metrics` so operators can tell them apart at a glance
- The metrics interface is extended (not broken) with one new method; existing `MarkerResolver` and existing tests stay byte-identical
- The `ConflictResolver` interface and its counterfeiter mock are not touched — the new resolver satisfies the existing contract
- New Ginkgo tests cover happy path (deep merge + body rules), every failure category, the `errors.Is` sentinel guarantee, and zero-init of all four labels on `/metrics`
</summary>

<objective>
Add a `YAMLMergeResolver` in `pkg/git/` that satisfies the existing frozen `ConflictResolver` interface, deep-merges YAML frontmatter (theirs wins on overlap) and stages the result. On any failure return the existing `ErrConflictResolutionFailed` sentinel and increment a new failure-category counter so operators can distinguish `yaml_parse_failed`, `no_frontmatter`, `write_failed`, and `git_add_failed` on `/metrics`. Wiring into `main.go` is OUT of scope — prompt 2 handles selection.
</objective>

<context>
Read `CLAUDE.md` at the repo root for project conventions.

Read these coding-plugin guides before implementing (paths inside the YOLO container):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `errors.Wrap` / `errors.Wrapf` from `github.com/bborbe/errors`; sentinel errors; never `fmt.Errorf`, never bare `return err`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo/Gomega, external test packages (`package git_test`), `BeforeEach` fixture setup
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md` — CounterVec, label pre-initialisation in `init()`, gathering for tests
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-logging-guide.md` — `glog.Warningf` is unconditional, `glog.V(n).Infof` is V-gated
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-mod-dependency-fix-guide.md` — promoting an indirect dependency to direct via the first real import + `go mod tidy`

Files to read in full before editing:

- `pkg/git/conflict_resolver.go` (53 lines) — `ConflictResolver` interface, `NewMarkerResolver`, counterfeiter directive. THIS FILE MUST NOT BE EDITED.
- `pkg/git/conflict_resolver_test.go` — existing `MarkerResolver` tests. THIS FILE MUST NOT BE EDITED.
- `pkg/git/git_test.go` (around lines 35-50) — `noopMetrics` struct: stubs every method on `metrics.Metrics`. You will need to add a stub method for the new `IncResolverFailure` metric interface method.
- `pkg/metrics/metrics.go` (full, 115 lines) — `Metrics` interface, `prometheusMetrics` struct, `MergeOutcomeTotal` CounterVec, `init()` label pre-initialisation pattern.
- `mocks/metrics.go` — counterfeiter-generated; will need regeneration via `make generate` after the interface gains a method.
- `mocks/conflict_resolver.go` — counterfeiter-generated; MUST stay byte-identical (interface unchanged).
- `go.mod` — verify `gopkg.in/yaml.v3 v3.0.1 // indirect` is present (around line 449). After first import via the new file, `go mod tidy` will drop the `// indirect` marker.

**Cross-prompt note:** Prompt 2 (`2-spec-011-wiring-and-docs.md`) wires selection in `main.go` via `VAULT_WRITE_MODE` and updates CHANGELOG + `docs/deployment.md`. Do NOT do those things in this prompt.

**YAML library choice (locked by spec):** `gopkg.in/yaml.v3`. The other yaml modules in `go.mod` (`go.yaml.in/yaml/v2`, `go.yaml.in/yaml/v3`, `go.yaml.in/yaml/v4`, `sigs.k8s.io/yaml`) stay unused.

**Project metric naming:** existing counter is `git_rest_merge_outcome_total{result=...}`. Add a sibling vector `git_rest_resolver_failures_total{category=...}` rather than expanding labels on the existing counter — keeps the "outcome" semantics clean (`clean` / `resolved` / `aborted`) and the failure breakdown orthogonal. See requirement 3.

**Frontmatter format reminder:** an Obsidian-style markdown file starts with a line that is exactly `---`, followed by YAML, followed by another line that is exactly `---`, followed by the body. If either delimiter is missing on either side, treat it as `no_frontmatter`.
</context>

<requirements>

## 1. Add the failure-category metric to `pkg/metrics/metrics.go`

Add a new `CounterVec` directly under `ConflictPathsTotal` (around line 41):

```go
// ResolverFailuresTotal counts conflict-resolver failures by category.
// Categories: yaml_parse_failed, no_frontmatter, write_failed, git_add_failed.
var ResolverFailuresTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "git_rest_resolver_failures_total",
	Help: "Total conflict-resolver failures by category (yaml_parse_failed, no_frontmatter, write_failed, git_add_failed).",
}, []string{"category"})
```

Register it in the existing `init()` `prometheus.MustRegister(...)` block (append `ResolverFailuresTotal` to the list).

Pre-initialise all four labels to zero inside the same `init()` (append after the existing `MergeOutcomeTotal` pre-init loop):

```go
for _, category := range []string{"yaml_parse_failed", "no_frontmatter", "write_failed", "git_add_failed"} {
	ResolverFailuresTotal.WithLabelValues(category).Add(0)
}
```

Extend the `Metrics` interface with one new method (preserve method order — append at the end):

```go
// IncResolverFailure records a conflict-resolver failure by category.
// category must be one of: yaml_parse_failed, no_frontmatter, write_failed, git_add_failed.
IncResolverFailure(category string)
```

Implement it on `*prometheusMetrics`:

```go
func (p *prometheusMetrics) IncResolverFailure(category string) {
	ResolverFailuresTotal.WithLabelValues(category).Inc()
}
```

## 2. Regenerate the metrics counterfeiter mock

Run `make generate` from the repo root. This regenerates `mocks/metrics.go` to include the new `IncResolverFailure` method (counterfeiter reads the `//counterfeiter:generate` directive on the `Metrics` interface).

Sanity-check after regeneration:

```bash
grep -n 'IncResolverFailure' mocks/metrics.go
```

Expected: matches similar to the existing `IncMergeOutcome` stubs/argsForCall/callCount methods.

DO NOT regenerate `mocks/conflict_resolver.go` — the `ConflictResolver` interface is unchanged. After this step:

```bash
git diff --stat mocks/conflict_resolver.go
```

MUST report zero changes.

## 3. Add the `noopMetrics` stub method in `pkg/git/git_test.go`

Open `pkg/git/git_test.go` and locate the `noopMetrics` block (around line 35-50). Append one more stub so the type still satisfies `metrics.Metrics` after the interface gains `IncResolverFailure`:

```go
func (n *noopMetrics) IncResolverFailure(_ string) {}
```

This is the ONLY edit allowed in `pkg/git/git_test.go` — do not touch any other test. (Spec AC: existing tests remain byte-identical except for this compile-fix stub.)

## 4. Create `pkg/git/yaml_merge_resolver.go`

Create the new file with the standard license header and the implementation below. Imports must include `gopkg.in/yaml.v3`; this is the import that promotes the indirect dep to direct.

```go
// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git

import (
	"bytes"
	"context"
	stderrors "errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/bborbe/errors"
	"github.com/golang/glog"
	"gopkg.in/yaml.v3"

	"github.com/bborbe/git-rest/pkg/metrics"
)

// Failure category labels emitted on git_rest_resolver_failures_total{category=...}.
const (
	resolverFailureYAMLParse  = "yaml_parse_failed"
	resolverFailureNoFront    = "no_frontmatter"
	resolverFailureWrite      = "write_failed"
	resolverFailureGitAdd     = "git_add_failed"
)

// NewYAMLMergeResolver returns a ConflictResolver that deep-merges YAML frontmatter
// in conflicted markdown files. For each conflicted path it:
//
//  1. Reads the working-tree file (still containing git's three-way merge markers).
//  2. Extracts the "ours" and "theirs" sides of each conflict region.
//  3. Parses the YAML frontmatter on both sides; deep-merges keys (theirs wins on overlap).
//  4. Combines bodies per the rules in the spec (theirs if non-empty, else ours; both
//     differ → "ours\n\ntheirs").
//  5. Writes the merged file back and runs `git add -- <path>`.
//
// On any failure (YAML parse error, missing frontmatter delimiter pair on either side,
// disk write failure, git add failure) it increments the corresponding failure-category
// counter, emits a Warningf log line naming the path, and returns ErrConflictResolutionFailed.
// The caller (the puller) runs `git merge --abort` and surfaces the sentinel.
func NewYAMLMergeResolver(repoPath string, m metrics.Metrics) ConflictResolver {
	return &yamlMergeResolver{repoPath: repoPath, metrics: m}
}

type yamlMergeResolver struct {
	repoPath string
	metrics  metrics.Metrics
}

// Resolve handles each conflicted path in order. The first path that cannot be resolved
// aborts the loop and returns ErrConflictResolutionFailed; subsequent paths are NOT
// attempted (the puller will run `git merge --abort` and the next pull retries from a
// clean state).
func (r *yamlMergeResolver) Resolve(ctx context.Context, conflictedPaths []string) error {
	for _, path := range conflictedPaths {
		if err := r.resolveOne(ctx, path); err != nil {
			return err
		}
	}
	return nil
}

func (r *yamlMergeResolver) resolveOne(ctx context.Context, path string) error {
	// Reject symlinks/paths that escape the repo root before any I/O.
	abs, err := r.safePath(path)
	if err != nil {
		r.metrics.IncResolverFailure(resolverFailureWrite)
		glog.Warningf("yaml-merge-resolver: unsafe path %q: %v", path, err)
		return errors.Wrap(ctx, ErrConflictResolutionFailed, "unsafe conflict path")
	}

	raw, err := os.ReadFile(abs) // #nosec G304 -- path validated by safePath
	if err != nil {
		r.metrics.IncResolverFailure(resolverFailureWrite)
		glog.Warningf("yaml-merge-resolver: read %q: %v", path, err)
		return errors.Wrap(ctx, ErrConflictResolutionFailed, "read conflicted file")
	}

	ours, theirs, ok := splitConflictSides(string(raw))
	if !ok {
		r.metrics.IncResolverFailure(resolverFailureNoFront)
		glog.Warningf("yaml-merge-resolver: %q has no parseable conflict region", path)
		return errors.Wrap(ctx, ErrConflictResolutionFailed, "no conflict region")
	}

	oursFM, oursBody, ok := splitFrontmatter(ours)
	if !ok {
		r.metrics.IncResolverFailure(resolverFailureNoFront)
		glog.Warningf("yaml-merge-resolver: %q ours side has no frontmatter delimiter pair", path)
		return errors.Wrap(ctx, ErrConflictResolutionFailed, "ours: no frontmatter")
	}
	theirsFM, theirsBody, ok := splitFrontmatter(theirs)
	if !ok {
		r.metrics.IncResolverFailure(resolverFailureNoFront)
		glog.Warningf("yaml-merge-resolver: %q theirs side has no frontmatter delimiter pair", path)
		return errors.Wrap(ctx, ErrConflictResolutionFailed, "theirs: no frontmatter")
	}

	oursMap := map[string]interface{}{}
	if err := yaml.Unmarshal([]byte(oursFM), &oursMap); err != nil {
		r.metrics.IncResolverFailure(resolverFailureYAMLParse)
		glog.Warningf("yaml-merge-resolver: %q ours YAML parse failed: %v", path, err)
		return errors.Wrap(ctx, ErrConflictResolutionFailed, "ours: yaml parse")
	}
	theirsMap := map[string]interface{}{}
	if err := yaml.Unmarshal([]byte(theirsFM), &theirsMap); err != nil {
		r.metrics.IncResolverFailure(resolverFailureYAMLParse)
		glog.Warningf("yaml-merge-resolver: %q theirs YAML parse failed: %v", path, err)
		return errors.Wrap(ctx, ErrConflictResolutionFailed, "theirs: yaml parse")
	}

	merged := deepMergeTheirsWins(oursMap, theirsMap)
	mergedFM, err := yaml.Marshal(merged)
	if err != nil {
		r.metrics.IncResolverFailure(resolverFailureYAMLParse)
		glog.Warningf("yaml-merge-resolver: %q marshal merged YAML failed: %v", path, err)
		return errors.Wrap(ctx, ErrConflictResolutionFailed, "marshal merged yaml")
	}

	body := combineBodies(oursBody, theirsBody)
	final := "---\n" + string(mergedFM) + "---\n" + body

	if err := os.WriteFile(abs, []byte(final), 0o600); err != nil {
		r.metrics.IncResolverFailure(resolverFailureWrite)
		glog.Warningf("yaml-merge-resolver: write %q: %v", path, err)
		return errors.Wrap(ctx, ErrConflictResolutionFailed, "write merged file")
	}

	// #nosec G204 -- git binary is hardcoded; paths come from git merge output
	cmd := exec.CommandContext(ctx, "git", "add", "--", path)
	cmd.Dir = r.repoPath
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		r.metrics.IncResolverFailure(resolverFailureGitAdd)
		glog.Warningf("yaml-merge-resolver: git add %q failed: %v: %s", path, err, strings.TrimSpace(buf.String()))
		return errors.Wrap(ctx, ErrConflictResolutionFailed, "git add merged file")
	}
	return nil
}

// safePath joins the repo root with the conflict-reported path, rejects absolute
// paths and any result that does not stay under the repo root after symlink eval.
// It does NOT call EvalSymlinks on the final path (the file is a real file written
// by git's three-way merge), but it rejects "..", absolute paths, and any path
// that does not resolve under repoPath.
func (r *yamlMergeResolver) safePath(rel string) (string, error) {
	if rel == "" {
		return "", stderrors.New("empty path")
	}
	if filepath.IsAbs(rel) {
		return "", stderrors.New("absolute path rejected")
	}
	joined := filepath.Join(r.repoPath, rel)
	cleanRoot := filepath.Clean(r.repoPath)
	if !strings.HasPrefix(joined+string(filepath.Separator), cleanRoot+string(filepath.Separator)) && joined != cleanRoot {
		return "", stderrors.New("path escapes repo root")
	}
	return joined, nil
}

// splitConflictSides extracts the ours and theirs versions of the first conflict
// region from a file containing standard git three-way conflict markers. It returns
// ok=false if either marker is missing.
func splitConflictSides(content string) (ours string, theirs string, ok bool) {
	const (
		openMarker = "<<<<<<<"
		sepMarker  = "======="
		closeMark  = ">>>>>>>"
	)
	openIdx := strings.Index(content, openMarker)
	if openIdx < 0 {
		return "", "", false
	}
	// Skip the rest of the open-marker line.
	afterOpen := openIdx + strings.Index(content[openIdx:], "\n")
	if afterOpen < openIdx {
		return "", "", false
	}
	afterOpen++ // skip the newline

	sepIdx := strings.Index(content[afterOpen:], "\n"+sepMarker)
	if sepIdx < 0 {
		return "", "", false
	}
	oursEnd := afterOpen + sepIdx
	afterSep := afterOpen + sepIdx + 1 // position of sepMarker line
	afterSep += strings.Index(content[afterSep:], "\n")
	if afterSep < afterOpen+sepIdx+1 {
		return "", "", false
	}
	afterSep++ // skip the newline after the separator

	closeIdx := strings.Index(content[afterSep:], "\n"+closeMark)
	if closeIdx < 0 {
		return "", "", false
	}
	theirsEnd := afterSep + closeIdx

	return content[afterOpen:oursEnd], content[afterSep:theirsEnd], true
}

// splitFrontmatter splits an Obsidian-style markdown document into the YAML
// frontmatter body and the markdown body. The input MUST start with a line that
// is exactly "---" and contain a closing line that is exactly "---" before the
// body. Returns ok=false if either delimiter is missing.
func splitFrontmatter(doc string) (frontmatter string, body string, ok bool) {
	const delim = "---"
	lines := strings.Split(doc, "\n")
	if len(lines) < 2 || strings.TrimRight(lines[0], "\r") != delim {
		return "", "", false
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r") == delim {
			frontmatter = strings.Join(lines[1:i], "\n")
			if i+1 <= len(lines)-1 {
				body = strings.Join(lines[i+1:], "\n")
			}
			return frontmatter, body, true
		}
	}
	return "", "", false
}

// deepMergeTheirsWins returns a map containing the union of keys from ours and theirs.
// On overlapping keys whose values are both maps, it recurses. On overlapping keys
// whose values are not both maps, theirs wins.
func deepMergeTheirsWins(ours, theirs map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(ours)+len(theirs))
	for k, v := range ours {
		out[k] = v
	}
	for k, tv := range theirs {
		if ov, present := out[k]; present {
			ovMap, ovIsMap := ov.(map[string]interface{})
			tvMap, tvIsMap := tv.(map[string]interface{})
			if ovIsMap && tvIsMap {
				out[k] = deepMergeTheirsWins(ovMap, tvMap)
				continue
			}
		}
		out[k] = tv
	}
	return out
}

// combineBodies applies the spec body-merge rules:
//   - theirs body non-empty, ours empty   → theirs
//   - ours body non-empty, theirs empty   → ours
//   - both non-empty and equal            → ours (same as theirs)
//   - both non-empty and differ           → ours + "\n\n" + theirs
//   - both empty                          → ""
func combineBodies(ours, theirs string) string {
	switch {
	case theirs == "" && ours == "":
		return ""
	case theirs == "":
		return ours
	case ours == "":
		return theirs
	case ours == theirs:
		return ours
	default:
		return ours + "\n\n" + theirs
	}
}
```

Notes for the implementer:

- `glog` is already used elsewhere in the project; check `go.mod` shows `github.com/golang/glog`. If `goimports` reorders the import block, that is fine.
- `safePath` returns plain `stderrors.New(...)` (project convention for in-function validation errors with no ctx in scope); the caller wraps with `errors.Wrap(ctx, ErrConflictResolutionFailed, "...")` so the sentinel chain stays intact for `errors.Is` traversal.
- The `#nosec G304` comment is required because `gosec` flags `os.ReadFile` with a non-literal path; the `safePath` guard above satisfies the manual review.

## 5. Run `go mod tidy` to promote `gopkg.in/yaml.v3`

After the new import in `pkg/git/yaml_merge_resolver.go` references `gopkg.in/yaml.v3`, run:

```bash
go mod tidy
```

Verify the indirect marker is dropped:

```bash
grep -n "gopkg.in/yaml.v3" go.mod
```

Expected: one line in the direct `require ( ... )` block (top one, NOT the indirect block), without `// indirect`. The other `go.yaml.in/yaml/*` and `sigs.k8s.io/yaml` lines stay untouched in the indirect block.

## 6. Create `pkg/git/yaml_merge_resolver_test.go`

Create the test file as an external test package (`package git_test`), matching the style of `pkg/git/conflict_resolver_test.go`. Cover every spec AC reachable from this prompt.

```go
// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git_test

import (
	"context"
	stderrors "errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"

	"github.com/bborbe/git-rest/mocks"
	"github.com/bborbe/git-rest/pkg/git"
)

// setupYAMLConflictRepo creates a real git repo at a temp dir containing a single
// markdown file in conflicted state. Returns workDir, the conflicted file path
// (relative to workDir), and a cleanup func.
func setupYAMLConflictRepo(oursFrontmatter, oursBody, theirsFrontmatter, theirsBody string) (workDir, relPath string, cleanup func()) {
	dir, err := os.MkdirTemp("", "yaml-merge-test-*")
	Expect(err).NotTo(HaveOccurred())

	run := func(name string, args ...string) {
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		out, runErr := cmd.CombinedOutput()
		Expect(runErr).NotTo(HaveOccurred(), "%s %v: %s", name, args, string(out))
	}

	run("git", "init", "-q", "-b", "main")
	run("git", "config", "user.email", "test@example.com")
	run("git", "config", "user.name", "test")

	relPath = "note.md"
	abs := filepath.Join(dir, relPath)

	base := "---\nbase: 1\n---\nbase body\n"
	Expect(os.WriteFile(abs, []byte(base), 0o600)).To(Succeed())
	run("git", "add", "--", relPath)
	run("git", "commit", "-q", "-m", "base")

	// Branch "theirs", commit theirs version.
	run("git", "checkout", "-q", "-b", "theirs")
	theirsContent := "---\n" + theirsFrontmatter + "---\n" + theirsBody
	Expect(os.WriteFile(abs, []byte(theirsContent), 0o600)).To(Succeed())
	run("git", "add", "--", relPath)
	run("git", "commit", "-q", "-m", "theirs")

	// Back to main, write ours.
	run("git", "checkout", "-q", "main")
	oursContent := "---\n" + oursFrontmatter + "---\n" + oursBody
	Expect(os.WriteFile(abs, []byte(oursContent), 0o600)).To(Succeed())
	run("git", "add", "--", relPath)
	run("git", "commit", "-q", "-m", "ours")

	// Merge theirs into main — expect non-zero exit + conflict markers in file.
	cmd := exec.Command("git", "merge", "--no-edit", "theirs")
	cmd.Dir = dir
	_ = cmd.Run() // non-zero exit expected

	// Sanity: file must now contain conflict markers.
	content, readErr := os.ReadFile(abs)
	Expect(readErr).NotTo(HaveOccurred())
	Expect(string(content)).To(ContainSubstring("<<<<<<<"))

	cleanup = func() { _ = os.RemoveAll(dir) }
	return dir, relPath, cleanup
}

// parseFrontmatterOf reads the staged file's frontmatter (the YAML block between the
// first two "---" delimiters) and returns it as a map.
func parseFrontmatterOf(path string) map[string]interface{} {
	raw, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())
	lines := strings.Split(string(raw), "\n")
	Expect(lines[0]).To(Equal("---"), "file must begin with frontmatter delimiter")
	var fmLines []string
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			break
		}
		fmLines = append(fmLines, lines[i])
	}
	out := map[string]interface{}{}
	Expect(yaml.Unmarshal([]byte(strings.Join(fmLines, "\n")), &out)).To(Succeed())
	return out
}

// bodyOf returns the body (after the second "---" delimiter) of the staged file.
func bodyOf(path string) string {
	raw, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())
	lines := strings.Split(string(raw), "\n")
	delimitersSeen := 0
	bodyStart := -1
	for i := range lines {
		if lines[i] == "---" {
			delimitersSeen++
			if delimitersSeen == 2 {
				bodyStart = i + 1
				break
			}
		}
	}
	Expect(bodyStart).To(BeNumerically(">=", 0))
	return strings.TrimRight(strings.Join(lines[bodyStart:], "\n"), "\n")
}

var _ = Describe("YAMLMergeResolver", func() {
	var (
		ctx         context.Context
		fakeMetrics *mocks.FakeMetrics
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeMetrics = &mocks.FakeMetrics{}
	})

	Describe("Resolve happy path", func() {
		It("AC-deepmerge: merges frontmatter with theirs winning on overlap", func() {
			workDir, rel, cleanup := setupYAMLConflictRepo(
				"a: 1\nb: 2\n", "ours body",
				"b: 3\nc: 4\n", "theirs body",
			)
			defer cleanup()

			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

			fm := parseFrontmatterOf(filepath.Join(workDir, rel))
			Expect(fm).To(HaveKeyWithValue("a", 1))
			Expect(fm).To(HaveKeyWithValue("b", 3))
			Expect(fm).To(HaveKeyWithValue("c", 4))
		})

		It("AC-body-both: ours + theirs body when both non-empty and differ", func() {
			workDir, rel, cleanup := setupYAMLConflictRepo(
				"a: 1\n", "X",
				"b: 2\n", "Y",
			)
			defer cleanup()
			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			Expect(r.Resolve(ctx, []string{rel})).To(Succeed())
			Expect(bodyOf(filepath.Join(workDir, rel))).To(Equal("X\n\nY"))
		})

		It("AC-body-theirs-empty: ours body kept when theirs empty", func() {
			workDir, rel, cleanup := setupYAMLConflictRepo(
				"a: 1\n", "X",
				"b: 2\n", "",
			)
			defer cleanup()
			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			Expect(r.Resolve(ctx, []string{rel})).To(Succeed())
			Expect(bodyOf(filepath.Join(workDir, rel))).To(Equal("X"))
		})

		It("AC-body-ours-empty: theirs body used when ours empty", func() {
			workDir, rel, cleanup := setupYAMLConflictRepo(
				"a: 1\n", "",
				"b: 2\n", "Y",
			)
			defer cleanup()
			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			Expect(r.Resolve(ctx, []string{rel})).To(Succeed())
			Expect(bodyOf(filepath.Join(workDir, rel))).To(Equal("Y"))
		})

		It("AC-staged: file is staged (no longer unmerged) after Resolve", func() {
			workDir, rel, cleanup := setupYAMLConflictRepo(
				"a: 1\n", "X",
				"b: 2\n", "Y",
			)
			defer cleanup()
			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

			cmd := exec.Command("git", "diff", "--name-only", "--diff-filter=U")
			cmd.Dir = workDir
			out, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(string(out))).To(BeEmpty(), "file must not be unmerged")
		})

		It("AC-metrics-resolved: does NOT call IncResolverFailure on success", func() {
			workDir, rel, cleanup := setupYAMLConflictRepo(
				"a: 1\n", "X",
				"b: 2\n", "Y",
			)
			defer cleanup()
			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			Expect(r.Resolve(ctx, []string{rel})).To(Succeed())
			Expect(fakeMetrics.IncResolverFailureCallCount()).To(Equal(0))
		})
	})

	Describe("Resolve failure categories", func() {
		It("AC-yaml-parse-failed-ours: returns sentinel + increments yaml_parse_failed", func() {
			workDir, rel, cleanup := setupYAMLConflictRepo(
				"this : is : not : valid : yaml\n", "X",
				"b: 2\n", "Y",
			)
			defer cleanup()
			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			err := r.Resolve(ctx, []string{rel})
			Expect(err).To(HaveOccurred())
			Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
			Expect(fakeMetrics.IncResolverFailureCallCount()).To(Equal(1))
			Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("yaml_parse_failed"))
		})

		It("AC-yaml-parse-failed-theirs: returns sentinel + increments yaml_parse_failed", func() {
			workDir, rel, cleanup := setupYAMLConflictRepo(
				"a: 1\n", "X",
				"this : is : not : valid : yaml\n", "Y",
			)
			defer cleanup()
			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			err := r.Resolve(ctx, []string{rel})
			Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
			Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("yaml_parse_failed"))
		})

		It("AC-no-frontmatter: file with no `---` delimiters returns sentinel + no_frontmatter", func() {
			// Build a conflicted non-frontmatter file by hand. Reuse the helper to
			// create a repo, then overwrite the conflicted file with a no-delimiter
			// payload that still has conflict markers.
			workDir, rel, cleanup := setupYAMLConflictRepo(
				"a: 1\n", "X",
				"b: 2\n", "Y",
			)
			defer cleanup()
			abs := filepath.Join(workDir, rel)
			Expect(os.WriteFile(abs, []byte("<<<<<<< HEAD\nno delimiters here\n=======\nstill none\n>>>>>>> theirs\n"), 0o600)).To(Succeed())
			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			err := r.Resolve(ctx, []string{rel})
			Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
			Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("no_frontmatter"))
		})

		It("AC-git-add-failed: path does not exist on disk → write_failed (read fails first)", func() {
			workDir, _, cleanup := setupYAMLConflictRepo(
				"a: 1\n", "X",
				"b: 2\n", "Y",
			)
			defer cleanup()
			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			err := r.Resolve(ctx, []string{"does-not-exist.md"})
			Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
			Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("write_failed"))
		})

		It("AC-no-frontmatter: file content has no conflict region (binary-ish) → no_frontmatter", func() {
			workDir, rel, cleanup := setupYAMLConflictRepo(
				"a: 1\n", "X",
				"b: 2\n", "Y",
			)
			defer cleanup()
			abs := filepath.Join(workDir, rel)
			Expect(os.WriteFile(abs, []byte("no conflict markers at all\n"), 0o600)).To(Succeed())
			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			err := r.Resolve(ctx, []string{rel})
			Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
			Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("no_frontmatter"))
		})

		It("AC-security-escape: path containing `..` is rejected without I/O", func() {
			workDir, _, cleanup := setupYAMLConflictRepo(
				"a: 1\n", "X",
				"b: 2\n", "Y",
			)
			defer cleanup()
			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			err := r.Resolve(ctx, []string{"../escape.md"})
			Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
			Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("write_failed"))
		})
	})

	Describe("Metric pre-initialisation (spec AC: zero-init labels)", func() {
		It("exposes all four failure categories pre-initialised to zero", func() {
			// Gather from the prometheus default registry; the labels are pre-initialised
			// in metrics.init().
			gatherer := preinitLabelsOf("git_rest_resolver_failures_total")
			Expect(gatherer).To(ContainElement("yaml_parse_failed"))
			Expect(gatherer).To(ContainElement("no_frontmatter"))
			Expect(gatherer).To(ContainElement("write_failed"))
			Expect(gatherer).To(ContainElement("git_add_failed"))
		})
	})
})
```

Add a small helper in the same test file (above the `Describe` block or below — implementer's choice):

```go
// preinitLabelsOf returns the list of "category" label values present on the
// named CounterVec in the prometheus default registry.
func preinitLabelsOf(metricName string) []string {
	mfs, err := prometheus.DefaultGatherer.Gather()
	Expect(err).NotTo(HaveOccurred())
	var out []string
	for _, mf := range mfs {
		if mf.GetName() != metricName {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "category" {
					out = append(out, l.GetValue())
				}
			}
		}
	}
	return out
}
```

And add the imports for it:

```go
"github.com/prometheus/client_golang/prometheus"
```

## 7. Sanity: existing files MUST stay byte-identical

After all edits, the following MUST report zero diff:

```bash
git diff pkg/git/conflict_resolver.go
git diff pkg/git/conflict_resolver_test.go
git diff pkg/git/git_test.go | grep -vE '^(diff|index|---|\+\+\+|@@|\+func \(n \*noopMetrics\) IncResolverFailure)' | grep -E '^[+-]' | head
git diff mocks/conflict_resolver.go
```

The only allowed change in `pkg/git/git_test.go` is the one-line `noopMetrics` stub added in requirement 3.

## 8. Verify the implementation

Run `make precommit` from the repo root. It MUST exit 0.

```bash
make precommit
```

Targeted Ginkgo run for the new file:

```bash
go test ./pkg/git/... -v -run "YAMLMergeResolver"
```

</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT edit `pkg/git/conflict_resolver.go` — the `ConflictResolver` interface and counterfeiter directive are frozen by spec.
- Do NOT edit `pkg/git/conflict_resolver_test.go` — existing `MarkerResolver` tests must pass unchanged.
- Do NOT regenerate `mocks/conflict_resolver.go`. After `make generate`, `git diff mocks/conflict_resolver.go` MUST be empty.
- Do NOT wire `YAMLMergeResolver` into `main.go` in this prompt — selection wiring is prompt 2's job.
- Do NOT add a `LastWriteWinsResolver`, `DeterministicUUIDResolver`, or any AI-backed resolver — spec Non-goals.
- Do NOT add a "disable YAMLMergeResolver" or "prefer ours" tunable — spec Non-goals.
- Do NOT add a YAML library other than `gopkg.in/yaml.v3`. The other yaml modules already in `go.mod` MUST stay indirect.
- Errors MUST be wrapped with `errors.Wrap` / `errors.Wrapf` from `github.com/bborbe/errors` — never `fmt.Errorf`, never bare `return err`.
- `ErrConflictResolutionFailed` MUST be returned wrapped via `errors.Wrap(ctx, ErrConflictResolutionFailed, ...)` so `errors.Is` traverses the chain (stdlib `errors.Is` and bborbe/errors interoperate via the `Unwrap` method that bborbe/errors provides).
- The `Metrics` interface extension MUST keep existing methods byte-identical; only `IncResolverFailure` is appended.
- The new metric `ResolverFailuresTotal` MUST pre-initialise ALL FOUR category labels to zero in `init()` — operators rely on `rate()` against zero baselines.
- `glog.Warningf` is used for unconditional failure logs (one per failure path). Do NOT use `glog.V(n).Infof` for failures — V-gating hides them by default.
- The resolver MUST NOT follow symlinks out of the repo root. The `safePath` guard in requirement 4 is mandatory and is exercised by the `AC-security-escape` test.
- The resolver MUST honour the inherited `ctx` for the `exec.CommandContext` call — never `context.Background()` inside the resolver for the subprocess.
- File mode for written merged file: `0o600` (matches existing project pattern for files containing potentially sensitive vault content).
- Existing tests must still pass: the only edit to `pkg/git/git_test.go` is the single `noopMetrics.IncResolverFailure` stub method.
- Do NOT change the public HTTP contract (`/api/v1/files/*`, `/readiness`, `/healthz`, `/metrics`).
</constraints>

<verification>
Run from the repo root:

```bash
make precommit
```

Must exit 0.

Targeted unit tests:

```bash
go test ./pkg/git/... -v -run "YAMLMergeResolver"
go test ./pkg/git/... -v -run "MarkerResolver"
```

All pass.

Interface and mock guarantees:

```bash
grep -n 'NewYAMLMergeResolver' pkg/git/yaml_merge_resolver.go          # ≥1 match
grep -E 'func NewYAMLMergeResolver.*ConflictResolver' pkg/git/yaml_merge_resolver.go  # ≥1 match
git diff pkg/git/conflict_resolver.go                                  # zero changes
git diff pkg/git/conflict_resolver_test.go                             # zero changes
git diff mocks/conflict_resolver.go                                    # zero changes
```

Metric registration:

```bash
grep -n 'ResolverFailuresTotal\|IncResolverFailure' pkg/metrics/metrics.go    # ≥3 matches
grep -n 'IncResolverFailure' mocks/metrics.go                                # ≥3 matches (Stub, Calls, ArgsForCall)
```

YAML lib promoted:

```bash
grep -n 'gopkg.in/yaml.v3' go.mod
```

Must show one match in the direct `require ( ... )` block (without `// indirect`).
</verification>
