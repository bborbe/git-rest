// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	main "github.com/bborbe/git-rest"
	"github.com/bborbe/git-rest/pkg/factory"
	"github.com/bborbe/git-rest/pkg/git"
	"github.com/bborbe/git-rest/pkg/metrics"
)

const claimTestExpectedSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func runGit(dir string, args ...string) string {
	full := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", full...)
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), string(out))
	return string(out)
}

func setupClaimRefRepo() (string, string, git.Git) {
	base := GinkgoT().TempDir()
	bare := filepath.Join(base, "origin.git")
	clone := filepath.Join(base, "clone")

	cmd := exec.Command("git", "init", "--bare", "--initial-branch=main", bare)
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), string(out))

	cmd = exec.Command("git", "clone", bare, clone)
	out, err = cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), string(out))

	runGit(clone, "config", "user.email", "test@example.com")
	runGit(clone, "config", "user.name", "Test")
	Expect(os.WriteFile(filepath.Join(clone, ".gitkeep"), nil, 0o644)).To(Succeed())
	runGit(clone, "add", ".gitkeep")
	runGit(clone, "commit", "-m", "init")
	runGit(clone, "push", "-u", "origin", "main")

	gitClient := factory.CreateGitClient(
		clone,
		metrics.NewMetrics(),
		libtime.NewCurrentDateTime(),
		"",
		git.NewMarkerResolver(clone),
	)
	return bare, clone, gitClient
}

func claimRefsTarget(key string) string {
	return (&url.URL{Path: "/api/v1/refs/" + key}).String()
}

func claimRouter(gitClient git.Git, repoPath, secret string) http.Handler {
	return main.NewRouter(
		context.Background(),
		gitClient,
		metrics.NewMetrics(),
		repoPath,
		secret,
	)
}

func doClaimRequest(
	handler http.Handler,
	method, target, body string,
	headers map[string]string,
) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func validClaimBody() string {
	return `{"cluster":"nuke","task_id":"t-1","lease_until":"2026-10-21T12:05:00Z","expected_sha":""}`
}

func claimRefListing(bare string) string {
	return runGit(bare, "for-each-ref")
}

type claimNamespaceRow struct {
	key    string
	target string
}

func claimNamespaceRows() []claimNamespaceRow {
	return []claimNamespaceRow{
		{key: "heads/main", target: claimRefsTarget("heads/main")},
		{key: "tags/x", target: claimRefsTarget("tags/x")},
		{key: "claims/../heads/main", target: claimRefsTarget("claims/../heads/main")},
		{
			key:    "claims/%2E%2E/heads/main",
			target: "/api/v1/refs/claims/%2E%2E/heads/main",
		},
		{key: "claims/", target: claimRefsTarget("claims/")},
		{key: "claims", target: claimRefsTarget("claims")},
		{key: "claims/a..b", target: claimRefsTarget("claims/a..b")},
		{key: "claims/x.lock", target: claimRefsTarget("claims/x.lock")},
		{key: "claims/a b", target: claimRefsTarget("claims/a b")},
		{key: "claims/a~1", target: claimRefsTarget("claims/a~1")},
		{key: "claims/a:b", target: claimRefsTarget("claims/a:b")},
		{key: "claims//x", target: claimRefsTarget("claims//x")},
		{key: "claims/x/", target: claimRefsTarget("claims/x/")},
	}
}

type claimInvalidPutBody struct {
	name string
	body string
}

func claimInvalidPutBodies() []claimInvalidPutBody {
	return []claimInvalidPutBody{
		{name: "missing cluster", body: `{"task_id":"t","lease_until":"2026-10-21T12:05:00Z"}`},
		{
			name: "empty cluster",
			body: `{"cluster":"","task_id":"t","lease_until":"2026-10-21T12:05:00Z"}`,
		},
		{name: "missing task_id", body: `{"cluster":"nuke","lease_until":"2026-10-21T12:05:00Z"}`},
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
		{name: "not json", body: `{`},
	}
}

func refCounterSnapshot() map[string]float64 {
	snapshot := make(map[string]float64, 12)
	for _, op := range []string{"get", "put", "delete"} {
		for _, result := range []string{"200", "400", "409", "500"} {
			snapshot[op+"/"+result] = testutil.ToFloat64(
				metrics.RefRequestsTotal.WithLabelValues(op, result),
			)
		}
	}
	return snapshot
}

func expectOnlyRefCounterDelta(
	before, after map[string]float64,
	changed string,
	delta float64,
) {
	keys := make([]string, 0, len(before))
	for key := range before {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if key == changed {
			Expect(after[key]-before[key]).To(Equal(delta), key)
			continue
		}
		Expect(after[key]).To(Equal(before[key]), key)
	}
}

