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
		fakeGit *gitmocks.FakeGit
		p       puller.Puller
	)

	BeforeEach(func() {
		fakeGit = &gitmocks.FakeGit{}
	})

	Describe("New", func() {
		It("returns a Puller", func() {
			p = puller.New(fakeGit, libtime.Duration(10*time.Millisecond), 0)
			Expect(p).NotTo(BeNil())
		})
	})

	Describe("Run", func() {
		BeforeEach(func() {
			p = puller.New(fakeGit, libtime.Duration(10*time.Millisecond), 0)
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
		It("bounds each pull with pullTimeout", func() {
			var observedDeadline bool
			fakeGit.PullStub = func(ctx context.Context) error {
				_, observedDeadline = ctx.Deadline()
				<-ctx.Done()
				return ctx.Err()
			}
			p = puller.New(fakeGit, libtime.Duration(10*time.Millisecond), 10*time.Millisecond)

			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			_ = p.Run(ctx)

			Expect(fakeGit.PullCallCount()).To(BeNumerically(">=", 1))
			Expect(observedDeadline).To(BeTrue())
		})
	})
})
