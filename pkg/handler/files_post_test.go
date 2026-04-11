// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/git-rest/mocks"
	"github.com/bborbe/git-rest/pkg/handler"
)

var _ = Describe("FilesPostHandler", func() {
	var (
		fakeGit *mocks.FakeGit
		h       http.Handler
		rec     *httptest.ResponseRecorder
	)

	BeforeEach(func() {
		fakeGit = new(mocks.FakeGit)
		h = handler.NewFilesPostHandler(fakeGit)
		rec = httptest.NewRecorder()
	})

	Context("happy path", func() {
		It("returns 200 with ok body", func() {
			req := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/files/foo.txt",
				bytes.NewBufferString("content"),
			)
			h.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(rec.Body.String()).To(ContainSubstring(`"ok"`))
		})

		It("passes correct path and body to git", func() {
			req := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/files/dir/file.txt",
				bytes.NewBufferString("data"),
			)
			h.ServeHTTP(rec, req)
			_, path, content := fakeGit.WriteFileArgsForCall(0)
			Expect(path).To(Equal("dir/file.txt"))
			Expect(content).To(Equal([]byte("data")))
		})
	})

	Context("path traversal", func() {
		It("returns 400", func() {
			fakeGit.WriteFileReturns(errWithMessage("path traversal not allowed"))
			req := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/files/../etc/passwd",
				bytes.NewBufferString("x"),
			)
			h.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusBadRequest))
			Expect(rec.Body.String()).To(ContainSubstring(`"error"`))
		})
	})

	Context("body too large", func() {
		It("returns 413", func() {
			// Create a body slightly over 10 MB
			large := strings.Repeat("x", 10*1024*1024+1)
			req := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/files/foo.txt",
				strings.NewReader(large),
			)
			h.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusRequestEntityTooLarge))
			Expect(rec.Body.String()).To(ContainSubstring("too large"))
		})
	})

	Context("git error", func() {
		It("returns 500", func() {
			fakeGit.WriteFileReturns(errWithMessage("internal git failure"))
			req := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/files/foo.txt",
				bytes.NewBufferString("content"),
			)
			h.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusInternalServerError))
			Expect(rec.Body.String()).To(ContainSubstring(`"error"`))
		})
	})
})
