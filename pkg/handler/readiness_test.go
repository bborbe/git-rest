// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/git-rest/pkg/git"
	"github.com/bborbe/git-rest/pkg/git/mocks"
	"github.com/bborbe/git-rest/pkg/handler"
)

var _ = Describe("ReadinessHandler", func() {
	var (
		fakeGit *mocks.FakeGit
		h       http.Handler
		rec     *httptest.ResponseRecorder
	)

	BeforeEach(func() {
		fakeGit = new(mocks.FakeGit)
		h = handler.NewReadinessHandler(fakeGit)
		rec = httptest.NewRecorder()
	})

	Context("clean and no push pending", func() {
		BeforeEach(func() {
			fakeGit.StatusReturns(git.Status{Clean: true, NoPushPending: true}, nil)
		})

		It("returns 200 ok", func() {
			req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
			h.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(rec.Body.String()).To(Equal("ok"))
		})
	})

	Context("not clean", func() {
		BeforeEach(func() {
			fakeGit.StatusReturns(git.Status{Clean: false, NoPushPending: true}, nil)
		})

		It("returns 503", func() {
			req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
			h.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusServiceUnavailable))
			Expect(rec.Body.String()).To(ContainSubstring("not ready"))
		})
	})

	Context("push pending", func() {
		BeforeEach(func() {
			fakeGit.StatusReturns(git.Status{Clean: true, NoPushPending: false}, nil)
		})

		It("returns 503", func() {
			req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
			h.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusServiceUnavailable))
			Expect(rec.Body.String()).To(ContainSubstring("not ready"))
		})
	})

	Context("status error", func() {
		BeforeEach(func() {
			fakeGit.StatusReturns(git.Status{}, errWithMessage("git status failed"))
		})

		It("returns 503", func() {
			req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
			h.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusServiceUnavailable))
			Expect(rec.Body.String()).To(ContainSubstring(`"error"`))
		})
	})
})
