// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"net/http"
)

// HeaderGatewayInitator is the request-header name the auth middleware
// expects to carry caller identity. The misspelling (missing the second
// 'i' in "Initator") is deliberate — it matches the existing caller-side
// convention and is part of the frozen public contract. Do not "fix" it.
const HeaderGatewayInitator = "X-Gateway-Initator"

// HeaderGatewaySecret is the request-header name the auth middleware
// expects to carry the shared secret value.
const HeaderGatewaySecret = "X-Gateway-Secret"

// NewGatewaySecretMiddleware returns a gorilla mux-compatible middleware
// (func(http.Handler) http.Handler) that enforces shared-secret header auth.
//
// Check order (first failure wins):
//  1. X-Gateway-Initator missing or empty → 500 (initiator identity required)
//  2. X-Gateway-Secret missing, empty, or not equal to secret → 401
//  3. Both present and matching → strip X-Gateway-Secret, call next
//
// "Missing" and "empty string" are treated identically for both headers:
// r.Header.Get returning "" triggers the same response regardless of whether
// the client omitted the header or sent an empty value.
//
// X-Gateway-Secret is deleted from the request before the inner handler is
// called so it cannot appear in downstream request logs or metrics labels.
func NewGatewaySecretMiddleware(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get(HeaderGatewayInitator) == "" {
				http.Error(w, "header 'X-Gateway-Initator' missing", http.StatusInternalServerError)
				return
			}
			if r.Header.Get(HeaderGatewaySecret) != secret {
				http.Error(
					w,
					"secret in header 'X-Gateway-Secret' is invalid => access denied",
					http.StatusUnauthorized,
				)
				return
			}
			r.Header.Del(HeaderGatewaySecret)
			next.ServeHTTP(w, r)
		})
	}
}
