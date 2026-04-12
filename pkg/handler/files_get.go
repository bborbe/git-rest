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

// NewFilesGetHandler returns a WithError handler that reads a file from the git repository.
func NewFilesGetHandler(g git.Git) libhttp.WithError {
	return libhttp.WithErrorFunc(
		func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
			path := strings.TrimPrefix(req.URL.Path, "/api/v1/files/")
			content, err := g.ReadFile(ctx, path)
			if err != nil {
				if errors.Is(err, git.ErrNotFound) {
					return libhttp.WrapWithStatusCode(err, http.StatusNotFound)
				}
				if errors.Is(err, git.ErrInvalidPath) {
					return libhttp.WrapWithStatusCode(err, http.StatusBadRequest)
				}
				return errors.Wrap(ctx, err, "read file")
			}
			resp.Header().Set("Content-Type", "application/octet-stream")
			// #nosec G705 -- content is read from the git repository; Content-Type is set to application/octet-stream
			_, _ = resp.Write(content)
			return nil
		},
	)
}
