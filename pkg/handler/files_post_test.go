// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"

	libhttp "github.com/bborbe/http"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/git-rest/mocks"
	"github.com/bborbe/git-rest/pkg/git"
	"github.com/bborbe/git-rest/pkg/handler"
)

var _ = Describe("FilesPostHandler", func() {
	var (
		fakeGit *mocks.FakeGit
		h       libhttp.WithError
		rec     *httptest.ResponseRecorder
		ctx     context.Context
	)

	BeforeEach(func() {
		fakeGit = new(mocks.FakeGit)
		h = handler.NewFilesPostHandler(fakeGit)
		rec = httptest.NewRecorder()
		ctx = context.Background()
	})

	Context("happy path", func() {
		It("returns 200 with ok body", func() {
			req := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/files/foo.txt",
				bytes.NewBufferString("content"),
			)
			err := h.ServeHTTP(ctx, rec, req)
			Expect(err).To(BeNil())
			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(rec.Body.String()).To(ContainSubstring(`"ok"`))
		})

		It("passes correct path and body to git", func() {
			req := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/files/dir/file.txt",
				bytes.NewBufferString("data"),
			)
			err := h.ServeHTTP(ctx, rec, req)
			Expect(err).To(BeNil())
			_, path, content := fakeGit.WriteFileArgsForCall(0)
			Expect(path).To(Equal("dir/file.txt"))
			Expect(content).To(Equal([]byte("data")))
		})
	})

	Context("invalid path", func() {
		It("returns 400 when git returns ErrInvalidPath", func() {
			fakeGit.WriteFileReturns(git.ErrInvalidPath)
			req := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/files/../etc/passwd",
				bytes.NewBufferString("x"),
			)
			err := h.ServeHTTP(ctx, rec, req)
			Expect(err).NotTo(BeNil())
			var errWithStatus libhttp.ErrorWithStatusCode
			Expect(errors.As(err, &errWithStatus)).To(BeTrue())
			Expect(errWithStatus.StatusCode()).To(Equal(http.StatusBadRequest))
		})

		It("returns 400 for .git path", func() {
			fakeGit.WriteFileReturns(git.ErrInvalidPath)
			req := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/files/.git/config",
				bytes.NewBufferString("x"),
			)
			err := h.ServeHTTP(ctx, rec, req)
			Expect(err).NotTo(BeNil())
			var errWithStatus libhttp.ErrorWithStatusCode
			Expect(errors.As(err, &errWithStatus)).To(BeTrue())
			Expect(errWithStatus.StatusCode()).To(Equal(http.StatusBadRequest))
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
			err := h.ServeHTTP(ctx, rec, req)
			Expect(err).NotTo(BeNil())
			var errWithStatus libhttp.ErrorWithStatusCode
			Expect(errors.As(err, &errWithStatus)).To(BeTrue())
			Expect(errWithStatus.StatusCode()).To(Equal(http.StatusRequestEntityTooLarge))
		})
	})

	Context("git error", func() {
		It("returns internal error", func() {
			fakeGit.WriteFileReturns(errWithMessage("internal git failure"))
			req := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/files/foo.txt",
				bytes.NewBufferString("content"),
			)
			err := h.ServeHTTP(ctx, rec, req)
			Expect(err).NotTo(BeNil())
		})
	})
})
