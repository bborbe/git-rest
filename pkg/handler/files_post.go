// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/bborbe/git-rest/pkg/git"
)

const maxBodyBytes = 10 * 1024 * 1024

// NewFilesPostHandler returns an http.Handler that writes a file to the git repository.
func NewFilesPostHandler(g git.Git) http.Handler {
	return &filesPostHandler{git: g}
}

type filesPostHandler struct {
	git git.Git
}

func (h *filesPostHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/files/")

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if err := h.git.WriteFile(r.Context(), path, body); err != nil {
		if errors.Is(err, git.ErrInvalidPath) {
			writeJSONError(w, http.StatusBadRequest, "invalid path")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSONOK(w)
}