func httpRequestsForPath(path string) float64 {
	mfs, err := prometheus.DefaultGatherer.Gather()
	Expect(err).NotTo(HaveOccurred())
	for _, mf := range mfs {
		if mf.GetName() != "git_rest_http_requests_total" {
			continue
		}
		for _, metric := range mf.GetMetric() {
			labels := make(map[string]string)
			for _, label := range metric.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}
			if labels["method"] == http.MethodPut &&
				labels["path"] == path &&
				labels["status"] == "200" {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return 0
}

var _ = Describe("Claim refs over HTTP", func() {
	Context("namespace guard and validation against a real origin", func() {
		It("rejects invalid requests without touching the origin", func() {
			bare, clone, gitClient := setupClaimRefRepo()
			router := claimRouter(gitClient, clone, "")
			before := claimRefListing(bare)

			for _, row := range claimNamespaceRows() {
				putRec := doClaimRequest(
					router, http.MethodPut, row.target, validClaimBody(), nil,
				)
				Expect(putRec.Code).To(Equal(http.StatusBadRequest), "PUT %q", row.key)

				deleteRec := doClaimRequest(
					router,
					http.MethodDelete,
					row.target+"?expected_sha="+claimTestExpectedSHA,
					"",
					nil,
				)
				Expect(deleteRec.Code).To(Equal(http.StatusBadRequest), "DELETE %q", row.key)
			}

			for _, row := range claimInvalidPutBodies() {
				rec := doClaimRequest(
					router, http.MethodPut, claimRefsTarget("claims/task/t-1"), row.body, nil,
				)
				Expect(rec.Code).To(Equal(http.StatusBadRequest), row.name)
			}

			largeRec := doClaimRequest(
				router,
				http.MethodPut,
				claimRefsTarget("claims/task/t-1"),
				strings.Repeat("x", 64*1024+1),
				nil,
			)
			Expect(largeRec.Code).To(Equal(http.StatusRequestEntityTooLarge))

			for _, target := range []string{
				claimRefsTarget("claims/task/t-1"),
				claimRefsTarget("claims/task/t-1") + "?expected_sha=xyz",
			} {
				rec := doClaimRequest(router, http.MethodDelete, target, "", nil)
				Expect(rec.Code).To(Equal(http.StatusBadRequest), target)
			}

			for _, target := range []string{
				"/api/v1/refs",
				"/api/v1/refs?prefix=heads/",
				"/api/v1/refs?prefix=claims/../x",
				"/api/v1/refs?prefix=claims/*",
				"/api/v1/refs?prefix=claims/task/%3F",
				"/api/v1/refs?prefix=claims/%5Ba%5D",
			} {
				rec := doClaimRequest(router, http.MethodGet, target, "", nil)
				Expect(rec.Code).To(Equal(http.StatusBadRequest), target)
			}

			Expect(claimRefListing(bare)).To(Equal(before))
		})
	})

	Context("ref counter", func() {
		It("moves only the asserted operation/result series", func() {
			_, clone, gitClient := setupClaimRefRepo()
			router := claimRouter(gitClient, clone, "")

			before := refCounterSnapshot()
			rec := doClaimRequest(
				router,
				http.MethodPut,
				claimRefsTarget("claims/counter/one"),
				validClaimBody(),
				nil,
			)
			Expect(rec.Code).To(Equal(http.StatusOK))
			afterSuccess := refCounterSnapshot()
			expectOnlyRefCounterDelta(before, afterSuccess, "put/200", 1)

			staleBody := `{"cluster":"nuke","task_id":"t-1","lease_until":"2026-10-21T12:05:00Z","expected_sha":"` +
				claimTestExpectedSHA + `"}`
			rec = doClaimRequest(
				router,
				http.MethodPut,
				claimRefsTarget("claims/counter/one"),
				staleBody,
				nil,
			)
			Expect(rec.Code).To(Equal(http.StatusConflict))
			afterConflict := refCounterSnapshot()
			expectOnlyRefCounterDelta(afterSuccess, afterConflict, "put/409", 1)
		})
	})

	Context("bounded path label", func() {
		It("records ref requests under the fixed template only", func() {
			_, clone, gitClient := setupClaimRefRepo()
			router := factory.CreateMetricsMiddleware(
				metrics.NewMetrics(),
				claimRouter(gitClient, clone, ""),
			)
			keys := []string{
				"claims/label/one",
				"claims/label/two",
				"claims/label/three",
			}
			before := httpRequestsForPath("/api/v1/refs/{key}")
			for _, key := range keys {
				rec := doClaimRequest(
					router, http.MethodPut, claimRefsTarget(key), validClaimBody(), nil,
				)
				Expect(rec.Code).To(Equal(http.StatusOK), key)
			}
			Expect(httpRequestsForPath("/api/v1/refs/{key}") - before).To(Equal(3.0))

			mfs, err := prometheus.DefaultGatherer.Gather()
			Expect(err).NotTo(HaveOccurred())
			for _, mf := range mfs {
				if mf.GetName() != "git_rest_http_requests_total" {
					continue
				}
				for _, metric := range mf.GetMetric() {
					for _, label := range metric.GetLabel() {
						if label.GetName() != "path" {
							continue
						}
						for _, key := range keys {
							Expect(label.GetValue()).NotTo(ContainSubstring(key))
						}
					}
				}
			}
		})
	})

	Context("auth applies", func() {
		It("rejects unauthenticated writes and leaves origin and counters unchanged", func() {
			bare, clone, gitClient := setupClaimRefRepo()
			router := claimRouter(gitClient, clone, "test-secret")
			beforeListing := claimRefListing(bare)
			beforeCounters := refCounterSnapshot()
			target := claimRefsTarget("claims/auth/one")

			noSecret := doClaimRequest(
				router,
				http.MethodPut,
				target,
				validClaimBody(),
				map[string]string{"X-Gateway-Initator": "integration-test"},
			)
			Expect(noSecret.Code).To(Equal(http.StatusUnauthorized))

			noHeaders := doClaimRequest(
				router, http.MethodPut, target, validClaimBody(), nil,
			)
			Expect(noHeaders.Code).To(Equal(http.StatusInternalServerError))

			Expect(claimRefListing(bare)).To(Equal(beforeListing))
			Expect(refCounterSnapshot()).To(Equal(beforeCounters))
		})
	})

	Context("nonce is ignored on input", func() {
		It("stores a fresh nonce that differs from the supplied one", func() {
			_, clone, gitClient := setupClaimRefRepo()
			router := claimRouter(gitClient, clone, "")
			suppliedNonce := "deadbeefdeadbeefdeadbeefdeadbeef"
			body := `{"cluster":"nuke","task_id":"t-1","lease_until":"2026-10-21T12:05:00Z","expected_sha":"","nonce":"` +
				suppliedNonce + `"}`

			rec := doClaimRequest(
				router, http.MethodPut, claimRefsTarget("claims/nonce/one"), body, nil,
			)
			Expect(rec.Code).To(Equal(http.StatusOK))
			var response map[string]string
			Expect(json.Unmarshal(rec.Body.Bytes(), &response)).To(Succeed())
			sha := response["sha"]
			Expect(sha).NotTo(BeEmpty())

			commit := runGit(clone, "cat-file", "-p", sha)
			_, message, found := strings.Cut(commit, "\n\n")
			Expect(found).To(BeTrue())
			var stored map[string]any
			Expect(json.Unmarshal([]byte(message), &stored)).To(Succeed())
			nonce, ok := stored["nonce"].(string)
			Expect(ok).To(BeTrue())
			Expect(nonce).To(MatchRegexp(`^[0-9a-f]{32}$`))
			Expect(nonce).NotTo(Equal(suppliedNonce))
		})
	})

	Context("metadata round-trip", func() {
		It("returns exactly the five contract keys with the written values", func() {
			_, clone, gitClient := setupClaimRefRepo()
			router := claimRouter(gitClient, clone, "")
			key := "claims/nuke/t-42"
			body := `{"expected_sha":"","cluster":"nuke","task_id":"t-42","lease_until":"2026-10-21T12:05:00Z"}`

			putRec := doClaimRequest(router, http.MethodPut, claimRefsTarget(key), body, nil)
			Expect(putRec.Code).To(Equal(http.StatusOK))
			var putResponse map[string]string
			Expect(json.Unmarshal(putRec.Body.Bytes(), &putResponse)).To(Succeed())

			getRec := doClaimRequest(router, http.MethodGet, "/api/v1/refs?prefix=claims/", "", nil)
			Expect(getRec.Code).To(Equal(http.StatusOK))
			var refs []map[string]any
			Expect(json.Unmarshal(getRec.Body.Bytes(), &refs)).To(Succeed())
			Expect(refs).To(HaveLen(1))

			keys := make([]string, 0, len(refs[0]))
			for name := range refs[0] {
				keys = append(keys, name)
			}
			Expect(keys).To(ConsistOf("ref", "sha", "cluster", "task_id", "lease_until"))
			Expect(refs[0]).NotTo(HaveKey("nonce"))
			Expect(refs[0]["ref"]).To(Equal("refs/" + key))
			Expect(refs[0]["sha"]).To(Equal(putResponse["sha"]))
			Expect(refs[0]["cluster"]).To(Equal("nuke"))
			Expect(refs[0]["task_id"]).To(Equal("t-42"))
			Expect(refs[0]["lease_until"]).To(Equal("2026-10-21T12:05:00Z"))
		})
	})
})
