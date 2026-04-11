// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/bborbe/git-rest/pkg/git"
)

// NewFilesGetHandler returns an http.Handler that reads a file from the git repository.
func NewFilesGetHandler(g git.Git) http.Handler {
	return &filesGetHandler{git: g}
}

type filesGetHandler struct {
	git git.Git
}

func (h *filesGetHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/files/")
	content, err := h.git.ReadFile(r.Context(), path)
	if err != nil {
		if errors.Is(err, git.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not found")
			return
		}
		if errors.Is(err, git.ErrInvalidPath) {
			writeJSONError(w, http.StatusBadRequest, "invalid path")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	// #nosec G705 -- content is read from the git repository; Content-Type is set to application/octet-stream
	_, _ = w.Write(content)
}
