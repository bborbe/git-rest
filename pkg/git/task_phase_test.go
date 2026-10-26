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

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/git-rest/mocks"
	"github.com/bborbe/git-rest/pkg/git"
)

// setupTaskMergeRepo builds a real three-way merge conflict at relPath, which
// must live in a task directory, so the vault-api resolver sees genuine base,
// ours and theirs index stages. Returns workDir and a cleanup func.
func setupTaskMergeRepo(relPath string) (workDir string, cleanup func()) {
	dir, err := os.MkdirTemp("", "task-phase-merge-*")
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

	abs := filepath.Join(dir, relPath)
	Expect(os.MkdirAll(filepath.Dir(abs), 0o750)).To(Succeed())
	Expect(os.WriteFile(abs, []byte("---\nphase: todo\n---\nbase\n"), 0o600)).To(Succeed())
	run("add", "--", relPath)
	run("commit", "-q", "-m", "base")

	run("checkout", "-q", "-b", "theirs")
	Expect(os.WriteFile(abs, []byte("---\nphase: planning\n---\ntheirs\n"), 0o600)).
		To(Succeed())
	run("add", "--", relPath)
	run("commit", "-q", "-m", "theirs")

	run("checkout", "-q", "main")
	Expect(os.WriteFile(abs, []byte("---\nphase: in_progress\n---\nours\n"), 0o600)).
		To(Succeed())
	run("add", "--", relPath)
	run("commit", "-q", "-m", "ours")

	merge := exec.Command("git", "merge", "--no-edit", "theirs")
	merge.Dir = dir
	_ = merge.Run() // non-zero exit expected

	return dir, func() { _ = os.RemoveAll(dir) }
}

var _ = Describe("normalizeTaskPhase", func() {
	DescribeTable(
		"rewrites only the documented phase alias",
		func(path, input, expected string) {
			Expect(git.NormalizeTaskPhaseForTest(path, []byte(input))).
				To(Equal([]byte(expected)))
		},
		Entry(
			"task file with the alias becomes execution",
			"tasks/x.md",
			"---\nphase: in_progress\ntitle: x\n---\nbody\n",
			"---\nphase: execution\ntitle: x\n---\nbody\n",
		),
		Entry(
			"25 Tasks file with the alias becomes execution",
			"25 Tasks/x.md",
			"---\nphase: in_progress\n---\nbody\n",
			"---\nphase: execution\n---\nbody\n",
		),
		Entry(
			"quoted alias becomes execution",
			"tasks/x.md",
			"---\nphase: \"in_progress\"\n---\nbody\n",
			"---\nphase: execution\n---\nbody\n",
		),
		Entry(
			"planning is unchanged",
			"tasks/x.md",
			"---\nphase: planning\n---\nbody\n",
			"---\nphase: planning\n---\nbody\n",
		),
		Entry(
			"execution is unchanged",
			"tasks/x.md",
			"---\nphase: execution\n---\nbody\n",
			"---\nphase: execution\n---\nbody\n",
		),
		Entry(
			"status in_progress is unchanged",
			"tasks/x.md",
			"---\nstatus: in_progress\n---\nbody\n",
			"---\nstatus: in_progress\n---\nbody\n",
		),
		Entry(
			"non-task path is unchanged",
			"notes/x.md",
			"---\nphase: in_progress\n---\nbody\n",
			"---\nphase: in_progress\n---\nbody\n",
		),
		Entry(
			"content without frontmatter is unchanged",
			"tasks/x.md",
			"phase: in_progress\n",
			"phase: in_progress\n",
		),
		Entry(
			"indented phase is unchanged",
			"tasks/x.md",
			"---\nmeta:\n  phase: in_progress\n---\nbody\n",
			"---\nmeta:\n  phase: in_progress\n---\nbody\n",
		),
		Entry(
			"CRLF alias becomes execution and keeps CRLF",
			"tasks/x.md",
			"---\r\nphase: in_progress\r\n---\r\nbody\r\n",
			"---\r\nphase: execution\r\n---\r\nbody\r\n",
		),
		Entry(
			"already-canonical document is byte-identical",
			"tasks/x.md",
			"---\nphase: execution\ntitle: x\n---\nbody\n",
			"---\nphase: execution\ntitle: x\n---\nbody\n",
		),
	)
})

var _ = Describe("task phase normalization on write paths", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("normalizes through WriteFile", func() {
		workDir, cleanup := initRepo()
		defer cleanup()
		g := git.New(
			workDir,
			&noopMetrics{},
			libtime.NewCurrentDateTime(),
			"",
			git.NewMarkerResolver(workDir),
		)

		Expect(g.WriteFile(
			ctx,
			"tasks/x.md",
			[]byte("---\nphase: in_progress\ntitle: x\n---\nbody\n"),
		)).To(Succeed())

		written, err := os.ReadFile(filepath.Join(workDir, "tasks/x.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(written)).To(Equal("---\nphase: execution\ntitle: x\n---\nbody\n"))
	})

	It("normalizes through WriteFileIfAbsent", func() {
		workDir, cleanup := initRepo()
		defer cleanup()
		g := git.New(
			workDir,
			&noopMetrics{},
			libtime.NewCurrentDateTime(),
			"",
			git.NewMarkerResolver(workDir),
		)

		Expect(g.WriteFileIfAbsent(
			ctx,
			"25 Tasks/x.md",
			[]byte("---\nphase: in_progress\n---\nbody\n"),
		)).To(Succeed())

		written, err := os.ReadFile(filepath.Join(workDir, "25 Tasks/x.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(written)).To(Equal("---\nphase: execution\n---\nbody\n"))
	})

	It("normalizes through the YAML merge resolver", func() {
		conflicted := "---\n" +
			"<<<<<<< HEAD\n" +
			"phase: in_progress\ntitle: ours\n" +
			"---\nours body\n" +
			"=======\n" +
			"title: theirs\n" +
			"---\ntheirs body\n" +
			">>>>>>> theirs\n"
		workDir, rel, cleanup := setupHandWrittenConflict("tasks/x.md", conflicted)
		defer cleanup()

		r := git.NewYAMLMergeResolver(workDir, &mocks.FakeMetrics{})
		Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

		fm := parseFrontmatterOf(filepath.Join(workDir, rel))
		Expect(fm).To(HaveKeyWithValue("phase", "execution"))
	})

	It("normalizes through the vault-api merge resolver", func() {
		workDir, cleanup := setupTaskMergeRepo("tasks/x.md")
		defer cleanup()

		client := &mocks.FakeVaultAPIMergeClient{}
		client.MergeReturns(git.VaultAPIMergeResult{
			Result:  "merged",
			Content: "---\nphase: in_progress\n---\nmerged body\n",
		}, nil)
		r := git.NewVaultAPIMergeResolver(
			workDir, client, &mocks.FakeConflictResolver{}, &mocks.FakeMetrics{},
		)

		Expect(r.Resolve(ctx, []string{"tasks/x.md"})).To(Succeed())

		written, err := os.ReadFile(filepath.Join(workDir, "tasks/x.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(written)).To(Equal("---\nphase: execution\n---\nmerged body\n"))
		Expect(strings.Contains(string(written), "in_progress")).To(BeFalse())
	})
})
