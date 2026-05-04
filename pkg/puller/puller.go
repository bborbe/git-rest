// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package puller

import (
	"context"
	"log/slog"
	"time"

	libtime "github.com/bborbe/time"

	"github.com/bborbe/git-rest/pkg/git"
)

//counterfeiter:generate -o ../../mocks/puller.go --fake-name FakePuller . Puller

// Puller periodically runs git pull on a repository.
type Puller interface {
	Run(ctx context.Context) error
}

// New returns a Puller that calls g.Pull on the given interval.
// pullTimeout bounds each individual pull subprocess; 0 means no timeout.
// state receives the outcome of every pull attempt.
func New(
	g git.Git,
	interval libtime.Duration,
	pullTimeout time.Duration,
	state PullStateWriter,
) Puller {
	return &puller{
		git:         g,
		interval:    interval,
		pullTimeout: pullTimeout,
		state:       state,
	}
}

type puller struct {
	git         git.Git
	interval    libtime.Duration
	pullTimeout time.Duration
	state       PullStateWriter
}

// Run starts the periodic pull loop. It returns when ctx is cancelled.
func (p *puller) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Duration(p.interval))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			p.runOnce(ctx)
		}
	}
}

// runOnce executes one pull attempt, bounded by pullTimeout, and records the outcome.
func (p *puller) runOnce(ctx context.Context) {
	pullCtx := ctx
	var cancel context.CancelFunc
	if p.pullTimeout > 0 {
		pullCtx, cancel = context.WithTimeout(ctx, p.pullTimeout)
		defer cancel()
	}

	err := p.git.Pull(pullCtx)
	p.state.RecordPull(err)
	if err != nil {
		slog.WarnContext(ctx, "git pull failed", "error", err)
	}
}
