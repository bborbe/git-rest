// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"net/http"

	libhttp "github.com/bborbe/http"
)

//counterfeiter:generate -o ../../mocks/readiness_state_reader.go --fake-name FakeReadinessStateReader . ReadinessStateReader

// ReadinessStateReader is the interface the readiness handler reads from.
// Implemented by the repo-path verdict returned by NewRepoReadiness.
type ReadinessStateReader interface {
	ReadinessStatus() (bool, string)
}

// NewReadinessHandler returns a handler that reports readiness from the given
// serving-state reader. It never invokes a git subprocess and never blocks on
// a mutex.
func NewReadinessHandler(s ReadinessStateReader) libhttp.WithError {
	return libhttp.WithErrorFunc(
		func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
			ready, reason := s.ReadinessStatus()
			if !ready {
				resp.WriteHeader(http.StatusServiceUnavailable)
				_, _ = resp.Write([]byte(reason))
				return nil
			}
			_, _ = resp.Write([]byte("ok"))
			return nil
		},
	)
}
