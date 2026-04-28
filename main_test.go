// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/format"
	"github.com/onsi/gomega/gexec"

	main "github.com/bborbe/git-rest"
)

//go:generate go run -mod=mod github.com/maxbrunsfeld/counterfeiter/v6 -generate

var _ = Describe("Main", func() {
	It("Compiles", func() {
		var err error
		_, err = gexec.Build(".", "-mod=mod")
		Expect(err).NotTo(HaveOccurred())
	})
})

var _ = Describe("CleanupStaleLocks", func() {
	var (
		ctx     context.Context
		repoDir string
		gitDir  string
	)

	BeforeEach(func() {
		ctx = context.Background()
		repoDir = GinkgoT().TempDir()
		gitDir = filepath.Join(repoDir, ".git")
	})

	Context("when .git/ does not exist", func() {
		It("returns nil", func() {
			Expect(main.CleanupStaleLocks(ctx, repoDir)).To(Succeed())
		})
	})

	Context("when .git/ exists but is empty", func() {
		BeforeEach(func() {
			Expect(os.MkdirAll(gitDir, 0o755)).To(Succeed())
		})
		It("returns nil and removes nothing", func() {
			Expect(main.CleanupStaleLocks(ctx, repoDir)).To(Succeed())
		})
	})

	Context("when .git/index.lock exists", func() {
		var lockPath string
		BeforeEach(func() {
			Expect(os.MkdirAll(gitDir, 0o755)).To(Succeed())
			lockPath = filepath.Join(gitDir, "index.lock")
			Expect(os.WriteFile(lockPath, []byte("stale"), 0o644)).To(Succeed())
		})
		It("removes the lock", func() {
			Expect(main.CleanupStaleLocks(ctx, repoDir)).To(Succeed())
			_, err := os.Stat(lockPath)
			Expect(os.IsNotExist(err)).To(BeTrue())
		})
	})

	Context("when nested .git/refs/heads/main.lock exists", func() {
		var lockPath string
		BeforeEach(func() {
			refsHeads := filepath.Join(gitDir, "refs", "heads")
			Expect(os.MkdirAll(refsHeads, 0o755)).To(Succeed())
			lockPath = filepath.Join(refsHeads, "main.lock")
			Expect(os.WriteFile(lockPath, []byte("stale"), 0o644)).To(Succeed())
		})
		It("removes the nested lock", func() {
			Expect(main.CleanupStaleLocks(ctx, repoDir)).To(Succeed())
			_, err := os.Stat(lockPath)
			Expect(os.IsNotExist(err)).To(BeTrue())
		})
	})

	Context("when non-lock files exist", func() {
		var headPath string
		BeforeEach(func() {
			Expect(os.MkdirAll(gitDir, 0o755)).To(Succeed())
			headPath = filepath.Join(gitDir, "HEAD")
			Expect(os.WriteFile(headPath, []byte("ref: refs/heads/main"), 0o644)).To(Succeed())
		})
		It("leaves them untouched", func() {
			Expect(main.CleanupStaleLocks(ctx, repoDir)).To(Succeed())
			_, err := os.Stat(headPath)
			Expect(err).NotTo(HaveOccurred())
		})
	})
})

func TestSuite(t *testing.T) {
	time.Local = time.UTC
	format.TruncatedDiff = false
	RegisterFailHandler(Fail)
	suiteConfig, reporterConfig := GinkgoConfiguration()
	suiteConfig.Timeout = 60 * time.Second
	RunSpecs(t, "Main Suite", suiteConfig, reporterConfig)
}
