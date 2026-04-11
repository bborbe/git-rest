// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"net/http"

	"github.com/bborbe/git-rest/pkg/git"
)

// NewReadinessHandler returns an http.Handler that reports readiness based on git status.
func NewReadinessHandler(g git.Git) http.Handler {
	return &readinessHandler{git: g}
}

type readinessHandler struct {
	git git.Git
}

func (h *readinessHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	status, err := h.git.Status(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if status.Clean && status.NoPushPending {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return
	}
	writeJSONError(w, http.StatusServiceUnavailable, "not ready")
}
