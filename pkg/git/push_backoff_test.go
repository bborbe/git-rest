// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git_test

import (
	"context"
	"testing"
	"time"

	"github.com/bborbe/git-rest/pkg/git"
)

// TestPushBackoff asserts the exponential base sequence and the jitter bounds.
// Each case is sampled repeatedly so the jitter range is exercised, not a
// single draw.
func TestPushBackoff(t *testing.T) {
	cases := []struct {
		attempt int
		base    time.Duration
	}{
		{attempt: 2, base: 100 * time.Millisecond},
		{attempt: 3, base: 200 * time.Millisecond},
		{attempt: 4, base: 400 * time.Millisecond},
		{attempt: 5, base: 800 * time.Millisecond},
		{attempt: 1, base: 100 * time.Millisecond},
		{attempt: 0, base: 100 * time.Millisecond},
		{attempt: -3, base: 100 * time.Millisecond},
	}
	for _, tc := range cases {
		var minSeen, maxSeen time.Duration
		for i := 0; i < 200; i++ {
			got := git.PushBackoffForTest(tc.attempt)
			if got < tc.base {
				t.Fatalf("attempt %d: delay %v below base %v", tc.attempt, got, tc.base)
			}
			if got >= tc.base+tc.base {
				t.Fatalf(
					"attempt %d: delay %v exceeds jitter ceiling %v",
					tc.attempt,
					got,
					tc.base+tc.base,
				)
			}
			if i == 0 || got < minSeen {
				minSeen = got
			}
			if i == 0 || got > maxSeen {
				maxSeen = got
			}
		}
		if minSeen == maxSeen {
			t.Fatalf(
				"attempt %d: 200 samples all returned %v, expected jitter",
				tc.attempt,
				minSeen,
			)
		}
	}
}

// TestSleepPushBackoff asserts the wiring: the waiter uses the attempt it is
// given, and an already-cancelled context returns immediately.
func TestSleepPushBackoff(t *testing.T) {
	start := time.Now()
	if err := git.SleepPushBackoffForTest(context.Background(), 5); err != nil {
		t.Fatalf("sleep: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 800*time.Millisecond {
		t.Fatalf("attempt 5 waited %v, want at least 800ms", elapsed)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("attempt 5 waited %v, want under 2s", elapsed)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start = time.Now()
	err := git.SleepPushBackoffForTest(ctx, 5)
	if err == nil {
		t.Fatal("expected context error, got nil")
	}
	if elapsed := time.Since(start); elapsed >= 100*time.Millisecond {
		t.Fatalf("cancelled context took %v, want immediate return", elapsed)
	}
}
