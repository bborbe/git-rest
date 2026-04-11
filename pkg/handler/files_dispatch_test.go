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

var _ = Describe("FilesDispatchHandler", func() {
	var (
		getH       http.Handler
		listH      http.Handler
		getCalled  int
		listCalled int
		h          http.Handler
		rec        *httptest.ResponseRecorder
	)

	BeforeEach(func() {
		getCalled = 0
		listCalled = 0
		getH = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			getCalled++
			w.WriteHeader(http.StatusOK)
		})
		listH = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			listCalled++
			w.WriteHeader(http.StatusOK)
		})
		h = handler.NewFilesDispatchHandler(getH, listH)
		rec = httptest.NewRecorder()
	})

	Context("request without glob parameter", func() {
		It("routes to getH", func() {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/files/foo.txt", nil)
			h.ServeHTTP(rec, req)
			Expect(getCalled).To(Equal(1))
			Expect(listCalled).To(Equal(0))
		})
	})

	Context("request with glob parameter", func() {
		It("routes to listH", func() {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/files/?glob=*.txt", nil)
			h.ServeHTTP(rec, req)
			Expect(getCalled).To(Equal(0))
			Expect(listCalled).To(Equal(1))
		})
	})
})
