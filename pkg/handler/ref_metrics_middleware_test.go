// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"net/http"
	"net/http/httptest"
	"strings"

	libhttp "github.com/bborbe/http"
	gorillamux "github.com/gorilla/mux"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/git-rest/mocks"
	"github.com/bborbe/git-rest/pkg/git"
	"github.com/bborbe/git-rest/pkg/handler"
)

var _ = Describe("RefMetricsMiddleware", func() {
	var (
		fakeGit     *mocks.FakeGit
		fakeMetrics *mocks.FakeMetrics
		router      *gorillamux.Router
	)

	BeforeEach(func() {
		fakeGit = new(mocks.FakeGit)
		fakeMetrics = new(mocks.FakeMetrics)
		router = gorillamux.NewRouter().SkipClean(true)
		router.Handle(
			"/api/v1/refs/{key:.*}",
			handler.NewRefMetricsMiddleware(
				fakeMetrics,
				"put",
				libhttp.NewJSONErrorHandler(handler.NewRefsPutHandler(fakeGit)),
			),
		).Methods(http.MethodPut)
	})

	It("records the operation and final 409 status", func() {
		fakeGit.PutClaimRefReturns("", git.ErrRefConflict)
		req := httptest.NewRequest(
			http.MethodPut,
			refsRequestTarget(validRefKey),
			strings.NewReader(validRefBody()),
		)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		Expect(rec.Code).To(Equal(http.StatusConflict))
		Expect(fakeMetrics.IncRefRequestCallCount()).To(Equal(1))
		op, result := fakeMetrics.IncRefRequestArgsForCall(0)
		Expect(op).To(Equal("put"))
		Expect(result).To(Equal("409"))
	})

	It("records the final 200 status on success", func() {
		fakeGit.PutClaimRefReturns("abc123", nil)
		req := httptest.NewRequest(
			http.MethodPut,
			refsRequestTarget(validRefKey),
			strings.NewReader(validRefBody()),
		)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(fakeMetrics.IncRefRequestCallCount()).To(Equal(1))
		op, result := fakeMetrics.IncRefRequestArgsForCall(0)
		Expect(op).To(Equal("put"))
		Expect(result).To(Equal("200"))
	})
})
