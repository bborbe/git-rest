// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git_test

import (
	"context"
	stderrors "errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"gopkg.in/yaml.v3"

	"github.com/bborbe/git-rest/mocks"
	"github.com/bborbe/git-rest/pkg/git"
)

// setupYAMLConflictRepo creates a real git repo at a temp dir containing a single
// markdown file in conflicted state. Returns workDir, the conflicted file path
// (relative to workDir), and a cleanup func.
func setupYAMLConflictRepo(
	oursFrontmatter, oursBody, theirsFrontmatter, theirsBody string,
) (workDir, relPath string, cleanup func()) {
	dir, err := os.MkdirTemp("", "yaml-merge-test-*")
	Expect(err).NotTo(HaveOccurred())

	run := func(name string, args ...string) {
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		out, runErr := cmd.CombinedOutput()
		Expect(runErr).NotTo(HaveOccurred(), "%s %v: %s", name, args, string(out))
	}

	run("git", "init", "-q", "-b", "main")
	run("git", "config", "user.email", "test@example.com")
	run("git", "config", "user.name", "test")

	relPath = "note.md"
	abs := filepath.Join(dir, relPath)

	base := "---\nbase: 1\n---\nbase body\n"
	Expect(os.WriteFile(abs, []byte(base), 0o600)).To(Succeed())
	run("git", "add", "--", relPath)
	run("git", "commit", "-q", "-m", "base")

	// Branch "theirs", commit theirs version.
	run("git", "checkout", "-q", "-b", "theirs")
	theirsContent := "---\n" + theirsFrontmatter + "---\n" + theirsBody
	Expect(os.WriteFile(abs, []byte(theirsContent), 0o600)).To(Succeed())
	run("git", "add", "--", relPath)
	run("git", "commit", "-q", "-m", "theirs")

	// Back to main, write ours.
	run("git", "checkout", "-q", "main")
	oursContent := "---\n" + oursFrontmatter + "---\n" + oursBody
	Expect(os.WriteFile(abs, []byte(oursContent), 0o600)).To(Succeed())
	run("git", "add", "--", relPath)
	run("git", "commit", "-q", "-m", "ours")

	// Merge theirs into main — expect non-zero exit + conflict markers in file.
	cmd := exec.Command("git", "merge", "--no-edit", "theirs")
	cmd.Dir = dir
	_ = cmd.Run() // non-zero exit expected

	// Sanity: file must now contain conflict markers.
	content, readErr := os.ReadFile(abs)
	Expect(readErr).NotTo(HaveOccurred())
	Expect(string(content)).To(ContainSubstring("<<<<<<<"))

	cleanup = func() { _ = os.RemoveAll(dir) }
	return dir, relPath, cleanup
}

// setupScalarMergeRepo builds a real repo whose note.md is left in a genuine
// conflicted merge. base, ours and theirs are full file contents. oursDate and
// theirsDate set GIT_AUTHOR_DATE and GIT_COMMITTER_DATE (RFC3339) on the
// respective commits; an empty string leaves the commit date at the current
// time. Returns workDir, the repo-relative path, and a cleanup func.
func setupScalarMergeRepo(
	base, ours, theirs, oursDate, theirsDate string,
) (workDir, relPath string, cleanup func()) {
	dir, err := os.MkdirTemp("", "scalar-merge-test-*")
	Expect(err).NotTo(HaveOccurred())

	runWithDate := func(date, name string, args ...string) {
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		if date != "" {
			cmd.Env = append(
				os.Environ(),
				"GIT_AUTHOR_DATE="+date,
				"GIT_COMMITTER_DATE="+date,
			)
		}
		out, runErr := cmd.CombinedOutput()
		Expect(runErr).NotTo(HaveOccurred(), "%s %v: %s", name, args, string(out))
	}
	run := func(name string, args ...string) {
		runWithDate("", name, args...)
	}

	run("git", "init", "-q", "-b", "main")
	run("git", "config", "user.email", "test@example.com")
	run("git", "config", "user.name", "test")

	relPath = "note.md"
	abs := filepath.Join(dir, relPath)

	Expect(os.WriteFile(abs, []byte(base), 0o600)).To(Succeed())
	run("git", "add", "--", relPath)
	run("git", "commit", "-q", "-m", "base")

	run("git", "checkout", "-q", "-b", "theirs")
	Expect(os.WriteFile(abs, []byte(theirs), 0o600)).To(Succeed())
	runWithDate(theirsDate, "git", "add", "--", relPath)
	runWithDate(theirsDate, "git", "commit", "-q", "-m", "theirs")

	run("git", "checkout", "-q", "main")
	Expect(os.WriteFile(abs, []byte(ours), 0o600)).To(Succeed())
	runWithDate(oursDate, "git", "add", "--", relPath)
	runWithDate(oursDate, "git", "commit", "-q", "-m", "ours")

	cmd := exec.Command("git", "merge", "--no-edit", "theirs")
	cmd.Dir = dir
	_ = cmd.Run() // non-zero exit expected

	content, readErr := os.ReadFile(abs)
	Expect(readErr).NotTo(HaveOccurred())
	Expect(string(content)).To(ContainSubstring("<<<<<<<"))

	cleanup = func() { _ = os.RemoveAll(dir) }
	return dir, relPath, cleanup
}

// setupScalarAddAddRepo builds a repo where both branches create note.md with
// different content, so the merge is an add/add conflict with no index stage 1
// for note.md. Returns workDir, the repo-relative path, and a cleanup func.
func setupScalarAddAddRepo(ours, theirs string) (workDir, relPath string, cleanup func()) {
	dir, err := os.MkdirTemp("", "scalar-addadd-test-*")
	Expect(err).NotTo(HaveOccurred())

	run := func(name string, args ...string) {
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		out, runErr := cmd.CombinedOutput()
		Expect(runErr).NotTo(HaveOccurred(), "%s %v: %s", name, args, string(out))
	}

	run("git", "init", "-q", "-b", "main")
	run("git", "config", "user.email", "test@example.com")
	run("git", "config", "user.name", "test")

	relPath = "note.md"
	abs := filepath.Join(dir, relPath)

	Expect(os.WriteFile(filepath.Join(dir, "README.md"), []byte("base\n"), 0o600)).To(Succeed())
	run("git", "add", "--", "README.md")
	run("git", "commit", "-q", "-m", "base")

	run("git", "checkout", "-q", "-b", "theirs")
	Expect(os.WriteFile(abs, []byte(theirs), 0o600)).To(Succeed())
	run("git", "add", "--", relPath)
	run("git", "commit", "-q", "-m", "theirs")

	run("git", "checkout", "-q", "main")
	Expect(os.WriteFile(abs, []byte(ours), 0o600)).To(Succeed())
	run("git", "add", "--", relPath)
	run("git", "commit", "-q", "-m", "ours")

	cmd := exec.Command("git", "merge", "--no-edit", "theirs")
	cmd.Dir = dir
	_ = cmd.Run() // non-zero exit expected

	content, readErr := os.ReadFile(abs)
	Expect(readErr).NotTo(HaveOccurred())
	Expect(string(content)).To(ContainSubstring("<<<<<<<"))

	cleanup = func() { _ = os.RemoveAll(dir) }
	return dir, relPath, cleanup
}

// setupHandWrittenConflict builds a repo with the given repo-relative path
// committed as a base file and then overwritten with a hand-written conflicted
// body (staged, not committed), so the resolver sees exactly the bytes given.
func setupHandWrittenConflict(
	path, conflicted string,
) (workDir, relPath string, cleanup func()) {
	dir, err := os.MkdirTemp("", "handwritten-conflict-test-*")
	Expect(err).NotTo(HaveOccurred())

	run := func(name string, args ...string) {
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		out, runErr := cmd.CombinedOutput()
		Expect(runErr).NotTo(HaveOccurred(), "%s %v: %s", name, args, string(out))
	}

	run("git", "init", "-q", "-b", "main")
	run("git", "config", "user.email", "test@example.com")
	run("git", "config", "user.name", "test")

	relPath = path
	abs := filepath.Join(dir, relPath)
	Expect(os.MkdirAll(filepath.Dir(abs), 0o750)).To(Succeed())
	Expect(os.WriteFile(abs, []byte("base\n"), 0o600)).To(Succeed())
	run("git", "add", "--", relPath)
	run("git", "commit", "-q", "-m", "base")

	Expect(os.WriteFile(abs, []byte(conflicted), 0o600)).To(Succeed())
	run("git", "add", "--", relPath)

	cleanup = func() { _ = os.RemoveAll(dir) }
	return dir, relPath, cleanup
}

// assertScalarQuarantine writes conflicted into a plain repo, optionally mutates
// the repo (for example to plant a merge-head file), then asserts that the
// default resolver quarantines the file untouched under no_frontmatter.
func assertScalarQuarantine(
	ctx context.Context,
	fakeMetrics *mocks.FakeMetrics,
	conflicted string,
	mutate func(workDir string),
) {
	workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
	defer cleanup()
	if mutate != nil {
		mutate(workDir)
	}

	abs := filepath.Join(workDir, rel)
	before, err := os.ReadFile(abs)
	Expect(err).NotTo(HaveOccurred())

	r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
	err = r.Resolve(ctx, []string{rel})
	Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
	Expect(fakeMetrics.IncResolverFailureCallCount()).To(Equal(1))
	Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("no_frontmatter"))

	after, err := os.ReadFile(abs)
	Expect(err).NotTo(HaveOccurred())
	Expect(after).To(Equal(before))
}

