---
status: completed
spec: [022-bug-coalesced-flush-discards-the-commit]
summary: Raised TestFlushDeadlineCut's elapsed bound from 6 s to 8 s with a rationale comment, corrected spec 022's Assumption 2 wording, and added a changelog entry; all verification passed.
execution_id: git-rest-exec-088-fix-flush-deadline-elapsed-bound
dark-factory-version: v0.198.7
created: "2026-10-24T21:35:48Z"
queued: "2026-10-24T21:50:41Z"
started: "2026-10-24T21:50:44Z"
completed: "2026-10-24T21:55:05Z"
---

# Fix the elapsed bound in TestFlushDeadlineCut

<summary>
- The rewritten `TestFlushDeadlineCut` is flaky on a correct build: the pull takes 5.6-6.3 s against a 6 s bound, so it fails most runs and passes on a fast one.
- The extra time is the delivery push the retained commit now makes — pre-fix the flush's reset left nothing ahead, so no push ran in that cycle.
- The bound rises to 8 s, which still discriminates a cut flush (10 s pull context, 15 s hook stall) from an uncut one.
- The assertion gains a comment naming that basis, so a later reader does not lower it back.
- Spec 022's Assumption 2 wording is corrected: the rewritten test keeps an elapsed bound, not the original value.
- No production code changes; the fix is test-only.
</summary>

<objective>
Make `TestFlushDeadlineCut` pass on a correct build by raising its elapsed bound to a value that still proves the flush was cut, and record why the bound moved so the next reader does not treat it as a loosened assertion.
</objective>

<context>
Read `pkg/git/flush_deadline_test.go` — `TestFlushDeadlineCut`, its `installFirstPushRejection` helper, and the `elapsed` assertion at the end of the body.

Read `specs/in-progress/022-bug-coalesced-flush-discards-the-commit.md` § Assumptions, second bullet.

Measured on the current tree: `go test -mod=mod ./pkg/git/ -run 'TestFlushDeadlineCut' -count=1` fails intermittently — five runs measured 5.63-5.83 s (passing) and failures reported `flush was not bounded: pull took 6.09s` and `6.248003584s`, so the 6 s bound sits inside the distribution.
</context>

<requirements>
## 1. Raise the elapsed bound (`pkg/git/flush_deadline_test.go`)

1.1 In `TestFlushDeadlineCut`, replace the final assertion:

```go
	if elapsed >= 6*time.Second {
		t.Fatalf("flush was not bounded: pull took %v", elapsed)
	}
```

with:

```go
	// 8s, not the 6s this test carried before the retained-commit change: the
	// cut baseline is the 3.3s flush third plus the 2s gitWaitDelay drain, and the
	// pull now also delivers the retained commit in the same cycle — a push the
	// pre-fix flow never spent. The bound still discriminates: an uncut flush
	// runs into the 10s pull context (measured 13.5s with the drain), and the
	// hook stalls the first push for up to 15s. Keep it below 10s.
	if elapsed >= 8*time.Second {
		t.Fatalf("flush was not bounded: pull took %v", elapsed)
	}
```

1.2 Change nothing else in the file: keep `installFirstPushRejection`, the other assertions, and `TestFlushHealthyRemoteUnchanged` exactly as they are.

## 2. Correct spec 022's Assumption 2 wording

2.1 In `specs/in-progress/022-bug-coalesced-flush-discards-the-commit.md` § Assumptions, the sentence describing the rewritten `TestFlushDeadlineCut` says it keeps "the elapsed-time bound". Replace that phrase with "an elapsed-time bound (raised from 6 s to 8 s, because the retained commit now makes a delivery push the pre-fix flow never spent)".

2.2 Do not change any other part of the spec — not its Acceptance Criteria, its Verification, its frontmatter, or its status.

## 3. Self-check

3.1 Run `go test -mod=mod ./pkg/git/ -run 'TestFlush' -count=1` and confirm it exits 0.
3.2 Confirm the change touches only `pkg/git/flush_deadline_test.go` and `specs/in-progress/022-bug-coalesced-flush-discards-the-commit.md` — `grep -c 'elapsed >= 8\*time.Second' pkg/git/flush_deadline_test.go` prints `1` and `grep -c 'raised from 6 s to 8 s' specs/in-progress/022-bug-coalesced-flush-discards-the-commit.md` prints `1`.
</requirements>

<constraints>
- Test-only change plus a spec-doc wording fix. Do not touch `pkg/git/git.go` or any other production file.
- Do not weaken any other assertion in `TestFlushDeadlineCut`, and do not delete the bound.
- Do not add a new Prometheus series, flag, or config field.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
```
go test -mod=mod ./pkg/git/ -run 'TestFlush' -count=1
go test -mod=mod -timeout 20m ./pkg/git/ -count=1
make precommit
```
All must exit 0.
</verification>
