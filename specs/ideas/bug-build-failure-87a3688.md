---
status: idea
kind: bug
---

# Build Failure: bborbe/git-rest

Filed automatically by the build-fix agent for the CI episode `87a36883b3f6cad0927e360b115e4a98a19d7777`.

## Summary

The default-branch build for `bborbe/git-rest` is failing; the build-fix diagnosis classified this as a code/test bug (verdict `file_spec`).

## Reproduction

Failing workflow(s): test

Episode SHA: `87a36883b3f6cad0927e360b115e4a98a19d7777`

Log evidence:

```text
| Workflow | Job | Failed Step | Run |
|---|---|---|---|
| CI | test | Run precommit checks | [Run](https://github.com/bborbe/git-rest/actions/runs/36545120297) |
```

## Expected vs Actual

**Expected:** green CI on the default branch.
**Actual:** `Test failure in pkg/memprof/reporter_test.go:89 - assertion 'Expected <int>: 0 to be > <int>: 0' indicates the code under test is not producing the expected live allocation count, a code/test bug in the repo rather than a dependency or vulnerability issue.`

## Why this is a bug

The default-branch build is the repository's quality gate; a red build blocks merges. Diagnosis: `Test failure in pkg/memprof/reporter_test.go:89 - assertion 'Expected <int>: 0 to be > <int>: 0' indicates the code under test is not producing the expected live allocation count, a code/test bug in the repo rather than a dependency or vulnerability issue.`
