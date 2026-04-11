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
	"github.com/bborbe/git-rest/pkg/handler"
)

var _ = Describe("MetricsMiddleware", func() {
	var (
		nextCalled  int
		nextH       http.Handler
		h           http.Handler
		rec         *httptest.ResponseRecorder
		fakeMetrics *mocks.FakeMetrics
	)

	BeforeEach(func() {
		nextCalled = 0
		fakeMetrics = &mocks.FakeMetrics{}
		nextH = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			nextCalled++
			w.WriteHeader(http.StatusOK)
		})
		h = handler.NewMetricsMiddleware(fakeMetrics, nextH)
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
		h = handler.NewMetricsMiddleware(fakeMetrics, nextH)
		req := httptest.NewRequest(http.MethodGet, "/missing", nil)
		h.ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusNotFound))
	})

	It("records HTTP request metric", func() {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		h.ServeHTTP(rec, req)
		Expect(fakeMetrics.IncHTTPRequestCallCount()).To(Equal(1))
		method, path, status := fakeMetrics.IncHTTPRequestArgsForCall(0)
		Expect(method).To(Equal(http.MethodGet))
		Expect(path).To(Equal("/healthz"))
		Expect(status).To(Equal("200"))
	})

	It("normalizes file paths to prevent cardinality explosion", func() {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/files/some/nested/file.txt", nil)
		h.ServeHTTP(rec, req)
		Expect(fakeMetrics.IncHTTPRequestCallCount()).To(Equal(1))
		_, path, _ := fakeMetrics.IncHTTPRequestArgsForCall(0)
		Expect(path).To(Equal("/api/v1/files/{path}"))
	})
})
