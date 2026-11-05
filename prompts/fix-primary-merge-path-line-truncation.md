---
status: draft
---

# Preserve every line outside the conflict regions on the primary merge path

<summary>
- A file whose conflict regions do not span the whole file keeps every line around them when merged
- Only the conflicted lines are rewritten; untouched lines are copied through byte-identical
- Each conflict region resolves from its own two sides, never from another region's content
- A file whose conflict region does span the whole file merges to the same frontmatter and body
- The frontmatter theirs-wins merge and the body concatenation keep their current semantics
- Two regression tests lock the behaviour: one single conflict region, one pair of regions
- Both regression tests fail before the change and pass after it
</summary>

<objective>
A clean resolve of a conflicted note no longer deletes every line outside the conflict regions; only the conflicted lines change.
</objective>

<context>
Read `docs/dod.md` for the project's Definition of Done and `README.md` for conventions.

`yamlMergeResolver.resolveOne` in `pkg/git/yaml_merge_resolver.go` has two paths.

**Fallback** — `applyPartitionedRules` (~line 1056). Used when the conflict regions sit wholly inside the frontmatter or wholly in the body. It splits the original file into lines, enumerates the regions (`enumerateConflictRegions`), builds one replacement slice per region, and splices them back with `spliceConflictRegions`. Lines outside every region survive verbatim. This path is correct; do not change it.

**Primary** — used when `splitFrontmatter` succeeds on both sides returned by `splitConflictSides`. It builds the output as

```go
final := "---\n" + string(mergedFM) + "---\n" + body
```

and writes that. `splitConflictSides` returns ONLY the accumulated two sides of the conflict regions, so every line outside them is missing from `final`. A 49-line note merged to 34 lines (one region) or 13 lines (two regions), with `Resolve` returning nil and the `resolved=[…] quarantined=[]` log line reading as success.

The truncation fires only when the FIRST conflict region carries a complete `---` pair on each side, so `splitFrontmatter` succeeds and the primary path runs. When it does not, the file falls to the fallback and is handled correctly — which is why this defect stayed hidden behind the quarantine path.

The doc comment on `splitConflictSides` still says "first conflict region" while the function accumulates every region.
</context>

<requirements>
1. In `resolveOne`, keep the primary path's frontmatter merge semantics unchanged: `parseYAML` on both sides, `deepMergeTheirsWins`, `yaml.Marshal` into `mergedFM`; keep `combineBodies` for body halves. Only the placement of the result into the file changes.

2. Resolve each region from ITS OWN sides, not from the flattened `splitConflictSides` result. For every `conflictRegion` build `oursRegion := strings.Join(region.oursLines, "\n")` and `theirsRegion := strings.Join(region.theirsLines, "\n")`.

   **Reattach the opening delimiter first.** `splitConflictSides` prepends `"---\n"` to a side that does not already start with it (`yaml_merge_resolver.go:394-401`), because git places the opening `---` above the first marker. The per-region logic must do the same or the commonest real shape breaks: a real `git merge` of a two-key note produces

   ```
   ---
   <<<<<<< HEAD
   a: 1
   b: 2
   ---
   ours body
   =======
   b: 3
   c: 4
   ---
   theirs body
   >>>>>>> theirs
   ```

   whose region sides are `a: 1\nb: 2\n---\nours body` — no leading `---`, so a bare `splitFrontmatter` fails and the YAML keys get emitted as body text. So when the file's line above the region is `---` and neither side starts with `"---\n"`, run `splitFrontmatter` on `"---\n" + oursRegion` / `"---\n" + theirsRegion`, and emit that replacement WITHOUT a synthetic leading `"---"` line — the file's own `---` sits outside the region and `spliceConflictRegions` preserves it. When a side already starts with `---`, keep that `---` in the replacement.

   Then run `splitFrontmatter` on each (reattached) side:
   - a region where both sides parse as frontmatter (including one straddling the boundary) becomes `opener + string(yaml.Marshal(deepMergeTheirsWins(parseYAML(ours), parseYAML(theirs)))) + "---\n" + combineBodies(oursBody, theirsBody)`, then `strings.Split(that, "\n")` is the replacement line slice, where `opener` is `"---\n"` when the region's sides carry their own opening `---`, and `""` when the region was reattached in the paragraph above — a `"---\n"` there duplicates the file's own delimiter, makes `splitFrontmatter` read an empty frontmatter and fails five existing specs;
   - a body-only region becomes `strings.Split(combineBodies(strings.Join(region.oursLines, "\n"), strings.Join(region.theirsLines, "\n")), "\n")` — the raw region sides, not the reattached strings, so a body region sitting directly below the frontmatter's closing `---` never gains a synthetic opener.
   Every region gets its own replacement; no region's content may appear in another region's replacement.

