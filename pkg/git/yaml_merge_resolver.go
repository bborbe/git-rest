// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git

import (
	"bytes"
	"context"
	stderrors "errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/bborbe/errors"
	"github.com/golang/glog"
	"gopkg.in/yaml.v3"

	"github.com/bborbe/git-rest/pkg/metrics"
)

// Failure category labels emitted on git_rest_resolver_failures_total{category=...}.
const (
	resolverFailureYAMLParse  = "yaml_parse_failed"
	resolverFailureNoFront    = "no_frontmatter"
	resolverFailureWrite      = "write_failed"
	resolverFailureGitAdd     = "git_add_failed"
	resolverFailureUnsafePath = "unsafe_path"
)

// NewYAMLMergeResolver returns a ConflictResolver that deep-merges YAML frontmatter
// in conflicted markdown files. For each conflicted path it:
//
//  1. Reads the working-tree file (still containing git's three-way merge markers).
//  2. Extracts the "ours" and "theirs" sides of each conflict region.
//  3. Parses the YAML frontmatter on both sides; deep-merges keys (theirs wins on overlap).
//  4. Combines bodies per the rules in the spec (theirs if non-empty, else ours; both
//     differ → "ours\n\ntheirs").
//  5. Writes the merged file back and runs `git add -- <path>`.
//
// On any failure (YAML parse error, missing frontmatter delimiter pair on either side,
// disk write failure, git add failure) it increments the corresponding failure-category
// counter, emits a Warningf log line naming the path, and returns ErrConflictResolutionFailed.
// The caller (the puller) runs `git merge --abort` and surfaces the sentinel.
func NewYAMLMergeResolver(repoPath string, m metrics.Metrics) ConflictResolver {
	return &yamlMergeResolver{repoPath: repoPath, metrics: m}
}

type yamlMergeResolver struct {
	repoPath string
	metrics  metrics.Metrics
}

// Resolve handles each conflicted path in order. The first path that cannot be resolved
// aborts the loop and returns ErrConflictResolutionFailed; subsequent paths are NOT
// attempted (the puller will run `git merge --abort` and the next pull retries from a
// clean state).
func (r *yamlMergeResolver) Resolve(ctx context.Context, conflictedPaths []string) error {
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

func (r *yamlMergeResolver) resolveOne(ctx context.Context, path string) error {
	abs, err := r.safePath(path)
	if err != nil {
		r.metrics.IncResolverFailure(resolverFailureUnsafePath)
		glog.Warningf("yaml-merge-resolver: unsafe path %q: %v", path, err)
		return errors.Wrap(ctx, ErrConflictResolutionFailed, "unsafe conflict path")
	}

	raw, err := os.ReadFile(abs) // #nosec G304 -- path validated by safePath
	if err != nil {
		r.metrics.IncResolverFailure(resolverFailureWrite)
		glog.Warningf("yaml-merge-resolver: read %q: %v", path, err)
		return errors.Wrap(ctx, ErrConflictResolutionFailed, "read conflicted file")
	}

	ours, theirs, ok := splitConflictSides(string(raw))
	if !ok {
		r.metrics.IncResolverFailure(resolverFailureNoFront)
		glog.Warningf("yaml-merge-resolver: %q has no parseable conflict region", path)
		return errors.Wrap(ctx, ErrConflictResolutionFailed, "no conflict region")
	}

	oursFM, oursBody, ok := splitFrontmatter(ours)
	if !ok {
		r.metrics.IncResolverFailure(resolverFailureNoFront)
		glog.Warningf("yaml-merge-resolver: %q ours side has no frontmatter delimiter pair", path)
		return errors.Wrap(ctx, ErrConflictResolutionFailed, "ours: no frontmatter")
	}
	theirsFM, theirsBody, ok := splitFrontmatter(theirs)
	if !ok {
		r.metrics.IncResolverFailure(resolverFailureNoFront)
		glog.Warningf("yaml-merge-resolver: %q theirs side has no frontmatter delimiter pair", path)
		return errors.Wrap(ctx, ErrConflictResolutionFailed, "theirs: no frontmatter")
	}

	oursMap, err := r.parseYAML(ctx, oursFM, path, "ours")
	if err != nil {
		return err
	}
	theirsMap, err := r.parseYAML(ctx, theirsFM, path, "theirs")
	if err != nil {
		return err
	}

	merged := deepMergeTheirsWins(oursMap, theirsMap)
	mergedFM, err := yaml.Marshal(merged)
	if err != nil {
		r.metrics.IncResolverFailure(resolverFailureYAMLParse)
		glog.Warningf("yaml-merge-resolver: %q marshal merged YAML failed: %v", path, err)
		return errors.Wrap(ctx, ErrConflictResolutionFailed, "marshal merged yaml")
	}

	body := combineBodies(oursBody, theirsBody)
	final := "---\n" + string(mergedFM) + "---\n" + body

	if err := os.WriteFile(abs, []byte(final), 0o600); err != nil {
		r.metrics.IncResolverFailure(resolverFailureWrite)
		glog.Warningf("yaml-merge-resolver: write %q: %v", path, err)
		return errors.Wrap(ctx, ErrConflictResolutionFailed, "write merged file")
	}

	if err := r.runGitAdd(ctx, path); err != nil {
		return err
	}
	return nil
}

func (r *yamlMergeResolver) parseYAML(
	ctx context.Context,
	fm, path, side string,
) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	if err := yaml.Unmarshal([]byte(fm), &out); err != nil {
		r.metrics.IncResolverFailure(resolverFailureYAMLParse)
		glog.Warningf("yaml-merge-resolver: %q %s YAML parse failed: %v", path, side, err)
		return nil, errors.Wrap(ctx, ErrConflictResolutionFailed, side+": yaml parse")
	}
	return out, nil
}

