// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git

import (
	"context"

	"github.com/bborbe/git-rest/pkg/metrics"
)

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