// parseFrontmatterOf reads the staged file's frontmatter (the YAML block between the
// first two "---" delimiters) and returns it as a map.
func parseFrontmatterOf(path string) map[string]interface{} {
	raw, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())
	lines := strings.Split(string(raw), "\n")
	Expect(lines[0]).To(Equal("---"), "file must begin with frontmatter delimiter")
	var fmLines []string
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			break
		}
		fmLines = append(fmLines, lines[i])
	}
	out := map[string]interface{}{}
	Expect(yaml.Unmarshal([]byte(strings.Join(fmLines, "\n")), &out)).To(Succeed())
	return out
}

// bodyOf returns the body (after the second "---" delimiter) of the staged file.
func bodyOf(path string) string {
	raw, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())
	lines := strings.Split(string(raw), "\n")
	delimitersSeen := 0
	bodyStart := -1
	for i := range lines {
		if lines[i] == "---" {
			delimitersSeen++
			if delimitersSeen == 2 {
				bodyStart = i + 1
				break
			}
		}
	}
	Expect(bodyStart).To(BeNumerically(">=", 0))
	return strings.TrimRight(strings.Join(lines[bodyStart:], "\n"), "\n")
}

// preinitLabelsOf returns the list of "category" label values present on the
// named CounterVec in the prometheus default registry.
func preinitLabelsOf(metricName string) []string {
	mfs, err := prometheus.DefaultGatherer.Gather()
	Expect(err).NotTo(HaveOccurred())
	var out []string
	for _, mf := range mfs {
		if mf.GetName() != metricName {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "category" {
					out = append(out, l.GetValue())
				}
			}
		}
	}
	return out
}

