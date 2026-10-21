// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"net/http"

	libhttp "github.com/bborbe/http"
	gorillamux "github.com/gorilla/mux"

	"github.com/bborbe/git-rest/pkg/git"
)

// NewRefsDeleteHandler returns a WithError handler that deletes a claim ref
// with the origin's compare-and-swap lease.
func NewRefsDeleteHandler(g git.Git) libhttp.WithError {
	return libhttp.WithErrorFunc(
		func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
			key := gorillamux.Vars(req)["key"]
			if err := git.ValidateClaimKey(ctx, key); err != nil {
				return libhttp.WrapWithStatusCode(err, http.StatusBadRequest)
			}
			expected := req.URL.Query().Get("expected_sha")
			if err := validateRefExpectedSHA(ctx, expected); err != nil {
				return libhttp.WrapWithStatusCode(err, http.StatusBadRequest)
			}
			if err := g.DeleteClaimRef(ctx, key, expected); err != nil {
				return mapRefWriteError(ctx, err, "delete claim ref")
			}
			return libhttp.SendJSONResponse(
				ctx,
				resp,
				map[string]bool{"ok": true},
				http.StatusOK,
			)
		},
	)
}
