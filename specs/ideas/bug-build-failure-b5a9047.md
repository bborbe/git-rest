---
status: idea
kind: bug
---

# Build Failure: bborbe/git-rest

Filed automatically by the build-fix agent for the CI episode `b5a90471509174497cef9cddf7eaf4ff70588694`.

## Summary

The default-branch build for `bborbe/git-rest` is failing; the build-fix diagnosis classified this as a code/test bug (verdict `file_spec`).

## Reproduction

Failing workflow(s): test

Episode SHA: `b5a90471509174497cef9cddf7eaf4ff70588694`

Log evidence:

```text
| Workflow | Job | Failed Step | Run |
|---|---|---|---|
| CI | test | Run precommit checks | [Run](https://github.com/bborbe/git-rest/actions/runs/39337299652) |
```

## Expected vs Actual

**Expected:** green CI on the default branch.
**Actual:** `Test at pkg/memprof/reporter_test.go:89 asserts count > 0 for non-runtime frame attributions but gets 0 — a test assertion failure in repo code, not a dependency issue.`

## Why this is a bug

The default-branch build is the repository's quality gate; a red build blocks merges. Diagnosis: `Test at pkg/memprof/reporter_test.go:89 asserts count > 0 for non-runtime frame attributions but gets 0 — a test assertion failure in repo code, not a dependency issue.`
