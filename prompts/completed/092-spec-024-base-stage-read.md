---
status: completed
spec: [024-bug-one-sided-frontmatter-adds-quarantine]
summary: Plumbed a fail-closed merge-base (index stage 1) key-set reader into the YAML conflict resolver without changing any resolution outcome
execution_id: git-rest-frontmatter-union-exec-092-spec-024-base-stage-read
dark-factory-version: v0.198.7
created: "2026-11-02T19:30:00Z"
queued: "2026-11-02T19:26:42Z"
started: "2026-11-02T19:26:44Z"
completed: "2026-11-02T19:30:19Z"
branch: dark-factory/bug-one-sided-frontmatter-adds-quarantine
---

# Read the merge-base frontmatter key set for a conflicted path

<summary>
- The YAML conflict resolver gains a read-only view of the merge-base version of the conflicted file, taken from the merge's own index stage 1.
- The reader reports "base unavailable" instead of guessing when the index holds no stage 1 (an add/add conflict) or when the base frontmatter is not valid YAML.
- The resolver can therefore tell, for any frontmatter key, whether the key existed before either side touched the file.
- Nothing about today's conflict behaviour changes in this step: the reader is plumbed in but no decision consults it yet.
- The resolver's existing test-only constructor is kept working, so the resolver never holds a nil base reader.
- The reader is exercised against a real git merge (real index stage 1), against a hand-written conflict with no index stages, and against a base whose frontmatter does not parse.
- Existing counter and newer-side conflict resolution, quarantine behaviour and metrics are untouched.
</summary>

<objective>
Plumb a fail-closed merge-base reader into the YAML conflict resolver so the follow-up prompt can decide whether a one-sided frontmatter key is an addition or a deletion, without this step changing any resolution outcome.
</objective>

<context>
Read `docs/dod.md` for the project's Definition of Done, then read these files before changing anything:

- `pkg/git/yaml_merge_resolver.go` — the whole file. Focus on `yamlMergeResolver`, `NewYAMLMergeResolver`, `gitMergeSideTimes`, `runGitOutput`, `splitFrontmatter`, `applyScalarFrontmatterRule`.
- `pkg/git/vault_api_merge_resolver.go` — the `sides` method. It already reads index stage 1 with `runGitOutput(ctx, r.repoPath, "show", ":1:"+path)`; the new reader is the same call shape, so follow it exactly.
- `pkg/git/export_test.go` — `NewYAMLMergeResolverWithSideTimes` builds a `yamlMergeResolver` struct literal directly. It must keep compiling and must set the new field.
- `pkg/git/yaml_merge_resolver_test.go` — the helpers `setupScalarMergeRepo` (a real conflicted merge, so index stage 1 exists) and `setupHandWrittenConflict` (markers written by hand, staged normally, so there is no stage 1), plus the `Describe("Scalar frontmatter rule")` block.
- `specs/in-progress/024-bug-one-sided-frontmatter-adds-quarantine.md` — `## Assumptions`, `## Failure Modes` row 1, `## Security / Abuse`, and the `## Suggested Decomposition` table row 1.

Coding guides (in-container paths):

- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors` only, never `fmt.Errorf`, never `context.Background()` in non-test `pkg/` code.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` — this file logs with `github.com/golang/glog`, not `log/slog`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 / Gomega conventions, external test package `git_test`, `export_test.go` as the test seam.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md` — unexported helper conventions, constructor → struct wiring.
</context>

<requirements>
## 1. Add the stage-1 reader to the resolver (`pkg/git/yaml_merge_resolver.go`)

1.1 Add a field to the `yamlMergeResolver` struct, keeping the existing fields and their order:

```go
type yamlMergeResolver struct {
	repoPath    string
	metrics     metrics.Metrics
	sideTimes   func(ctx context.Context) (ours int64, theirs int64, err error)
	baseContent func(ctx context.Context, path string) (string, error)
}
```

1.2 In `NewYAMLMergeResolver`, set the new field so the constructor returns a fully wired resolver:

```go
return &yamlMergeResolver{
	repoPath:    repoPath,
	metrics:     m,
	sideTimes:   gitMergeSideTimes(repoPath),
	baseContent: gitBaseContent(repoPath),
}
```

1.3 Add the reader factory next to `gitMergeSideTimes`, following `vaultAPIMergeResolver.sides` for the git invocation. The path is passed as part of a single `:1:<path>` argv element, exactly as the vault-api resolver does — do not shell-interpolate it:

```go
// gitBaseContent returns a function that reads the merge-base version of a
// conflicted path out of the merge's index stage 1. Stage 1 exists only for a
// path the merge actually conflicted against a common ancestor; an add/add
// conflict has no stage 1 and the read fails, which callers must treat as
// "no base to consult" rather than as an empty base.
func gitBaseContent(repoPath string) func(ctx context.Context, path string) (string, error) {
	return func(ctx context.Context, path string) (string, error) {
		return runGitOutput(ctx, repoPath, "show", ":1:"+path)
	}
}
```

1.4 Add the key-set accessor as a method on `*yamlMergeResolver`, placed after `baseContent`'s helper or immediately before `applyScalarFrontmatterRule`:

```go
// baseKeySet returns the top-level frontmatter keys of the merge-base version of
// path. ok is false when the base cannot be read at all (no index stage 1, for
// example an add/add conflict) or when its frontmatter is not parseable YAML.
// Callers that need the base to decide something MUST decline when ok is false:
// an unreadable base is indistinguishable from an unknown one, and guessing
// there would resurrect a deleted key.
//
// The keys are taken from a full YAML unmarshal rather than from single-line
// scalar entries, so a base key whose value is a nested mapping, a list or a
// block scalar still counts as present.
func (r *yamlMergeResolver) baseKeySet(
	ctx context.Context, path string,
) (map[string]struct{}, bool) {
	content, err := r.baseContent(ctx, path)
	if err != nil {
		glog.Warningf("yaml-merge-resolver: %q merge-base stage unreadable: %v", path, err)
		return nil, false
	}
	frontmatter, _, ok := splitFrontmatter(content)
	if !ok {
		return nil, false
	}
	parsed := map[string]interface{}{}
	if err := yaml.Unmarshal([]byte(frontmatter), &parsed); err != nil {
		glog.Warningf(
			"yaml-merge-resolver: %q merge-base frontmatter parse failed: %v",
			path,
			err,
		)
		return nil, false
	}
	keys := make(map[string]struct{}, len(parsed))
	for key := range parsed {
		keys[key] = struct{}{}
	}
	return keys, true
}
```

Do not change the existing `glog` import to `log/slog`, and do not add a new import for the YAML parse — `gopkg.in/yaml.v3` is already imported. Do not reuse the existing `parseYAML` helper: it increments `resolverFailureYAMLParse` and returns `ErrConflictResolutionFailed`, whereas an unreadable base is a decline, not a resolution failure.

1.5 `baseKeySet` is deliberately not called from `applyScalarFrontmatterRule` in this prompt: the follow-up prompt wires it into the union rule. It is referenced from `export_test.go`, which keeps the `unused` linter satisfied. Do not add a call site that discards its result.

## 2. Keep the test-only constructor whole (`pkg/git/export_test.go`)

2.1 `NewYAMLMergeResolverWithSideTimes` builds `&yamlMergeResolver{...}` directly. Add `baseContent: gitBaseContent(repoPath)` to that literal so the resolver never holds a nil reader.

2.2 Add a test-only accessor next to it:

```go
// YAMLBaseKeySetForTest exposes the merge-base key reader to the external test
// package. Test builds only.
func YAMLBaseKeySetForTest(
	ctx context.Context, repoPath, path string,
) (map[string]struct{}, bool) {
	r := &yamlMergeResolver{repoPath: repoPath, baseContent: gitBaseContent(repoPath)}
	return r.baseKeySet(ctx, path)
}
```

## 3. Tests for the reader (`pkg/git/yaml_merge_resolver_test.go`)

Add these as `It` blocks inside the existing `Describe("Scalar frontmatter rule")` block, after the `AC4: rejection shapes keep quarantining` table. They assert the reader only; no resolution outcome changes in this prompt.

3.1 Real merge, base readable:

```go
It("reads the merge-base key set from index stage 1", func() {
	base := "---\npr_state: open\nphase: planning\n---\nbody\n"
	ours := "---\npr_state: open\nphase: done\n---\nbody\n"
	theirs := "---\npr_state: closed\nphase: planning\n---\nbody\n"
	workDir, rel, cleanup := setupScalarMergeRepo(base, ours, theirs, "", "")
	defer cleanup()

	keys, ok := git.YAMLBaseKeySetForTest(ctx, workDir, rel)
	Expect(ok).To(BeTrue())
	Expect(keys).To(HaveKey("pr_state"))
	Expect(keys).To(HaveKey("phase"))
})
```

The fixture must genuinely conflict — `setupScalarMergeRepo` already asserts the file carries `<<<<<<<`, so a clean merge would fail the setup rather than pass silently.

3.2 Hand-written conflict, base unreadable:

```go
It("reports the base as unreadable when the index has no stage 1", func() {
	workDir, rel, cleanup := setupHandWrittenConflict(
		"note.md",
		"---\n<<<<<<< HEAD\npr_state: open\n=======\nprevious_assignee: someone\n"+
			">>>>>>> theirs\ntitle: probe\n---\nbody\n",
	)
	defer cleanup()

	_, ok := git.YAMLBaseKeySetForTest(ctx, workDir, rel)
	Expect(ok).To(BeFalse())
})
```

3.3 Base frontmatter that does not parse as YAML:

```go
It("reports the base as unreadable when its frontmatter is not YAML", func() {
	base := "---\ntitle: [unclosed\n---\nbody\n"
	ours := "---\ntitle: a\n---\nbody\n"
	theirs := "---\ntitle: b\n---\nbody\n"
	workDir, rel, cleanup := setupScalarMergeRepo(base, ours, theirs, "", "")
	defer cleanup()

	_, ok := git.YAMLBaseKeySetForTest(ctx, workDir, rel)
	Expect(ok).To(BeFalse())
})
```

3.4 Do not modify any existing spec in that file. In particular the `Entry("one-sided key", …)` case must keep expecting `ErrConflictResolutionFailed`: this prompt changes no resolution path, and the follow-up prompt preserves it because a hand-written conflict has no `MERGE_HEAD` and no stage 1 — the region declines on the shared non-counter key's missing merge-parent timestamps before any base is read, and the one-sided key would decline on the unreadable base.

## 4. Self-check before finishing

4.1 Run the targeted suite and confirm it is green: `GOFLAGS=-buildvcs=false go test ./pkg/git/ -count=1`.

4.2 Confirm `gitBaseContent` has exactly one definition, `baseKeySet` exactly one definition, and that `NewYAMLMergeResolver` and `NewYAMLMergeResolverWithSideTimes` both set `baseContent` — see `<verification>` for the greps.
</requirements>

<constraints>
- `pkg/git/yaml_merge_resolver.go` keeps `github.com/golang/glog`; never introduce `log/slog` in that file.
- Errors are wrapped with `github.com/bborbe/errors`; never `fmt.Errorf`.
- No new `context.Background()` in non-test `pkg/` code; this prompt adds no timestamps, and any future timestamp must come from the injected `sideTimes` getter, never `time.Now()`.
- The resolver stays fail-closed: an unreadable base is reported as `ok=false`, never as an empty key set.
- The path handed to `git show` is a separate argv element (`":1:"+path`); it is not interpolated into a shell string.
- This prompt changes no resolution outcome. The scalar-frontmatter rule, the deep-merge path, quarantine, and every metric counter must behave exactly as before.
- Do not delete or weaken `Entry("one-sided key", …)` in `pkg/git/yaml_merge_resolver_test.go`; a later prompt keeps it passing.
- No change to `bborbe/vault-api`; its body-conflict policy is out of scope.
- Precommit budgets: funlen 80, gocognit 20, nestif 4, golines 100.
- `.dark-factory.yaml` sets `workflow: direct`, `pr: false`, `autoRelease: false`: commit to the branch directly; no PR or auto-release is expected.
- Do NOT commit — dark-factory handles git.
- Do not touch `CHANGELOG.md`, `docs/deployment.md`, or `specs/in-progress/016-yaml-scalar-conflict-autoresolve.md`: this step ships no user-visible behaviour and the follow-up prompt owns the changelog entry, the operator docs and the supersession note. Report the project DoD's CHANGELOG line as not-applicable rather than adding an entry.
- Existing tests must still pass.
</constraints>

<verification>
Run inside the container:

```
GOFLAGS=-buildvcs=false go build ./...
GOFLAGS=-buildvcs=false go test ./pkg/git/ -count=1
make test
make precommit
```

All four must exit 0.

Then confirm the observable outcomes (each line must print `ok`):

```
test "$(grep -c 'func gitBaseContent(repoPath string)' pkg/git/yaml_merge_resolver.go)" = "1" && echo ok
test "$(grep -c 'func (r \*yamlMergeResolver) baseKeySet' pkg/git/yaml_merge_resolver.go)" = "1" && echo ok
test "$(grep -c 'baseContent: gitBaseContent(repoPath)' pkg/git/yaml_merge_resolver.go)" = "1" && echo ok
test "$(grep -c 'baseContent: gitBaseContent(repoPath)' pkg/git/export_test.go)" = "1" && echo ok
test "$(grep -c 'func YAMLBaseKeySetForTest' pkg/git/export_test.go)" = "1" && echo ok
test "$(grep -c 'YAMLBaseKeySetForTest' pkg/git/yaml_merge_resolver_test.go)" = "3" && echo ok
! grep -q 'log/slog' pkg/git/yaml_merge_resolver.go && echo ok
! grep -q 'fmt.Errorf' pkg/git/yaml_merge_resolver.go && echo ok
```

`make precommit` is the authoritative gate: it runs format, generate, the full test suite, and the linter. If it fails, fix the failure and re-run only the failing target until it passes, then run `make precommit` once more.
</verification>
