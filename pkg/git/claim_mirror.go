// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git

import (
	"context"
	"strings"

	"github.com/bborbe/errors"
)

// claimMirrorNamespace is the local namespace the claims refspec writes.
const claimMirrorNamespace = "refs/claim-mirror/"

// claimMirrorRefspec carries the claim namespace into a mirror namespace the
// default heads refspec cannot reach.
const claimMirrorRefspec = "+refs/claims/*:" + claimMirrorNamespace + "*"

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

// hasConfigLine reports whether output contains want as a whole line.
func hasConfigLine(output []byte, want string) bool {
	for _, line := range strings.Split(string(output), "\n") {
		if strings.TrimSuffix(line, "\r") == want {
			return true
		}
	}
	return false
}
