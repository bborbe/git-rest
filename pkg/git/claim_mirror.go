// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/bborbe/errors"
)

// claimMirrorNamespace is the local namespace the claims refspec writes.
const claimMirrorNamespace = "refs/claim-mirror/"

// claimRefNamespace is the origin namespace mirrored into claimMirrorNamespace.
const claimRefNamespace = "refs/" + claimKeyPrefix

// claimMirrorRefspec carries the claim namespace into a mirror namespace the
// default heads refspec cannot reach.
const claimMirrorRefspec = "+refs/claims/*:" + claimMirrorNamespace + "*"

const (
	// claimMirrorStalenessFactor multiplies the pull interval to derive the
	// staleness bound.
	claimMirrorStalenessFactor = 2
	// claimMirrorMinStaleness is the floor of the staleness bound.
	claimMirrorMinStaleness = 60 * time.Second
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
func (g *git) claimMirrorFreshAtTime() time.Time {
	g.claimMirrorMu.RLock()
	defer g.claimMirrorMu.RUnlock()
	return g.claimMirrorFreshAt
}

// claimMirrorAge reports the age of the last claims-specific fetch and whether
// the mirror is fresh enough to serve. A never-recorded time, or one older than
// the staleness bound, reports false. A negative age (wall clock stepped
// backwards) is clamped to zero so it cannot be mistaken for stale.
func (g *git) claimMirrorAge() (time.Duration, bool) {
	freshAt := g.claimMirrorFreshAtTime()
	if freshAt.IsZero() {
		return 0, false
	}
	age := time.Since(freshAt)
	if age < 0 {
		age = 0
	}
	if age > g.claimMirrorStalenessBound() {
		return 0, false
	}
	return age, true
}

// listClaimRefsFromMirror lists claim refs from the local mirror. served=false
// means the caller must take the live path; it is returned for every condition
// that would make a mirror answer untrustworthy, and never together with a
// partial result.
func (g *git) listClaimRefsFromMirror(
	ctx context.Context,
	prefix string,
) (claims []ClaimRef, served bool, err error) {
	out, err := g.runCmdOutput(
		ctx,
		g.repoPath,
		"for-each-ref",
		"--format=%(objectname) %(refname)",
		claimMirrorNamespace,
	)
	if err != nil {
		slog.WarnContext(
			ctx,
			"git-rest: listing the local claim mirror failed; using the live origin",
			"error", err,
		)
		return nil, false, nil
	}
	matched := make([]RemoteRef, 0)
	for _, line := range strings.Split(string(out), "\n") {
		sha, ref, found := strings.Cut(line, " ")
		if !found {
			continue
		}
		claimRef := claimRefNamespace + strings.TrimPrefix(ref, claimMirrorNamespace)
		if !matchesClaimPrefix(claimRef, prefix) {
			continue
		}
		matched = append(matched, RemoteRef{SHA: sha, Ref: claimRef})
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].Ref < matched[j].Ref })
	if len(matched) == 0 {
		return []ClaimRef{}, true, nil
	}
	result := make([]ClaimRef, 0, len(matched))
	for _, entry := range matched {
		select {
		case <-ctx.Done():
			return nil, false, errors.Wrap(ctx, ctx.Err(), "claim ref listing cancelled")
		default:
		}
		message, readErr := g.origin.ReadCommitMessage(ctx, entry.SHA)
		if readErr != nil {
			slog.WarnContext(
				ctx,
				"git-rest: reading a mirrored claim commit failed; using the live origin",
				"ref", entry.Ref,
				"sha", entry.SHA,
				"error", readErr,
			)
			return nil, false, nil
		}
		claim := ClaimRef{Ref: entry.Ref, SHA: entry.SHA}
		cluster, taskID, leaseUntil, parseErr := parseClaimMetadata(ctx, message)
		if parseErr != nil {
			slog.WarnContext(
				ctx,
				"claim ref has unparseable metadata",
				"ref", entry.Ref,
				"sha", entry.SHA,
				"error", parseErr,
			)
			result = append(result, claim)
			continue
		}
		claim.Cluster = cluster
		claim.TaskID = taskID
		claim.LeaseUntil = leaseUntil
		result = append(result, claim)
	}
	return result, true, nil
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
