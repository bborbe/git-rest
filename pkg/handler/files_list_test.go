// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/git-rest/pkg/git/mocks"
	"github.com/bborbe/git-rest/pkg/handler"
)

var _ = Describe("FilesListHandler", func() {
	var (
		fakeGit *mocks.FakeGit
		h       http.Handler
		rec     *httptest.ResponseRecorder
	)

	BeforeEach(func() {
		fakeGit = new(mocks.FakeGit)
		h = handler.NewFilesListHandler(fakeGit)
		rec = httptest.NewRecorder()
	})

	Context("happy path with files", func() {
		BeforeEach(func() {
			fakeGit.ListFilesReturns([]string{"a.txt", "b.txt"}, nil)
		})

		It("returns 200 with JSON array", func() {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/files/", nil)
			h.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))
			Expect(rec.Body.String()).To(ContainSubstring("a.txt"))
			Expect(rec.Body.String()).To(ContainSubstring("b.txt"))
		})

		It("passes glob query param to git", func() {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/files/?glob=*.txt", nil)
			h.ServeHTTP(rec, req)
			_, pattern := fakeGit.ListFilesArgsForCall(0)
			Expect(pattern).To(Equal("*.txt"))
		})
	})

	Context("empty result", func() {
		BeforeEach(func() {
			fakeGit.ListFilesReturns(nil, nil)
		})

		It("returns [] not null", func() {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/files/", nil)
			h.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(rec.Body.String()).To(ContainSubstring("[]"))
			Expect(rec.Body.String()).NotTo(ContainSubstring("null"))
		})
	})

	Context("git error", func() {
		BeforeEach(func() {
			fakeGit.ListFilesReturns(nil, errWithMessage("internal git failure"))
		})

		It("returns 500", func() {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/files/", nil)
			h.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusInternalServerError))
			Expect(rec.Body.String()).To(ContainSubstring(`"error"`))
		})
	})
})
