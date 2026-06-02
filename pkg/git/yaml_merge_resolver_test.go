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
