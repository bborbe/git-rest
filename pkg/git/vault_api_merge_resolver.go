// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git

import (
	"bytes"
	"context"
	stderrors "errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bborbe/errors"
	"github.com/golang/glog"

	"github.com/bborbe/git-rest/pkg/metrics"
)

// resolverFailureVaultAPI labels a conflict the vault-api merge path could not
// resolve — an unreachable service, a refused request, or a returned Reason.
// Every such case falls through to the fallback resolver, so this counter
// measures how often the automatic path declined rather than how often the
// merge failed outright.
const resolverFailureVaultAPI = "vault_api_failed"

// Side is one side of a diverged file as vault-api's merge endpoint expects it:
// the file's content at that revision plus the revision's committer time, which
// the merge uses to break ties between two edits to the same frontmatter key.
type Side struct {
	Content       string
	CommitterTime time.Time
}

// VaultAPIMergeResult reports what the merge decided. Result is "merged" or
// "quarantined"; Reason names why a quarantine happened.
type VaultAPIMergeResult struct {
	Result string `json:"result"`
	Reason string `json:"reason"`
}

// vaultAPIMerged is the only Result that means the file was reconciled. Any
// other value — a quarantine, or a shape this client does not recognise — is
// treated as a decline so the caller falls back.
const vaultAPIMerged = "merged"

// VaultAPIMergeClient resolves a diverged file through vault-api's
// POST /api/v1/merge. It exists as an interface so the resolver can be tested
// without an HTTP server.
//
//counterfeiter:generate -o ../../mocks/vault_api_merge_client.go --fake-name FakeVaultAPIMergeClient . VaultAPIMergeClient
type VaultAPIMergeClient interface {
	Merge(ctx context.Context, path string, base, ours, theirs Side) (VaultAPIMergeResult, error)
}

// NewVaultAPIMergeResolver returns a ConflictResolver that hands each conflicted
// path to vault-api and stages whatever the merge wrote.
//
// It exists because git-rest is the only process that sees a divergence: the
// pull runs here, and the three sides exist only while the merge is open, as
// index stages 1/2/3. vault-api owns the merge itself — the per-key frontmatter
// rules — and writes the result through git-rest's own write API, so this
// resolver's job is to supply the sides and then stage the outcome.
//
// fallback is invoked per path whenever vault-api cannot resolve: it is
// unreachable, it answers a quarantine (a body both sides edited differently
// carries no content to stage), or it returns any other failure. The fallback
// keeps the pre-existing YAML-merge behaviour, so enabling this path can only
// add resolutions, never remove one.
func NewVaultAPIMergeResolver(
	repoPath string,
	client VaultAPIMergeClient,
	fallback ConflictResolver,
	m metrics.Metrics,
) ConflictResolver {
	return &vaultAPIMergeResolver{
		repoPath:  repoPath,
		client:    client,
		fallback:  fallback,
		metrics:   m,
		sideTimes: gitMergeSideTimes(repoPath),
	}
}

type vaultAPIMergeResolver struct {
	repoPath  string
	client    VaultAPIMergeClient
	fallback  ConflictResolver
	metrics   metrics.Metrics
	sideTimes func(ctx context.Context) (ours int64, theirs int64, err error)
}

// Resolve handles each conflicted path independently. A path vault-api cannot
// resolve falls back on its own; the remaining paths still get their turn,
// which is deliberately unlike yamlMergeResolver's abort-on-first-failure: a
// decline here is an expected outcome, not a broken state.
func (r *vaultAPIMergeResolver) Resolve(ctx context.Context, conflictedPaths []string) error {
	for _, path := range conflictedPaths {
		select {
		case <-ctx.Done():
			return errors.Wrap(ctx, ctx.Err(), "context cancelled")
		default:
		}
		if err := r.resolveOne(ctx, path); err != nil {
			return errors.Wrapf(ctx, err, "resolve conflict %q", path)
		}
	}
	return nil
}

