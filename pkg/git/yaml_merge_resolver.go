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
	"strconv"
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

	oursFM, oursBody, oursOK := splitFrontmatter(ours)
	theirsFM, theirsBody, theirsOK := splitFrontmatter(theirs)
	if !oursOK || !theirsOK {
		applied, ruleErr := r.applyScalarFrontmatterRule(ctx, path, abs, string(raw))
		if applied {
			return ruleErr
		}
		r.metrics.IncResolverFailure(resolverFailureNoFront)
		glog.Warningf(
			"yaml-merge-resolver: %q has no frontmatter delimiter pair on one side and the scalar-frontmatter rule does not apply",
			path,
		)
		return errors.Wrap(ctx, ErrConflictResolutionFailed, "no frontmatter delimiter pair")
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

// conflictRegion is one conflict region located in the original file's lines.
type conflictRegion struct {
	startLine   int      // index of the "<<<<<<<" line
	oursLines   []string // lines between "<<<<<<<" and "=======", in order
	theirsLines []string // lines between "=======" and ">>>>>>>", in order
	endLine     int      // index of the ">>>>>>>" line
}

// enumerateConflictRegions returns every conflict region in lines, in file
// order. ok is false when the markers are unbalanced, a region is nested, or
// the file holds no region at all.
func enumerateConflictRegions(lines []string) ([]conflictRegion, bool) {
	scanner := &conflictRegionScanner{regions: make([]conflictRegion, 0, len(lines))}
	for i, line := range lines {
		if !scanner.consume(line, i) {
			return nil, false
		}
	}
	if scanner.open || len(scanner.regions) == 0 {
		return nil, false
	}
	return scanner.regions, true
}

// conflictRegionScanner is the state machine enumerateConflictRegions drives,
// split out so each function stays under the cognitive-complexity limit.
type conflictRegionScanner struct {
	regions       []conflictRegion
	current       conflictRegion
	open          bool
	separatorSeen bool
}

// consume advances the scanner by one line. ok is false on unbalanced markers.
func (s *conflictRegionScanner) consume(line string, index int) bool {
	switch {
	case strings.HasPrefix(line, "<<<<<<<"):
		if s.open {
			return false
		}
		s.open = true
		s.separatorSeen = false
		s.current = conflictRegion{startLine: index}
	case strings.HasPrefix(line, "======="):
		if !s.open || s.separatorSeen {
			return false
		}
		s.separatorSeen = true
	case strings.HasPrefix(line, ">>>>>>>"):
		if !s.open || !s.separatorSeen {
			return false
		}
		s.current.endLine = index
		s.regions = append(s.regions, s.current)
		s.open = false
		s.separatorSeen = false
	default:
		s.appendContentLine(line)
	}
	return true
}

func (s *conflictRegionScanner) appendContentLine(line string) {
	if !s.open {
		return
	}
	if s.separatorSeen {
		s.current.theirsLines = append(s.current.theirsLines, line)
		return
	}
	s.current.oursLines = append(s.current.oursLines, line)
}

// frontmatterBounds returns the index of the closing "---" line in the original
// file. The opening delimiter is always line 0 when ok is true.
func frontmatterBounds(lines []string) (closeIndex int, ok bool) {
	const delim = "---"
	if len(lines) == 0 || strings.TrimRight(lines[0], "\r") != delim {
		return 0, false
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r") == delim {
			return i, true
		}
	}
	return 0, false
}

// scalarEntry is one parsed frontmatter line: the raw line plus its parsed YAML
// value node.
type scalarEntry struct {
	line  string
	value *yaml.Node
}