func (r *yamlMergeResolver) runGitAdd(ctx context.Context, path string) error {
	// #nosec G204 -- git binary is hardcoded; paths come from git merge output
	cmd := exec.CommandContext(ctx, "git", "add", "--", path)
	cmd.Dir = r.repoPath
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		r.metrics.IncResolverFailure(resolverFailureGitAdd)
		glog.Warningf(
			"yaml-merge-resolver: git add %q failed: %v: %s",
			path,
			err,
			strings.TrimSpace(buf.String()),
		)
		return errors.Wrap(ctx, ErrConflictResolutionFailed, "git add merged file")
	}
	return nil
}

// safePath joins the repo root with the conflict-reported path, rejects absolute
// paths and any result that does not stay under the repo root after symlink eval.
// It does NOT call EvalSymlinks on the final path (the file is a real file written
// by git's three-way merge), but it rejects "..", absolute paths, and any path
// that does not resolve under repoPath.
func (r *yamlMergeResolver) safePath(rel string) (string, error) {
	if rel == "" {
		return "", stderrors.New("empty path")
	}
	if filepath.IsAbs(rel) {
		return "", stderrors.New("absolute path rejected")
	}
	joined := filepath.Join(r.repoPath, rel)
	cleanRoot := filepath.Clean(r.repoPath)
	if !strings.HasPrefix(
		joined+string(filepath.Separator),
		cleanRoot+string(filepath.Separator),
	) &&
		joined != cleanRoot {
		return "", stderrors.New("path escapes repo root")
	}
	return joined, nil
}

