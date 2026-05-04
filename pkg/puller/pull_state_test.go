// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package puller_test

import (
	"errors"
	"fmt"
	"time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/git-rest/pkg/git"
	"github.com/bborbe/git-rest/pkg/puller"
)

var _ = Describe("PullStateCache", func() {
	const freshness = 90 * time.Second
	var (
		clock libtime.CurrentDateTime
		t0    libtime.DateTime
	)

	BeforeEach(func() {
		clock = libtime.NewCurrentDateTime()
		t0 = libtime.DateTime(time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC))
		clock.SetNow(t0)
	})

	Describe("cold start (no pull yet)", func() {
		It("returns not-ready with 'no successful pull yet'", func() {
			cache := puller.NewPullStateCache(clock, freshness)
			ready, reason := cache.ReadinessStatus()
			Expect(ready).To(BeFalse())
			Expect(reason).To(Equal("no successful pull yet"))
		})
	})

	Describe("after a successful pull", func() {
		It("returns ready", func() {
			cache := puller.NewPullStateCache(clock, freshness)
			cache.RecordPull(nil)
			ready, reason := cache.ReadinessStatus()
			Expect(ready).To(BeTrue())
			Expect(reason).To(Equal("ok"))
		})
	})

	Describe("after a failed pull (first ever)", func() {
		It("returns not-ready with 'no successful pull yet'", func() {
			cache := puller.NewPullStateCache(clock, freshness)
			cache.RecordPull(errors.New("ssh timeout"))
			ready, reason := cache.ReadinessStatus()
			Expect(ready).To(BeFalse())
			Expect(reason).To(Equal("no successful pull yet"))
		})
	})

	Describe("stale cache (last success beyond freshness threshold)", func() {
		It("returns not-ready with stale message", func() {
			cache := puller.NewPullStateCache(clock, freshness)
			cache.RecordPull(nil)
			// advance clock past freshness window
			clock.SetNow(libtime.DateTime(time.Time(t0).Add(2 * freshness)))
			ready, reason := cache.ReadinessStatus()
			Expect(ready).To(BeFalse())
			Expect(reason).To(ContainSubstring("last successful pull stale"))
			Expect(reason).NotTo(ContainSubstring("context canceled"))
		})

		It("includes last pull error in stale message when a later pull failed", func() {
			cache := puller.NewPullStateCache(clock, freshness)
			cache.RecordPull(nil)
			clock.SetNow(libtime.DateTime(time.Time(t0).Add(2 * freshness)))
			cache.RecordPull(
				errors.New("ssh: connect to host github.com port 22: Operation timed out"),
			)
			ready, reason := cache.ReadinessStatus()
			Expect(ready).To(BeFalse())
			Expect(reason).To(ContainSubstring("last pull failed"))
			Expect(reason).To(ContainSubstring("ssh"))
			Expect(reason).NotTo(ContainSubstring("context canceled"))
		})
	})

	Describe("zero freshness threshold", func() {
		It("never expires after first success", func() {
			cache := puller.NewPullStateCache(clock, 0)
			cache.RecordPull(nil)
			clock.SetNow(libtime.DateTime(time.Time(t0).Add(24 * time.Hour)))
			ready, _ := cache.ReadinessStatus()
			Expect(ready).To(BeTrue())
		})
	})

	Describe("rebase conflict (immediate 503)", func() {
		Context("after a prior success, followed by a rebase conflict", func() {
			It("returns not-ready immediately with conflict path", func() {
				cache := puller.NewPullStateCache(clock, freshness)
				cache.RecordPull(nil) // prior success
				cache.RecordPull(&git.RebaseConflictError{Path: "tasks/foo.md"})
				ready, reason := cache.ReadinessStatus()
				Expect(ready).To(BeFalse())
				Expect(reason).To(Equal("last pull failed: rebase conflict at tasks/foo.md"))
			})

			It("returns 503 before the freshness threshold expires", func() {
				cache := puller.NewPullStateCache(clock, freshness)
				cache.RecordPull(nil)
				// Conflict immediately after success — clock NOT advanced
				cache.RecordPull(&git.RebaseConflictError{Path: "tasks/foo.md"})
				ready, _ := cache.ReadinessStatus()
				Expect(ready).To(BeFalse())
			})

			It("body does not contain the git config hint substring", func() {
				cache := puller.NewPullStateCache(clock, freshness)
				cache.RecordPull(nil)
				cache.RecordPull(&git.RebaseConflictError{Path: "tasks/foo.md"})
				_, reason := cache.ReadinessStatus()
				Expect(
					reason,
				).NotTo(ContainSubstring("Need to specify how to reconcile divergent branches"))
			})

			It("body does not contain 'context canceled'", func() {
				cache := puller.NewPullStateCache(clock, freshness)
				cache.RecordPull(nil)
				cache.RecordPull(&git.RebaseConflictError{Path: "tasks/foo.md"})
				_, reason := cache.ReadinessStatus()
				Expect(reason).NotTo(ContainSubstring("context canceled"))
			})
		})

		Context("first-ever pull is a rebase conflict (no prior success)", func() {
			It("returns not-ready with conflict path (not 'no successful pull yet')", func() {
				cache := puller.NewPullStateCache(clock, freshness)
				cache.RecordPull(&git.RebaseConflictError{Path: "tasks/bar.md"})
				ready, reason := cache.ReadinessStatus()
				Expect(ready).To(BeFalse())
				Expect(reason).To(Equal("last pull failed: rebase conflict at tasks/bar.md"))
			})
		})

		Context("after conflict, a subsequent successful pull clears the error", func() {
			It("returns ready after the conflict is resolved", func() {
				cache := puller.NewPullStateCache(clock, freshness)
				cache.RecordPull(nil)
				cache.RecordPull(&git.RebaseConflictError{Path: "tasks/foo.md"})
				// Human resolves the conflict; next pull tick succeeds
				cache.RecordPull(nil)
				ready, reason := cache.ReadinessStatus()
				Expect(ready).To(BeTrue())
				Expect(reason).To(Equal("ok"))
			})
		})

		Context("transient error (non-conflict) within freshness window", func() {
			It("still returns ready (freshness-threshold behavior unchanged)", func() {
				cache := puller.NewPullStateCache(clock, freshness)
				cache.RecordPull(nil)
				// Transient network error — NOT a RebaseConflictError
				cache.RecordPull(
					fmt.Errorf("ssh: connect to host github.com port 22: Connection refused"),
				)
				ready, _ := cache.ReadinessStatus()
				Expect(ready).To(BeTrue())
			})
		})

		Context("conflict followed by transient error (sticky)", func() {
			It("still surfaces the conflict path on readiness", func() {
				cache := puller.NewPullStateCache(clock, freshness)
				cache.RecordPull(nil)
				cache.RecordPull(&git.RebaseConflictError{Path: "tasks/foo.md"})
				cache.RecordPull(fmt.Errorf("ssh: connection refused"))
				ready, reason := cache.ReadinessStatus()
				Expect(ready).To(BeFalse())
				Expect(reason).To(Equal("last pull failed: rebase conflict at tasks/foo.md"))
			})

			It("clears the sticky conflict only on a subsequent successful pull", func() {
				cache := puller.NewPullStateCache(clock, freshness)
				cache.RecordPull(&git.RebaseConflictError{Path: "tasks/foo.md"})
				cache.RecordPull(fmt.Errorf("network error"))
				cache.RecordPull(nil)
				ready, reason := cache.ReadinessStatus()
				Expect(ready).To(BeTrue())
				Expect(reason).To(Equal("ok"))
			})
		})
	})
})