func (r *vaultAPIMergeResolver) resolveOne(ctx context.Context, path string) error {
	base, ours, theirs, err := r.sides(ctx, path)
	if err != nil {
		// No three sides means this conflict is not a modify/modify one — an
		// add/add or delete/modify conflict has no base, and a merge of one
		// side against nothing is not a merge. Leave it to the fallback.
		r.metrics.IncResolverFailure(resolverFailureVaultAPI)
		glog.Infof("vault-api-merge-resolver: %q has no three sides (%v), falling back", path, err)
		return r.fallback.Resolve(ctx, []string{path})
	}

	result, err := r.client.Merge(ctx, path, base, ours, theirs)
	if err != nil {
		r.metrics.IncResolverFailure(resolverFailureVaultAPI)
		glog.Warningf(
			"vault-api-merge-resolver: %q merge request failed: %v, falling back",
			path,
			err,
		)
		return r.fallback.Resolve(ctx, []string{path})
	}
	if result.Result != vaultAPIMerged {
		r.metrics.IncResolverFailure(resolverFailureVaultAPI)
		glog.Infof(
			"vault-api-merge-resolver: %q declined (result=%q reason=%q), falling back",
			path,
			result.Result,
			result.Reason,
		)
		return r.fallback.Resolve(ctx, []string{path})
	}

	// vault-api wrote the merged bytes through git-rest's write API, so they are
	// already in the working tree. Staging is all that is left: the puller's
	// merge commit picks the file up from the index.
	//
	// The write is checked rather than assumed. A "merged" answer whose write did
	// not land — a coalesced write still pending, a target outside this working
	// tree — leaves git's marker file in place, and staging it would commit the
	// markers while the log claimed a clean merge. Markers mean the outcome did
	// not arrive, so that is a decline, not a success.
	content, err := r.readConflicted(path)
	if err != nil {
		r.metrics.IncResolverFailure(resolverFailureVaultAPI)
		glog.Warningf("vault-api-merge-resolver: read %q after merge: %v, falling back", path, err)
		return r.fallback.Resolve(ctx, []string{path})
	}
	if bytes.Contains(content, []byte("<<<<<<<")) {
		r.metrics.IncResolverFailure(resolverFailureVaultAPI)
		glog.Warningf(
			"vault-api-merge-resolver: %q reported merged but still carries conflict markers, falling back",
			path,
		)
		return r.fallback.Resolve(ctx, []string{path})
	}

	if err := runGitAdd(ctx, r.repoPath, path); err != nil {
		r.metrics.IncResolverFailure(resolverFailureGitAdd)
		glog.Warningf("vault-api-merge-resolver: git add %q failed: %v", path, err)
		return errors.Wrap(ctx, ErrConflictResolutionFailed, "git add merged file")
	}
	r.metrics.IncAutoresolved()
	glog.Infof("vault-api-merge-resolver: %q merged by vault-api", path)
	return nil
}

// readConflicted returns the working-tree file for path, refusing a path that
// escapes the repository. It mirrors yamlMergeResolver.safePath, which is a
// method on the other resolver and so cannot be reused here.
func (r *vaultAPIMergeResolver) readConflicted(path string) ([]byte, error) {
	if path == "" || filepath.IsAbs(path) {
		return nil, stderrors.New("path is empty or absolute")
	}
	joined := filepath.Join(r.repoPath, path)
	cleanRoot := filepath.Clean(r.repoPath)
	if !strings.HasPrefix(joined, cleanRoot+string(filepath.Separator)) {
		return nil, stderrors.New("path escapes repo root")
	}
	return os.ReadFile(joined) // #nosec G304 -- path confined to the repo root above
}

// sides reads the three versions of path out of the merge's index stages and
// pairs each with its committer time. Stage 1 is the merge base, 2 is ours
// (HEAD), 3 is theirs (MERGE_HEAD).
//
// The times come from the commits rather than the blobs: git stores no committer
// time on a blob, and the merge needs the times to decide which side's edit to a
// shared key is newer.
func (r *vaultAPIMergeResolver) sides(
	ctx context.Context, path string,
) (base, ours, theirs Side, err error) {
	baseContent, err := runGitOutput(ctx, r.repoPath, "show", ":1:"+path)
	if err != nil {
		return Side{}, Side{}, Side{}, errors.Wrap(ctx, err, "read base stage")
	}
	oursContent, err := runGitOutput(ctx, r.repoPath, "show", ":2:"+path)
	if err != nil {
		return Side{}, Side{}, Side{}, errors.Wrap(ctx, err, "read ours stage")
	}
	theirsContent, err := runGitOutput(ctx, r.repoPath, "show", ":3:"+path)
	if err != nil {
		return Side{}, Side{}, Side{}, errors.Wrap(ctx, err, "read theirs stage")
	}

	oursSeconds, theirsSeconds, err := r.sideTimes(ctx)
	if err != nil {
		return Side{}, Side{}, Side{}, errors.Wrap(ctx, err, "read merge-parent times")
	}
	baseSeconds, err := r.baseTime(ctx)
	if err != nil {
		return Side{}, Side{}, Side{}, errors.Wrap(ctx, err, "read merge-base time")
	}

	return Side{
		Content:       baseContent,
		CommitterTime: time.Unix(baseSeconds, 0).UTC(),
	}, Side{
		Content:       oursContent,
		CommitterTime: time.Unix(oursSeconds, 0).UTC(),
	}, Side{
		Content:       theirsContent,
		CommitterTime: time.Unix(theirsSeconds, 0).UTC(),
	}, nil
}

// baseTime returns the committer timestamp of the merge base of HEAD and
// MERGE_HEAD — the commit stage 1 was taken from.
func (r *vaultAPIMergeResolver) baseTime(ctx context.Context) (int64, error) {
	theirsSHA, err := gitMergeHeadSHA(ctx, r.repoPath)
	if err != nil {
		return 0, err
	}
	out, err := runGitOutput(ctx, r.repoPath, "merge-base", "HEAD", theirsSHA)
	if err != nil {
		return 0, err
	}
	baseSHA := strings.TrimSpace(out)
	if baseSHA == "" {
		return 0, errors.New(ctx, "git merge-base returned an empty revision")
	}
	return gitCommitTimestamp(ctx, r.repoPath, baseSHA)
}

// runGitAdd stages one path. It mirrors yamlMergeResolver.runGitAdd, which is a
// method; this resolver needs the same behaviour per path and has no receiver
// state to carry it.
func runGitAdd(ctx context.Context, repoPath, path string) error {
	_, err := runGitOutput(ctx, repoPath, "add", "--", path)
	return err
}
