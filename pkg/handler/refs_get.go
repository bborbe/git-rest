// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"net/http"

	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"

	"github.com/bborbe/git-rest/pkg/git"
)

// NewRefsGetHandler returns a WithError handler that lists claim refs under a
// validated prefix, read live from the origin.
func NewRefsGetHandler(g git.Git) libhttp.WithError {
	return libhttp.WithErrorFunc(
		func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
			prefix := req.URL.Query().Get("prefix")
			if err := validateRefPrefix(ctx, prefix); err != nil {
				return libhttp.WrapWithStatusCode(err, http.StatusBadRequest)
			}
			refs, err := g.ListClaimRefs(ctx, prefix)
			if err != nil {
				if errors.Is(err, git.ErrInvalidRefName) {
					return libhttp.WrapWithStatusCode(err, http.StatusBadRequest)
				}
				return errors.Wrap(ctx, err, "list claim refs")
			}
			if refs == nil {
				refs = []git.ClaimRef{}
			}
			return libhttp.SendJSONResponse(ctx, resp, refs, http.StatusOK)
		},
	)
}
