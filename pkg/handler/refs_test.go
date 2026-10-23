// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	libhttp "github.com/bborbe/http"
	gorillamux "github.com/gorilla/mux"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/git-rest/mocks"
	"github.com/bborbe/git-rest/pkg/git"
	"github.com/bborbe/git-rest/pkg/handler"
)

const validRefKey = "claims/task/t-1"

func refsRequestTarget(key string) string {
	return (&url.URL{Path: "/api/v1/refs/" + key}).String()
}

func newRefsTestRouter() *gorillamux.Router {
	return gorillamux.NewRouter().SkipClean(true)
}

func registerRefsHandlers(router *gorillamux.Router, fakeGit *mocks.FakeGit) {
	router.Handle(
		"/api/v1/refs",
		libhttp.NewJSONErrorHandler(handler.NewRefsGetHandler(fakeGit)),
	).Methods(http.MethodGet)
	router.Handle(
		"/api/v1/refs/{key:.*}",
		libhttp.NewJSONErrorHandler(handler.NewRefsPutHandler(fakeGit)),
	).Methods(http.MethodPut)
	router.Handle(
		"/api/v1/refs/{key:.*}",
		libhttp.NewJSONErrorHandler(handler.NewRefsDeleteHandler(fakeGit)),
	).Methods(http.MethodDelete)
}

func validRefBody() string {
	return `{"cluster":"nuke","task_id":"t-1","lease_until":"2026-10-21T12:05:00Z","expected_sha":""}`
}

const validExpectedSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// failingReader returns an error on every read, exercising the non-MaxBytes
// body-read failure path.
type failingReader struct{}

func (failingReader) Read(_ []byte) (int, error) {
	return 0, errors.New("read failed")
}

func serveRefsRequest(
	router http.Handler,
	method string,
	target string,
	body string,
) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// errorCodeFromBody decodes the JSON error code from a response body.
func errorCodeFromBody(rec *httptest.ResponseRecorder) string {
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	Expect(json.Unmarshal(rec.Body.Bytes(), &body)).To(Succeed())
	return body.Error.Code
}

