// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/git-rest/pkg/handler"
)

var _ = Describe("GatewaySecretMiddleware", func() {
	const secret = "test-secret-value"

	var (
		mw         func(http.Handler) http.Handler
		nextCalled int
		nextH      http.Handler
		rec        *httptest.ResponseRecorder
	)

	BeforeEach(func() {
		nextCalled = 0
		nextH = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			nextCalled++
			w.WriteHeader(http.StatusOK)
		})
		mw = handler.NewGatewaySecretMiddleware(secret)
		rec = httptest.NewRecorder()
	})

	// --- 500: missing / empty X-Gateway-Initator ---

	Context("when X-Gateway-Initator header is absent", func() {
		It("returns 500 with exact body", func() {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/files/foo.md", nil)
			req.Header.Set("X-Gateway-Secret", secret)
			mw(nextH).ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusInternalServerError))
			Expect(
				strings.TrimSpace(rec.Body.String()),
			).To(Equal("header 'X-Gateway-Initator' missing"))
			Expect(nextCalled).To(Equal(0))
		})
	})

	Context("when X-Gateway-Initator header is present but empty", func() {
		It("returns 500 with exact body (empty treated as missing)", func() {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/files/foo.md", nil)
			req.Header.Set("X-Gateway-Initator", "")
			req.Header.Set("X-Gateway-Secret", secret)
			mw(nextH).ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusInternalServerError))
			Expect(
				strings.TrimSpace(rec.Body.String()),
			).To(Equal("header 'X-Gateway-Initator' missing"))
			Expect(nextCalled).To(Equal(0))
		})
	})

	// --- 401: bad / missing X-Gateway-Secret (with valid initiator) ---

	Context("when X-Gateway-Secret header is absent", func() {
		It("returns 401 with exact body", func() {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/files/foo.md", nil)
			req.Header.Set("X-Gateway-Initator", "test-caller")
			mw(nextH).ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusUnauthorized))
			Expect(
				strings.TrimSpace(rec.Body.String()),
			).To(Equal("secret in header 'X-Gateway-Secret' is invalid => access denied"))
			Expect(nextCalled).To(Equal(0))
		})
	})

	Context("when X-Gateway-Secret header contains wrong value", func() {
		It("returns 401 with exact body", func() {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/files/foo.md", nil)
			req.Header.Set("X-Gateway-Initator", "test-caller")
			req.Header.Set("X-Gateway-Secret", "wrong-secret")
			mw(nextH).ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusUnauthorized))
			Expect(
				strings.TrimSpace(rec.Body.String()),
			).To(Equal("secret in header 'X-Gateway-Secret' is invalid => access denied"))
			Expect(nextCalled).To(Equal(0))
		})
	})

	Context("when X-Gateway-Secret header is present but empty", func() {
		It("returns 401 with exact body (empty treated as missing)", func() {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/files/foo.md", nil)
			req.Header.Set("X-Gateway-Initator", "test-caller")
			req.Header.Set("X-Gateway-Secret", "")
			mw(nextH).ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusUnauthorized))
			Expect(
				strings.TrimSpace(rec.Body.String()),
			).To(Equal("secret in header 'X-Gateway-Secret' is invalid => access denied"))
			Expect(nextCalled).To(Equal(0))
		})
	})

	// --- 200: correct headers ---

	Context("when both headers are correct", func() {
		It("calls the inner handler and returns its status", func() {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/files/foo.md", nil)
			req.Header.Set("X-Gateway-Initator", "test-caller")
			req.Header.Set("X-Gateway-Secret", secret)
			mw(nextH).ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(nextCalled).To(Equal(1))
		})

		It("strips X-Gateway-Secret from the request before forwarding", func() {
			var capturedSecret string
			capturingHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturedSecret = r.Header.Get("X-Gateway-Secret")
				w.WriteHeader(http.StatusOK)
			})
			req := httptest.NewRequest(http.MethodGet, "/api/v1/files/foo.md", nil)
			req.Header.Set("X-Gateway-Initator", "test-caller")
			req.Header.Set("X-Gateway-Secret", secret)
			mw(capturingHandler).ServeHTTP(rec, req)
			Expect(capturedSecret).To(Equal(""))
		})
	})
})
