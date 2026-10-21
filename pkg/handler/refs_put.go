// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"net/http"

	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"
	gorillamux "github.com/gorilla/mux"

	"github.com/bborbe/git-rest/pkg/git"
)

// NewRefsPutHandler returns a WithError handler that creates or renews a claim
// ref with the origin's compare-and-swap lease.
func NewRefsPutHandler(g git.Git) libhttp.WithError {
	return libhttp.WithErrorFunc(
		func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
			key := gorillamux.Vars(req)["key"]
			if err := git.ValidateClaimKey(ctx, key); err != nil {
				return libhttp.WrapWithStatusCode(err, http.StatusBadRequest)
			}
			request, err := readRefRequest(ctx, resp, req)
			if err != nil {
				return err
			}
			sha, err := g.PutClaimRef(ctx, key, request)
			if err != nil {
				return mapRefWriteError(ctx, err, "put claim ref")
			}
			return libhttp.SendJSONResponse(
				ctx,
				resp,
				map[string]string{"ref": "refs/" + key, "sha": sha},
				http.StatusOK,
			)
		},
	)
}

// mapRefWriteError translates a claim-ref write failure into the HTTP status
// contract: a lost lease is 409, an invalid name is 400, everything else 500.
func mapRefWriteError(ctx context.Context, err error, message string) error {
	if errors.Is(err, git.ErrRefConflict) {
		return libhttp.WrapWithStatusCode(err, http.StatusConflict)
	}
	if errors.Is(err, git.ErrInvalidRefName) {
		return libhttp.WrapWithStatusCode(err, http.StatusBadRequest)
	}
	return errors.Wrap(ctx, err, message)
}