// splitConflictSides extracts the ours and theirs versions of the first conflict
// region from a file containing standard git three-way conflict markers. It returns
// ok=false if either marker is missing.
//
// Assumption: the conflict region is a self-contained frontmatter block — either it
// already starts with "---" (the opening delimiter was inside the conflict), or the
// base frontmatter's opening "---" was outside (above) the conflict markers and we
// reattach it via the conditional prepend below. Keys that live in the frontmatter
// but OUTSIDE the conflict region are NOT merged; the resolver only reconciles what
// the three-way merge actually disagreed on. This matches the spec's "agent vault
// write" scope (157-increment prod evidence: conflicts are frontmatter-shape
// disagreements where both sides ARE the whole block). For non-frontmatter-shape
// inputs, splitFrontmatter rejects the result and the resolver falls through to the
// no_frontmatter category — no silent content loss.
func splitConflictSides(content string) (oursStr string, theirsStr string, ok bool) {
	const (
		openMarker  = "<<<<<<<"
		sepMarker   = "======="
		closeMarker = ">>>>>>>"
	)

	lines := strings.Split(content, "\n")
	var oursLines, theirsLines []string
	inOurs := false
	inTheirs := false

	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, openMarker):
			inOurs = true
			inTheirs = false
		case strings.HasPrefix(line, sepMarker):
			inOurs = false
			inTheirs = true
		case strings.HasPrefix(line, closeMarker):
			inTheirs = false
		case inOurs:
			oursLines = append(oursLines, line)
		case inTheirs:
			theirsLines = append(theirsLines, line)
		}
	}

	if len(oursLines) == 0 || len(theirsLines) == 0 {
		return "", "", false
	}
	oursStr = strings.Join(oursLines, "\n")
	theirsStr = strings.Join(theirsLines, "\n")
	// Prepend "---\n" if not present.
	// This happens because git places <<<<<<< HEAD and ======= after the base frontmatter.
	if !strings.HasPrefix(oursStr, "---\n") {
		oursStr = "---\n" + oursStr
	}
	if !strings.HasPrefix(theirsStr, "---\n") {
		theirsStr = "---\n" + theirsStr
	}
	return oursStr, theirsStr, true
}

// splitFrontmatter splits an Obsidian-style markdown document into the YAML
// frontmatter body and the markdown body. The input MUST start with a line that
// is exactly "---" and contain a closing line that is exactly "---" before the
// body. Returns ok=false if either delimiter is missing.
func splitFrontmatter(doc string) (frontmatter string, body string, ok bool) {
	const delim = "---"
	lines := strings.Split(doc, "\n")
	if len(lines) < 2 || strings.TrimRight(lines[0], "\r") != delim {
		return "", "", false
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r") == delim {
			frontmatter = strings.Join(lines[1:i], "\n")
			if i+1 <= len(lines)-1 {
				body = strings.Join(lines[i+1:], "\n")
			}
			return frontmatter, body, true
		}
	}
	return "", "", false
}

// deepMergeTheirsWins returns a map containing the union of keys from ours and theirs.
// On overlapping keys whose values are both maps, it recurses. On overlapping keys
// whose values are not both maps, theirs wins.
func deepMergeTheirsWins(ours, theirs map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(ours)+len(theirs))
	for k, v := range ours {
		out[k] = v
	}
	for k, tv := range theirs {
		if ov, present := out[k]; present {
			ovMap, ovIsMap := ov.(map[string]interface{})
			tvMap, tvIsMap := tv.(map[string]interface{})
			if ovIsMap && tvIsMap {
				out[k] = deepMergeTheirsWins(ovMap, tvMap)
				continue
			}
		}
		out[k] = tv
	}
	return out
}

// combineBodies applies the spec body-merge rules:
//   - theirs body non-empty, ours empty   → theirs
//   - ours body non-empty, theirs empty   → ours
//   - both non-empty and equal            → ours (same as theirs)
//   - both non-empty and differ           → ours + "\n\n" + theirs
//   - both empty                          → ""
func combineBodies(ours, theirs string) string {
	switch {
	case theirs == "" && ours == "":
		return ""
	case theirs == "":
		return ours
	case ours == "":
		return theirs
	case ours == theirs:
		return ours
	default:
		return ours + "\n\n" + theirs
	}
}
