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

// NewFilesListHandler returns a WithError handler that lists files in the git repository matching a glob pattern.
func NewFilesListHandler(g git.Git) libhttp.WithError {
	return libhttp.WithErrorFunc(
		func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
			glob := req.URL.Query().Get("glob")
			files, err := g.ListFiles(ctx, glob)
			if err != nil {
				return errors.Wrap(ctx, err, "list files")
			}
			if files == nil {
				files = []string{}
			}
			return libhttp.SendJSONResponse(ctx, resp, files, http.StatusOK)
		},
	)
}
