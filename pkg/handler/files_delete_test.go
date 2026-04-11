// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/git-rest/mocks"
	"github.com/bborbe/git-rest/pkg/git"
	"github.com/bborbe/git-rest/pkg/handler"
)

var _ = Describe("FilesDeleteHandler", func() {
	var (
		fakeGit *mocks.FakeGit
		h       http.Handler
		rec     *httptest.ResponseRecorder
	)

	BeforeEach(func() {
		fakeGit = new(mocks.FakeGit)
		h = handler.NewFilesDeleteHandler(fakeGit)
		rec = httptest.NewRecorder()
	})

	Context("happy path", func() {
		It("returns 200 with ok body", func() {
			req := httptest.NewRequest(http.MethodDelete, "/api/v1/files/foo.txt", nil)
			h.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(rec.Body.String()).To(ContainSubstring(`"ok"`))
		})

		It("passes correct path to git", func() {
			req := httptest.NewRequest(http.MethodDelete, "/api/v1/files/dir/file.txt", nil)
			h.ServeHTTP(rec, req)
			_, path := fakeGit.DeleteFileArgsForCall(0)
			Expect(path).To(Equal("dir/file.txt"))
		})
	})

	Context("file not found", func() {
		BeforeEach(func() {
			fakeGit.DeleteFileReturns(git.ErrNotFound)
		})

		It("returns 404", func() {
			req := httptest.NewRequest(http.MethodDelete, "/api/v1/files/missing.txt", nil)
			h.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusNotFound))
			Expect(rec.Body.String()).To(ContainSubstring("not found"))
		})
	})

	Context("invalid path", func() {
		It("returns 400 when git returns ErrInvalidPath", func() {
			fakeGit.DeleteFileReturns(git.ErrInvalidPath)
			req := httptest.NewRequest(http.MethodDelete, "/api/v1/files/../etc/passwd", nil)
			h.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusBadRequest))
			Expect(rec.Body.String()).To(ContainSubstring(`"error"`))
		})

		It("returns 400 for .git path", func() {
			fakeGit.DeleteFileReturns(git.ErrInvalidPath)
			req := httptest.NewRequest(http.MethodDelete, "/api/v1/files/.git/config", nil)
			h.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusBadRequest))
			Expect(rec.Body.String()).To(ContainSubstring(`"error"`))
		})
	})

	Context("git error", func() {
		BeforeEach(func() {
			fakeGit.DeleteFileReturns(errWithMessage("internal git failure"))
		})

		It("returns 500", func() {
			req := httptest.NewRequest(http.MethodDelete, "/api/v1/files/foo.txt", nil)
			h.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusInternalServerError))
			Expect(rec.Body.String()).To(ContainSubstring(`"error"`))
		})
	})
})
