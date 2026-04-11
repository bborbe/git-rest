// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/git-rest/pkg/handler"
)

var _ = Describe("MetricsMiddleware", func() {
	var (
		nextCalled int
		nextH      http.Handler
		h          http.Handler
		rec        *httptest.ResponseRecorder
	)

	BeforeEach(func() {
		nextCalled = 0
		nextH = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			nextCalled++
			w.WriteHeader(http.StatusOK)
		})
		h = handler.NewMetricsMiddleware(nextH)
		rec = httptest.NewRecorder()
	})

	It("calls next handler", func() {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		h.ServeHTTP(rec, req)
		Expect(nextCalled).To(Equal(1))
		Expect(rec.Code).To(Equal(http.StatusOK))
	})

	It("passes response through from next handler", func() {
		nextH = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})
		h = handler.NewMetricsMiddleware(nextH)
		req := httptest.NewRequest(http.MethodGet, "/missing", nil)
		h.ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusNotFound))
	})
})