var _ = Describe("Claim ref handlers", func() {
	var (
		fakeGit *mocks.FakeGit
		router  *gorillamux.Router
	)

	BeforeEach(func() {
		fakeGit = new(mocks.FakeGit)
		router = newRefsTestRouter()
		registerRefsHandlers(router, fakeGit)
	})

	Context("namespace guard", func() {
		type namespaceRow struct {
			key    string
			target string
		}
		rows := []namespaceRow{
			{key: "heads/main", target: refsRequestTarget("heads/main")},
			{key: "tags/x", target: refsRequestTarget("tags/x")},
			{key: "claims/../heads/main", target: refsRequestTarget("claims/../heads/main")},
			{
				key:    "claims/%2E%2E/heads/main",
				target: "/api/v1/refs/claims/%2E%2E/heads/main",
			},
			{key: "claims/", target: refsRequestTarget("claims/")},
			{key: "claims", target: refsRequestTarget("claims")},
			{key: "claims/a..b", target: refsRequestTarget("claims/a..b")},
			{key: "claims/x.lock", target: refsRequestTarget("claims/x.lock")},
			{key: "claims/a b", target: refsRequestTarget("claims/a b")},
			{key: "claims/a~1", target: refsRequestTarget("claims/a~1")},
			{key: "claims/a:b", target: refsRequestTarget("claims/a:b")},
			{key: "claims//x", target: refsRequestTarget("claims//x")},
			{key: "claims/x/", target: refsRequestTarget("claims/x/")},
		}

		It("rejects every key with 400 and never calls git", func() {
			for _, row := range rows {
				putRec := serveRefsRequest(router, http.MethodPut, row.target, validRefBody())
				Expect(putRec.Code).To(Equal(http.StatusBadRequest), "PUT %q", row.key)

				deleteTarget := row.target + "?expected_sha=" + validExpectedSHA
				deleteRec := serveRefsRequest(router, http.MethodDelete, deleteTarget, "")
				Expect(deleteRec.Code).To(Equal(http.StatusBadRequest), "DELETE %q", row.key)
			}
			Expect(fakeGit.PutClaimRefCallCount()).To(Equal(0))
			Expect(fakeGit.DeleteClaimRefCallCount()).To(Equal(0))
			Expect(fakeGit.ListClaimRefsCallCount()).To(Equal(0))
		})
	})

	Context("PUT validation", func() {
		invalidBodies := []struct {
			name string
			body string
		}{
			{name: "missing cluster", body: `{"task_id":"t","lease_until":"2026-10-21T12:05:00Z"}`},
			{
				name: "empty cluster",
				body: `{"cluster":"","task_id":"t","lease_until":"2026-10-21T12:05:00Z"}`,
			},
			{
				name: "missing task_id",
				body: `{"cluster":"nuke","lease_until":"2026-10-21T12:05:00Z"}`,
			},
			{
				name: "empty task_id",
				body: `{"cluster":"nuke","task_id":"","lease_until":"2026-10-21T12:05:00Z"}`,
			},
			{
				name: "bad lease_until",
				body: `{"cluster":"nuke","task_id":"t","lease_until":"tomorrow"}`,
			},
			{
				name: "uppercase expected_sha",
				body: `{"cluster":"nuke","task_id":"t","lease_until":"2026-10-21T12:05:00Z","expected_sha":"` + strings.Repeat(
					"A",
					40,
				) + `"}`,
			},
			{
				name: "short expected_sha",
				body: `{"cluster":"nuke","task_id":"t","lease_until":"2026-10-21T12:05:00Z","expected_sha":"abc"}`,
			},
			{
				name: "colon expected_sha",
				body: `{"cluster":"nuke","task_id":"t","lease_until":"2026-10-21T12:05:00Z","expected_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa:"}`,
			},
			{name: "not json", body: `{`},
		}

		It("returns 400 for every invalid body and never calls git", func() {
			for _, row := range invalidBodies {
				rec := serveRefsRequest(
					router,
					http.MethodPut,
					refsRequestTarget(validRefKey),
					row.body,
				)
				Expect(rec.Code).To(Equal(http.StatusBadRequest), row.name)
			}
			Expect(fakeGit.PutClaimRefCallCount()).To(Equal(0))
		})

		It("returns 413 for a body over 64 KiB", func() {
			large := strings.Repeat("x", 64*1024+1)
			rec := serveRefsRequest(router, http.MethodPut, refsRequestTarget(validRefKey), large)
			Expect(rec.Code).To(Equal(http.StatusRequestEntityTooLarge))
			Expect(fakeGit.PutClaimRefCallCount()).To(Equal(0))
		})

		It("returns 500 when reading the body fails", func() {
			req := httptest.NewRequest(
				http.MethodPut,
				refsRequestTarget(validRefKey),
				failingReader{},
			)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusInternalServerError))
			Expect(fakeGit.PutClaimRefCallCount()).To(Equal(0))
		})
	})

	Context("DELETE validation", func() {
		It("returns 400 when expected_sha is missing or invalid", func() {
			for _, target := range []string{
				refsRequestTarget(validRefKey),
				refsRequestTarget(validRefKey) + "?expected_sha=",
				refsRequestTarget(validRefKey) + "?expected_sha=xyz",
				refsRequestTarget(validRefKey) + "?expected_sha=" + strings.Repeat("A", 40),
			} {
				rec := serveRefsRequest(router, http.MethodDelete, target, "")
				Expect(rec.Code).To(Equal(http.StatusBadRequest), target)
			}
			Expect(fakeGit.DeleteClaimRefCallCount()).To(Equal(0))
		})
	})

	Context("GET validation", func() {
		It("returns 400 for every invalid prefix and never calls git", func() {
			for _, target := range []string{
				"/api/v1/refs",
				"/api/v1/refs?prefix=",
				"/api/v1/refs?prefix=heads/",
				"/api/v1/refs?prefix=claims/../x",
				"/api/v1/refs?prefix=claims/*",
				"/api/v1/refs?prefix=claims/task/%3F",
				"/api/v1/refs?prefix=claims/%5Ba%5D",
			} {
				rec := serveRefsRequest(router, http.MethodGet, target, "")
				Expect(rec.Code).To(Equal(http.StatusBadRequest), target)
			}
			Expect(fakeGit.ListClaimRefsCallCount()).To(Equal(0))
		})
	})

	Context("success bodies", func() {
		It("returns ref and sha for PUT", func() {
			fakeGit.PutClaimRefReturns("abc123", nil)
			rec := serveRefsRequest(
				router,
				http.MethodPut,
				refsRequestTarget(validRefKey),
				validRefBody(),
			)
			Expect(rec.Code).To(Equal(http.StatusOK))
			var body map[string]string
			Expect(json.Unmarshal(rec.Body.Bytes(), &body)).To(Succeed())
			Expect(body).To(Equal(map[string]string{
				"ref": "refs/" + validRefKey,
				"sha": "abc123",
			}))
		})

		It("returns ok for DELETE", func() {
			fakeGit.DeleteClaimRefReturns(nil)
			rec := serveRefsRequest(
				router,
				http.MethodDelete,
				refsRequestTarget(validRefKey)+"?expected_sha="+validExpectedSHA,
				"",
			)
			Expect(rec.Code).To(Equal(http.StatusOK))
			var body map[string]bool
			Expect(json.Unmarshal(rec.Body.Bytes(), &body)).To(Succeed())
			Expect(body).To(Equal(map[string]bool{"ok": true}))
		})

		It("returns an empty JSON array for an empty list", func() {
			fakeGit.ListClaimRefsReturns([]git.ClaimRef{}, nil)
			rec := serveRefsRequest(router, http.MethodGet, "/api/v1/refs?prefix=claims/", "")
			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(strings.TrimSpace(rec.Body.String())).To(Equal("[]"))
		})

		It("returns an empty JSON array for a nil list", func() {
			fakeGit.ListClaimRefsReturns(nil, nil)
			rec := serveRefsRequest(router, http.MethodGet, "/api/v1/refs?prefix=claims/", "")
			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(strings.TrimSpace(rec.Body.String())).To(Equal("[]"))
		})

		It("returns exactly the five contract keys for a populated list", func() {
			fakeGit.ListClaimRefsReturns([]git.ClaimRef{{
				Ref:        "refs/claims/task/t-1",
				SHA:        "abc123",
				Cluster:    "nuke",
				TaskID:     "t-1",
				LeaseUntil: "2026-10-21T12:05:00Z",
			}}, nil)
			rec := serveRefsRequest(router, http.MethodGet, "/api/v1/refs?prefix=claims/", "")
			Expect(rec.Code).To(Equal(http.StatusOK))

			var body []map[string]any
			Expect(json.Unmarshal(rec.Body.Bytes(), &body)).To(Succeed())
			Expect(body).To(HaveLen(1))
			keys := make([]string, 0, len(body[0]))
			for key := range body[0] {
				keys = append(keys, key)
			}
			Expect(keys).To(ConsistOf("ref", "sha", "cluster", "task_id", "lease_until"))
			Expect(body[0]).NotTo(HaveKey("nonce"))
		})
	})

	Context("error mapping", func() {
		It("maps PUT errors to 409, 400 and 500", func() {
			cases := []struct {
				name   string
				err    error
				status int
				code   string
			}{
				{
					name:   "lost lease",
					err:    errors.Join(git.ErrRefConflict, errWithMessage("push rejected")),
					status: http.StatusConflict,
					code:   handler.ErrorCodeClaimConflict,
				},
				{
					name:   "invalid ref name",
					err:    git.ErrInvalidRefName,
					status: http.StatusBadRequest,
				},
				{
					name:   "other failure",
					err:    errWithMessage("boom"),
					status: http.StatusInternalServerError,
					code:   "INTERNAL_ERROR",
				},
			}
			for _, tc := range cases {
				fakeGit.PutClaimRefReturns("", tc.err)
				rec := serveRefsRequest(
					router,
					http.MethodPut,
					refsRequestTarget(validRefKey),
					validRefBody(),
				)
				Expect(rec.Code).To(Equal(tc.status), tc.name)
				if tc.code != "" {
					Expect(errorCodeFromBody(rec)).To(Equal(tc.code), tc.name)
				}
			}
		})

		It("maps DELETE errors to 409, 400 and 500", func() {
			cases := []struct {
				name   string
				err    error
				status int
				code   string
			}{
				{
					name:   "lost lease",
					err:    errors.Join(git.ErrRefConflict, errWithMessage("push rejected")),
					status: http.StatusConflict,
					code:   handler.ErrorCodeClaimConflict,
				},
				{
					name:   "invalid ref name",
					err:    git.ErrInvalidRefName,
					status: http.StatusBadRequest,
				},
				{
					name:   "other failure",
					err:    errWithMessage("boom"),
					status: http.StatusInternalServerError,
					code:   "INTERNAL_ERROR",
				},
			}
			for _, tc := range cases {
				fakeGit.DeleteClaimRefReturns(tc.err)
				rec := serveRefsRequest(
					router,
					http.MethodDelete,
					refsRequestTarget(validRefKey)+"?expected_sha="+validExpectedSHA,
					"",
				)
				Expect(rec.Code).To(Equal(tc.status), tc.name)
				if tc.code != "" {
					Expect(errorCodeFromBody(rec)).To(Equal(tc.code), tc.name)
				}
			}
		})

		It("maps GET errors to 400 and 500", func() {
			fakeGit.ListClaimRefsReturns(nil, git.ErrInvalidRefName)
			rec := serveRefsRequest(router, http.MethodGet, "/api/v1/refs?prefix=claims/", "")
			Expect(rec.Code).To(Equal(http.StatusBadRequest))

			fakeGit.ListClaimRefsReturns(nil, git.ErrNoRemote)
			rec = serveRefsRequest(router, http.MethodGet, "/api/v1/refs?prefix=claims/", "")
			Expect(rec.Code).To(Equal(http.StatusInternalServerError))

			fakeGit.ListClaimRefsReturns(nil, errors.New("boom"))
			rec = serveRefsRequest(router, http.MethodGet, "/api/v1/refs?prefix=claims/", "")
			Expect(rec.Code).To(Equal(http.StatusInternalServerError))
		})
	})
})
