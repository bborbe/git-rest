// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"

	libhttp "github.com/bborbe/http"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/git-rest/mocks"
	"github.com/bborbe/git-rest/pkg/git"
	"github.com/bborbe/git-rest/pkg/handler"
)

var _ = Describe("ReadinessHandler", func() {
	var (
		fakeGit *mocks.FakeGit
		h       libhttp.WithError
		rec     *httptest.ResponseRecorder
		ctx     context.Context
	)

	BeforeEach(func() {
		fakeGit = new(mocks.FakeGit)
		h = handler.NewReadinessHandler(fakeGit)
		rec = httptest.NewRecorder()
		ctx = context.Background()
	})

	Context("clean and no push pending", func() {
		BeforeEach(func() {
			fakeGit.StatusReturns(git.Status{Clean: true, NoPushPending: true}, nil)
		})

		It("returns 200 ok", func() {
			req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
			err := h.ServeHTTP(ctx, rec, req)
			Expect(err).To(BeNil())
			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(rec.Body.String()).To(Equal("ok"))
		})
	})

	Context("not clean", func() {
		BeforeEach(func() {
			fakeGit.StatusReturns(git.Status{Clean: false, NoPushPending: true}, nil)
		})

		It("returns 503 error", func() {
			req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
			err := h.ServeHTTP(ctx, rec, req)
			Expect(err).NotTo(BeNil())
			var errWithStatus libhttp.ErrorWithStatusCode
			Expect(errors.As(err, &errWithStatus)).To(BeTrue())
			Expect(errWithStatus.StatusCode()).To(Equal(http.StatusServiceUnavailable))
			Expect(err.Error()).To(ContainSubstring("not ready"))
		})
	})

	Context("push pending", func() {
		BeforeEach(func() {
			fakeGit.StatusReturns(git.Status{Clean: true, NoPushPending: false}, nil)
		})

		It("returns 503 error", func() {
			req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
			err := h.ServeHTTP(ctx, rec, req)
			Expect(err).NotTo(BeNil())
			var errWithStatus libhttp.ErrorWithStatusCode
			Expect(errors.As(err, &errWithStatus)).To(BeTrue())
			Expect(errWithStatus.StatusCode()).To(Equal(http.StatusServiceUnavailable))
		})
	})

	Context("status error", func() {
		BeforeEach(func() {
			fakeGit.StatusReturns(git.Status{}, errWithMessage("git status failed"))
		})

		It("returns 503 error", func() {
			req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
			err := h.ServeHTTP(ctx, rec, req)
			Expect(err).NotTo(BeNil())
			var errWithStatus libhttp.ErrorWithStatusCode
			Expect(errors.As(err, &errWithStatus)).To(BeTrue())
			Expect(errWithStatus.StatusCode()).To(Equal(http.StatusServiceUnavailable))
		})
	})
})