var _ = Describe("YAMLMergeResolver", func() {
	var (
		ctx         context.Context
		fakeMetrics *mocks.FakeMetrics
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeMetrics = &mocks.FakeMetrics{}
	})

	Describe("Resolve happy path", func() {
		It("AC-deepmerge: merges frontmatter with theirs winning on overlap", func() {
			workDir, rel, cleanup := setupYAMLConflictRepo(
				"a: 1\nb: 2\n", "ours body",
				"b: 3\nc: 4\n", "theirs body",
			)
			defer cleanup()

			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

			fm := parseFrontmatterOf(filepath.Join(workDir, rel))
			Expect(fm).To(HaveKeyWithValue("a", 1))
			Expect(fm).To(HaveKeyWithValue("b", 3))
			Expect(fm).To(HaveKeyWithValue("c", 4))
		})

		It("AC-body-both: ours + theirs body when both non-empty and differ", func() {
			workDir, rel, cleanup := setupYAMLConflictRepo(
				"a: 1\n", "X",
				"b: 2\n", "Y",
			)
			defer cleanup()
			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			Expect(r.Resolve(ctx, []string{rel})).To(Succeed())
			Expect(bodyOf(filepath.Join(workDir, rel))).To(Equal("X\n\nY"))
		})

		It("AC-body-theirs-empty: ours body kept when theirs empty", func() {
			workDir, rel, cleanup := setupYAMLConflictRepo(
				"a: 1\n", "X",
				"b: 2\n", "",
			)
			defer cleanup()
			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			Expect(r.Resolve(ctx, []string{rel})).To(Succeed())
			Expect(bodyOf(filepath.Join(workDir, rel))).To(Equal("X"))
		})

		It("AC-body-ours-empty: theirs body used when ours empty", func() {
			workDir, rel, cleanup := setupYAMLConflictRepo(
				"a: 1\n", "",
				"b: 2\n", "Y",
			)
			defer cleanup()
			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			Expect(r.Resolve(ctx, []string{rel})).To(Succeed())
			Expect(bodyOf(filepath.Join(workDir, rel))).To(Equal("Y"))
		})

		// A conflict region that lies entirely below the frontmatter. Every shape
		// here quarantines today, through one of two gates: splitConflictSides
		// returns ok=false when either side is empty (:374), and for a
		// both-non-empty body region it prepends "---\n", so splitFrontmatter
		// finds no closing delimiter (:113).
		Describe("Resolve body-only conflict regions", func() {
			It("AC-bodyonly-ours-empty: theirs body replaces the region verbatim", func() {
				prefix := "---\npage_type: daily-note\n---\n\n# Daily Plan\n\nintro\n"
				suffix := "\n\n## Later\n\ntail\n"

				theirsLines := make([]string, 0, 98)
				for i := 1; i <= 98; i++ {
					theirsLines = append(theirsLines, fmt.Sprintf("theirs line %d", i))
				}
				theirsBody := strings.Join(theirsLines, "\n")

				conflicted := prefix + "<<<<<<< HEAD\n=======\n" +
					theirsBody + "\n>>>>>>> theirs\n" + suffix

				workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
				defer cleanup()

				r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
				Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

				written, err := os.ReadFile(filepath.Join(workDir, rel))
				Expect(err).NotTo(HaveOccurred())
				// Every byte outside the region is unchanged, and the region is
				// replaced by the theirs body verbatim — the empty-ours -> theirs rule.
				Expect(string(written)).To(Equal(prefix + theirsBody + "\n" + suffix))
				Expect(string(written)).NotTo(ContainSubstring("<<<<<<<"))
				Expect(string(written)).NotTo(ContainSubstring(">>>>>>>"))
				Expect(string(written)).NotTo(ContainSubstring("======="))
			})

			It("AC-bodyonly-theirs-empty: ours body kept verbatim", func() {
				prefix := "---\npage_type: note\n---\n\n# Heading\n\n"
				suffix := "\n\ntail\n"
				oursBody := "keep me\nand me"

				conflicted := prefix + "<<<<<<< HEAD\n" + oursBody + "\n=======\n" +
					">>>>>>> theirs\n" + suffix

				workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
				defer cleanup()

				r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
				Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

				written, err := os.ReadFile(filepath.Join(workDir, rel))
				Expect(err).NotTo(HaveOccurred())
				Expect(string(written)).To(Equal(prefix + oursBody + "\n" + suffix))
			})

			It("AC-bodyonly-both-differ: ours + blank line + theirs", func() {
				prefix := "---\npage_type: goal\n---\n\n# Heading\n\n"
				suffix := "\n\ntail\n"
				oursBody := "a\nb\nc"
				theirsBody := "a\nb\nc\nd"

				conflicted := prefix + "<<<<<<< HEAD\n" + oursBody + "\n=======\n" +
					theirsBody + "\n>>>>>>> theirs\n" + suffix

				workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
				defer cleanup()

				r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
				Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

				written, err := os.ReadFile(filepath.Join(workDir, rel))
				Expect(err).NotTo(HaveOccurred())
				Expect(
					string(written),
				).To(Equal(prefix + oursBody + "\n\n" + theirsBody + "\n" + suffix))
			})

			It("AC-bodyonly-nonmarkdown: a .txt body conflict still quarantines", func() {
				prefix := "header\n\n"
				suffix := "\n\ntail\n"
				oursBody := "a\nb\nc"
				theirsBody := "a\nb\nc\nd"

				conflicted := prefix + "<<<<<<< HEAD\n" + oursBody + "\n=======\n" +
					theirsBody + "\n>>>>>>> theirs\n" + suffix

				workDir, rel, cleanup := setupHandWrittenConflict("note.txt", conflicted)
				defer cleanup()

				r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
				err := r.Resolve(ctx, []string{rel})
				Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())

				after, readErr := os.ReadFile(filepath.Join(workDir, rel))
				Expect(readErr).NotTo(HaveOccurred())
				Expect(string(after)).To(Equal(conflicted))
			})
		})

		Describe("Body-only conflict regions: extended", func() {
			It("resolves an empty-ours region without a resolver failure", func() {
				prefix := "---\npage_type: daily-note\n---\n\n# Daily Plan\n\nintro\n"
				suffix := "\n\n## Later\n\ntail\n"
				theirsLines := make([]string, 0, 98)
				for i := 1; i <= 98; i++ {
					theirsLines = append(theirsLines, fmt.Sprintf("theirs line %d", i))
				}
				theirsBody := strings.Join(theirsLines, "\n")
				conflicted := prefix + "<<<<<<< HEAD\n=======\n" +
					theirsBody + "\n>>>>>>> theirs\n" + suffix

				workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
				defer cleanup()

				r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
				Expect(r.Resolve(ctx, []string{rel})).To(Succeed())
				Expect(fakeMetrics.IncResolverFailureCallCount()).To(Equal(0))
			})

			It("resolves an empty-theirs region without a resolver failure", func() {
				prefix := "---\npage_type: note\n---\n\n# Heading\n\n"
				suffix := "\n\ntail\n"
				conflicted := prefix + "<<<<<<< HEAD\nkeep me\nand me\n=======\n" +
					">>>>>>> theirs\n" + suffix

				workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
				defer cleanup()

				r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
				Expect(r.Resolve(ctx, []string{rel})).To(Succeed())
				Expect(fakeMetrics.IncResolverFailureCallCount()).To(Equal(0))
			})

			It("resolves a both-differ region without a resolver failure", func() {
				prefix := "---\npage_type: goal\n---\n\n# Heading\n\n"
				suffix := "\n\ntail\n"
				conflicted := prefix + "<<<<<<< HEAD\na\nb\nc\n=======\n" +
					"a\nb\nc\nd\n>>>>>>> theirs\n" + suffix

				workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
				defer cleanup()

				r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
				Expect(r.Resolve(ctx, []string{rel})).To(Succeed())
				Expect(fakeMetrics.IncResolverFailureCallCount()).To(Equal(0))
			})

			It("resolves two body regions independently and preserves the separator", func() {
				conflicted := "---\npage_type: note\n---\n" +
					"<<<<<<< HEAD\nfirst ours\n=======\nfirst theirs\n>>>>>>> theirs\n" +
					"SEPARATOR LINE\n" +
					"<<<<<<< HEAD\nsecond ours\n=======\nsecond theirs\n>>>>>>> theirs\n"

				workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
				defer cleanup()

				r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
				Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

				written, err := os.ReadFile(filepath.Join(workDir, rel))
				Expect(err).NotTo(HaveOccurred())
				Expect(string(written)).To(Equal(
					"---\npage_type: note\n---\n" +
						"first ours\n\nfirst theirs\n" +
						"SEPARATOR LINE\n" +
						"second ours\n\nsecond theirs\n",
				))
				Expect(string(written)).NotTo(ContainSubstring("<<<<<<<"))
				Expect(string(written)).NotTo(ContainSubstring("======="))
				Expect(string(written)).NotTo(ContainSubstring(">>>>>>>"))
			})

			It("normalises the task phase alias on a task-shaped path", func() {
				conflicted := "---\npage_type: task\nphase: in_progress\ntitle: probe\n---\n" +
					"<<<<<<< HEAD\nours body\n=======\ntheirs body\n>>>>>>> theirs\n"

				workDir, rel, cleanup := setupHandWrittenConflict("25 Tasks/note.md", conflicted)
				defer cleanup()

				r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
				Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

				written, err := os.ReadFile(filepath.Join(workDir, rel))
				Expect(err).NotTo(HaveOccurred())
				Expect(string(written)).To(Equal(
					"---\npage_type: task\nphase: execution\ntitle: probe\n---\n" +
						"ours body\n\ntheirs body\n",
				))
			})

			It("quarantines a non-markdown path with a frontmatter block", func() {
				conflicted := "---\npage_type: note\n---\n" +
					"<<<<<<< HEAD\nbody a\n=======\nbody b\n>>>>>>> theirs\n"

				workDir, rel, cleanup := setupHandWrittenConflict("note.txt", conflicted)
				defer cleanup()

				abs := filepath.Join(workDir, rel)
				before, err := os.ReadFile(abs)
				Expect(err).NotTo(HaveOccurred())

				r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
				err = r.Resolve(ctx, []string{rel})
				Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
				Expect(fakeMetrics.IncResolverFailureCallCount()).To(Equal(1))
				Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("no_frontmatter"))

				after, err := os.ReadFile(abs)
				Expect(err).NotTo(HaveOccurred())
				Expect(after).To(Equal(before))
			})

			It("quarantines a markdown file with no frontmatter block", func() {
				conflicted := "header\n\n" +
					"<<<<<<< HEAD\nbody a\n=======\nbody b\n>>>>>>> theirs\n"

				workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
				defer cleanup()

				abs := filepath.Join(workDir, rel)
				before, err := os.ReadFile(abs)
				Expect(err).NotTo(HaveOccurred())

				r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
				err = r.Resolve(ctx, []string{rel})
				Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
				Expect(fakeMetrics.IncResolverFailureCallCount()).To(Equal(1))
				Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("no_frontmatter"))

				after, err := os.ReadFile(abs)
				Expect(err).NotTo(HaveOccurred())
				Expect(after).To(Equal(before))
			})

			It("rejects the whole file when a region crosses the boundary", func() {
				conflicted := "---\npage_type: note\n" +
					"<<<<<<< HEAD\nbody ours\n=======\ntags: [b]\n---\nbody theirs\n>>>>>>> theirs\n" +
					"<<<<<<< HEAD\ntail ours\n=======\ntail theirs\n>>>>>>> theirs\n"

				workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
				defer cleanup()

				abs := filepath.Join(workDir, rel)
				before, err := os.ReadFile(abs)
				Expect(err).NotTo(HaveOccurred())

				r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
				err = r.Resolve(ctx, []string{rel})
				Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
				Expect(fakeMetrics.IncResolverFailureCallCount()).To(Equal(1))
				Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("no_frontmatter"))

				after, err := os.ReadFile(abs)
				Expect(err).NotTo(HaveOccurred())
				Expect(after).To(Equal(before))
			})

			It("stages a body-only region in a genuine conflicted merge", func() {
				workDir, rel, cleanup := setupYAMLConflictRepo(
					"a: 1\n", "X",
					"a: 1\n", "Y",
				)
				defer cleanup()

				r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
				Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

				diffCmd := exec.Command("git", "diff", "--name-only", "--diff-filter=U")
				diffCmd.Dir = workDir
				diffOut, err := diffCmd.CombinedOutput()
				Expect(err).NotTo(HaveOccurred())
				Expect(strings.TrimSpace(string(diffOut))).To(BeEmpty())

				statusCmd := exec.Command("git", "status", "--porcelain")
				statusCmd.Dir = workDir
				statusOut, err := statusCmd.CombinedOutput()
				Expect(err).NotTo(HaveOccurred())
				for _, line := range strings.Split(string(statusOut), "\n") {
					Expect(strings.HasPrefix(line, "UU")).To(BeFalse(), "unmerged entry: %q", line)
				}

				Expect(bodyOf(filepath.Join(workDir, rel))).To(Equal("X\n\nY"))
			})

			It("reports write_failed when the body-resolved file cannot be written", func() {
				if os.Geteuid() == 0 {
					Skip("requires a non-root user")
				}
				conflicted := "---\npage_type: note\n---\n" +
					"<<<<<<< HEAD\nbody a\n=======\nbody b\n>>>>>>> theirs\n"
				workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
				defer cleanup()

				abs := filepath.Join(workDir, rel)
				before, err := os.ReadFile(abs)
				Expect(err).NotTo(HaveOccurred())
				Expect(os.Chmod(abs, 0o400)).To(Succeed())

				r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
				err = r.Resolve(ctx, []string{rel})
				Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
				Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("write_failed"))

				after, err := os.ReadFile(abs)
				Expect(err).NotTo(HaveOccurred())
				Expect(after).To(Equal(before))
			})

			It("reports git_add_failed when staging the body-resolved file fails", func() {
				conflicted := "---\npage_type: note\n---\n" +
					"<<<<<<< HEAD\nbody a\n=======\nbody b\n>>>>>>> theirs\n"
				workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
				defer cleanup()
				Expect(os.RemoveAll(filepath.Join(workDir, ".git"))).To(Succeed())

				r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
				err := r.Resolve(ctx, []string{rel})
				Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
				Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("git_add_failed"))
			})
		})

		It("AC-staged: file is staged (no longer unmerged) after Resolve", func() {
			workDir, rel, cleanup := setupYAMLConflictRepo(
				"a: 1\n", "X",
				"b: 2\n", "Y",
			)
			defer cleanup()
			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

			cmd := exec.Command("git", "diff", "--name-only", "--diff-filter=U")
			cmd.Dir = workDir
			out, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(string(out))).To(BeEmpty(), "file must not be unmerged")
		})

		It("AC-metrics-resolved: does NOT call IncResolverFailure on success", func() {
			workDir, rel, cleanup := setupYAMLConflictRepo(
				"a: 1\n", "X",
				"b: 2\n", "Y",
			)
			defer cleanup()
			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			Expect(r.Resolve(ctx, []string{rel})).To(Succeed())
			Expect(fakeMetrics.IncResolverFailureCallCount()).To(Equal(0))
		})
	})

	Describe("Resolve failure categories", func() {
		It("AC-yaml-parse-failed-ours: returns sentinel + increments yaml_parse_failed", func() {
			workDir, rel, cleanup := setupYAMLConflictRepo(
				"this : is : not : valid : yaml\n", "X",
				"b: 2\n", "Y",
			)
			defer cleanup()
			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			err := r.Resolve(ctx, []string{rel})
			Expect(err).To(HaveOccurred())
			Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
			Expect(fakeMetrics.IncResolverFailureCallCount()).To(Equal(1))
			Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("yaml_parse_failed"))
		})

		It("AC-yaml-parse-failed-theirs: returns sentinel + increments yaml_parse_failed", func() {
			workDir, rel, cleanup := setupYAMLConflictRepo(
				"a: 1\n", "X",
				"this : is : not : valid : yaml\n", "Y",
			)
			defer cleanup()
			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			err := r.Resolve(ctx, []string{rel})
			Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
			Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("yaml_parse_failed"))
		})

		It(
			"AC-no-frontmatter: file with no `---` delimiters returns sentinel + no_frontmatter",
			func() {
				// Build a conflicted non-frontmatter file by hand. Reuse the helper to
				// create a repo, then overwrite the conflicted file with a no-delimiter
				// payload that still has conflict markers.
				workDir, rel, cleanup := setupYAMLConflictRepo(
					"a: 1\n", "X",
					"b: 2\n", "Y",
				)
				defer cleanup()
				abs := filepath.Join(workDir, rel)
				Expect(
					os.WriteFile(
						abs,
						[]byte(
							"<<<<<<< HEAD\nno delimiters here\n=======\nstill none\n>>>>>>> theirs\n",
						),
						0o600,
					),
				).To(Succeed())
				r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
				err := r.Resolve(ctx, []string{rel})
				Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
				Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("no_frontmatter"))
			},
		)

		It(
			"AC-read-failed: path does not exist on disk → write_failed (read fails first)",
			func() {
				workDir, _, cleanup := setupYAMLConflictRepo(
					"a: 1\n", "X",
					"b: 2\n", "Y",
				)
				defer cleanup()
				r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
				err := r.Resolve(ctx, []string{"does-not-exist.md"})
				Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
				Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("write_failed"))
			},
		)

		It(
			"AC-git-add-failed: merged file written successfully, but `git add` fails (no .git/) → git_add_failed",
			func() {
				workDir, rel, cleanup := setupYAMLConflictRepo(
					"a: 1\n", "X",
					"b: 2\n", "Y",
				)
				defer cleanup()
				// Break the git repo AFTER the conflict was produced: remove .git/
				// entirely. The resolver's read+parse+merge+write all succeed (the
				// conflicted file is still on disk), but `git add` fails because the
				// working directory is no longer a git repository.
				Expect(os.RemoveAll(filepath.Join(workDir, ".git"))).To(Succeed())
				r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
				err := r.Resolve(ctx, []string{rel})
				Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
				Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("git_add_failed"))
			},
		)

		It(
			"AC-no-frontmatter: file content has no conflict region (binary-ish) → no_frontmatter",
			func() {
				workDir, rel, cleanup := setupYAMLConflictRepo(
					"a: 1\n", "X",
					"b: 2\n", "Y",
				)
				defer cleanup()
				abs := filepath.Join(workDir, rel)
				Expect(
					os.WriteFile(abs, []byte("no conflict markers at all\n"), 0o600),
				).To(Succeed())
				r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
				err := r.Resolve(ctx, []string{rel})
				Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
				Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("no_frontmatter"))
			},
		)

		It(
			"AC-security-escape: path containing `..` is rejected without I/O → unsafe_path",
			func() {
				workDir, _, cleanup := setupYAMLConflictRepo(
					"a: 1\n", "X",
					"b: 2\n", "Y",
				)
				defer cleanup()
				r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
				err := r.Resolve(ctx, []string{"../escape.md"})
				Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
				Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("unsafe_path"))
			},
		)

		It("AC-security-absolute: absolute path is rejected → unsafe_path", func() {
			workDir, _, cleanup := setupYAMLConflictRepo(
				"a: 1\n", "X",
				"b: 2\n", "Y",
			)
			defer cleanup()
			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			err := r.Resolve(ctx, []string{"/etc/passwd"})
			Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
			Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("unsafe_path"))
		})

		It(
			"AC-conflict-region-starts-with-delim: ours/theirs each contain a self-contained "+
				"`---`-bracketed frontmatter block → merges cleanly without double-`---` prepend",
			func() {
				workDir, rel, cleanup := setupYAMLConflictRepo(
					"a: 1\n", "X",
					"b: 2\n", "Y",
				)
				defer cleanup()
				abs := filepath.Join(workDir, rel)
				// Synthetic conflict where the opening `---` is INSIDE the conflict
				// markers on each side. This exercises the conditional-prepend branch
				// where the prepend MUST NOT fire (otherwise we'd produce "---\n---\n"
				// and corrupt the merged YAML).
				Expect(os.WriteFile(abs, []byte(
					"<<<<<<< HEAD\n"+
						"---\n"+
						"a: 1\n"+
						"---\n"+
						"X\n"+
						"=======\n"+
						"---\n"+
						"b: 2\n"+
						"---\n"+
						"Y\n"+
						">>>>>>> branch\n",
				), 0o600)).To(Succeed())
				r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
				err := r.Resolve(ctx, []string{rel})
				Expect(err).NotTo(HaveOccurred())
				// Merged frontmatter must parse to {a: 1, b: 2} (theirs wins on overlap;
				// here keys are disjoint so the result is the union).
				fm := parseFrontmatterOf(abs)
				Expect(fm).To(HaveKeyWithValue("a", 1))
				Expect(fm).To(HaveKeyWithValue("b", 2))
				// And the file must not contain a double `---` artifact.
				raw, readErr := os.ReadFile(abs)
				Expect(readErr).NotTo(HaveOccurred())
				Expect(strings.Contains(string(raw), "---\n---\n")).To(BeFalse(),
					"merged file must not contain double `---` opener")
			},
		)
	})

	Describe("Scalar frontmatter rule", func() {
		scalarFixture := func(ours, theirs string) string {
			return "---\n<<<<<<< HEAD\n" + ours + "\n=======\n" + theirs +
				"\n>>>>>>> theirs\ntitle: probe\n---\nbody\n"
		}

		It("AC1: resolves a counter conflict in place", func() {
			base := "---\ntrigger_count: 9\ntitle: probe\n---\nbody\n"
			ours := "---\ntrigger_count: 3\ntitle: probe\n---\nbody\n"
			theirs := "---\ntrigger_count: 0\ntitle: probe\n---\nbody\n"
			workDir, rel, cleanup := setupScalarMergeRepo(base, ours, theirs, "", "")
			defer cleanup()

			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

			written, err := os.ReadFile(filepath.Join(workDir, rel))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(written)).To(Equal("---\ntrigger_count: 3\ntitle: probe\n---\nbody\n"))

			cmd := exec.Command("git", "diff", "--name-only", "--diff-filter=U")
			cmd.Dir = workDir
			out, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(string(out))).To(BeEmpty())
			Expect(fakeMetrics.IncResolverFailureCallCount()).To(Equal(0))
			Expect(fakeMetrics.IncAutoresolvedCallCount()).To(Equal(1))
		})

		DescribeTable(
			"AC2: counter keys take the greater value",
			func(oursLine, theirsLine, expected string) {
				workDir, rel, cleanup := setupHandWrittenConflict(
					"note.md",
					scalarFixture(oursLine, theirsLine),
				)
				defer cleanup()

				r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
				Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

				written, err := os.ReadFile(filepath.Join(workDir, rel))
				Expect(err).NotTo(HaveOccurred())
				lines := strings.Split(string(written), "\n")
				Expect(lines).To(ContainElement(expected))
				Expect(string(written)).NotTo(ContainSubstring("<<<<<<<"))
				Expect(string(written)).NotTo(ContainSubstring(">>>>>>>"))
			},
			Entry("retry_count", "retry_count: 1", "retry_count: 5", "retry_count: 5"),
			Entry(
				"metrics_ prefix",
				"metrics_retries: 2",
				"metrics_retries: 7",
				"metrics_retries: 7",
			),
			Entry("trigger_count", "trigger_count: 3", "trigger_count: 0", "trigger_count: 3"),
		)

		DescribeTable("AC4: rejection shapes keep quarantining",
			func(conflicted string) {
				workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
				defer cleanup()

				abs := filepath.Join(workDir, rel)
				before, err := os.ReadFile(abs)
				Expect(err).NotTo(HaveOccurred())

				r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
				err = r.Resolve(ctx, []string{rel})
				Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
				Expect(fakeMetrics.IncResolverFailureCallCount()).To(Equal(1))
				Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("no_frontmatter"))
				Expect(fakeMetrics.IncAutoresolvedCallCount()).To(Equal(0))

				after, err := os.ReadFile(abs)
				Expect(err).NotTo(HaveOccurred())
				Expect(after).To(Equal(before))
			},
			Entry("list item",
				"---\n<<<<<<< HEAD\n- a\n=======\n- b\n>>>>>>> theirs\ntitle: probe\n---\nbody\n"),
			Entry("block scalar",
				"---\n<<<<<<< HEAD\ndesc: |\n  line one\n=======\n"+
					"desc: plain\n>>>>>>> theirs\ntitle: probe\n---\nbody\n"),
			Entry("nested mapping",
				"---\n<<<<<<< HEAD\nowner:\n  name: a\n=======\n"+
					"owner:\n  name: b\n>>>>>>> theirs\ntitle: probe\n---\nbody\n"),
			Entry("flow collection",
				"---\n<<<<<<< HEAD\ntags: [a, b]\n=======\ntags: [c]\n"+
					">>>>>>> theirs\ntitle: probe\n---\nbody\n"),
			// The shared key is a counter so the region resolves it without
			// merge-parent timestamps; that makes the one-sided key `b` the input
			// that decides the outcome. With a shared non-counter key the region
			// declined before `b` was ever examined, and a regression in the
			// one-sided base guard would have gone unnoticed.
			Entry("one-sided key",
				"---\n<<<<<<< HEAD\ntrigger_count: 3\nb: 2\n=======\ntrigger_count: 0\n"+
					">>>>>>> theirs\ntitle: probe\n---\nbody\n"),
			Entry("duplicate key",
				"---\n<<<<<<< HEAD\na: 1\na: 2\n=======\na: 3\n"+
					">>>>>>> theirs\ntitle: probe\n---\nbody\n"),
		)

		It("AC: resolves two one-sided additions to the union of both sides", func() {
			base := "---\ncreated_date: \"2026-11-02\"\nphase: planning\n---\n\nbody line\n"
			ours := "---\ncreated_date: \"2026-11-02\"\nphase: planning\npr_state: open\n---\n\nbody line\n"
			theirs := "---\ncreated_date: \"2026-11-02\"\nphase: planning\n" +
				"previous_assignee: someone\n---\n\nbody line\n"
			workDir, rel, cleanup := setupScalarMergeRepo(base, ours, theirs, "", "")
			defer cleanup()

			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

			written, err := os.ReadFile(filepath.Join(workDir, rel))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(written)).To(Equal(
				"---\ncreated_date: \"2026-11-02\"\nphase: planning\npr_state: open\n" +
					"previous_assignee: someone\n---\n\nbody line\n",
			))
			Expect(string(written)).NotTo(ContainSubstring("<<<<<<<"))
			Expect(string(written)).NotTo(ContainSubstring(">>>>>>>"))
			Expect(fakeMetrics.IncAutoresolvedCallCount()).To(Equal(1))
			Expect(fakeMetrics.IncResolverFailureCallCount()).To(Equal(0))
		})

		It("AC: orders ours-side keys first, then the theirs-only keys", func() {
			conflicted := "---\n<<<<<<< HEAD\nzeta: 1\nalpha: 2\n=======\n" +
				"omega: 3\nbeta: 4\n>>>>>>> theirs\ntitle: probe\n---\nbody\n"
			workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
			defer cleanup()

			r := git.NewYAMLMergeResolverWithBase(
				workDir,
				fakeMetrics,
				"---\ntitle: probe\n---\nbody\n",
				true,
			)
			Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

			written, err := os.ReadFile(filepath.Join(workDir, rel))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(written)).To(Equal(
				"---\nzeta: 1\nalpha: 2\nomega: 3\nbeta: 4\ntitle: probe\n---\nbody\n",
			))
		})

		It("AC: a one-sided key the base carries still quarantines", func() {
			conflicted := "---\n<<<<<<< HEAD\ntrigger_count: 3\n=======\n" +
				"pr_state: closed\ntrigger_count: 0\n>>>>>>> theirs\n---\nbody\n"
			workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
			defer cleanup()

			abs := filepath.Join(workDir, rel)
			before, err := os.ReadFile(abs)
			Expect(err).NotTo(HaveOccurred())

			base := "---\npr_state: open\ntrigger_count: 9\n---\nbody\n"
			r := git.NewYAMLMergeResolverWithBase(workDir, fakeMetrics, base, true)
			err = r.Resolve(ctx, []string{rel})
			Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
			Expect(fakeMetrics.IncResolverFailureCallCount()).To(Equal(1))
			Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("no_frontmatter"))
			Expect(fakeMetrics.IncAutoresolvedCallCount()).To(Equal(0))

			after, err := os.ReadFile(abs)
			Expect(err).NotTo(HaveOccurred())
			Expect(after).To(Equal(before))
		})

		It("AC: an unreadable merge base keeps the one-sided shape quarantined", func() {
			conflicted := "---\n<<<<<<< HEAD\npr_state: open\n=======\n" +
				"previous_assignee: someone\n>>>>>>> theirs\ntitle: probe\n---\nbody\n"
			workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
			defer cleanup()

			abs := filepath.Join(workDir, rel)
			before, err := os.ReadFile(abs)
			Expect(err).NotTo(HaveOccurred())

			r := git.NewYAMLMergeResolverWithBase(workDir, fakeMetrics, "", false)
			err = r.Resolve(ctx, []string{rel})
			Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
			Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("no_frontmatter"))
			Expect(fakeMetrics.IncAutoresolvedCallCount()).To(Equal(0))

			after, err := os.ReadFile(abs)
			Expect(err).NotTo(HaveOccurred())
			Expect(after).To(Equal(before))
		})

		// git may fold the deletion and the adjacent edit into one region or split
		// them; either way the region must quarantine, and the assertion is on the
		// outcome, not on the region count.
		It("AC: a real merge whose base carries the one-sided key quarantines", func() {
			base := "---\npr_state: open\nphase: planning\n---\nbody\n"
			ours := "---\nphase: done\n---\nbody\n"
			theirs := "---\npr_state: closed\nphase: planning\n---\nbody\n"
			workDir, rel, cleanup := setupScalarMergeRepo(base, ours, theirs, "", "")
			defer cleanup()

			abs := filepath.Join(workDir, rel)
			before, err := os.ReadFile(abs)
			Expect(err).NotTo(HaveOccurred())

			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			err = r.Resolve(ctx, []string{rel})
			Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
			Expect(fakeMetrics.IncAutoresolvedCallCount()).To(Equal(0))

			after, err := os.ReadFile(abs)
			Expect(err).NotTo(HaveOccurred())
			Expect(after).To(Equal(before))
		})

		It("AC: an add/add conflict with no merge base quarantines", func() {
			ours := "---\npr_state: open\n---\nbody\n"
			theirs := "---\nprevious_assignee: someone\n---\nbody\n"
			workDir, rel, cleanup := setupScalarAddAddRepo(ours, theirs)
			defer cleanup()

			abs := filepath.Join(workDir, rel)
			before, err := os.ReadFile(abs)
			Expect(err).NotTo(HaveOccurred())

			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			err = r.Resolve(ctx, []string{rel})
			Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
			Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("no_frontmatter"))
			Expect(fakeMetrics.IncAutoresolvedCallCount()).To(Equal(0))

			after, err := os.ReadFile(abs)
			Expect(err).NotTo(HaveOccurred())
			Expect(after).To(Equal(before))
		})

		It("AC: counters and newer-side rules survive a one-sided add in the same region", func() {
			base := "---\ntrigger_count: 9\ntitle: probe\n---\nbody\n"
			ours := "---\ntrigger_count: 3\npr_state: open\ntitle: probe\n---\nbody\n"
			theirs := "---\ntrigger_count: 0\nprevious_assignee: someone\ntitle: probe\n---\nbody\n"
			workDir, rel, cleanup := setupScalarMergeRepo(base, ours, theirs, "", "")
			defer cleanup()

			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

			written, err := os.ReadFile(filepath.Join(workDir, rel))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(written)).To(ContainSubstring("trigger_count: 3"))
			Expect(string(written)).To(ContainSubstring("pr_state: open"))
			Expect(string(written)).To(ContainSubstring("previous_assignee: someone"))
			Expect(strings.Count(string(written), "trigger_count: 3")).To(Equal(1))
			Expect(string(written)).NotTo(ContainSubstring("<<<<<<<"))
			Expect(fakeMetrics.IncAutoresolvedCallCount()).To(Equal(1))
			Expect(fakeMetrics.IncResolverFailureCallCount()).To(Equal(0))
		})

		It("AC: the newer side wins a shared key while both one-sided keys are kept", func() {
			base := "---\nassignee: base\ntitle: probe\n---\nbody\n"
			ours := "---\nassignee: alice\npr_state: open\ntitle: probe\n---\nbody\n"
			theirs := "---\nassignee: bob\nprevious_assignee: someone\ntitle: probe\n---\nbody\n"
			workDir, rel, cleanup := setupScalarMergeRepo(
				base, ours, theirs,
				"2026-01-01T00:00:00Z", "2026-01-02T00:00:00Z",
			)
			defer cleanup()

			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

			written, err := os.ReadFile(filepath.Join(workDir, rel))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(written)).To(ContainSubstring("assignee: bob"))
			Expect(string(written)).To(ContainSubstring("pr_state: open"))
			Expect(string(written)).To(ContainSubstring("previous_assignee: someone"))
			Expect(string(written)).NotTo(ContainSubstring("assignee: alice"))
		})

		It("resolves a union region and a counter region in the same file", func() {
			conflicted := "---\n" +
				"<<<<<<< HEAD\npr_state: open\n=======\nprevious_assignee: someone\n>>>>>>> theirs\n" +
				"<<<<<<< HEAD\ntrigger_count: 3\n=======\ntrigger_count: 0\n>>>>>>> theirs\n" +
				"---\nbody\n"
			workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
			defer cleanup()

			r := git.NewYAMLMergeResolverWithBase(
				workDir,
				fakeMetrics,
				"---\ntitle: probe\n---\nbody\n",
				true,
			)
			Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

			written, err := os.ReadFile(filepath.Join(workDir, rel))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(written)).To(Equal(
				"---\npr_state: open\nprevious_assignee: someone\ntrigger_count: 3\n---\nbody\n",
			))
			Expect(fakeMetrics.IncAutoresolvedCallCount()).To(Equal(1))
		})

		// The union rule decides each region on its own, so a key one side adds
		// in one region and the other side adds in a separate region must still
		// be caught: writing both would leave a duplicate mapping key that the
		// next yaml.Unmarshal of the frontmatter rejects.
		It("AC: declines the file when one key would be emitted in two regions", func() {
			conflicted := "---\n" +
				"<<<<<<< HEAD\nk: 1\n=======\nn: 1\n>>>>>>> theirs\n" +
				"title: probe\n" +
				"<<<<<<< HEAD\nm: 1\n=======\nk: 2\n>>>>>>> theirs\n" +
				"---\nbody\n"
			workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
			defer cleanup()

			abs := filepath.Join(workDir, rel)
			before, err := os.ReadFile(abs)
			Expect(err).NotTo(HaveOccurred())

			r := git.NewYAMLMergeResolverWithBase(
				workDir,
				fakeMetrics,
				"---\ntitle: probe\n---\nbody\n",
				true,
			)
			err = r.Resolve(ctx, []string{rel})
			Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
			Expect(fakeMetrics.IncResolverFailureCallCount()).To(Equal(1))
			Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("no_frontmatter"))
			Expect(fakeMetrics.IncAutoresolvedCallCount()).To(Equal(0))

			after, err := os.ReadFile(abs)
			Expect(err).NotTo(HaveOccurred())
			Expect(after).To(Equal(before))
		})

		// The shared-key branch records through the same set. A counter key
		// resolves without merge-parent timestamps, so the second region reaches
		// the duplicate check instead of declining on unreadable timestamps.
		It("AC: declines the file when a shared key appears in two regions", func() {
			conflicted := "---\n" +
				"<<<<<<< HEAD\ntrigger_count: 1\n=======\ntrigger_count: 2\n>>>>>>> theirs\n" +
				"title: probe\n" +
				"<<<<<<< HEAD\ntrigger_count: 1\n=======\ntrigger_count: 2\n>>>>>>> theirs\n" +
				"---\nbody\n"
			workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
			defer cleanup()

			abs := filepath.Join(workDir, rel)
			before, err := os.ReadFile(abs)
			Expect(err).NotTo(HaveOccurred())

			r := git.NewYAMLMergeResolverWithBase(
				workDir,
				fakeMetrics,
				"---\ntitle: probe\n---\nbody\n",
				true,
			)
			err = r.Resolve(ctx, []string{rel})
			Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
			Expect(fakeMetrics.IncResolverFailureCallCount()).To(Equal(1))
			Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("no_frontmatter"))
			Expect(fakeMetrics.IncAutoresolvedCallCount()).To(Equal(0))

			after, err := os.ReadFile(abs)
			Expect(err).NotTo(HaveOccurred())
			Expect(after).To(Equal(before))
		})

		// A key added by ours in a clean hunk outside every region and added
		// again by theirs inside a region is the shape the region-spanning guard
		// alone cannot see: spliceConflictRegions copies the outside line verbatim,
		// so seeding the emitted set with the outside keys is what catches it.
		It("AC: declines the file when a region re-emits a key already outside it", func() {
			base := "---\ncreated_date: \"2026-11-02\"\nphase: planning\n" +
				"title: probe\nassignee: base\n---\nbody\n"
			ours := "---\npr_state: open\ncreated_date: \"2026-11-02\"\nphase: planning\n" +
				"title: probe\nassignee: alice\n---\nbody\n"
			theirs := "---\ncreated_date: \"2026-11-02\"\nphase: planning\n" +
				"title: probe\nassignee: bob\npr_state: closed\n---\nbody\n"
			workDir, rel, cleanup := setupScalarMergeRepo(
				base, ours, theirs,
				"2026-01-01T00:00:00Z", "2026-01-02T00:00:00Z",
			)
			defer cleanup()

			abs := filepath.Join(workDir, rel)
			before, err := os.ReadFile(abs)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(before)).To(ContainSubstring("pr_state: open\ncreated_date:"))
			Expect(string(before)).To(ContainSubstring("pr_state: closed\n>>>>>>> theirs"))

			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			err = r.Resolve(ctx, []string{rel})
			Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
			Expect(fakeMetrics.IncResolverFailureCallCount()).To(Equal(1))
			Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("no_frontmatter"))
			Expect(fakeMetrics.IncAutoresolvedCallCount()).To(Equal(0))

			after, err := os.ReadFile(abs)
			Expect(err).NotTo(HaveOccurred())
			Expect(after).To(Equal(before))
		})

		It("reads the merge-base key set from index stage 1", func() {
			base := "---\npr_state: open\nphase: planning\n---\nbody\n"
			ours := "---\npr_state: open\nphase: done\n---\nbody\n"
			theirs := "---\npr_state: closed\nphase: planning\n---\nbody\n"
			workDir, rel, cleanup := setupScalarMergeRepo(base, ours, theirs, "", "")
			defer cleanup()

			keys, ok := git.YAMLBaseKeySetForTest(ctx, workDir, rel)
			Expect(ok).To(BeTrue())
			Expect(keys).To(HaveKey("pr_state"))
			Expect(keys).To(HaveKey("phase"))
		})

		It("reports the base as unreadable when the index has no stage 1", func() {
			workDir, rel, cleanup := setupHandWrittenConflict(
				"note.md",
				"---\n<<<<<<< HEAD\npr_state: open\n=======\nprevious_assignee: someone\n"+
					">>>>>>> theirs\ntitle: probe\n---\nbody\n",
			)
			defer cleanup()

			_, ok := git.YAMLBaseKeySetForTest(ctx, workDir, rel)
			Expect(ok).To(BeFalse())
		})

		It("reports the base as unreadable when its frontmatter is not YAML", func() {
			base := "---\ntitle: [unclosed\n---\nbody\n"
			ours := "---\ntitle: a\n---\nbody\n"
			theirs := "---\ntitle: b\n---\nbody\n"
			workDir, rel, cleanup := setupScalarMergeRepo(base, ours, theirs, "", "")
			defer cleanup()

			_, ok := git.YAMLBaseKeySetForTest(ctx, workDir, rel)
			Expect(ok).To(BeFalse())
		})

		It("AC5: one bad region makes the whole file quarantine", func() {
			conflicted := "---\n" +
				"<<<<<<< HEAD\ntrigger_count: 3\n=======\ntrigger_count: 0\n>>>>>>> theirs\n" +
				"<<<<<<< HEAD\n- a\n=======\n- b\n>>>>>>> theirs\n" +
				"---\nbody\n"
			workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
			defer cleanup()

			abs := filepath.Join(workDir, rel)
			before, err := os.ReadFile(abs)
			Expect(err).NotTo(HaveOccurred())

			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			err = r.Resolve(ctx, []string{rel})
			Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
			Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("no_frontmatter"))

			after, err := os.ReadFile(abs)
			Expect(err).NotTo(HaveOccurred())
			Expect(after).To(Equal(before))
		})

		It("resolves multiple counter regions independently", func() {
			conflicted := "---\n" +
				"<<<<<<< HEAD\ntrigger_count: 3\n=======\ntrigger_count: 0\n>>>>>>> theirs\n" +
				"title: probe\n" +
				"<<<<<<< HEAD\nretry_count: 1\n=======\nretry_count: 5\n>>>>>>> theirs\n" +
				"---\nbody\n"
			workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
			defer cleanup()

			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

			written, err := os.ReadFile(filepath.Join(workDir, rel))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(written)).To(Equal(
				"---\ntrigger_count: 3\ntitle: probe\nretry_count: 5\n---\nbody\n",
			))
		})

		It("AC6: preserves every line outside the conflict region byte-for-byte", func() {
			conflicted := "---\n" +
				"<<<<<<< HEAD\ntrigger_count: 3\n=======\ntrigger_count: 0\n>>>>>>> theirs\n" +
				"title: probe\ntags:\n  - x\n---\nbody line one\nbody line two\n"
			workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
			defer cleanup()

			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

			written, err := os.ReadFile(filepath.Join(workDir, rel))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(written)).To(Equal(
				"---\ntrigger_count: 3\ntitle: probe\ntags:\n  - x\n---\nbody line one\nbody line two\n",
			))
		})

		It("leaves non-markdown files on the pre-existing path", func() {
			conflicted := scalarFixture("trigger_count: 3", "trigger_count: 0")
			workDir, rel, cleanup := setupHandWrittenConflict("note.txt", conflicted)
			defer cleanup()

			abs := filepath.Join(workDir, rel)
			before, err := os.ReadFile(abs)
			Expect(err).NotTo(HaveOccurred())

			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			err = r.Resolve(ctx, []string{rel})
			Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
			Expect(fakeMetrics.IncResolverFailureCallCount()).To(Equal(1))
			Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("no_frontmatter"))

			after, err := os.ReadFile(abs)
			Expect(err).NotTo(HaveOccurred())
			Expect(after).To(Equal(before))
		})

		It("keeps the pre-existing path when an extracted side is empty", func() {
			conflicted := "---\n" +
				"<<<<<<< HEAD\n=======\ntrigger_count: 0\n>>>>>>> theirs\n" +
				"title: probe\n---\nbody\n"
			workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
			defer cleanup()

			abs := filepath.Join(workDir, rel)
			before, err := os.ReadFile(abs)
			Expect(err).NotTo(HaveOccurred())

			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			err = r.Resolve(ctx, []string{rel})
			Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
			Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("no_frontmatter"))

			after, err := os.ReadFile(abs)
			Expect(err).NotTo(HaveOccurred())
			Expect(after).To(Equal(before))
		})

		It("rejects a region that crosses the frontmatter/body boundary", func() {
			conflicted := "---\n<<<<<<< HEAD\ntrigger_count: 3\n---\nbody\n" +
				"=======\ntrigger_count: 0\n>>>>>>> theirs\n"
			workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
			defer cleanup()

			abs := filepath.Join(workDir, rel)
			before, err := os.ReadFile(abs)
			Expect(err).NotTo(HaveOccurred())

			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			err = r.Resolve(ctx, []string{rel})
			Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
			Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("no_frontmatter"))

			after, err := os.ReadFile(abs)
			Expect(err).NotTo(HaveOccurred())
			Expect(after).To(Equal(before))
		})

		It("reports git_add_failed when staging the scalar-resolved file fails", func() {
			workDir, rel, cleanup := setupHandWrittenConflict(
				"note.md",
				scalarFixture("trigger_count: 3", "trigger_count: 0"),
			)
			defer cleanup()
			Expect(os.RemoveAll(filepath.Join(workDir, ".git"))).To(Succeed())

			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			err := r.Resolve(ctx, []string{rel})
			Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
			Expect(fakeMetrics.IncResolverFailureCallCount()).To(Equal(1))
			Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("git_add_failed"))
			Expect(fakeMetrics.IncAutoresolvedCallCount()).To(Equal(0))
		})

		DescribeTable(
			"AC2/AC3: counters take the greater value, other scalars take the newer side",
			func(oursLine, theirsLine string, oursTime, theirsTime int64, expected string) {
				workDir, rel, cleanup := setupHandWrittenConflict(
					"note.md",
					"---\n<<<<<<< HEAD\n"+oursLine+"\n=======\n"+theirsLine+
						"\n>>>>>>> theirs\n---\nbody\n",
				)
				defer cleanup()

				r := git.NewYAMLMergeResolverWithSideTimes(
					workDir,
					fakeMetrics,
					oursTime,
					theirsTime,
				)
				Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

				written, err := os.ReadFile(filepath.Join(workDir, rel))
				Expect(err).NotTo(HaveOccurred())
				lines := strings.Split(string(written), "\n")
				Expect(lines).To(ContainElement(expected))
				Expect(string(written)).NotTo(ContainSubstring("<<<<<<<"))
				Expect(string(written)).NotTo(ContainSubstring(">>>>>>>"))
			},
			Entry("counter wins even when ours is newer",
				"trigger_count: 3", "trigger_count: 0", int64(10), int64(5),
				"trigger_count: 3"),
			Entry("counter wins when theirs is newer",
				"trigger_count: 3", "trigger_count: 0", int64(5), int64(10),
				"trigger_count: 3"),
			Entry("retry_count takes the greater value",
				"retry_count: 1", "retry_count: 5", int64(10), int64(5),
				"retry_count: 5"),
			Entry("metrics_ prefix takes the greater value",
				"metrics_retries: 2", "metrics_retries: 7", int64(5), int64(10),
				"metrics_retries: 7"),
			Entry("non-counter takes the newer theirs",
				"assignee: alice", "assignee: bob", int64(5), int64(10),
				"assignee: bob"),
			Entry("non-counter keeps the newer ours",
				"assignee: alice", "assignee: bob", int64(10), int64(5),
				"assignee: alice"),
			Entry("non-counter tie keeps ours",
				"assignee: alice", "assignee: bob", int64(10), int64(10),
				"assignee: alice"),
			Entry("unlisted integer takes the newer side",
				"attempt_count: 1", "attempt_count: 9", int64(5), int64(10),
				"attempt_count: 9"),
			Entry("quoted counter falls back to the newer side",
				`trigger_count: "3"`, "trigger_count: 0", int64(5), int64(10),
				"trigger_count: 0"),
		)

		It("AC3: re-emits the newer non-counter line verbatim", func() {
			workDir, rel, cleanup := setupHandWrittenConflict(
				"note.md",
				"---\n<<<<<<< HEAD\nassignee: \"alice\"\n=======\n"+
					"assignee: \"bob\" # newer\n>>>>>>> theirs\n---\nbody\n",
			)
			defer cleanup()

			r := git.NewYAMLMergeResolverWithSideTimes(workDir, fakeMetrics, 5, 10)
			Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

			written, err := os.ReadFile(filepath.Join(workDir, rel))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(written)).To(ContainSubstring(`assignee: "bob" # newer`))
			Expect(string(written)).NotTo(ContainSubstring(`assignee: "alice"`))
		})

		It("AC3: git-backed getter picks the newer merge parent", func() {
			base := "---\nassignee: base\n---\nbody\n"
			ours := "---\nassignee: alice\n---\nbody\n"
			theirs := "---\nassignee: bob\n---\nbody\n"
			workDir, rel, cleanup := setupScalarMergeRepo(
				base,
				ours,
				theirs,
				"2026-01-01T00:00:00Z",
				"2026-01-02T00:00:00Z",
			)
			defer cleanup()

			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

			written, err := os.ReadFile(filepath.Join(workDir, rel))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(written)).To(ContainSubstring("assignee: bob"))
			Expect(fakeMetrics.IncResolverFailureCallCount()).To(Equal(0))
		})

		It("AC3: git-backed getter works in a linked worktree", func() {
			base := "---\nassignee: base\n---\nbody\n"
			ours := "---\nassignee: alice\n---\nbody\n"
			theirs := "---\nassignee: bob\n---\nbody\n"
			workDir, _, cleanup := setupScalarMergeRepo(
				base,
				ours,
				theirs,
				"2026-01-01T00:00:00Z",
				"2026-01-02T00:00:00Z",
			)
			defer cleanup()

			// Abort the main-worktree merge so the linked worktree can merge cleanly.
			runGit := func(dir string, args ...string) string {
				cmd := exec.Command("git", args...)
				cmd.Dir = dir
				out, err := cmd.CombinedOutput()
				Expect(err).NotTo(HaveOccurred(), "git %v: %s", args, string(out))
				return strings.TrimSpace(string(out))
			}
			runGit(workDir, "merge", "--abort")

			wtDir := workDir + "-wt"
			defer func() { _ = os.RemoveAll(wtDir) }()
			runGit(workDir, "worktree", "add", wtDir, "-b", "wt")

			cmd := exec.Command("git", "merge", "--no-edit", "theirs")
			cmd.Dir = wtDir
			_ = cmd.Run() // non-zero exit expected

			r := git.NewYAMLMergeResolver(wtDir, fakeMetrics)
			Expect(r.Resolve(ctx, []string{"note.md"})).To(Succeed())

			written, err := os.ReadFile(filepath.Join(wtDir, "note.md"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(written)).To(ContainSubstring("assignee: bob"))
		})

		It("AC4: missing merge head quarantines with the file untouched", func() {
			assertScalarQuarantine(
				ctx,
				fakeMetrics,
				"---\n<<<<<<< HEAD\nassignee: alice\n=======\n"+
					"assignee: bob\n>>>>>>> theirs\n---\nbody\n",
				nil,
			)
		})

		It("AC4: multi-valued merge head quarantines with the file untouched", func() {
			writeMergeHead := func(workDir string) {
				cmd := exec.Command("git", "rev-parse", "HEAD")
				cmd.Dir = workDir
				out, err := cmd.CombinedOutput()
				Expect(err).NotTo(HaveOccurred(), "git rev-parse HEAD: %s", string(out))
				sha := strings.TrimSpace(string(out))
				Expect(os.WriteFile(
					filepath.Join(workDir, ".git", "MERGE_HEAD"),
					[]byte(sha+"\n"+sha+"\n"),
					0o600,
				)).To(Succeed())
			}
			assertScalarQuarantine(
				ctx,
				fakeMetrics,
				"---\n<<<<<<< HEAD\nassignee: alice\n=======\n"+
					"assignee: bob\n>>>>>>> theirs\n---\nbody\n",
				writeMergeHead,
			)
		})

		It("AC4: unreadable merge head timestamp quarantines", func() {
			writeMergeHead := func(workDir string) {
				Expect(os.WriteFile(
					filepath.Join(workDir, ".git", "MERGE_HEAD"),
					[]byte(strings.Repeat("0", 40)+"\n"),
					0o600,
				)).To(Succeed())
			}
			assertScalarQuarantine(
				ctx,
				fakeMetrics,
				"---\n<<<<<<< HEAD\nassignee: alice\n=======\n"+
					"assignee: bob\n>>>>>>> theirs\n---\nbody\n",
				writeMergeHead,
			)
		})

		It("counter-only file resolves without a merge parent", func() {
			workDir, rel, cleanup := setupHandWrittenConflict(
				"note.md",
				"---\n<<<<<<< HEAD\ntrigger_count: 3\n=======\n"+
					"trigger_count: 0\n>>>>>>> theirs\n---\nbody\n",
			)
			defer cleanup()

			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

			written, err := os.ReadFile(filepath.Join(workDir, rel))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(written)).To(Equal("---\ntrigger_count: 3\n---\nbody\n"))
		})

		It("AC5: an undecidable timestamp quarantines the whole file", func() {
			assertScalarQuarantine(
				ctx,
				fakeMetrics,
				"---\n<<<<<<< HEAD\ntrigger_count: 3\n=======\ntrigger_count: 0\n"+
					">>>>>>> theirs\n<<<<<<< HEAD\nassignee: alice\n=======\n"+
					"assignee: bob\n>>>>>>> theirs\n---\nbody\n",
				nil,
			)
		})

		It("increments once per resolved path", func() {
			conflicted := scalarFixture("trigger_count: 3", "trigger_count: 0")
			workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
			defer cleanup()
			Expect(os.WriteFile(
				filepath.Join(workDir, "note2.md"),
				[]byte(conflicted),
				0o600,
			)).To(Succeed())

			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			Expect(r.Resolve(ctx, []string{rel, "note2.md"})).To(Succeed())
			Expect(fakeMetrics.IncAutoresolvedCallCount()).To(Equal(2))
		})

		It("reports write_failed when the scalar-resolved file cannot be written", func() {
			if os.Geteuid() == 0 {
				Skip("requires a non-root user")
			}
			workDir, rel, cleanup := setupHandWrittenConflict(
				"note.md",
				scalarFixture("trigger_count: 3", "trigger_count: 0"),
			)
			defer cleanup()

			abs := filepath.Join(workDir, rel)
			before, err := os.ReadFile(abs)
			Expect(err).NotTo(HaveOccurred())
			Expect(os.Chmod(abs, 0o400)).To(Succeed())

			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			err = r.Resolve(ctx, []string{rel})
			Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
			Expect(fakeMetrics.IncResolverFailureCallCount()).To(Equal(1))
			Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("write_failed"))
			Expect(fakeMetrics.IncAutoresolvedCallCount()).To(Equal(0))

			after, err := os.ReadFile(abs)
			Expect(err).NotTo(HaveOccurred())
			Expect(after).To(Equal(before))
		})
	})

	Describe("Body merge metric", func() {
		bodyConflict := "---\npage_type: note\n---\n\n# Heading\n\nintro\n" +
			"<<<<<<< HEAD\n=======\ntheirs line\n>>>>>>> theirs\n"

		It("increments once per body-resolved path and not the scalar or failure counters", func() {
			workDir, rel, cleanup := setupHandWrittenConflict("note.md", bodyConflict)
			defer cleanup()

			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

			Expect(fakeMetrics.IncBodyMergedCallCount()).To(Equal(1))
			Expect(fakeMetrics.IncAutoresolvedCallCount()).To(Equal(0))
			Expect(fakeMetrics.IncResolverFailureCallCount()).To(Equal(0))
		})

		It("increments once per path across two body-resolved paths", func() {
			workDir, rel, cleanup := setupHandWrittenConflict("note.md", bodyConflict)
			defer cleanup()
			Expect(os.WriteFile(
				filepath.Join(workDir, "note2.md"),
				[]byte(bodyConflict),
				0o600,
			)).To(Succeed())

			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			Expect(r.Resolve(ctx, []string{rel, "note2.md"})).To(Succeed())

			Expect(fakeMetrics.IncBodyMergedCallCount()).To(Equal(2))
		})

		It("does not increment on the deep-merge path", func() {
			workDir, rel, cleanup := setupYAMLConflictRepo(
				"a: 1\n", "X",
				"b: 2\n", "Y",
			)
			defer cleanup()

			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

			Expect(fakeMetrics.IncBodyMergedCallCount()).To(Equal(0))
		})

		It("does not increment on the scalar-frontmatter path", func() {
			conflicted := "---\n<<<<<<< HEAD\ntrigger_count: 3\n=======\n" +
				"trigger_count: 0\n>>>>>>> theirs\ntitle: probe\n---\nbody\n"
			workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
			defer cleanup()

			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

			Expect(fakeMetrics.IncAutoresolvedCallCount()).To(Equal(1))
			Expect(fakeMetrics.IncBodyMergedCallCount()).To(Equal(0))
		})

		It("does not increment when the body shape is rejected", func() {
			conflicted := "---\npage_type: note\n" +
				"<<<<<<< HEAD\nbody ours\n=======\ntags: [b]\n---\nbody theirs\n>>>>>>> theirs\n" +
				"<<<<<<< HEAD\ntail ours\n=======\ntail theirs\n>>>>>>> theirs\n"
			workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
			defer cleanup()

			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			err := r.Resolve(ctx, []string{rel})
			Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())

			Expect(fakeMetrics.IncBodyMergedCallCount()).To(Equal(0))
			Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("no_frontmatter"))
		})
	})

	Describe("Mixed frontmatter and body conflict regions", func() {
		It("resolves a counter region in the frontmatter and a prose region in the body", func() {
			conflicted := "---\n" +
				"title: probe\n" +
				"<<<<<<< HEAD\n" +
				"trigger_count: 8\n" +
				"=======\n" +
				"trigger_count: 1\n" +
				">>>>>>> theirs\n" +
				"status: in_progress\n" +
				"---\n" +
				"\n" +
				"# Heading\n" +
				"\n" +
				"<<<<<<< HEAD\n" +
				"ours body\n" +
				"=======\n" +
				"theirs body\n" +
				">>>>>>> theirs\n" +
				"\n" +
				"tail\n"

			workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
			defer cleanup()

			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

			raw, err := os.ReadFile(filepath.Join(workDir, rel))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(raw)).To(Equal(
				"---\ntitle: probe\ntrigger_count: 8\nstatus: in_progress\n---\n\n# Heading\n\n" +
					"ours body\n\ntheirs body\n\ntail\n",
			))

			diffCmd := exec.Command("git", "diff", "--name-only")
			diffCmd.Dir = workDir
			diffOut, err := diffCmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(string(diffOut))).To(BeEmpty())
		})

		It("increments both counters once and no failure counter", func() {
			conflicted := "---\n" +
				"title: probe\n" +
				"<<<<<<< HEAD\n" +
				"trigger_count: 8\n" +
				"=======\n" +
				"trigger_count: 1\n" +
				">>>>>>> theirs\n" +
				"status: in_progress\n" +
				"---\n" +
				"\n" +
				"# Heading\n" +
				"\n" +
				"<<<<<<< HEAD\n" +
				"ours body\n" +
				"=======\n" +
				"theirs body\n" +
				">>>>>>> theirs\n" +
				"\n" +
				"tail\n"

			workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
			defer cleanup()

			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

			Expect(fakeMetrics.IncAutoresolvedCallCount()).To(Equal(1))
			Expect(fakeMetrics.IncBodyMergedCallCount()).To(Equal(1))
			Expect(fakeMetrics.IncResolverFailureCallCount()).To(Equal(0))
		})

		It("aborts the whole file when the frontmatter half declines", func() {
			conflicted := "---\n" +
				"title: probe\n" +
				"<<<<<<< HEAD\n" +
				"assignee: alice\n" +
				"=======\n" +
				"assignee: bob\n" +
				">>>>>>> theirs\n" +
				"status: in_progress\n" +
				"---\n" +
				"\n" +
				"<<<<<<< HEAD\n" +
				"ours body\n" +
				"=======\n" +
				"theirs body\n" +
				">>>>>>> theirs\n"

			workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
			defer cleanup()

			abs := filepath.Join(workDir, rel)
			before, err := os.ReadFile(abs)
			Expect(err).NotTo(HaveOccurred())

			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			err = r.Resolve(ctx, []string{rel})
			Expect(stderrors.Is(err, git.ErrConflictResolutionFailed)).To(BeTrue())
			Expect(fakeMetrics.IncResolverFailureArgsForCall(0)).To(Equal("no_frontmatter"))
			Expect(fakeMetrics.IncBodyMergedCallCount()).To(Equal(0))

			after, err := os.ReadFile(abs)
			Expect(err).NotTo(HaveOccurred())
			Expect(after).To(Equal(before))
		})
	})

	Describe("Metric pre-initialisation (spec AC: zero-init labels)", func() {
		It("exposes all four failure categories pre-initialised to zero", func() {
			// Gather from the prometheus default registry; the labels are pre-initialised
			// in metrics.init().
			gatherer := preinitLabelsOf("git_rest_resolver_failures_total")
			Expect(gatherer).To(ContainElement("yaml_parse_failed"))
			Expect(gatherer).To(ContainElement("no_frontmatter"))
			Expect(gatherer).To(ContainElement("write_failed"))
			Expect(gatherer).To(ContainElement("git_add_failed"))
			Expect(gatherer).To(ContainElement("unsafe_path"))
		})
	})

	// Regression: the primary path (splitConflictSides → splitFrontmatter →
	// deepMergeTheirsWins → combineBodies) rebuilt the whole file from the two
	// sides of the conflict regions and dropped every line outside them, so a
	// file whose first conflict region did not span the file came back truncated.
	// The fixtures below are the only shape that reaches that path: the first
	// conflict region must carry a complete `---` pair on each side so
	// splitFrontmatter succeeds. Otherwise the file falls to the line-preserving
	// fallback (applyPartitionedRules) and the defect never fires.
	Describe("Line preservation when the first conflict region does not span the file", func() {
		lineCount := func(s string) int {
			return len(strings.Split(strings.TrimSuffix(s, "\n"), "\n"))
		}

		// containsInOrder asserts every non-empty line in want appears in out's
		// lines, in the same order. Combined with an exact line count this catches
		// a dropped line, a reordered block and duplicated content.
		containsInOrder := func(out string, want []string) {
			outLines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
			i := 0
			for _, w := range want {
				if w == "" {
					continue
				}
				for i < len(outLines) && outLines[i] != w {
					i++
				}
				Expect(i).To(BeNumerically("<", len(outLines)), "line %q missing or out of order in:\n%s", w, out)
				i++
			}
		}

		// expectResolved asserts the exact written line count. A correct resolve
		// deletes the conflict markers and the duplicated frontmatter, so the
		// result is SHORTER than the conflicted input: a floor at the input's own
		// line count is unsatisfiable, and a floor below the correct count cannot
		// catch a truncation.
		expectResolved := func(out string, wantLines int, outside []string) {
			Expect(out).NotTo(ContainSubstring("<<<<<<<"))
			Expect(out).NotTo(ContainSubstring("======="))
			Expect(out).NotTo(ContainSubstring(">>>>>>>"))
			Expect(lineCount(out)).To(Equal(wantLines),
				"written file has %d lines, want %d\n--- output ---\n%s", lineCount(out), wantLines, out)
			containsInOrder(out, outside)
		}

		It("preserves every line outside a single non-spanning conflict region", func() {
			oursBody := make([]string, 0, 14)
			theirsBody := make([]string, 0, 14)
			for i := 1; i <= 14; i++ {
				oursBody = append(oursBody, fmt.Sprintf("ours body line %d", i))
				theirsBody = append(theirsBody, fmt.Sprintf("theirs body line %d", i))
			}
			outside := []string{
				"", "## Tail heading", "", "tail paragraph one",
				"", "tail paragraph two", "", "tail paragraph three",
			}

			lines := []string{"<<<<<<< HEAD", "---", "title: ours", "trigger_count: 8", "status: in_progress", "---"}
			lines = append(lines, oursBody...)
			lines = append(lines, "=======", "---", "title: theirs", "trigger_count: 1", "status: in_progress", "---")
			lines = append(lines, theirsBody...)
			lines = append(lines, ">>>>>>> theirs")
			lines = append(lines, outside...)
			Expect(lines).To(HaveLen(49), "fixture must be 49 lines")
			conflicted := strings.Join(lines, "\n") + "\n"

			workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
			defer cleanup()

			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

			raw, err := os.ReadFile(filepath.Join(workDir, rel))
			Expect(err).NotTo(HaveOccurred())
			// 34 replacement lines (2 delimiters + 3 merged keys + 29 body lines) + 8 outside.
			expectResolved(string(raw), 42, outside)
		})

		It("preserves every line outside two conflict regions when the first straddles the frontmatter boundary", func() {
			region1Ours := []string{"---", "title: ours", "trigger_count: 8", "---", "ours body line 1", "ours body line 2"}
			region1Theirs := []string{"---", "title: theirs", "trigger_count: 1", "---", "theirs body line 1", "theirs body line 2"}
			region2Ours := []string{"ours counter 1", "ours counter 2"}
			region2Theirs := []string{"theirs counter 1", "theirs counter 2"}
			outside1 := []string{"", "## Middle heading", "", "middle paragraph one", "", "middle paragraph two", ""}
			outside2 := []string{"", "## Tail heading", ""}
			for i := 1; i <= 17; i++ {
				outside2 = append(outside2, fmt.Sprintf("tail line %d", i))
			}

			lines := []string{"<<<<<<< HEAD"}
			lines = append(lines, region1Ours...)
			lines = append(lines, "=======")
			lines = append(lines, region1Theirs...)
			lines = append(lines, ">>>>>>> theirs")
			lines = append(lines, outside1...)
			lines = append(lines, "<<<<<<< HEAD")
			lines = append(lines, region2Ours...)
			lines = append(lines, "=======")
			lines = append(lines, region2Theirs...)
			lines = append(lines, ">>>>>>> theirs")
			lines = append(lines, outside2...)
			Expect(lines).To(HaveLen(49), "fixture must be 49 lines")
			conflicted := strings.Join(lines, "\n") + "\n"

			outside := append(append([]string{}, outside1...), outside2...)

			workDir, rel, cleanup := setupHandWrittenConflict("note.md", conflicted)
			defer cleanup()

			r := git.NewYAMLMergeResolver(workDir, fakeMetrics)
			Expect(r.Resolve(ctx, []string{rel})).To(Succeed())

			raw, err := os.ReadFile(filepath.Join(workDir, rel))
			Expect(err).NotTo(HaveOccurred())
			// 9 lines for region 1 + 5 for region 2 + 27 outside.
			expectResolved(string(raw), 41, outside)
			// Each region resolves from its own sides: no region's content may be
			// duplicated into another region's replacement.
			Expect(strings.Count(string(raw), "ours body line 1")).To(Equal(1))
			Expect(strings.Count(string(raw), "theirs counter 1")).To(Equal(1))
		})
	})
})
