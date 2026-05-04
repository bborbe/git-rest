// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package puller

import (
	"fmt"
	"sync"
	"time"

	libtime "github.com/bborbe/time"
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
	lastSuccessAt      libtime.DateTime // zero = no successful pull yet
	lastErr            error            // nil = last pull succeeded
	freshnessThreshold time.Duration
}

// RecordPull records the outcome of one pull attempt at the current time.
// If err is nil the success timestamp is updated; otherwise only the error is stored.
func (c *PullStateCache) RecordPull(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil {
		c.lastSuccessAt = c.currentDateTime.Now()
	}
	c.lastErr = err
}

// ReadinessStatus returns (true, "ok") when the cache is healthy, or
// (false, reason) when the pod should not receive traffic.
//
// Rules (checked in order):
//  1. lastSuccessAt is zero → "no successful pull yet"
//  2. time since lastSuccessAt > freshnessThreshold → stale; include last error if any
//  3. Otherwise → ready
func (c *PullStateCache) ReadinessStatus() (bool, string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
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
