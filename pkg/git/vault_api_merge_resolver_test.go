// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git_test

import (
	"context"
	stderrors "errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/git-rest/mocks"
	"github.com/bborbe/git-rest/pkg/git"
	"github.com/bborbe/git-rest/pkg/metrics"
)

// setupAddAddConflictRepo creates a repo whose file exists on both branches but
// not on their common ancestor. git leaves such a conflict with only stages 2
// and 3 — there is no stage 1 — which is the case the vault-api resolver must
// hand to its fallback rather than post a base-less merge.
func setupAddAddConflictRepo() (workDir, relPath string, cleanup func()) {
	dir, err := os.MkdirTemp("", "vault-api-add-add-*")
	Expect(err).NotTo(HaveOccurred())

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, runErr := cmd.CombinedOutput()
		Expect(runErr).NotTo(HaveOccurred(), "git %v: %s", args, string(out))
	}

	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	// An unrelated commit, so the two branches share a base that lacks the file.
	Expect(os.WriteFile(filepath.Join(dir, "seed.md"), []byte("seed\n"), 0o600)).To(Succeed())
	run("add", "--", "seed.md")
	run("commit", "-q", "-m", "seed")

	relPath = "note.md"
	abs := filepath.Join(dir, relPath)

	run("checkout", "-q", "-b", "theirs")
	Expect(os.WriteFile(abs, []byte("---\ntitle: Theirs\n---\ntheirs\n"), 0o600)).To(Succeed())
	run("add", "--", relPath)
	run("commit", "-q", "-m", "theirs adds")

	run("checkout", "-q", "main")
	Expect(os.WriteFile(abs, []byte("---\ntitle: Ours\n---\nours\n"), 0o600)).To(Succeed())
	run("add", "--", relPath)
	run("commit", "-q", "-m", "ours adds")

	merge := exec.Command("git", "merge", "--no-edit", "theirs")
	merge.Dir = dir
	_ = merge.Run() // non-zero exit expected

	return dir, relPath, func() { _ = os.RemoveAll(dir) }
}

// unmergedStages returns the stage entries git still holds for path. After a
// successful `git add` the list is empty: the conflict is resolved.
func unmergedStages(workDir, path string) string {
	cmd := exec.Command("git", "ls-files", "-u", "--", path)
	cmd.Dir = workDir
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "git ls-files -u: %s", string(out))
	return strings.TrimSpace(string(out))
}

