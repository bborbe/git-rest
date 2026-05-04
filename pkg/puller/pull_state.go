// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package puller

import (
	stderrors "errors"
	"fmt"
	"sync"
	"time"

	libtime "github.com/bborbe/time"

	"github.com/bborbe/git-rest/pkg/git"
)

//counterfeiter:generate -o ../../mocks/pull_state_writer.go --fake-name FakePullStateWriter . PullStateWriter

// PullStateWriter is the write side of the pull outcome cache.
// The puller calls RecordPull after every pull attempt.
type PullStateWriter interface {
	RecordPull(err error)
}

// NewPullStateCache returns a PullStateCache with the given freshness threshold.
// Threshold is typically 3 × PullInterval.
// A zero threshold disables freshness checking (always fresh after first success).
// currentDateTime is the time source — pass libtime.NewCurrentDateTime() in production.
func NewPullStateCache(
	currentDateTime libtime.CurrentDateTimeGetter,
	freshnessThreshold time.Duration,
) *PullStateCache {
	return &PullStateCache{
		currentDateTime:    currentDateTime,
		freshnessThreshold: freshnessThreshold,
	}
}

// PullStateCache stores the outcome of the most recent pull attempts.
// It implements both PullStateWriter (written by the puller) and
// ReadinessStatus (read by the readiness handler — see pkg/handler).
// Safe for concurrent use.
type PullStateCache struct {
	mu                 sync.RWMutex
	currentDateTime    libtime.CurrentDateTimeGetter
	lastSuccessAt      libtime.DateTime         // zero = no successful pull yet
	lastErr            error                    // most recent error (transient or conflict); nil = last pull succeeded
	lastConflict       *git.RebaseConflictError // sticky: cleared only by a successful pull
	freshnessThreshold time.Duration
}

// RecordPull records the outcome of one pull attempt at the current time.
// On success: lastSuccessAt is updated, lastErr is cleared, AND lastConflict is cleared.
// On rebase conflict: lastConflict is set (sticky until next success), lastErr also stores it.
// On transient error: lastErr is updated; lastConflict is NOT touched (preserves conflict visibility
// in readiness body across intervening transient errors).
func (c *PullStateCache) RecordPull(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err == nil {
		c.lastSuccessAt = c.currentDateTime.Now()
		c.lastErr = nil
		c.lastConflict = nil
		return
	}

	c.lastErr = err

	var conflictErr *git.RebaseConflictError
	if stderrors.As(err, &conflictErr) {
		c.lastConflict = conflictErr
	}
	// Transient errors (non-conflict) do NOT clear lastConflict.
}

// ReadinessStatus returns (true, "ok") when the cache is healthy, or
// (false, reason) when the pod should not receive traffic.
//
// Rules (checked in order):
//  1. lastConflict is set → immediate 503 naming the conflict path (sticky until resolved by success)
//  2. lastSuccessAt is zero → "no successful pull yet"
//  3. time since lastSuccessAt > freshnessThreshold → stale; include last error if any
//  4. Otherwise → ready
func (c *PullStateCache) ReadinessStatus() (bool, string) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Rebase conflicts are hard errors: return 503 immediately, before the
	// freshness window expires. Sticky — only a successful pull clears this.
	if c.lastConflict != nil {
		return false, "last pull failed: rebase conflict at " + c.lastConflict.Path
	}

	if time.Time(c.lastSuccessAt).IsZero() {
		return false, "no successful pull yet"
	}
	age := time.Time(c.currentDateTime.Now()).Sub(time.Time(c.lastSuccessAt))
	if c.freshnessThreshold > 0 && age > c.freshnessThreshold {
		msg := fmt.Sprintf("last successful pull stale (%v ago)", age.Round(time.Second))
		if c.lastErr != nil {
			msg += ": last pull failed: " + c.lastErr.Error()
		}
		return false, msg
	}
	return true, "ok"
}
