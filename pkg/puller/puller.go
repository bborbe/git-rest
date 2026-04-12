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
func New(g git.Git, interval libtime.Duration) Puller {
	return &puller{
		git:      g,
		interval: interval,
	}
}

type puller struct {
	git      git.Git
	interval libtime.Duration
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
			if err := p.git.Pull(ctx); err != nil {
				slog.WarnContext(ctx, "git pull failed", "error", err)
			}
		}
	}
}
