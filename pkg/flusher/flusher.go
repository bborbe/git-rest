// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package flusher

import (
	"context"
	"log/slog"
	"time"

	libtime "github.com/bborbe/time"

	"github.com/bborbe/git-rest/pkg/git"
)

// Flusher periodically flushes due pending writes on a git client.
type Flusher interface {
	Run(ctx context.Context) error
}

// New returns a Flusher that ticks on tickInterval and flushes when the git
// client reports the pending set is due.
func New(g git.Git, tickInterval libtime.Duration) Flusher {
	return &flusher{git: g, tickInterval: tickInterval}
}

type flusher struct {
	git          git.Git
	tickInterval libtime.Duration
}

// Run starts the flush loop. It returns when ctx is cancelled.
func (f *flusher) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Duration(f.tickInterval))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := f.git.FlushPendingIfDue(ctx); err != nil {
				slog.WarnContext(ctx, "git-rest: flush pending writes failed", "error", err)
			}
		}
	}
}
