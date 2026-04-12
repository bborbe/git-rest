// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"net/http"
	"strings"

	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"

	"github.com/bborbe/git-rest/pkg/git"
)

// NewFilesDeleteHandler returns a WithError handler that deletes a file from the git repository.
func NewFilesDeleteHandler(g git.Git) libhttp.WithError {
	return libhttp.WithErrorFunc(
		func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
			path := strings.TrimPrefix(req.URL.Path, "/api/v1/files/")
			if err := g.DeleteFile(ctx, path); err != nil {
				if errors.Is(err, git.ErrNotFound) {
					return libhttp.WrapWithStatusCode(err, http.StatusNotFound)
				}
				if errors.Is(err, git.ErrInvalidPath) {
					return libhttp.WrapWithStatusCode(err, http.StatusBadRequest)
				}
				return errors.Wrap(ctx, err, "delete file")
			}
			return libhttp.SendJSONResponse(ctx, resp, map[string]bool{"ok": true}, http.StatusOK)
		},
	)
}
