// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package puller_test

import (
	"context"
	"errors"
	"time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	gitmocks "github.com/bborbe/git-rest/mocks"
	"github.com/bborbe/git-rest/pkg/puller"
)

var _ = Describe("Puller", func() {
	var (
		fakeGit       *gitmocks.FakeGit
		fakePullState *gitmocks.FakePullStateWriter
		p             puller.Puller
	)

	BeforeEach(func() {
		fakeGit = &gitmocks.FakeGit{}
		fakePullState = &gitmocks.FakePullStateWriter{}
	})

	Describe("New", func() {
		It("returns a Puller", func() {
			p = puller.New(fakeGit, libtime.Duration(10*time.Millisecond), 0, fakePullState)
			Expect(p).NotTo(BeNil())
		})
	})

	Describe("Run", func() {
		BeforeEach(func() {
			p = puller.New(fakeGit, libtime.Duration(10*time.Millisecond), 0, fakePullState)
		})

		It("calls Pull on each tick and stops when context is cancelled", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
			defer cancel()

			err := p.Run(ctx)
			Expect(err).To(Equal(context.DeadlineExceeded))
			Expect(fakeGit.PullCallCount()).To(BeNumerically(">=", 1))
		})

		It("continues running after a Pull error", func() {
			pullErr := errors.New("pull failed")
			callCount := 0
			fakeGit.PullStub = func(ctx context.Context) error {
				callCount++
				if callCount == 1 {
					return pullErr
				}
				return nil
			}

			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
			defer cancel()

			err := p.Run(ctx)
			Expect(err).To(Equal(context.DeadlineExceeded))
			Expect(fakeGit.PullCallCount()).To(BeNumerically(">=", 2))
		})

		It("returns ctx.Err() when context is cancelled", func() {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			err := p.Run(ctx)
			Expect(err).To(Equal(context.Canceled))
		})
	})

	Describe("runOnce via Run", func() {
		It("records success when Pull returns nil", func() {
			fakeGit.PullStub = func(ctx context.Context) error { return nil }
			p = puller.New(fakeGit, libtime.Duration(10*time.Millisecond), 0, fakePullState)

			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
			defer cancel()
			_ = p.Run(ctx)

			Expect(fakePullState.RecordPullCallCount()).To(BeNumerically(">=", 1))
			Expect(fakePullState.RecordPullArgsForCall(0)).To(BeNil())
		})

		It("records failure when Pull returns an error", func() {
			pullErr := errors.New("network unreachable")
			fakeGit.PullStub = func(ctx context.Context) error { return pullErr }
			p = puller.New(fakeGit, libtime.Duration(10*time.Millisecond), 0, fakePullState)

			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
			defer cancel()
			_ = p.Run(ctx)

			Expect(fakePullState.RecordPullCallCount()).To(BeNumerically(">=", 1))
			Expect(fakePullState.RecordPullArgsForCall(0)).To(MatchError(pullErr))
		})

		It("timeout aborts pull and records non-nil error", func() {
			fakeGit.PullStub = func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			}
			pullTimeout := 10 * time.Millisecond
			p = puller.New(
				fakeGit,
				libtime.Duration(10*time.Millisecond),
				pullTimeout,
				fakePullState,
			)

			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			_ = p.Run(ctx)

			Expect(fakePullState.RecordPullCallCount()).To(BeNumerically(">=", 1))
			Expect(fakePullState.RecordPullArgsForCall(0)).NotTo(BeNil())
		})

		It("wires RecordPull into PullStateCache correctly", func() {
			fakeGit.PullStub = func(ctx context.Context) error { return nil }
			cache := puller.NewPullStateCache(libtime.NewCurrentDateTime(), time.Hour)
			p = puller.New(fakeGit, libtime.Duration(10*time.Millisecond), 0, cache)

			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
			defer cancel()
			_ = p.Run(ctx)

			ready, reason := cache.ReadinessStatus()
			Expect(ready).To(BeTrue())
			Expect(reason).To(Equal("ok"))
		})
	})
})
