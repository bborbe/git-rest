// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git

import (
	"context"
	"time"

	"github.com/bborbe/git-rest/pkg/metrics"
)

// CleanupStaleLocksForCycle exposes the per-cycle lock walk with an injectable
// liveness probe, so external tests can drive the undetermined branch without
// package-level mutable state. Test builds only.
func CleanupStaleLocksForCycle(
	repoPath string,
	probe func(context.Context, string) (bool, bool),
) func(context.Context) error {
	g := &git{repoPath: repoPath}
	return func(ctx context.Context) error {
		return g.cleanupStaleLocks(ctx, probe)
	}
}

// LockHeldByProcess exposes the liveness probe for direct tests. Test builds only.
var LockHeldByProcess = lockHeldByProcess

// RetryPushForTest exposes retryPush so external tests can drive the
// push-retry merge path directly. Forcing a real non-fast-forward rejection
// deterministically is a race, so the path is exercised here instead.
func RetryPushForTest(ctx context.Context, g Git, upstream string, nextAttempt int) error {
	impl, ok := g.(*git)
	if !ok {
		panic("RetryPushForTest: Git is not a *git")
	}
	return impl.retryPush(ctx, upstream, nextAttempt)
}

// PushBackoffForTest exposes pushBackoff so external tests can assert the
// exponential delay sequence and its jitter bounds. Test builds only.
func PushBackoffForTest(attempt int) time.Duration {
	return pushBackoff(attempt)
}

// SleepPushBackoffForTest exposes sleepPushBackoff so external tests can prove
// the waiter is wired to the attempt it is given. Test builds only.
func SleepPushBackoffForTest(ctx context.Context, attempt int) error {
	return sleepPushBackoff(ctx, attempt)
}

// NewGitWithClaimOrigin returns a Git whose claim-ref listing talks to origin
// instead of the git binary. Test builds only.
func NewGitWithClaimOrigin(origin ClaimOrigin) Git {
	return &git{origin: origin}
}

// NewGitWithClaimMirrorForTest returns a Git backed by repoPath whose
// origin-contacting operations go to origin. Test builds only.
func NewGitWithClaimMirrorForTest(repoPath string, origin ClaimOrigin) Git {
	return &git{repoPath: repoPath, origin: origin}
}

// NewYAMLMergeResolverWithSideTimes exposes a resolver whose merge-parent
// committer timestamps are stubbed, so external tests can choose the newer side
// deterministically. Test builds only.
func NewYAMLMergeResolverWithSideTimes(
	repoPath string,
	m metrics.Metrics,
	ours, theirs int64,
) ConflictResolver {
	return &yamlMergeResolver{
		repoPath: repoPath,
		metrics:  m,
		sideTimes: func(context.Context) (int64, int64, error) {
			return ours, theirs, nil
		},
	}
}
