// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	stderrors "errors"
	"log/slog"
	"os/exec"
	"regexp"
	"sort"
	"strings"

	"github.com/bborbe/errors"
)

// emptyTreeSHA is the object id of git's empty tree. Claim commits sit on it so
// they are parentless and carry no content.
const emptyTreeSHA = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// claimKeyPrefix is the ref namespace reserved for claims.
const claimKeyPrefix = "claims/"

// expectedSHAPattern matches a full-length lowercase hex object id. It accepts
// both SHA-1 (40) and SHA-256 (64) repositories.
var expectedSHAPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// IsObjectID reports whether sha is a full-length lowercase hex object id
// (40 hex for SHA-1, 64 hex for SHA-256).
func IsObjectID(sha string) bool {
	return expectedSHAPattern.MatchString(sha)
}

// ClaimRef is one claim ref as read live from the origin. The JSON field set is
// the HTTP response contract: exactly these five keys, never a nonce.
type ClaimRef struct {
	Ref        string `json:"ref"`
	SHA        string `json:"sha"`
	Cluster    string `json:"cluster"`
	TaskID     string `json:"task_id"`
	LeaseUntil string `json:"lease_until"`
}

// ClaimRefRequest is the create/renew request. ExpectedSHA empty means the ref
// must not exist.
type ClaimRefRequest struct {
	ExpectedSHA string `json:"expected_sha"`
	Cluster     string `json:"cluster"`
	TaskID      string `json:"task_id"`
	LeaseUntil  string `json:"lease_until"`
}

// ErrRefConflict is returned when the origin's compare-and-swap rejects a claim
// ref write because another writer won the race.
var ErrRefConflict = stderrors.New("claim ref conflict")

// ErrInvalidRefName is returned when a claim key or expected sha is not a safe,
// well-formed ref name.
var ErrInvalidRefName = stderrors.New("invalid claim ref name")

// ErrNoRemote is returned when a claim ref operation needs the origin but the
// repository has no remote configured.
var ErrNoRemote = stderrors.New("no origin remote configured")

// RemoteRef is one ref advertised by the origin.
type RemoteRef struct {
	SHA string
	Ref string
}

// ClaimOrigin abstracts the origin-contacting git operations used by claim-ref
// listing. The production implementation is gitOrigin; tests inject a fake.
//
//counterfeiter:generate -o ../../mocks/claim_origin.go --fake-name FakeClaimOrigin . ClaimOrigin
type ClaimOrigin interface {
	ListRefs(ctx context.Context, pattern string) ([]RemoteRef, error)
	FetchCommits(ctx context.Context, shas []string) error
	ReadCommitMessage(ctx context.Context, sha string) ([]byte, error)
}

// gitOrigin is the production ClaimOrigin, backed by the git binary.
type gitOrigin struct {
	g *git
}

// ListRefs lists the refs advertised by origin that match pattern.
func (o *gitOrigin) ListRefs(ctx context.Context, pattern string) ([]RemoteRef, error) {
	if !o.g.hasRemote(ctx) {
		return nil, errors.Wrap(ctx, ErrNoRemote, "no origin remote configured")
	}
	out, err := o.g.runCmdOutput(ctx, o.g.repoPath, "ls-remote", "--refs", "origin", pattern)
	if err != nil {
		return nil, errors.Wrap(ctx, err, "git ls-remote origin")
	}
	return parseRemoteRefs(out), nil
}

// parseRemoteRefs parses `git ls-remote` output lines of the form
// "<sha>\t<ref>", skipping empty lines.
func parseRemoteRefs(out []byte) []RemoteRef {
	lines := strings.Split(string(out), "\n")
	entries := make([]RemoteRef, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		sha, ref, found := strings.Cut(line, "\t")
		if !found {
			continue
		}
		entries = append(entries, RemoteRef{SHA: sha, Ref: ref})
	}
	return entries
}

// FetchCommits fetches the given commits by sha in one git invocation, without
// touching FETCH_HEAD or any remote-tracking ref. An empty list is a no-op.
func (o *gitOrigin) FetchCommits(ctx context.Context, shas []string) error {
	if len(shas) == 0 {
		return nil
	}
	args := make([]string, 0, 4+len(shas))
	args = append(args, "fetch", "--no-tags", "--no-write-fetch-head", "origin")
	args = append(args, shas...)
	return o.g.runCmd(ctx, o.g.repoPath, args...)
}

