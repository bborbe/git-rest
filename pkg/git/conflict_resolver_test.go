// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/git-rest/pkg/git"
)

// setupConflictFixture creates a pair of repos with a genuine same-line conflict.
// Returns workDir (has .git/MERGE_HEAD after setup), and a cleanup func.
// The conflict is on "shared.txt": remote wrote "remote\n", local wrote "local\n".
func setupConflictFixture() (workDir string, cleanup func()) {
	workDir, externalPush, c := setupPullFixture()

	// External writer commits "remote\n" to shared.txt and pushes.
	externalPush("shared.txt", "remote\n")

	// Local writer commits "local\n" to the same file (creates divergence + conflict).
	writeLocalCommit(workDir, "shared.txt", "local\n")

	// Fetch remote so FETCH_HEAD / tracking ref is up to date.
	runGit(workDir, "fetch")

	// Derive upstream tracking ref without hardcoding branch name.
	upstream := strings.TrimSpace(
		gitOutputStr(workDir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"),
	)

	// Run merge — expected to fail with a conflict on shared.txt.
	cmd := exec.Command("git", "merge", "--no-edit", upstream)
	cmd.Dir = workDir
	_ = cmd.Run() // non-zero exit expected; working tree now has conflicted shared.txt

	_, statErr := os.Stat(filepath.Join(workDir, ".git", "MERGE_HEAD"))
	if statErr != nil {
		panic("setupConflictFixture: expected .git/MERGE_HEAD to exist after failed merge")
	}

	return workDir, c
}

var _ = Describe("MarkerResolver", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	Describe("Resolve", func() {
		Context("AC4 happy path: conflicted file staged after Resolve, markers preserved", func() {
			It("stages the conflicted file and leaves content with conflict markers", func() {
				workDir, cleanup := setupConflictFixture()
				defer cleanup()

				resolver := git.NewMarkerResolver(workDir)
				err := resolver.Resolve(ctx, []string{"shared.txt"})
				Expect(err).NotTo(HaveOccurred())

				// File must no longer appear as unmerged.
				unmerged := strings.TrimSpace(
					gitOutputStr(workDir, "diff", "--name-only", "--diff-filter=U"),
				)
				Expect(unmerged).To(BeEmpty(), "shared.txt should not be unmerged after Resolve")

				// Content must still contain conflict markers (not cleaned up by resolver).
				content, readErr := os.ReadFile(filepath.Join(workDir, "shared.txt"))
				Expect(readErr).NotTo(HaveOccurred())
				Expect(
					string(content),
				).To(ContainSubstring("<<<<<<<"), "conflict markers must be preserved")
				Expect(
					string(content),
				).To(ContainSubstring("======="), "conflict markers must be preserved")
				Expect(
					string(content),
				).To(ContainSubstring(">>>>>>>"), "conflict markers must be preserved")

				// Both versions must be in the file.
				Expect(
					string(content),
				).To(ContainSubstring("remote"), "remote version must be preserved")
				Expect(
					string(content),
				).To(ContainSubstring("local"), "local version must be preserved")
			})
		})

		Context("AC4 error path: non-existent path returns error", func() {
			It("returns a wrapped error when the path does not exist", func() {
				workDir, cleanup := setupConflictFixture()
				defer cleanup()

				resolver := git.NewMarkerResolver(workDir)
				err := resolver.Resolve(ctx, []string{"does-not-exist.txt"})
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("does-not-exist.txt"))
			})
		})
	})
})
