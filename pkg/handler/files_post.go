// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"

	"github.com/bborbe/git-rest/pkg/git"
)

const maxBodyBytes = 10 * 1024 * 1024

// NewFilesPostHandler returns a WithError handler that writes a file to the git repository.
func NewFilesPostHandler(g git.Git) libhttp.WithError {
	return libhttp.WithErrorFunc(
		func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
			path := strings.TrimPrefix(req.URL.Path, "/api/v1/files/")
			req.Body = http.MaxBytesReader(resp, req.Body, maxBodyBytes)
			body, err := io.ReadAll(req.Body)
			if err != nil {
				var maxBytesErr *http.MaxBytesError
				if errors.As(err, &maxBytesErr) {
					return libhttp.WrapWithStatusCode(err, http.StatusRequestEntityTooLarge)
				}
				return errors.Wrap(ctx, err, "read request body")
			}
			if err := g.WriteFile(ctx, path, body); err != nil {
				if errors.Is(err, git.ErrInvalidPath) {
					return libhttp.WrapWithStatusCode(err, http.StatusBadRequest)
				}
				return errors.Wrap(ctx, err, "write file")
			}
			return libhttp.SendJSONResponse(ctx, resp, map[string]bool{"ok": true}, http.StatusOK)
		},
	)
}
