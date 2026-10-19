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
			Entry("outside frontmatter",
				"---\ntitle: probe\n---\n<<<<<<< HEAD\nbody a\n=======\nbody b\n>>>>>>> theirs\n"),
			Entry("one-sided key",
				"---\n<<<<<<< HEAD\na: 1\nb: 2\n=======\na: 1\n"+
					">>>>>>> theirs\ntitle: probe\n---\nbody\n"),
			Entry("duplicate key",
				"---\n<<<<<<< HEAD\na: 1\na: 2\n=======\na: 3\n"+
					">>>>>>> theirs\ntitle: probe\n---\nbody\n"),
		)

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
		})

		It("reports write_failed when the scalar-resolved file cannot be written", func() {
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
})