3. Replace the string rebuild with the existing in-place mechanism, following `applyPartitionedRules` as the exemplar. This block runs only where the primary path runs today — still behind the `splitConflictSides` + `splitFrontmatter` gate in `resolveOne`. Do not move it ahead of that gate, and do not route files that `applyPartitionedRules` already handles into it.
   - `lines := strings.Split(string(raw), "\n")`
   - `regions, ok := enumerateConflictRegions(lines)`
   - build the replacement slice per region (requirement 2)
   - `final := spliceConflictRegions(lines, regions, replacements)`
   - If `ok` is false, do NOT rebuild the file and do NOT invent a failure category: return `errors.Wrap(ctx, ErrConflictResolutionFailed, "enumerate conflict regions")` without writing, and leave the existing metric counters untouched. Add a test for this arm: take a resolvable whole-file region, append a stray `<<<<<<< stray` line so the `splitConflictSides` + `splitFrontmatter` gate still passes but `enumerateConflictRegions` does not, then assert the wrapped error, a byte-identical untouched file, and zero `IncResolverFailure` calls.

4. Keep the write exactly as it is today: `os.WriteFile(abs, normalizeTaskPhase(path, []byte(final)), 0o600)`. `normalizeTaskPhase` must not be dropped.

5. A whole-file conflict still merges to the same content. Write this test FIRST, run it against the unmodified code, and paste the exact `string(written)` value (via `%q`) into the assertion as a literal. Write the fixture to disk with NO trailing newline after `>>>>>>> theirs`. Use this fixture:

   ```
   <<<<<<< HEAD
   ---
   title: ours
   trigger_count: 8
   ---
   ours body
   =======
   ---
   title: theirs
   trigger_count: 1
   ---
   theirs body
   >>>>>>> theirs
   ```

   Then make the change and confirm the same literal still passes. Do not write the fixture with a trailing newline: requirement 3 copies every line outside a region verbatim, including the final empty element of `strings.Split(raw, "\n")`, so a trailing-newline fixture gains a `\n` that today's code drops and can never be byte-identical. With no trailing newline the two outputs are identical and equal to `"---\ntitle: theirs\ntrigger_count: 1\n---\nours body\n\ntheirs body"` (7 lines).

6. The two regression tests for the non-spanning shape are ALREADY present in `pkg/git/yaml_merge_resolver_test.go` — the uncommitted `Describe("Line preservation when the first conflict region does not span the file")` block inside the outer `Describe("YAMLMergeResolver", …)`. Do NOT add a second copy; edit the existing block in place if it needs a change. Verify with

   ```
   grep -c 'Line preservation when the first conflict region does not span the file' pkg/git/yaml_merge_resolver_test.go
   ```

   returning exactly `1`. If the grep returns `0`, stop and report — the block is missing from the working tree; do not reconstruct it from memory.

   Both fixtures are 49 lines and both put a complete `---` pair on each side of the first conflict region. Their assertions are exact: **42** written lines for the single-region fixture and **41** for the two-region fixture, with no conflict markers, every outside line present in order, and no region's content duplicated into another. Those counts are arithmetic, not magic — 34 replacement + 8 outside = 42, and 9 + 5 + 27 = 41. A correct resolve is SHORTER than the 49-line conflicted input because it deletes the marker lines and the duplicated frontmatter: fixture 1 loses 3 markers, 2 duplicated delimiters and 3 duplicated keys while gaining one blank separator; fixture 2 loses 6 markers across its two regions. Do not "fix" a failing count by padding the output.

7. Fix the stale `splitConflictSides` doc comment so it no longer claims only the first conflict region is extracted.

8. Do not change the failure categories, the quarantine behaviour, or the metric increments on the failure arms.

9. Add a `## Unreleased` section to `CHANGELOG.md` (it does not exist today — the top section is `## v0.40.4`) with one bullet naming the truncation. Create it above the newest version heading; do not fold the bullet into a released section.

10. Before finishing, re-run `<verification>` and confirm it passes; walk each requirement above against the change.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- The fix lives in `pkg/git/yaml_merge_resolver.go` — never add a second resolver
- `context.Background()` must NOT appear in non-test `pkg/` code — only in `main.go`; test files may use it in a `BeforeEach`
- Errors are wrapped with `github.com/bborbe/errors` — never `fmt.Errorf`
- `pkg/git/yaml_merge_resolver.go` is a legacy `github.com/golang/glog` file (as is `vault_api_merge_resolver.go`): keep `glog` there, and never mix the two loggers in one file
- Do not touch quarantine behaviour or the `no_frontmatter` failure category
</constraints>

<verification>
make precommit
</verification>
