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

// NewReadinessHandler returns a WithError handler that reports readiness based on git status.
func NewReadinessHandler(g git.Git) libhttp.WithError {
	return libhttp.WithErrorFunc(
		func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
			status, err := g.Status(ctx)
			if err != nil {
				return libhttp.WrapWithStatusCode(
					errors.Wrap(ctx, err, "git status"),
					http.StatusServiceUnavailable,
				)
			}
			if !status.Clean || !status.NoPushPending {
				return libhttp.WrapWithStatusCode(
					errors.New(ctx, "not ready"),
					http.StatusServiceUnavailable,
				)
			}
			_, _ = resp.Write([]byte("ok"))
			return nil
		},
	)
}
