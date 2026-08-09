// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git

import (
	"bytes"
	"context"
	"os/exec"
	"strings"

	"github.com/bborbe/errors"
)

// ConflictResolver is called by the puller when git merge produces unresolvable conflicts.
// Resolve receives the list of conflicted file paths as reported by git merge output.
// It must stage each path (git add) before returning nil so the puller can commit the merge.
// On error the puller runs git merge --abort and returns ErrConflictResolutionFailed.
//
//counterfeiter:generate -o ../../mocks/conflict_resolver.go --fake-name FakeConflictResolver . ConflictResolver
type ConflictResolver interface {
	Resolve(ctx context.Context, conflictedPaths []string) error
}

// NewMarkerResolver returns a ConflictResolver that preserves git's standard conflict markers.
// It stages each conflicted file as-is: the <<<<<<< / ======= / >>>>>>> markers written by
// git's three-way merge are left intact in the committed file. The next human or agent edit
// resolves them naturally.
func NewMarkerResolver(repoPath string) ConflictResolver {
	return &markerResolver{repoPath: repoPath}
}

type markerResolver struct {
	repoPath string
}

// Resolve stages each conflicted path without modifying its content. git add is sufficient
// because git's three-way merge already wrote the marker-annotated content to disk.
func (r *markerResolver) Resolve(ctx context.Context, conflictedPaths []string) error {
	for _, path := range conflictedPaths {
		select {
		case <-ctx.Done():
			return errors.Wrap(ctx, ctx.Err(), "context cancelled")
		default:
		}
		// #nosec G204 -- git binary is hardcoded; paths come from git merge output, not user input
		cmd := exec.CommandContext(ctx, "git", "add", "--", path)
		cmd.Dir = r.repoPath
		var buf bytes.Buffer
		cmd.Stdout = &buf
		cmd.Stderr = &buf
		if err := cmd.Run(); err != nil {
			return errors.Wrapf(ctx, err, "git add %s: %s", path, strings.TrimSpace(buf.String()))
		}
	}
	return nil
}
