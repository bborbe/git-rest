// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"

	libhttp "github.com/bborbe/http"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/git-rest/mocks"
	"github.com/bborbe/git-rest/pkg/handler"
)

var _ = Describe("ReadinessHandler", func() {
	var (
		fakeState *mocks.FakeReadinessStateReader
		h         libhttp.WithError
		rec       *httptest.ResponseRecorder
		ctx       context.Context
	)

	BeforeEach(func() {
		fakeState = &mocks.FakeReadinessStateReader{}
		h = handler.NewReadinessHandler(fakeState)
		rec = httptest.NewRecorder()
		ctx = context.Background()
	})

	Context("when ReadinessStatus returns ready", func() {
		BeforeEach(func() {
			fakeState.ReadinessStatusReturns(true, "ok")
		})

		It("returns 200", func() {
			req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
			err := h.ServeHTTP(ctx, rec, req)
			Expect(err).To(BeNil())
			Expect(rec.Code).To(Equal(http.StatusOK))
		})

		It("returns body 'ok'", func() {
			req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
			_ = h.ServeHTTP(ctx, rec, req)
			Expect(rec.Body.String()).To(Equal("ok"))
		})
	})

	Context("when ReadinessStatus returns not ready: no successful pull yet", func() {
		BeforeEach(func() {
			fakeState.ReadinessStatusReturns(false, "no successful pull yet")
		})

		It("returns 503", func() {
			req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
			err := h.ServeHTTP(ctx, rec, req)
			Expect(err).To(BeNil())
			Expect(rec.Code).To(Equal(http.StatusServiceUnavailable))
		})

		It("body contains 'no successful pull yet'", func() {
			req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
			_ = h.ServeHTTP(ctx, rec, req)
			Expect(rec.Body.String()).To(ContainSubstring("no successful pull yet"))
		})

		It("body does not contain 'context canceled'", func() {
			req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
			_ = h.ServeHTTP(ctx, rec, req)
			Expect(rec.Body.String()).NotTo(ContainSubstring("context canceled"))
		})
	})

	Context("when ReadinessStatus returns not ready: stale cache", func() {
		BeforeEach(func() {
			fakeState.ReadinessStatusReturns(
				false,
				"last successful pull stale (2m30s ago): last pull failed: ssh timeout",
			)
		})

		It("returns 503", func() {
			req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
			err := h.ServeHTTP(ctx, rec, req)
			Expect(err).To(BeNil())
			Expect(rec.Code).To(Equal(http.StatusServiceUnavailable))
		})

		It("body contains the stale reason", func() {
			req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
			_ = h.ServeHTTP(ctx, rec, req)
			Expect(rec.Body.String()).To(ContainSubstring("last successful pull stale"))
		})

		It("body does not contain 'context canceled'", func() {
			req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
			_ = h.ServeHTTP(ctx, rec, req)
			Expect(rec.Body.String()).NotTo(ContainSubstring("context canceled"))
		})
	})
})