// ReadCommitMessage returns the commit message of sha.
func (o *gitOrigin) ReadCommitMessage(ctx context.Context, sha string) ([]byte, error) {
	out, err := o.g.runCmdOutput(ctx, o.g.repoPath, "cat-file", "-p", sha)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "git cat-file -p %s", sha)
	}
	_, message, found := strings.Cut(string(out), "\n\n")
	if !found {
		return out, nil
	}
	return []byte(message), nil
}

// ListClaimRefs returns the claim refs under refs/<prefix>, read live from the
// origin. All listed commits are fetched in one batched git invocation; when
// that fetch fails, each commit is fetched on its own. An entry is dropped with
// one WARN only when a re-list shows its ref vanished or moved; a fetch failure
// for a ref that still points at the listed sha is returned as an error.
func (g *git) ListClaimRefs(ctx context.Context, prefix string) ([]ClaimRef, error) {
	entries, err := g.origin.ListRefs(ctx, "refs/"+prefix+"*")
	if err != nil {
		return nil, errors.Wrap(ctx, err, "list claim refs")
	}
	matched := make([]RemoteRef, 0, len(entries))
	for _, entry := range entries {
		if matchesClaimPrefix(entry.Ref, prefix) {
			matched = append(matched, entry)
		}
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].Ref < matched[j].Ref })
	if len(matched) == 0 {
		return []ClaimRef{}, nil
	}
	batchOK := g.fetchClaimCommits(ctx, matched)
	result := make([]ClaimRef, 0, len(matched))
	for _, entry := range matched {
		select {
		case <-ctx.Done():
			return nil, errors.Wrap(ctx, ctx.Err(), "claim ref listing cancelled")
		default:
		}
		claim, keep, err := g.readClaimRef(ctx, entry, batchOK)
		if err != nil {
			return nil, err
		}
		if keep {
			result = append(result, claim)
		}
	}
	return result, nil
}

// fetchClaimCommits fetches every listed sha in one git invocation and reports
// whether it succeeded. A failed batch is logged at debug only: the per-entry
// fallback owns the user-visible outcome and its WARN.
func (g *git) fetchClaimCommits(ctx context.Context, matched []RemoteRef) bool {
	shas := make([]string, 0, len(matched))
	for _, entry := range matched {
		shas = append(shas, entry.SHA)
	}
	if err := g.origin.FetchCommits(ctx, shas); err != nil {
		slog.DebugContext(
			ctx,
			"claim ref batch fetch failed; falling back to per-sha fetches",
			"error", err,
		)
		return false
	}
	return true
}

// readClaimRef reads and parses one listed claim ref. When the batched fetch
// failed it retries the entry on its own; a failed retry is classified by
// resolveFetchFailure. It returns keep=false when the ref vanished or moved
// between listing and fetching.
func (g *git) readClaimRef(
	ctx context.Context,
	entry RemoteRef,
	batchOK bool,
) (ClaimRef, bool, error) {
	claim := ClaimRef{Ref: entry.Ref, SHA: entry.SHA}
	if !batchOK {
		if err := g.origin.FetchCommits(ctx, []string{entry.SHA}); err != nil {
			return g.resolveFetchFailure(ctx, entry, err)
		}
	}
	message, err := g.origin.ReadCommitMessage(ctx, entry.SHA)
	if err != nil {
		return ClaimRef{}, false, errors.Wrapf(ctx, err, "read claim commit %s", entry.SHA)
	}
	cluster, taskID, leaseUntil, err := parseClaimMetadata(ctx, message)
	if err != nil {
		slog.WarnContext(
			ctx,
			"claim ref has unparseable metadata",
			"ref", entry.Ref,
			"sha", entry.SHA,
			"error", err,
		)
		return claim, true, nil
	}
	claim.Cluster = cluster
	claim.TaskID = taskID
	claim.LeaseUntil = leaseUntil
	return claim, true, nil
}

