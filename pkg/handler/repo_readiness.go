// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import "os"

// NewRepoReadiness returns a ReadinessStateReader that reports ready whenever
// repoPath exists and can be stat'ed. It consults no pull, push, fetch or merge
// state, so a failed sync attempt never changes its verdict.
func NewRepoReadiness(repoPath string) ReadinessStateReader {
	return &repoReadiness{repoPath: repoPath}
}

type repoReadiness struct {
	repoPath string
}

// ReadinessStatus returns (true, "ok") when the served repository path is
// readable, or (false, "repo path unreadable") when it is missing or
// unreadable. The reason never contains a host path.
func (r *repoReadiness) ReadinessStatus() (bool, string) {
	if _, err := os.Stat(r.repoPath); err != nil {
		return false, "repo path unreadable"
	}
	return true, "ok"
}

var _ ReadinessStateReader = (*repoReadiness)(nil)
