// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import "net/http"

// NewFilesDispatchHandler routes GET /api/v1/files/ to listH when the glob query
// parameter is present, and to getH otherwise.
func NewFilesDispatchHandler(getH, listH http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("glob") {
			listH.ServeHTTP(w, r)
			return
		}
		getH.ServeHTTP(w, r)
	})
}