// resolveFetchFailure decides what a failed per-sha fetch means. A re-list
// proves whether the ref still points at the listed sha: when it does, the
// fetch failure is returned (the listing answers 500); when the ref vanished or
// moved, the entry is dropped with exactly one WARN. A failed re-list is
// returned as an error too, naming the original fetch failure.
func (g *git) resolveFetchFailure(
	ctx context.Context,
	entry RemoteRef,
	fetchErr error,
) (ClaimRef, bool, error) {
	refs, err := g.origin.ListRefs(ctx, entry.Ref)
	if err != nil {
		return ClaimRef{}, false, errors.Wrapf(
			ctx,
			err,
			"re-list %s after fetch failure (%v)",
			entry.Ref,
			fetchErr,
		)
	}
	if claimRefStillAt(refs, entry) {
		return ClaimRef{}, false, errors.Wrapf(
			ctx,
			fetchErr,
			"fetch claim commit %s for %s",
			entry.SHA,
			entry.Ref,
		)
	}
	slog.WarnContext(
		ctx,
		"dropping claim ref: ref moved or vanished between listing and fetching",
		"ref", entry.Ref,
		"sha", entry.SHA,
		"error", fetchErr,
	)
	return ClaimRef{}, false, nil
}

// claimRefStillAt reports whether refs contains an exact entry for ref at its
// listed sha. The re-list is matched by exact ref equality because ls-remote
// matches a pattern by ref-name tail.
func claimRefStillAt(refs []RemoteRef, entry RemoteRef) bool {
	for _, ref := range refs {
		if ref.Ref == entry.Ref && ref.SHA == entry.SHA {
			return true
		}
	}
	return false
}

// matchesClaimPrefix reports whether ref belongs to prefix by whole path
// components, so claims/task/1 never matches claims/task/10.
func matchesClaimPrefix(ref, prefix string) bool {
	base := "refs/" + prefix
	if strings.HasSuffix(prefix, "/") {
		return strings.HasPrefix(ref, base)
	}
	return ref == base || strings.HasPrefix(ref, base+"/")
}

// claimMetadata is the JSON payload stored in a claim commit message.
type claimMetadata struct {
	Cluster    string `json:"cluster"`
	TaskID     string `json:"task_id"`
	LeaseUntil string `json:"lease_until"`
}

// parseClaimMetadata decodes the caller-visible fields of a claim commit
// message. Unknown keys are ignored and the nonce is never returned.
func parseClaimMetadata(
	ctx context.Context,
	message []byte,
) (cluster, taskID, leaseUntil string, err error) {
	var metadata claimMetadata
	if err := json.Unmarshal(message, &metadata); err != nil {
		return "", "", "", errors.Wrap(ctx, err, "parse claim metadata")
	}
	return metadata.Cluster, metadata.TaskID, metadata.LeaseUntil, nil
}

// claimCommitMessage is the JSON payload written into a claim commit.
type claimCommitMessage struct {
	Cluster    string `json:"cluster"`
	TaskID     string `json:"task_id"`
	LeaseUntil string `json:"lease_until"`
	Nonce      string `json:"nonce"`
}

// PutClaimRef creates or renews refs/<key> with the origin's compare-and-swap.
// It returns the new commit sha. A lease mismatch returns ErrRefConflict.
//
// It does not take g.mu: the operation touches the origin and the object
// database only, and the origin's compare-and-swap is the only correctness
// mechanism.
func (g *git) PutClaimRef(
	ctx context.Context,
	key string,
	request ClaimRefRequest,
) (string, error) {
	if err := ValidateClaimKey(ctx, key); err != nil {
		return "", err
	}
	if err := validateExpectedSHA(ctx, request.ExpectedSHA); err != nil {
		return "", err
	}
	if !g.hasRemote(ctx) {
		return "", errors.Wrap(ctx, ErrNoRemote, "no origin remote configured")
	}
	nonce, err := newClaimNonce(ctx)
	if err != nil {
		return "", err
	}
	message, err := json.Marshal(claimCommitMessage{
		Cluster:    request.Cluster,
		TaskID:     request.TaskID,
		LeaseUntil: request.LeaseUntil,
		Nonce:      nonce,
	})
	if err != nil {
		return "", errors.Wrap(ctx, err, "marshal claim commit message")
	}
	out, err := g.runCmdOutput(ctx, g.repoPath, "commit-tree", emptyTreeSHA, "-m", string(message))
	if err != nil {
		return "", errors.Wrap(ctx, err, "git commit-tree")
	}
	sha := strings.TrimSpace(string(out))
	if err := g.pushClaimRef(ctx, key, request.ExpectedSHA, sha); err != nil {
		return "", err
	}
	return sha, nil
}

