// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/bborbe/errors"
)

// Header names shared with vault-api's merge endpoint. The caller header is
// spelled here because git-rest is the value vault-api recognises: without it
// the automatic path would be indistinguishable from an operator's curl in
// vault-api's log.
const (
	gatewaySecretHeader = "X-Gateway-Secret"
	mergeCallerHeader   = "X-Merge-Caller"
	mergeCallerGitRest  = "git-rest"
)

// vaultAPIMergeTimeout bounds one merge request. It is generous relative to the
// pull timeout because the merge is a network round trip that vault-api answers
// by writing through git-rest's own write API — a two-hop operation — but it is
// bounded so a hung vault-api cannot stall the pull loop indefinitely.
const vaultAPIMergeTimeout = 30 * time.Second

// maxMergeResponseBytes caps the response read, so a misbehaving server cannot
// make this process allocate without limit. A merge response is a small JSON
// object; the merged content travels the write path, not this response.
const maxMergeResponseBytes = 1 << 20

// vaultAPIMergeSide is one side on the wire. CommitterTime is an RFC3339 string
// because vault-api rejects a malformed time with a named field, and a
// time.Time would encode in a shape that error could not describe.
type vaultAPIMergeSide struct {
	Content       string `json:"content"`
	CommitterTime string `json:"committer_time"`
}

// vaultAPIMergeRequest is the body of POST /api/v1/merge. Apply is always false
// here: this process owns the working tree and writes the returned bytes itself.
type vaultAPIMergeRequest struct {
	Path   string            `json:"path"`
	Base   vaultAPIMergeSide `json:"base"`
	Ours   vaultAPIMergeSide `json:"ours"`
	Theirs vaultAPIMergeSide `json:"theirs"`
	Apply  bool              `json:"apply"`
}

// NewHTTPVaultAPIMergeClient returns a VaultAPIMergeClient that posts to
// baseURL's /api/v1/merge with the shared gateway secret.
func NewHTTPVaultAPIMergeClient(baseURL, secret string) VaultAPIMergeClient {
	return &httpVaultAPIMergeClient{
		mergeURL: baseURL + "/api/v1/merge",
		secret:   secret,
		client:   &http.Client{Timeout: vaultAPIMergeTimeout},
	}
}

type httpVaultAPIMergeClient struct {
	mergeURL string
	secret   string
	client   *http.Client
}

// Merge posts the three sides and reports what vault-api decided. A non-2xx
// answer is an error, not a quarantine: the resolver treats both the same way
// (fall back), but the log line differs, and an operator reading it needs to
// know whether vault-api declined or never answered.
func (c *httpVaultAPIMergeClient) Merge(
	ctx context.Context,
	path string,
	base, ours, theirs Side,
) (VaultAPIMergeResult, error) {
	body, err := json.Marshal(vaultAPIMergeRequest{
		Path:   path,
		Base:   wireSide(base),
		Ours:   wireSide(ours),
		Theirs: wireSide(theirs),
		Apply:  false,
	})
	if err != nil {
		return VaultAPIMergeResult{}, errors.Wrap(ctx, err, "marshal merge request")
	}

	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, c.mergeURL, bytes.NewReader(body),
	)
	if err != nil {
		return VaultAPIMergeResult{}, errors.Wrap(ctx, err, "build merge request")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(gatewaySecretHeader, c.secret)
	request.Header.Set(mergeCallerHeader, mergeCallerGitRest)

	response, err := c.client.Do(request)
	if err != nil {
		return VaultAPIMergeResult{}, errors.Wrap(ctx, err, "post merge request")
	}
	defer func() { _ = response.Body.Close() }()

	// The body is read before the status is judged, so an error response can
	// name vault-api's own reason rather than only its status code.
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxMergeResponseBytes))
	if err != nil {
		return VaultAPIMergeResult{}, errors.Wrap(ctx, err, "read merge response")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return VaultAPIMergeResult{}, errors.Errorf(
			ctx, "vault-api answered %d: %s", response.StatusCode, string(raw),
		)
	}

	var result VaultAPIMergeResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return VaultAPIMergeResult{}, errors.Wrap(ctx, err, "decode merge response")
	}
	return result, nil
}

// wireSide converts one side to its wire shape.
func wireSide(side Side) vaultAPIMergeSide {
	return vaultAPIMergeSide{
		Content:       side.Content,
		CommitterTime: side.CommitterTime.UTC().Format(time.RFC3339),
	}
}

// compile-time proof the interface is satisfied, so a signature drift fails the
// build here rather than at the call site in main.
var _ VaultAPIMergeClient = &httpVaultAPIMergeClient{}
