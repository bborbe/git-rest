// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"

	"github.com/bborbe/git-rest/pkg/git"
)

// refBodyLimit is the maximum accepted request body for a claim-ref write.
const refBodyLimit = 64 * 1024

// refExpectedSHAPattern matches a full-length lowercase hex object id.
var refExpectedSHAPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// validateRefPrefix rejects a prefix that is empty, outside claims/, or that
// could be interpreted as a glob or a traversal by the origin.
func validateRefPrefix(ctx context.Context, prefix string) error {
	if prefix == "" {
		return errors.Errorf(ctx, "prefix must not be empty")
	}
	if !strings.HasPrefix(prefix, "claims/") {
		return errors.Errorf(ctx, "prefix %q must start with %q", prefix, "claims/")
	}
	if strings.Contains(prefix, "..") {
		return errors.Errorf(ctx, "prefix %q must not contain %q", prefix, "..")
	}
	if strings.ContainsAny(prefix, "*?[") {
		return errors.Errorf(ctx, "prefix %q must not contain glob characters", prefix)
	}
	return nil
}

// validateRefRequest validates the caller-visible fields of a create/renew
// request. Unknown JSON keys are ignored before this runs.
func validateRefRequest(ctx context.Context, request git.ClaimRefRequest) error {
	if request.Cluster == "" {
		return errors.Errorf(ctx, "cluster must not be empty")
	}
	if request.TaskID == "" {
		return errors.Errorf(ctx, "task_id must not be empty")
	}
	if _, err := time.Parse(time.RFC3339, request.LeaseUntil); err != nil {
		return errors.Wrap(ctx, err, "lease_until must be RFC3339")
	}
	if request.ExpectedSHA != "" && !refExpectedSHAPattern.MatchString(request.ExpectedSHA) {
		return errors.Errorf(ctx, "expected_sha must be 40 or 64 lowercase hex characters")
	}
	return nil
}

// validateRefExpectedSHA validates the delete query parameter, which is
// mandatory and must be a full-length lowercase hex object id.
func validateRefExpectedSHA(ctx context.Context, sha string) error {
	if sha == "" {
		return errors.Errorf(ctx, "expected_sha must not be empty")
	}
	if !refExpectedSHAPattern.MatchString(sha) {
		return errors.Errorf(ctx, "expected_sha must be 40 or 64 lowercase hex characters")
	}
	return nil
}

// readRefRequest reads and validates the create/renew body. The returned error
// already carries its HTTP status so the JSON error handler preserves it.
func readRefRequest(
	ctx context.Context,
	resp http.ResponseWriter,
	req *http.Request,
) (git.ClaimRefRequest, error) {
	var request git.ClaimRefRequest
	req.Body = http.MaxBytesReader(resp, req.Body, refBodyLimit)
	body, err := io.ReadAll(req.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return request, libhttp.WrapWithStatusCode(err, http.StatusRequestEntityTooLarge)
		}
		return request, errors.Wrap(ctx, err, "read request body")
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return request, libhttp.WrapWithStatusCode(
			errors.Wrap(ctx, err, "decode request body"),
			http.StatusBadRequest,
		)
	}
	if err := validateRefRequest(ctx, request); err != nil {
		return request, libhttp.WrapWithStatusCode(err, http.StatusBadRequest)
	}
	return request, nil
}