// parseScalarEntry reports whether line is a single-line "key: value" mapping
// entry whose value is a YAML scalar, returning the key.
func parseScalarEntry(line string) (key string, entry scalarEntry, ok bool) {
	trimmed := strings.TrimRight(line, "\r")
	if trimmed == "" {
		return "", scalarEntry{}, false
	}
	if trimmed[0] == ' ' || trimmed[0] == '\t' {
		return "", scalarEntry{}, false
	}
	if trimmed == "-" || strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "#") {
		return "", scalarEntry{}, false
	}

	var document yaml.Node
	if err := yaml.Unmarshal([]byte(trimmed), &document); err != nil {
		return "", scalarEntry{}, false
	}
	if len(document.Content) != 1 {
		return "", scalarEntry{}, false
	}
	mapping := document.Content[0]
	if mapping.Kind != yaml.MappingNode || len(mapping.Content) != 2 {
		return "", scalarEntry{}, false
	}

	keyNode := mapping.Content[0]
	valueNode := mapping.Content[1]
	if keyNode.Kind != yaml.ScalarNode || keyNode.Tag != "!!str" ||
		keyNode.Value == "" || keyNode.Anchor != "" {
		return "", scalarEntry{}, false
	}
	if valueNode.Kind != yaml.ScalarNode || valueNode.Anchor != "" ||
		valueNode.Style == yaml.LiteralStyle || valueNode.Style == yaml.FoldedStyle {
		return "", scalarEntry{}, false
	}

	return keyNode.Value, scalarEntry{line: trimmed, value: valueNode}, true
}

// scalarKeyEntry pairs a key with its parsed entry on the ours side.
type scalarKeyEntry struct {
	key   string
	entry scalarEntry
}

// scalarRegion is an eligible conflict region with both sides parsed.
type scalarRegion struct {
	conflictRegion
	ours   []scalarKeyEntry // ours-side order
	theirs map[string]scalarEntry
}

// planScalarRegions decides whether the whole file is eligible for the
// scalar-frontmatter rule. It inspects shape only: no timestamps, no I/O.
func planScalarRegions(lines []string) ([]scalarRegion, bool) {
	closeIndex, ok := frontmatterBounds(lines)
	if !ok {
		return nil, false
	}
	conflicts, ok := enumerateConflictRegions(lines)
	if !ok {
		return nil, false
	}

	regions := make([]scalarRegion, 0, len(conflicts))
	previousEnd := 0
	for _, region := range conflicts {
		if !regionInsideFrontmatter(region, closeIndex, previousEnd) {
			return nil, false
		}
		ours, theirs, parsed := parseScalarSide(region.oursLines, region.theirsLines)
		if !parsed {
			return nil, false
		}
		regions = append(regions, scalarRegion{
			conflictRegion: region,
			ours:           ours,
			theirs:         theirs,
		})
		previousEnd = region.endLine
	}

	if len(regions) == 0 {
		return nil, false
	}
	return regions, true
}

// regionInsideFrontmatter reports whether region lies strictly between the
// opening and closing delimiters and does not overlap the previous region.
func regionInsideFrontmatter(region conflictRegion, closeIndex, previousEnd int) bool {
	return region.startLine > 0 &&
		region.endLine < closeIndex &&
		region.startLine > previousEnd
}

// parseScalarSide parses both sides of one region and enforces the key-set
// guards: at least one key per side, no duplicates, identical key sets.
func parseScalarSide(
	oursLines, theirsLines []string,
) ([]scalarKeyEntry, map[string]scalarEntry, bool) {
	ours, ok := parseScalarOurs(oursLines)
	if !ok {
		return nil, nil, false
	}
	theirs, ok := parseScalarTheirs(theirsLines)
	if !ok {
		return nil, nil, false
	}
	if len(ours) == 0 || len(theirs) == 0 || len(ours) != len(theirs) {
		return nil, nil, false
	}
	for _, oursEntry := range ours {
		if _, present := theirs[oursEntry.key]; !present {
			return nil, nil, false
		}
	}
	return ours, theirs, true
}

func parseScalarOurs(lines []string) ([]scalarKeyEntry, bool) {
	ours := make([]scalarKeyEntry, 0, len(lines))
	seen := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		key, entry, ok := parseScalarEntry(line)
		if !ok {
			return nil, false
		}
		if _, duplicate := seen[key]; duplicate {
			return nil, false
		}
		seen[key] = struct{}{}
		ours = append(ours, scalarKeyEntry{key: key, entry: entry})
	}
	return ours, true
}

