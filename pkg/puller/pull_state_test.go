// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package puller_test

import (
	"errors"
	"time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

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
})
