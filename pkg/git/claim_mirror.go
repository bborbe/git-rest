// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git

import (
	"context"
	"strings"
	"time"

	"github.com/bborbe/errors"
)

// claimMirrorNamespace is the local namespace the claims refspec writes.
const claimMirrorNamespace = "refs/claim-mirror/"

// claimMirrorRefspec carries the claim namespace into a mirror namespace the
// default heads refspec cannot reach.
const claimMirrorRefspec = "+refs/claims/*:" + claimMirrorNamespace + "*"

const (
	// claimMirrorStalenessFactor multiplies the pull interval to derive the
	// staleness bound.
	claimMirrorStalenessFactor = 2 //nolint:unused // consumed by the mirror read path
	// claimMirrorMinStaleness is the floor of the staleness bound.
	claimMirrorMinStaleness = 60 * time.Second //nolint:unused // consumed by the mirror read path
)

// ConfigureClaimMirror makes the clone's origin fetch carry the claim namespace
// into refs/claim-mirror/* and enables pruning, so the puller's existing fetch
// refreshes the mirror. It is idempotent: a second call adds no duplicate entry.
// A failure is returned to the caller, which logs it and keeps serving.
func (g *git) ConfigureClaimMirror(ctx context.Context) error {
	// `git config --get-all` exits non-zero when the key is absent. That is not
	// an error for this step: it means there are no entries to compare against.
	current, _ := g.runCmdRaw(ctx, g.repoPath, "config", "--get-all", "remote.origin.fetch")
	if !hasConfigLine(current, claimMirrorRefspec) {
		if err := g.runCmd(
			ctx,
			g.repoPath,
			"config",
			"--add",
			"remote.origin.fetch",
			claimMirrorRefspec,
		); err != nil {
			return errors.Wrap(ctx, err, "add claim mirror fetch refspec")
		}
	}
	if err := g.runCmd(
		ctx,
		g.repoPath,
		"config",
		"remote.origin.prune",
		"true",
	); err != nil {
		return errors.Wrap(ctx, err, "enable remote.origin.prune")
	}
	return nil
}

// claimMirrorStalenessBound returns twice the configured pull interval, with a
// floor of 60 seconds. It is derived, never configured separately.
//
//nolint:unused // consumed by the mirror read path
func (g *git) claimMirrorStalenessBound() time.Duration {
	bound := claimMirrorStalenessFactor * time.Duration(g.pullInterval)
	if bound < claimMirrorMinStaleness {
		return claimMirrorMinStaleness
	}
	return bound
}

// hasClaimRefspec reports whether the clone's origin fetch currently carries
// the claim mirror refspec.
func (g *git) hasClaimRefspec(ctx context.Context) bool {
	out, err := g.runCmdRaw(ctx, g.repoPath, "config", "--get-all", "remote.origin.fetch")
	if err != nil {
		return false
	}
	return hasConfigLine(out, claimMirrorRefspec)
}

// recordClaimMirrorFetch advances the last claims-specific fetch time. It is
// called only after a successful git fetch, and only when the claim refspec was
// present for that fetch: a clone that never got the refspec must never look
// fresh, otherwise it could answer an empty mirror authoritatively.
func (g *git) recordClaimMirrorFetch(ctx context.Context, at time.Time) {
	if !g.hasClaimRefspec(ctx) {
		return
	}
	g.claimMirrorMu.Lock()
	g.claimMirrorFreshAt = at
	g.claimMirrorMu.Unlock()
}

// claimMirrorFreshAtTime returns the recorded time, zero when never recorded.
//
//nolint:unused // consumed by the mirror read path
func (g *git) claimMirrorFreshAtTime() time.Time {
	g.claimMirrorMu.RLock()
	defer g.claimMirrorMu.RUnlock()
	return g.claimMirrorFreshAt
}

// hasConfigLine reports whether output contains want as a whole line.
func hasConfigLine(output []byte, want string) bool {
	for _, line := range strings.Split(string(output), "\n") {
		if strings.TrimSuffix(line, "\r") == want {
			return true
		}
	}
	return false
}
