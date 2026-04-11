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

// NewFilesDeleteHandler returns an http.Handler that deletes a file from the git repository.
func NewFilesDeleteHandler(g git.Git) http.Handler {
	return &filesDeleteHandler{git: g}
}

type filesDeleteHandler struct {
	git git.Git
}

func (h *filesDeleteHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/files/")
	if err := h.git.DeleteFile(r.Context(), path); err != nil {
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
	writeJSONOK(w)
}
