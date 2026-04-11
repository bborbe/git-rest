// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"encoding/json"
	"net/http"

	"github.com/bborbe/git-rest/pkg/git"
)

// NewFilesListHandler returns an http.Handler that lists files in the git repository matching a glob pattern.
func NewFilesListHandler(g git.Git) http.Handler {
	return &filesListHandler{git: g}
}

type filesListHandler struct {
	git git.Git
}

func (h *filesListHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	glob := r.URL.Query().Get("glob")
	files, err := h.git.ListFiles(r.Context(), glob)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if files == nil {
		files = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(files)
}