func parseScalarTheirs(lines []string) (map[string]scalarEntry, bool) {
	theirs := make(map[string]scalarEntry, len(lines))
	for _, line := range lines {
		key, entry, ok := parseScalarEntry(line)
		if !ok {
			return nil, false
		}
		if _, duplicate := theirs[key]; duplicate {
			return nil, false
		}
		theirs[key] = entry
	}
	return theirs, true
}

// isCounterKey reports whether key takes the max-of-both-sides rule.
func isCounterKey(key string) bool {
	return key == "trigger_count" || key == "retry_count" || strings.HasPrefix(key, "metrics_")
}

// integerValue returns the integer a parsed scalar node carries, and whether it
// carries one at all.
func integerValue(node *yaml.Node) (int64, bool) {
	if node == nil || node.Tag != "!!int" {
		return 0, false
	}
	value, err := strconv.ParseInt(node.Value, 10, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

// resolveScalarEntry returns the replacement line for one key in one region.
// ok is false when the rule cannot decide this key, which makes the whole file
// ineligible.
func resolveScalarEntry(key string, ours, theirs scalarEntry) (line string, ok bool) {
	if isCounterKey(key) {
		oursValue, oursOK := integerValue(ours.value)
		theirsValue, theirsOK := integerValue(theirs.value)
		if oursOK && theirsOK {
			maxValue := oursValue
			if theirsValue > oursValue {
				maxValue = theirsValue
			}
			return key + ": " + strconv.FormatInt(maxValue, 10), true
		}
	}
	return "", false
}

// spliceScalarRegions returns the original file with each conflict region
// replaced by its resolved lines. Every line outside a region is copied
// verbatim, so the closing "---" and the body are byte-identical.
func spliceScalarRegions(
	lines []string,
	regions []scalarRegion,
	replacements [][]string,
) string {
	out := make([]string, 0, len(lines))
	cursor := 0
	for i, region := range regions {
		out = append(out, lines[cursor:region.startLine]...)
		out = append(out, replacements[i]...)
		cursor = region.endLine + 1
	}
	out = append(out, lines[cursor:]...)
	return strings.Join(out, "\n")
}

// applyScalarFrontmatterRule attempts the scalar-frontmatter rule on the raw
// conflicted file. applied reports whether the rule took responsibility for the
// file; when it did, the file has been written and staged and err is the outcome.
func (r *yamlMergeResolver) applyScalarFrontmatterRule(
	ctx context.Context, path, abs, raw string,
) (applied bool, err error) {
	if !strings.EqualFold(filepath.Ext(path), ".md") {
		return false, nil
	}

	lines := strings.Split(raw, "\n")
	regions, ok := planScalarRegions(lines)
	if !ok {
		return false, nil
	}

	replacements := make([][]string, 0, len(regions))
	for _, region := range regions {
		resolved := make([]string, 0, len(region.ours))
		for _, oursEntry := range region.ours {
			line, resolvedOK := resolveScalarEntry(
				oursEntry.key,
				oursEntry.entry,
				region.theirs[oursEntry.key],
			)
			if !resolvedOK {
				return false, nil
			}
			resolved = append(resolved, line)
		}
		replacements = append(replacements, resolved)
	}

	final := spliceScalarRegions(lines, regions, replacements)

	select {
	case <-ctx.Done():
		return true, errors.Wrap(ctx, ctx.Err(), "scalar resolve cancelled")
	default:
	}

	// #nosec G703 -- path validated by safePath
	if writeErr := os.WriteFile(abs, []byte(final), 0o600); writeErr != nil {
		r.metrics.IncResolverFailure(resolverFailureWrite)
		glog.Warningf("yaml-merge-resolver: write scalar-resolved %q: %v", path, writeErr)
		return true, errors.Wrap(ctx, ErrConflictResolutionFailed, "write scalar-resolved file")
	}

	if err := r.runGitAdd(ctx, path); err != nil {
		return true, err
	}
	return true, nil
}