var _ = Describe("VaultAPIMergeResolver", func() {
	var (
		ctx         context.Context
		workDir     string
		relPath     string
		cleanup     func()
		client      *mocks.FakeVaultAPIMergeClient
		fallback    *mocks.FakeConflictResolver
		fakeMetrics *mocks.FakeMetrics
		resolver    git.ConflictResolver
	)

	BeforeEach(func() {
		ctx = context.Background()
		workDir, relPath, cleanup = setupYAMLConflictRepo(
			"phase: execution\n", "body\n",
			"phase: planning\n", "body\n",
		)
		client = &mocks.FakeVaultAPIMergeClient{}
		fallback = &mocks.FakeConflictResolver{}
		fakeMetrics = &mocks.FakeMetrics{}
		resolver = git.NewVaultAPIMergeResolver(workDir, client, fallback, fakeMetrics)
	})

	AfterEach(func() {
		cleanup()
	})

	It("posts the three sides and writes the merged bytes", func() {
		// vault-api returns the bytes rather than writing them, because this
		// process holds the repository mutex for the whole pull.
		mergedContent := "---\nphase: execution\ntitle: Merged\n---\nbody\n"
		client.MergeReturns(git.VaultAPIMergeResult{
			Result:  "merged",
			Content: mergedContent,
		}, nil)

		Expect(resolver.Resolve(ctx, []string{relPath})).To(Succeed())

		Expect(client.MergeCallCount()).To(Equal(1))
		_, path, base, ours, theirs := client.MergeArgsForCall(0)
		Expect(path).To(Equal(relPath))
		// Three distinct contents can only come from the three index stages: the
		// working tree holds git's marker-annotated merge, not any one side.
		Expect(base.Content).To(ContainSubstring("base: 1"))
		Expect(ours.Content).To(ContainSubstring("phase: execution"))
		Expect(theirs.Content).To(ContainSubstring("phase: planning"))
		Expect(ours.CommitterTime.IsZero()).To(BeFalse())
		Expect(theirs.CommitterTime.IsZero()).To(BeFalse())
		Expect(base.CommitterTime.IsZero()).To(BeFalse())

		// The bytes landed verbatim and the conflict is resolved in the index.
		written, err := os.ReadFile(filepath.Join(workDir, relPath))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(written)).To(Equal(mergedContent))
		Expect(unmergedStages(workDir, relPath)).To(BeEmpty())
		Expect(fallback.ResolveCallCount()).To(Equal(0))
	})

	It("falls back when vault-api reports merged without the bytes", func() {
		// A merged answer carrying no content has nothing to stage, so staging
		// would leave git's marker file in the index while the log claimed a
		// clean merge.
		client.MergeReturns(git.VaultAPIMergeResult{Result: "merged"}, nil)
		fallback.ResolveReturns(stderrors.New("fallback failed"))

		err := resolver.Resolve(ctx, []string{relPath})

		Expect(fallback.ResolveCallCount()).To(Equal(1))
		Expect(unmergedStages(workDir, relPath)).NotTo(BeEmpty())
		Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("vault_api_empty_content"))
		Expect(git.QuarantineCauseFromError(err)).To(Equal(metrics.QuarantineCauseEmptyContent))
	})

	It("falls back when vault-api quarantines the file", func() {
		client.MergeReturns(
			git.VaultAPIMergeResult{Result: "quarantined", Reason: "body_conflict"}, nil,
		)

		// The fallback resolves the file, so Resolve returns nil and there is no
		// tagged error to read the cause from. The category still records the
		// deliberate decline.
		Expect(resolver.Resolve(ctx, []string{relPath})).To(Succeed())

		Expect(client.MergeCallCount()).To(Equal(1))
		Expect(fallback.ResolveCallCount()).To(Equal(1))
		_, paths := fallback.ResolveArgsForCall(0)
		Expect(paths).To(Equal([]string{relPath}))
		Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("vault_api_declined"))
	})

	It("tags a decline with the declined cause when the fallback also fails", func() {
		// The previous case has the fallback resolve the file, so Resolve returns
		// nil and the Declined tag is never exercised. Here the fallback fails too,
		// so the file is quarantined and the cause must be readable as Declined.
		client.MergeReturns(
			git.VaultAPIMergeResult{Result: "quarantined", Reason: "body_conflict"}, nil,
		)
		fallback.ResolveReturns(stderrors.New("fallback failed"))

		err := resolver.Resolve(ctx, []string{relPath})

		Expect(fallback.ResolveCallCount()).To(Equal(1))
		Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("vault_api_declined"))
		Expect(git.QuarantineCauseFromError(err)).To(Equal(metrics.QuarantineCauseDeclined))
	})

	It("falls back when vault-api is unreachable", func() {
		client.MergeReturns(git.VaultAPIMergeResult{}, stderrors.New("connection refused"))
		fallback.ResolveReturns(stderrors.New("fallback failed"))

		err := resolver.Resolve(ctx, []string{relPath})

		Expect(fallback.ResolveCallCount()).To(Equal(1))
		Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("vault_api_error"))
		Expect(git.QuarantineCauseFromError(err)).To(Equal(metrics.QuarantineCauseBreak))
	})

	It("falls back without posting when the conflict has no base stage", func() {
		// An add/add conflict has stages 2 and 3 only. A merge of one side against
		// nothing is not a merge, so the request must never be sent.
		addDir, addPath, cleanupAddAdd := setupAddAddConflictRepo()
		defer cleanupAddAdd()
		addResolver := git.NewVaultAPIMergeResolver(
			addDir, client, fallback, fakeMetrics,
		)

		fallback.ResolveReturns(stderrors.New("fallback failed"))

		err := addResolver.Resolve(ctx, []string{addPath})

		Expect(client.MergeCallCount()).To(Equal(0))
		Expect(fallback.ResolveCallCount()).To(Equal(1))
		Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("vault_api_no_three_sides"))
		Expect(git.QuarantineCauseFromError(err)).To(Equal(metrics.QuarantineCauseNoThreeSides))
	})

	It("keeps resolving the remaining paths after one falls back", func() {
		// A decline is an expected outcome, not a broken state: unlike the YAML
		// resolver it must not abort the whole list on the first one.
		client.MergeReturns(git.VaultAPIMergeResult{}, stderrors.New("boom"))

		Expect(resolver.Resolve(ctx, []string{relPath, relPath})).To(Succeed())

		Expect(client.MergeCallCount()).To(Equal(2))
		Expect(fallback.ResolveCallCount()).To(Equal(2))
	})
})
