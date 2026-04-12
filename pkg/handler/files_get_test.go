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

var _ = Describe("FilesGetHandler", func() {
	var (
		fakeGit *mocks.FakeGit
		h       libhttp.WithError
		rec     *httptest.ResponseRecorder
		ctx     context.Context
	)

	BeforeEach(func() {
		fakeGit = new(mocks.FakeGit)
		h = handler.NewFilesGetHandler(fakeGit)
		rec = httptest.NewRecorder()
		ctx = context.Background()
	})

	Context("happy path", func() {
		BeforeEach(func() {
			fakeGit.ReadFileReturns([]byte("hello world"), nil)
		})

		It("returns 200 with file content", func() {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/files/foo.txt", nil)
			err := h.ServeHTTP(ctx, rec, req)
			Expect(err).To(BeNil())
			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(rec.Body.String()).To(Equal("hello world"))
			Expect(rec.Header().Get("Content-Type")).To(Equal("application/octet-stream"))
		})

		It("passes correct path to git", func() {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/files/subdir/file.txt", nil)
			err := h.ServeHTTP(ctx, rec, req)
			Expect(err).To(BeNil())
			_, path := fakeGit.ReadFileArgsForCall(0)
			Expect(path).To(Equal("subdir/file.txt"))
		})
	})

	Context("file not found", func() {
		BeforeEach(func() {
			fakeGit.ReadFileReturns(nil, git.ErrNotFound)
		})

		It("returns 404 error", func() {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/files/missing.txt", nil)
			err := h.ServeHTTP(ctx, rec, req)
			Expect(err).NotTo(BeNil())
			var errWithStatus libhttp.ErrorWithStatusCode
			Expect(errors.As(err, &errWithStatus)).To(BeTrue())
			Expect(errWithStatus.StatusCode()).To(Equal(http.StatusNotFound))
		})
	})

	Context("invalid path", func() {
		It("returns 400 when git returns ErrInvalidPath", func() {
			fakeGit.ReadFileReturns(nil, git.ErrInvalidPath)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/files/../etc/passwd", nil)
			err := h.ServeHTTP(ctx, rec, req)
			Expect(err).NotTo(BeNil())
			var errWithStatus libhttp.ErrorWithStatusCode
			Expect(errors.As(err, &errWithStatus)).To(BeTrue())
			Expect(errWithStatus.StatusCode()).To(Equal(http.StatusBadRequest))
		})

		It("returns 400 for .git path", func() {
			fakeGit.ReadFileReturns(nil, git.ErrInvalidPath)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/files/.git/config", nil)
			err := h.ServeHTTP(ctx, rec, req)
			Expect(err).NotTo(BeNil())
			var errWithStatus libhttp.ErrorWithStatusCode
			Expect(errors.As(err, &errWithStatus)).To(BeTrue())
			Expect(errWithStatus.StatusCode()).To(Equal(http.StatusBadRequest))
		})
	})

	Context("git error", func() {
		BeforeEach(func() {
			fakeGit.ReadFileReturns(nil, errWithMessage("internal git failure"))
		})

		It("returns internal error", func() {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/files/foo.txt", nil)
			err := h.ServeHTTP(ctx, rec, req)
			Expect(err).NotTo(BeNil())
		})
	})
})