// DeleteClaimRef deletes refs/<key> with the origin's compare-and-swap.
// A lease mismatch or an absent ref returns ErrRefConflict.
//
// It does not take g.mu: the operation only pushes to the origin, and the
// origin's compare-and-swap is the only correctness mechanism.
func (g *git) DeleteClaimRef(ctx context.Context, key string, expectedSHA string) error {
	if err := ValidateClaimKey(ctx, key); err != nil {
		return err
	}
	if expectedSHA == "" {
		return errors.Wrap(ctx, ErrInvalidRefName, "expected sha must not be empty")
	}
	if err := validateExpectedSHA(ctx, expectedSHA); err != nil {
		return err
	}
	if !g.hasRemote(ctx) {
		return errors.Wrap(ctx, ErrNoRemote, "no origin remote configured")
	}
	return g.pushClaimRef(ctx, key, expectedSHA, "")
}

// pushClaimRef pushes sha (or a deletion when sha is empty) to refs/<key> under
// the origin's compare-and-swap lease. It pushes exactly once and never retries.
func (g *git) pushClaimRef(ctx context.Context, key, expectedSHA, sha string) error {
	ref := "refs/" + key
	lease := "--force-with-lease=" + ref + ":" + expectedSHA
	out, err := g.runCmdRaw(ctx, g.repoPath, "push", lease, "origin", sha+":"+ref)
	if err == nil {
		return nil
	}
	if isLeaseConflict(string(out)) {
		return errors.Wrapf(ctx, ErrRefConflict, "push %s rejected", ref)
	}
	return errors.Wrapf(ctx, err, "git push %s: %s", ref, string(out))
}

// isLeaseConflict reports whether git push output describes a lost
// compare-and-swap race rather than a transport or hook failure.
func isLeaseConflict(output string) bool {
	for _, marker := range []string{
		"stale info",
		"remote ref does not exist",
		"cannot lock ref",
	} {
		if strings.Contains(output, marker) {
			return true
		}
	}
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, "[remote rejected]") {
			continue
		}
		start := strings.LastIndex(line, "(")
		end := strings.LastIndex(line, ")")
		if start < 0 || end <= start {
			continue
		}
		reason := line[start+1 : end]
		for _, marker := range []string{
			"expected",
			"failed to update ref",
			"reference already exists",
			"reference does not exist",
		} {
			if strings.Contains(reason, marker) {
				return true
			}
		}
	}
	return false
}

// newClaimNonce returns 32 lowercase hex characters of fresh randomness. The
// nonce guarantees that two writes of the same claim never share a commit sha,
// so the lease check can never be skipped as "everything up-to-date".
func newClaimNonce(ctx context.Context) (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", errors.Wrap(ctx, err, "generate claim nonce")
	}
	return hex.EncodeToString(buf), nil
}

// ValidateClaimKey rejects any key that is not a safe refs/claims/ ref name.
// The key is the full path below refs/, e.g. "claims/task/t-42".
func ValidateClaimKey(ctx context.Context, key string) error {
	if !strings.HasPrefix(key, claimKeyPrefix) {
		return errors.Wrapf(
			ctx,
			ErrInvalidRefName,
			"key %q must start with %q",
			key,
			claimKeyPrefix,
		)
	}
	if len(key) == len(claimKeyPrefix) {
		return errors.Wrapf(
			ctx,
			ErrInvalidRefName,
			"key %q has no components after %q",
			key,
			claimKeyPrefix,
		)
	}
	for _, component := range strings.Split(key, "/") {
		if component == "" || component == "." || component == ".." {
			return errors.Wrapf(
				ctx,
				ErrInvalidRefName,
				"key %q contains an empty or traversal component",
				key,
			)
		}
	}
	// #nosec G204 -- binary is hardcoded to "git"; the key is passed as a separate argument
	cmd := exec.CommandContext(ctx, "git", "check-ref-format", "refs/"+key)
	cmd.WaitDelay = waitDelay
	out, err := cmd.CombinedOutput()
	if err = normalizeWaitDelay(cmd, err); err != nil {
		return errors.Wrapf(ctx, ErrInvalidRefName, "invalid ref %q: %s", key, string(out))
	}
	return nil
}

// validateExpectedSHA accepts an empty sha or a full-length lowercase hex
// object id.
func validateExpectedSHA(ctx context.Context, sha string) error {
	if sha == "" {
		return nil
	}
	if !IsObjectID(sha) {
		return errors.Wrapf(ctx, ErrInvalidRefName, "invalid expected sha %q", sha)
	}
	return nil
}
