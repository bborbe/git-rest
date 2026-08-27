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
// When the request carries ?create_only=1, the write is create-only: an already-occupied
// path returns 409 Conflict instead of overwriting the existing file.
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
			if err := writeFile(ctx, g, path, body, req.URL.Query().Get("create_only") == "1"); err != nil {
				if errors.Is(err, git.ErrInvalidPath) {
					return libhttp.WrapWithStatusCode(err, http.StatusBadRequest)
				}
				if errors.Is(err, git.ErrFileExists) {
					return libhttp.WrapWithStatusCode(err, http.StatusConflict)
				}
				return errors.Wrap(ctx, err, "write file")
			}
			return libhttp.SendJSONResponse(ctx, resp, map[string]bool{"ok": true}, http.StatusOK)
		},
	)
}

// writeFile dispatches to the create-only or the upsert write based on createOnly.
// Extracted from the handler to keep NewFilesPostHandler's cognitive complexity
// within the linter budget.
func writeFile(ctx context.Context, g git.Git, path string, body []byte, createOnly bool) error {
	if createOnly {
		return g.WriteFileIfAbsent(ctx, path, body)
	}
	return g.WriteFile(ctx, path, body)
}
