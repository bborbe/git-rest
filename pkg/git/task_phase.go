// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git

import "strings"

// taskPhaseAlias is the documented phase alias that vault-cli's
// NormalizeTaskPhase maps onto the canonical "execution" value.
const taskPhaseAlias = "in_progress"

// taskPhaseCanonical is the canonical phase value the alias is rewritten to.
const taskPhaseCanonical = "execution"

// normalizeTaskPhase rewrites the documented `in_progress` phase alias to the
// canonical `execution` inside a task file's frontmatter. It returns content
// byte-identical when path is not a task file, when there is no frontmatter
// block, or when no line carries the alias.
//
// Only a top-level `phase:` line is considered: an indented key (nested under
// another key or inside a list) is left untouched, matching the flat
// frontmatter vault-cli reads and writes. Only the first matching line is
// rewritten, and every other line — `status: in_progress` included — survives
// byte-identically.
func normalizeTaskPhase(path string, content []byte) []byte {
	if !isTaskFilePath(path) {
		return content
	}
	if _, _, ok := splitFrontmatter(string(content)); !ok {
		return content
	}

	// Splitting on "\n" and joining on "\n" reconstructs the input exactly, so
	// the delimiters and their line endings survive untouched.
	lines := strings.Split(string(content), "\n")

	// Locate the closing delimiter; only lines between the two delimiters are
	// frontmatter.
	closing := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r") == "---" {
			closing = i
			break
		}
	}
	if closing < 0 {
		return content
	}

	for i := 1; i < closing; i++ {
		line := lines[i]
		lineNoCR := strings.TrimSuffix(line, "\r")
		if len(lineNoCR) == 0 || lineNoCR[0] == ' ' || lineNoCR[0] == '\t' {
			continue
		}
		if !strings.HasPrefix(lineNoCR, "phase:") {
			continue
		}
		value := strings.Trim(lineNoCR[len("phase:"):], " \t")
		value = trimOneQuotePair(value)
		if value != taskPhaseAlias {
			continue
		}
		replacement := "phase: " + taskPhaseCanonical
		if strings.HasSuffix(line, "\r") {
			replacement += "\r"
		}
		lines[i] = replacement
		return []byte(strings.Join(lines, "\n"))
	}

	return content
}

// trimOneQuotePair strips one pair of surrounding single or double quotes from
// value. Any other quoting shape is returned unchanged.
func trimOneQuotePair(value string) string {
	if len(value) < 2 {
		return value
	}
	first, last := value[0], value[len(value)-1]
	if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
		return value[1 : len(value)-1]
	}
	return value
}
