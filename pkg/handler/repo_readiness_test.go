// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/git-rest/pkg/handler"
)

var _ = Describe("RepoReadiness", func() {
	It("reports ready for a readable repository path", func() {
		repoDir := GinkgoT().TempDir()
		ready, reason := handler.NewRepoReadiness(repoDir).ReadinessStatus()
		Expect(ready).To(BeTrue())
		Expect(reason).To(Equal("ok"))
	})

	It("reports ready before any pull has run (cold start)", func() {
		repoDir := GinkgoT().TempDir()
		ready, reason := handler.NewRepoReadiness(repoDir).ReadinessStatus()
		Expect(ready).To(BeTrue())
		Expect(reason).To(Equal("ok"))
	})

	It("reports not ready for a missing repository path", func() {
		repoDir := GinkgoT().TempDir()
		Expect(os.RemoveAll(repoDir)).To(Succeed())
		ready, reason := handler.NewRepoReadiness(repoDir).ReadinessStatus()
		Expect(ready).To(BeFalse())
		Expect(reason).To(Equal("repo path unreadable"))
	})

	It("reports not ready when an ancestor of the repository path is a regular file", func() {
		dir := GinkgoT().TempDir()
		blocker := filepath.Join(dir, "blocker")
		Expect(os.WriteFile(blocker, []byte("not a directory"), 0o600)).To(Succeed())
		repoPath := filepath.Join(dir, "blocker", "repo")
		ready, reason := handler.NewRepoReadiness(repoPath).ReadinessStatus()
		Expect(ready).To(BeFalse())
		Expect(reason).To(Equal("repo path unreadable"))
	})

	It("never leaks a host path or a sync word in the reason", func() {
		readyDir := GinkgoT().TempDir()
		_, readyReason := handler.NewRepoReadiness(readyDir).ReadinessStatus()
		Expect(readyReason).NotTo(ContainSubstring(readyDir))
		Expect(readyReason).NotTo(MatchRegexp(`(?i)push|pull|sync|conflict`))

		missingDir := GinkgoT().TempDir()
		Expect(os.RemoveAll(missingDir)).To(Succeed())
		_, missingReason := handler.NewRepoReadiness(missingDir).ReadinessStatus()
		Expect(missingReason).NotTo(ContainSubstring(missingDir))
		Expect(missingReason).NotTo(MatchRegexp(`(?i)push|pull|sync|conflict`))
	})
})
