// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	libhttp "github.com/bborbe/http"

	"github.com/bborbe/git-rest/mocks"
	"github.com/bborbe/git-rest/pkg/git"
	"github.com/bborbe/git-rest/pkg/handler"
)

// The TestClaimMirror* functions below are deliberately plain testing.T tests in
// a package that otherwise bootstraps Ginkgo through TestSuite. The Ginkgo entry
// point cannot be selected with `go test -run`, and spec 022's acceptance
// criteria and the verification gate both select these by name, so a Ginkgo
// conversion would silently turn every one of them into a no-op run. Precedent:
// TestClaimCreateRace and its siblings in claim_refs_test.go.
//
// TestClaimMirrorConfigureFetch proves the startup step is idempotent: applied
// twice it leaves exactly the default heads refspec plus the claim mirror
// refspec, and enables pruning.
func TestClaimMirrorConfigureFetch(t *testing.T) {
	fixture := newClaimFixture(t)
	client := newClaimClient(fixture.cloneA)

	for i := 0; i < 2; i++ {
		if err := client.ConfigureClaimMirror(context.Background()); err != nil {
			t.Fatalf("ConfigureClaimMirror call %d: %v", i+1, err)
		}
	}

	lines := strings.Split(
		strings.TrimSpace(mustGit(t, fixture.cloneA, "config", "--get-all", "remote.origin.fetch")),
		"\n",
	)
	if len(lines) != 2 {
		t.Fatalf("want exactly 2 fetch refspecs, got %d: %q", len(lines), lines)
	}
	want := map[string]bool{
		"+refs/heads/*:refs/remotes/origin/*": false,
		"+refs/claims/*:refs/claim-mirror/*":  false,
	}
	for _, line := range lines {
		seen, ok := want[line]
		if !ok {
			t.Fatalf("unexpected fetch refspec %q", line)
		}
		if seen {
			t.Fatalf("duplicate fetch refspec %q", line)
		}
		want[line] = true
	}

	prune := strings.TrimSpace(mustGit(t, fixture.cloneA, "config", "--get", "remote.origin.prune"))
	if prune != "true" {
		t.Fatalf("want remote.origin.prune=true, got %q", prune)
	}
}

// TestClaimMirrorPullRefreshesAndPrunes proves the puller's existing fetch
// carries the claim namespace, prunes a deleted claim, and that only a
// successful fetch advances the claims-specific freshness time.
func TestClaimMirrorPullRefreshesAndPrunes(t *testing.T) {
	fixture := newClaimFixture(t)
	client := newClaimClient(fixture.cloneA)
	configureClaimIdentity(t, fixture.bare)
	ctx := context.Background()

	if err := client.ConfigureClaimMirror(ctx); err != nil {
		t.Fatalf("ConfigureClaimMirror: %v", err)
	}
	if err := client.Pull(ctx); err != nil {
		t.Fatalf("initial Pull: %v", err)
	}

	if got := strings.TrimSpace(
		mustGit(t, fixture.cloneA, "for-each-ref", "refs/claim-mirror/"),
	); got != "" {
		t.Fatalf("want an empty mirror before any claim, got %q", got)
	}

	sha := strings.TrimSpace(mustGit(
		t,
		fixture.bare,
		"commit-tree",
		claimEmptyTreeSHA,
		"-m",
		`{"cluster":"nuke","task_id":"t1","lease_until":"2026-10-25T00:00:00Z","nonce":"0123456789abcdef0123456789abcdef"}`,
	))
	mustGit(t, fixture.bare, "update-ref", "refs/claims/task/t1", sha)

	if err := client.Pull(ctx); err != nil {
		t.Fatalf("Pull after claim create: %v", err)
	}
	if got := strings.TrimSpace(
		mustGit(t, fixture.cloneA, "rev-parse", "refs/claim-mirror/task/t1"),
	); got != sha {
		t.Fatalf("want mirror ref at %s, got %q", sha, got)
	}

	mustGit(t, fixture.bare, "update-ref", "-d", "refs/claims/task/t1")
	if err := client.Pull(ctx); err != nil {
		t.Fatalf("Pull after claim delete: %v", err)
	}
	if got := strings.TrimSpace(
		mustGit(t, fixture.cloneA, "for-each-ref", "refs/claim-mirror/"),
	); got != "" {
		t.Fatalf("want the mirror pruned after the claim was deleted, got %q", got)
	}

	sha2 := strings.TrimSpace(mustGit(
		t,
		fixture.bare,
		"commit-tree",
		claimEmptyTreeSHA,
		"-m",
		`{"cluster":"nuke","task_id":"t2","lease_until":"2026-10-25T00:00:00Z","nonce":"0123456789abcdef0123456789abcdef"}`,
	))
	mustGit(t, fixture.bare, "update-ref", "refs/claims/task/t2", sha2)
	if err := client.Pull(ctx); err != nil {
		t.Fatalf("Pull after second claim create: %v", err)
	}
	freshBefore := git.ClaimMirrorFreshAtForTest(client)
	if freshBefore.IsZero() {
		t.Fatal("want a non-zero freshness time after a successful pull")
	}

	mustGit(t, fixture.cloneA, "remote", "set-url", "origin", "/nonexistent/origin.git")
	if err := client.Pull(ctx); err == nil {
		t.Fatal("want a pull error against a severed origin, got nil")
	}
	if got := git.ClaimMirrorFreshAtForTest(client); !got.Equal(freshBefore) {
		t.Fatalf(
			"want freshness unchanged after a failed fetch, got %v (before %v)",
			got,
			freshBefore,
		)
	}

	assertMirrorServesSeveredOrigin(t, client, sha2)
}

// assertMirrorServesSeveredOrigin asserts the read still answers from the mirror
// after the origin was severed.
func assertMirrorServesSeveredOrigin(t *testing.T, client git.Git, wantSHA string) {
	t.Helper()
	claims, err := client.ListClaimRefs(context.Background(), "claims/")
	if err != nil {
		t.Fatalf("ListClaimRefs against a severed origin with a fresh mirror: %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("want exactly one claim from the mirror, got %d: %+v", len(claims), claims)
	}
	if claims[0].Ref != "refs/claims/task/t2" || claims[0].SHA != wantSHA {
		t.Fatalf("want the mirrored claim at %s, got %+v", wantSHA, claims[0])
	}
}

// TestClaimMirrorConfigFailureKeepsServing proves a failed configuration step is
// returned to the caller without panicking, and that the client still answers
// claim reads through the live origin.
func TestClaimMirrorConfigFailureKeepsServing(t *testing.T) {
	fixture := newClaimFixture(t)
	gitDir := filepath.Join(fixture.cloneA, ".git")

	if err := os.Chmod(gitDir, 0o500); err != nil {
		t.Fatalf("chmod .git read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(gitDir, 0o700) })

	probe := filepath.Join(gitDir, ".probe")
	if err := os.WriteFile(probe, []byte("x"), 0o600); err == nil {
		_ = os.Remove(probe)
		t.Skip("running as root: cannot enforce a read-only .git directory")
	}

	fake := &mocks.FakeClaimOrigin{}
	fake.ListRefsStub = func(context.Context, string) ([]git.RemoteRef, error) {
		return nil, nil
	}
	g := git.NewGitWithClaimMirrorForTest(fixture.cloneA, fake)

	if err := g.ConfigureClaimMirror(context.Background()); err == nil {
		t.Fatal("want a configuration error against a read-only .git directory, got nil")
	}

	claims, err := g.ListClaimRefs(context.Background(), "claims/")
	if err != nil {
		t.Fatalf("ListClaimRefs after failed configuration: %v", err)
	}
	if claims == nil {
		t.Fatal("want a non-nil empty slice, got nil")
	}
	if len(claims) != 0 {
		t.Fatalf("want no claims, got %d", len(claims))
	}
	if got := fake.ListRefsCallCount(); got != 1 {
		t.Fatalf("want 1 live ListRefs call, got %d", got)
	}
}

// newMirrorRepo returns a git repository whose mirror namespace holds refs
// pointing at real claim commits, keyed by claim ref ("refs/claims/…").
func newMirrorRepo(
	t *testing.T,
	messages map[string]string,
) (repoPath string, shas map[string]string) {
	t.Helper()
	repoPath = filepath.Join(t.TempDir(), "mirror")
	if err := os.MkdirAll(repoPath, 0o750); err != nil {
		t.Fatalf("create mirror repo dir: %v", err)
	}
	mustGit(t, repoPath, "init")
	configureClaimIdentity(t, repoPath)
	shas = make(map[string]string, len(messages))
	for ref, message := range messages {
		key := strings.TrimPrefix(ref, "refs/claims/")
		sha := strings.TrimSpace(mustGit(
			t,
			repoPath,
			"commit-tree",
			claimEmptyTreeSHA,
			"-m",
			message,
		))
		mustGit(t, repoPath, "update-ref", "refs/claim-mirror/"+key, sha)
		shas[ref] = sha
	}
	return repoPath, shas
}

// newMirrorReadFake returns a FakeClaimOrigin whose commit-message read is
// answered from messages, keyed by claim ref and resolved through shas. A sha
// with no entry fails, modelling a missing local object.
func newMirrorReadFake(
	shas map[string]string,
	messages map[string]string,
) *mocks.FakeClaimOrigin {
	fake := &mocks.FakeClaimOrigin{}
	fake.ReadCommitMessageStub = func(_ context.Context, sha string) ([]byte, error) {
		for ref, refSHA := range shas {
			if refSHA == sha {
				return []byte(messages[ref]), nil
			}
		}
		return nil, errors.New("cat-file: object missing")
	}
	fake.ReadCommitMessagesStub = func(
		_ context.Context,
		requested []string,
	) (map[string][]byte, error) {
		bySHA := make(map[string]string, len(shas))
		for ref, refSHA := range shas {
			bySHA[refSHA] = messages[ref]
		}
		result := make(map[string][]byte, len(requested))
		for _, sha := range requested {
			message, ok := bySHA[sha]
			if !ok {
				return nil, errors.New("cat-file: object missing")
			}
			result[sha] = []byte(message)
		}
		return result, nil
	}
	return fake
}

// countLogLines counts the log lines that contain marker.
func countLogLines(logs, marker string) int {
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		if strings.Contains(line, marker) {
			count++
		}
	}
	return count
}

// TestClaimMirrorServesWithoutNetwork proves a fresh mirror answers a read with
// zero origin lister and fetch calls, and names its source with an age.
func TestClaimMirrorServesWithoutNetwork(t *testing.T) {
	message := `{"cluster":"nuke","task_id":"t1","lease_until":"2026-10-25T00:00:00Z"}`
	messages := map[string]string{"refs/claims/task/t1": message}
	repo, shas := newMirrorRepo(t, messages)
	fake := newMirrorReadFake(shas, messages)
	g := git.NewGitWithClaimMirrorForTest(repo, fake)
	git.SetClaimMirrorFreshAtForTest(g, time.Now())

	logs, restore := captureSlogLogs()
	defer restore()

	claims, err := g.ListClaimRefs(context.Background(), "claims/")
	if err != nil {
		t.Fatalf("ListClaimRefs: %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("want one claim, got %d: %+v", len(claims), claims)
	}
	if claims[0].Ref != "refs/claims/task/t1" ||
		claims[0].SHA != shas["refs/claims/task/t1"] ||
		claims[0].Cluster != "nuke" {
		t.Fatalf("unexpected mirrored claim: %+v", claims[0])
	}
	if got := fake.ListRefsCallCount(); got != 0 {
		t.Fatalf("want no origin ListRefs call, got %d", got)
	}
	if got := fake.FetchCommitsCallCount(); got != 0 {
		t.Fatalf("want no origin FetchCommits call, got %d", got)
	}
	if got := countLogLines(logs.String(), "served_from=mirror"); got != 1 {
		t.Fatalf("want exactly one served_from=mirror line, got %d in:\n%s", got, logs)
	}
	if !regexp.MustCompile(`age_s=[0-9]+`).MatchString(logs.String()) {
		t.Fatalf("want an age_s=<n> field, got:\n%s", logs)
	}
}

// TestClaimMirrorSeveredOrigin proves a populated, fresh mirror answers the same
// list after the origin URL is severed.
func TestClaimMirrorSeveredOrigin(t *testing.T) {
	fixture := newClaimFixture(t)
	client := newClaimClient(fixture.cloneA)
	configureClaimIdentity(t, fixture.bare)
	ctx := context.Background()

	if err := client.ConfigureClaimMirror(ctx); err != nil {
		t.Fatalf("ConfigureClaimMirror: %v", err)
	}
	sha := createClaimCommit(
		t,
		fixture.bare,
		"refs/claims/task/t1",
		`{"cluster":"nuke","task_id":"t1","lease_until":"2026-10-25T00:00:00Z"}`,
	)
	if err := client.Pull(ctx); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	before := listClaimRefs(t, client, "claims/")
	if len(before) != 1 || before[0].Ref != "refs/claims/task/t1" || before[0].SHA != sha {
		t.Fatalf("unexpected pre-severing list: %+v", before)
	}

	mustGit(t, fixture.cloneA, "remote", "set-url", "origin", "/nonexistent/origin.git")
	after := listClaimRefs(t, client, "claims/")
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("severed list %+v differs from reachable list %+v", after, before)
	}
}

// TestClaimMirrorPrefixSemantics proves the whole-path-component prefix rule and
// the claim-ref namespace mapping on the mirror path.
func TestClaimMirrorPrefixSemantics(t *testing.T) {
	messages := map[string]string{
		"refs/claims/task/1":   `{"cluster":"c","task_id":"1","lease_until":"2026-10-25T00:00:00Z"}`,
		"refs/claims/task/10":  `{"cluster":"c","task_id":"10","lease_until":"2026-10-25T00:00:00Z"}`,
		"refs/claims/tasks/1":  `{"cluster":"c","task_id":"s1","lease_until":"2026-10-25T00:00:00Z"}`,
		"refs/claims/repo/a/b": `{"cluster":"c","task_id":"ab","lease_until":"2026-10-25T00:00:00Z"}`,
	}
	repo, shas := newMirrorRepo(t, messages)
	fake := newMirrorReadFake(shas, messages)
	g := git.NewGitWithClaimMirrorForTest(repo, fake)
	git.SetClaimMirrorFreshAtForTest(g, time.Now())

	cases := []struct {
		prefix string
		want   []string
	}{
		{"claims/task/1", []string{"refs/claims/task/1"}},
		{"claims/task", []string{"refs/claims/task/1", "refs/claims/task/10"}},
		{"claims/repo/a", []string{"refs/claims/repo/a/b"}},
		{
			"claims/",
			[]string{
				"refs/claims/repo/a/b",
				"refs/claims/task/1",
				"refs/claims/task/10",
				"refs/claims/tasks/1",
			},
		},
	}
	for _, tc := range cases {
		claims, err := g.ListClaimRefs(context.Background(), tc.prefix)
		if err != nil {
			t.Fatalf("ListClaimRefs(%q): %v", tc.prefix, err)
		}
		got := make([]string, 0, len(claims))
		for _, claim := range claims {
			got = append(got, claim.Ref)
			if !strings.HasPrefix(claim.Ref, "refs/claims/") {
				t.Fatalf("prefix %q returned non-claim ref %q", tc.prefix, claim.Ref)
			}
			if strings.Contains(claim.Ref, "refs/claim-mirror/") {
				t.Fatalf("prefix %q returned a mirror ref %q", tc.prefix, claim.Ref)
			}
		}
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Fatalf("prefix %q returned %v, want %v", tc.prefix, got, tc.want)
		}
	}
	if got := fake.ListRefsCallCount(); got != 0 {
		t.Fatalf("want no origin ListRefs call, got %d", got)
	}
}

// TestClaimMirrorStaleFallsBack proves an unset or too-old freshness time takes
// the live origin path.
func TestClaimMirrorStaleFallsBack(t *testing.T) {
	repo, _ := newMirrorRepo(t, map[string]string{
		"refs/claims/mirror/x": `{"cluster":"mirror","task_id":"x","lease_until":"2026-10-25T00:00:00Z"}`,
	})
	newLiveFake := func() *mocks.FakeClaimOrigin {
		fake := &mocks.FakeClaimOrigin{}
		fake.ListRefsStub = func(_ context.Context, _ string) ([]git.RemoteRef, error) {
			return []git.RemoteRef{{SHA: claimShaA, Ref: "refs/claims/a"}}, nil
		}
		fake.FetchCommitsStub = func(_ context.Context, _ []string) error { return nil }
		fake.ReadCommitMessageStub = func(_ context.Context, _ string) ([]byte, error) {
			return claimMessages[claimShaA], nil
		}
		fake.ReadCommitMessagesStub = func(
			_ context.Context,
			shas []string,
		) (map[string][]byte, error) {
			messages := make(map[string][]byte, len(shas))
			for _, sha := range shas {
				messages[sha] = claimMessages[claimShaA]
			}
			return messages, nil
		}
		return fake
	}

	t.Run("unset freshness takes the live path", func(t *testing.T) {
		fake := newLiveFake()
		g := git.NewGitWithClaimMirrorForTest(repo, fake)
		logs, restore := captureSlogLogs()
		defer restore()
		assertLiveClaimList(t, g, fake, logs)
	})

	t.Run("older than the bound takes the live path", func(t *testing.T) {
		fake := newLiveFake()
		g := git.NewGitWithClaimMirrorForTest(repo, fake)
		git.SetClaimMirrorFreshAtForTest(g, time.Now().Add(-time.Hour))
		logs, restore := captureSlogLogs()
		defer restore()
		assertLiveClaimList(t, g, fake, logs)
	})
}

// assertLiveClaimList asserts the live-path entry was returned, the lister was
// called once, and the read named the origin as its source.
func assertLiveClaimList(
	t *testing.T,
	g git.Git,
	fake *mocks.FakeClaimOrigin,
	logs *bytes.Buffer,
) {
	t.Helper()
	claims, err := g.ListClaimRefs(context.Background(), "claims/")
	if err != nil {
		t.Fatalf("ListClaimRefs: %v", err)
	}
	if len(claims) != 1 || claims[0].Ref != "refs/claims/a" || claims[0].SHA != claimShaA {
		t.Fatalf("want the live entry, got %+v", claims)
	}
	if got := fake.ListRefsCallCount(); got != 1 {
		t.Fatalf("want exactly one live ListRefs call, got %d", got)
	}
	if got := countLogLines(logs.String(), "served_from=origin"); got != 1 {
		t.Fatalf("want exactly one served_from=origin line, got %d in:\n%s", got, logs)
	}
}

// TestClaimMirrorRefspecAbsentFallsBack proves a clone that never got the claim
// refspec can never answer an empty mirror: freshness stays unset and the read
// fails on the severed origin naming ls-remote.
func TestClaimMirrorRefspecAbsentFallsBack(t *testing.T) {
	fixture := newClaimFixture(t)
	client := newClaimClient(fixture.cloneA)
	ctx := context.Background()

	createClaimCommit(
		t,
		fixture.bare,
		"refs/claims/task/t1",
		`{"cluster":"nuke","task_id":"t1","lease_until":"2026-10-25T00:00:00Z"}`,
	)
	if err := client.Pull(ctx); err != nil {
		t.Fatalf("Pull without the claim refspec: %v", err)
	}
	if !git.ClaimMirrorFreshAtForTest(client).IsZero() {
		t.Fatal("want freshness unset on a clone without the claim refspec")
	}

	mustGit(t, fixture.cloneA, "remote", "set-url", "origin", "/nonexistent/origin.git")
	claims, err := client.ListClaimRefs(ctx, "claims/")
	if err == nil {
		t.Fatalf("want an error from the severed origin, got claims %+v", claims)
	}
	if !strings.Contains(err.Error(), "ls-remote") {
		t.Fatalf("want the error to name ls-remote, got %v", err)
	}
	if len(claims) != 0 {
		t.Fatalf("want no claims, got %+v", claims)
	}
}

// TestClaimMirrorMissingObjectFallsBack proves a mirror ref whose commit object
// is absent falls back to the live path rather than returning partial data.
func TestClaimMirrorMissingObjectFallsBack(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "mirror")
	if err := os.MkdirAll(repo, 0o750); err != nil {
		t.Fatalf("create repo dir: %v", err)
	}
	mustGit(t, repo, "init")
	configureClaimIdentity(t, repo)

	refDir := filepath.Join(repo, ".git", "refs", "claim-mirror", "task")
	if err := os.MkdirAll(refDir, 0o750); err != nil {
		t.Fatalf("create mirror ref dir: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(refDir, "t1"),
		[]byte(strings.Repeat("a", 40)+"\n"),
		0o600,
	); err != nil {
		t.Fatalf("write loose mirror ref: %v", err)
	}

	fake := &mocks.FakeClaimOrigin{}
	fake.ListRefsStub = func(_ context.Context, _ string) ([]git.RemoteRef, error) {
		return []git.RemoteRef{{SHA: claimShaB, Ref: "refs/claims/b"}}, nil
	}
	fake.FetchCommitsStub = func(_ context.Context, _ []string) error { return nil }
	fake.ReadCommitMessageStub = func(_ context.Context, sha string) ([]byte, error) {
		if sha == claimShaB {
			return claimMessages[claimShaB], nil
		}
		return nil, errors.New("cat-file: object missing")
	}
	fake.ReadCommitMessagesStub = func(
		_ context.Context,
		shas []string,
	) (map[string][]byte, error) {
		messages := make(map[string][]byte, len(shas))
		for _, sha := range shas {
			if sha != claimShaB {
				return nil, errors.New("cat-file: object missing")
			}
			messages[sha] = claimMessages[claimShaB]
		}
		return messages, nil
	}

	g := git.NewGitWithClaimMirrorForTest(repo, fake)
	git.SetClaimMirrorFreshAtForTest(g, time.Now())
	logs, restore := captureSlogLogs()
	defer restore()

	claims, err := g.ListClaimRefs(context.Background(), "claims/")
	if err != nil {
		t.Fatalf("ListClaimRefs: %v", err)
	}
	if len(claims) != 1 || claims[0].Ref != "refs/claims/b" || claims[0].SHA != claimShaB {
		t.Fatalf("want the live path's full list, got %+v", claims)
	}
	if got := fake.ListRefsCallCount(); got != 1 {
		t.Fatalf("want exactly one live ListRefs call, got %d", got)
	}
	if got := countLogLines(logs.String(), "served_from=origin"); got != 1 {
		t.Fatalf("want exactly one served_from=origin line, got %d in:\n%s", got, logs)
	}
}

// assertClaimJSONKeySets asserts every claim object carries exactly the five
// caller-visible keys and never a nonce.
func assertClaimJSONKeySets(t *testing.T, objects []map[string]json.RawMessage) {
	t.Helper()
	wantKeys := "cluster,lease_until,ref,sha,task_id"
	for _, object := range objects {
		encoded, err := json.Marshal(object)
		if err != nil {
			t.Fatalf("re-marshal object: %v", err)
		}
		if got := strings.Join(jsonKeys(t, encoded), ","); got != wantKeys {
			t.Fatalf("claim key set %q, want %q", got, wantKeys)
		}
		if _, ok := object["nonce"]; ok {
			t.Fatal("claim JSON must not carry a nonce")
		}
	}
}

// TestClaimMirrorResponseContract proves the mirror path keeps the JSON key set,
// the non-JSON claim shape, the empty-array shape and the handler's status codes.
func TestClaimMirrorResponseContract(t *testing.T) {
	valid := `{"cluster":"nuke","task_id":"t1","lease_until":"2026-10-25T00:00:00Z","nonce":"0123456789abcdef0123456789abcdef"}`
	raw := "not json"
	messages := map[string]string{
		"refs/claims/task/valid": valid,
		"refs/claims/task/raw":   raw,
	}
	repo, shas := newMirrorRepo(t, messages)
	fake := newMirrorReadFake(shas, messages)
	g := git.NewGitWithClaimMirrorForTest(repo, fake)
	git.SetClaimMirrorFreshAtForTest(g, time.Now())

	claims, err := g.ListClaimRefs(context.Background(), "claims/")
	if err != nil {
		t.Fatalf("ListClaimRefs: %v", err)
	}
	if len(claims) != 2 {
		t.Fatalf("want two claims, got %d: %+v", len(claims), claims)
	}
	body, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	var objects []map[string]json.RawMessage
	if err := json.Unmarshal(body, &objects); err != nil {
		t.Fatalf("decode claims body: %v", err)
	}
	assertClaimJSONKeySets(t, objects)
	byRef := make(map[string]git.ClaimRef, len(claims))
	for _, claim := range claims {
		byRef[claim.Ref] = claim
	}
	rawClaim, ok := byRef["refs/claims/task/raw"]
	if !ok {
		t.Fatalf("non-JSON claim missing from %+v", claims)
	}
	if rawClaim.SHA != shas["refs/claims/task/raw"] ||
		rawClaim.Cluster != "" || rawClaim.TaskID != "" || rawClaim.LeaseUntil != "" {
		t.Fatalf("non-JSON claim shape: %+v", rawClaim)
	}

	assertEmptyMirrorJSONBody(t)
	assertRefsHandlerStatuses(t, g)
}

// assertEmptyMirrorJSONBody asserts a configured, genuinely empty mirror answers
// [] rather than null.
func assertEmptyMirrorJSONBody(t *testing.T) {
	t.Helper()
	emptyRepo, _ := newMirrorRepo(t, map[string]string{})
	gEmpty := git.NewGitWithClaimMirrorForTest(emptyRepo, &mocks.FakeClaimOrigin{})
	git.SetClaimMirrorFreshAtForTest(gEmpty, time.Now())
	emptyClaims, err := gEmpty.ListClaimRefs(context.Background(), "claims/")
	if err != nil {
		t.Fatalf("ListClaimRefs on an empty mirror: %v", err)
	}
	emptyBody, err := json.Marshal(emptyClaims)
	if err != nil {
		t.Fatalf("marshal empty claims: %v", err)
	}
	if string(emptyBody) != "[]" {
		t.Fatalf("empty mirror body %q, want []", string(emptyBody))
	}
}

// assertRefsHandlerStatuses asserts the handler rejects a glob prefix before any
// git command and answers 200 for a mirror-served read.
func assertRefsHandlerStatuses(t *testing.T, g git.Git) {
	t.Helper()
	fakeGit := &mocks.FakeGit{}
	h := libhttp.NewJSONErrorHandler(handler.NewRefsGetHandler(fakeGit))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/refs?prefix=claims/*", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("glob prefix status %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if got := fakeGit.ListClaimRefsCallCount(); got != 0 {
		t.Fatalf("glob prefix reached git %d times, want 0", got)
	}

	mirrorHandler := libhttp.NewJSONErrorHandler(handler.NewRefsGetHandler(g))
	mirrorReq := httptest.NewRequest(http.MethodGet, "/api/v1/refs?prefix=claims/", nil)
	mirrorRec := httptest.NewRecorder()
	mirrorHandler.ServeHTTP(mirrorRec, mirrorReq)
	if mirrorRec.Code != http.StatusOK {
		t.Fatalf("mirror-served status %d, want %d", mirrorRec.Code, http.StatusOK)
	}
}

// newReadAfterWriteFake returns a fake whose per-item and batched message reads
// both answer with the same valid claim message for any sha.
func newReadAfterWriteFake() *mocks.FakeClaimOrigin {
	message := []byte(
		`{"cluster":"nuke","task_id":"t1","lease_until":"2026-10-25T00:00:00Z","nonce":"x"}`,
	)
	fake := &mocks.FakeClaimOrigin{}
	fake.ReadCommitMessageStub = func(context.Context, string) ([]byte, error) {
		return message, nil
	}
	fake.ReadCommitMessagesStub = func(
		_ context.Context,
		shas []string,
	) (map[string][]byte, error) {
		messages := make(map[string][]byte, len(shas))
		for _, sha := range shas {
			messages[sha] = message
		}
		return messages, nil
	}
	return fake
}

// TestClaimMirrorReadAfterWrite proves a write on this instance is visible to
// the very next read without a pull, and that the read was served from the
// mirror rather than falling back to the origin.
func TestClaimMirrorReadAfterWrite(t *testing.T) {
	fixture := newClaimFixture(t)
	configureClaimIdentity(t, fixture.bare)
	ctx := context.Background()

	realClient := newClaimClient(fixture.cloneA)
	if err := realClient.ConfigureClaimMirror(ctx); err != nil {
		t.Fatalf("ConfigureClaimMirror: %v", err)
	}
	if err := realClient.Pull(ctx); err != nil {
		t.Fatalf("Pull: %v", err)
	}

	fake := newReadAfterWriteFake()
	g := git.NewGitWithClaimMirrorForTest(fixture.cloneA, fake)
	git.SetClaimMirrorFreshAtForTest(g, time.Now())
	freshAt := git.ClaimMirrorFreshAtForTest(g)

	logs, restore := captureSlogLogs()
	defer restore()

	sha, err := g.PutClaimRef(ctx, "claims/task/t1", git.ClaimRefRequest{
		Cluster:    "nuke",
		TaskID:     "t1",
		LeaseUntil: "2026-10-25T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("PutClaimRef: %v", err)
	}
	if sha == "" {
		t.Fatal("want a non-empty sha from PutClaimRef")
	}
	if got := git.ClaimMirrorFreshAtForTest(g); !got.Equal(freshAt) {
		t.Fatalf("want freshness unchanged by a write, got %v (before %v)", got, freshAt)
	}

	claims, err := g.ListClaimRefs(ctx, "claims/")
	if err != nil {
		t.Fatalf("ListClaimRefs after put: %v", err)
	}
	if len(claims) != 1 || claims[0].Ref != "refs/claims/task/t1" || claims[0].SHA != sha {
		t.Fatalf("want the just-written claim at %s, got %+v", sha, claims)
	}
	if got := fake.ListRefsCallCount(); got != 0 {
		t.Fatalf("want no origin ListRefs call, got %d", got)
	}
	if got := countLogLines(logs.String(), "served_from=mirror"); got < 1 {
		t.Fatalf("want a served_from=mirror line, got %d in:\n%s", got, logs)
	}

	if err := g.DeleteClaimRef(ctx, "claims/task/t1", sha); err != nil {
		t.Fatalf("DeleteClaimRef: %v", err)
	}
	if got := git.ClaimMirrorFreshAtForTest(g); !got.Equal(freshAt) {
		t.Fatalf("want freshness unchanged by a delete, got %v (before %v)", got, freshAt)
	}

	claims, err = g.ListClaimRefs(ctx, "claims/")
	if err != nil {
		t.Fatalf("ListClaimRefs after delete: %v", err)
	}
	if len(claims) != 0 {
		t.Fatalf("want no claims after delete, got %+v", claims)
	}
	if got := fake.ListRefsCallCount(); got != 0 {
		t.Fatalf("want no origin ListRefs call, got %d", got)
	}
	if got := countLogLines(logs.String(), "served_from=mirror"); got < 2 {
		t.Fatalf("want a second served_from=mirror line, got %d in:\n%s", got, logs)
	}
}

// TestClaimMirrorCrossCluster proves another cluster's claim becomes visible
// after exactly one pull cycle, not before it.
func TestClaimMirrorCrossCluster(t *testing.T) {
	fixture := newClaimFixture(t)
	ctx := context.Background()

	clientA := newClaimClient(fixture.cloneA)
	clientB := newClaimClient(fixture.cloneB)
	for name, client := range map[string]git.Git{"A": clientA, "B": clientB} {
		if err := client.ConfigureClaimMirror(ctx); err != nil {
			t.Fatalf("ConfigureClaimMirror on %s: %v", name, err)
		}
		if err := client.Pull(ctx); err != nil {
			t.Fatalf("Pull on %s: %v", name, err)
		}
	}

	shaA, err := clientA.PutClaimRef(ctx, "claims/task/t9", git.ClaimRefRequest{
		Cluster:    "cluster-a",
		TaskID:     "t9",
		LeaseUntil: claimLeaseUntil,
	})
	if err != nil {
		t.Fatalf("PutClaimRef: %v", err)
	}

	claimsB, err := clientB.ListClaimRefs(ctx, "claims/")
	if err != nil {
		t.Fatalf("ListClaimRefs before pull: %v", err)
	}
	for _, claim := range claimsB {
		if claim.Ref == "refs/claims/task/t9" {
			t.Fatalf("cluster B saw the claim before its pull: %+v", claimsB)
		}
	}

	if err := clientB.Pull(ctx); err != nil {
		t.Fatalf("Pull on B: %v", err)
	}
	claimsB, err = clientB.ListClaimRefs(ctx, "claims/")
	if err != nil {
		t.Fatalf("ListClaimRefs after pull: %v", err)
	}
	if len(claimsB) != 1 ||
		claimsB[0].Ref != "refs/claims/task/t9" ||
		claimsB[0].SHA != shaA {
		t.Fatalf("want the cross-cluster claim at %s after one pull, got %+v", shaA, claimsB)
	}
}

// TestClaimMirrorSyncStateUnchanged proves a ref GET, PUT and DELETE leave the
// clone's fetch configuration and remote-tracking refs byte-identical.
func TestClaimMirrorSyncStateUnchanged(t *testing.T) {
	fixture := newClaimFixture(t)
	ctx := context.Background()

	client := newClaimClient(fixture.cloneA)
	if err := client.ConfigureClaimMirror(ctx); err != nil {
		t.Fatalf("ConfigureClaimMirror: %v", err)
	}
	if err := client.Pull(ctx); err != nil {
		t.Fatalf("Pull: %v", err)
	}

	beforeFetch := mustGit(t, fixture.cloneA, "config", "--get-all", "remote.origin.fetch")
	beforeRefs := mustGit(t, fixture.cloneA, "for-each-ref", "refs/remotes/origin")
	if strings.TrimSpace(beforeRefs) == "" {
		t.Fatal("want a non-empty remote-tracking listing so the comparison can fail")
	}
	if !strings.Contains(beforeRefs, "refs/remotes/origin/") {
		t.Fatalf("want refs/remotes/origin/ in the pre-operation listing, got %q", beforeRefs)
	}

	if _, err := client.ListClaimRefs(ctx, "claims/"); err != nil {
		t.Fatalf("ListClaimRefs: %v", err)
	}
	sha, err := client.PutClaimRef(ctx, "claims/task/sync", git.ClaimRefRequest{
		Cluster:    "nuke",
		TaskID:     "sync",
		LeaseUntil: claimLeaseUntil,
	})
	if err != nil {
		t.Fatalf("PutClaimRef: %v", err)
	}
	if err := client.DeleteClaimRef(ctx, "claims/task/sync", sha); err != nil {
		t.Fatalf("DeleteClaimRef: %v", err)
	}

	afterFetch := mustGit(t, fixture.cloneA, "config", "--get-all", "remote.origin.fetch")
	afterRefs := mustGit(t, fixture.cloneA, "for-each-ref", "refs/remotes/origin")
	if afterFetch != beforeFetch {
		t.Fatalf("fetch config changed:\nbefore %q\nafter  %q", beforeFetch, afterFetch)
	}
	if afterRefs != beforeRefs {
		t.Fatalf("remote-tracking refs changed:\nbefore %q\nafter  %q", beforeRefs, afterRefs)
	}
}

// TestClaimRefBatchProcessCount proves a listing starts a constant number of
// git processes regardless of how many refs match the prefix, on both the live
// and mirror paths, and that the commit-message read is one `cat-file --batch`.
func TestClaimRefBatchProcessCount(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("locate git: %v", err)
	}
	shimDir := t.TempDir()
	counterPath := filepath.Join(shimDir, "counter")
	writeGitShim(t, filepath.Join(shimDir, "git"), realGit, counterPath)

	for _, tc := range []struct {
		name      string
		mirror    bool
		wantTotal int
	}{
		{name: "live", mirror: false, wantTotal: 4},
		{name: "mirror", mirror: true, wantTotal: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			counts := make(map[int]gitProcessCounts, 2)
			for _, size := range []int{300, 600} {
				counts[size] = measureClaimRefProcesses(
					t, tc.mirror, size, shimDir, counterPath,
				)
				if counts[size].total != tc.wantTotal {
					t.Fatalf(
						"%d refs: want %d git processes, got %d (%+v)",
						size, tc.wantTotal, counts[size].total, counts[size],
					)
				}
				if counts[size].catFile != 1 {
					t.Fatalf(
						"%d refs: want exactly one cat-file process, got %d",
						size, counts[size].catFile,
					)
				}
			}
			if counts[300].total != counts[600].total {
				t.Fatalf(
					"process count changed with ref count: 300 -> %d, 600 -> %d",
					counts[300].total, counts[600].total,
				)
			}
		})
	}
}

// gitProcessCounts is the number of git processes a measurement observed.
type gitProcessCounts struct {
	total   int
	catFile int
}

// measureClaimRefProcesses seeds size claim refs, issues the refs GET with the
// shim on PATH, and returns the git process counts it observed.
func measureClaimRefProcesses(
	t *testing.T,
	mirror bool,
	size int,
	shimDir string,
	counterPath string,
) gitProcessCounts {
	t.Helper()
	fixture := newClaimFixture(t)
	configureClaimIdentity(t, fixture.bare)
	seedClaimRefs(t, fixture.bare, size)

	client := newClaimClient(fixture.cloneA)
	ctx := context.Background()
	if mirror {
		if err := client.ConfigureClaimMirror(ctx); err != nil {
			t.Fatalf("ConfigureClaimMirror: %v", err)
		}
		if err := client.Pull(ctx); err != nil {
			t.Fatalf("Pull: %v", err)
		}
	}

	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", shimDir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatalf("prepend git shim to PATH: %v", err)
	}
	if err := os.WriteFile(counterPath, nil, 0o600); err != nil {
		t.Fatalf("truncate git counter: %v", err)
	}

	logs, restore := captureSlogLogs()
	defer restore()
	h := libhttp.NewJSONErrorHandler(handler.NewRefsGetHandler(client))
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/refs?prefix=claims%2Fsuccess%2F",
		nil,
	)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if err := os.Setenv("PATH", oldPath); err != nil {
		t.Fatalf("restore PATH: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("GET status %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var claims []git.ClaimRef
	if err := json.Unmarshal(rec.Body.Bytes(), &claims); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(claims) != size {
		t.Fatalf("want %d refs, got %d", size, len(claims))
	}
	wantSource := "served_from=origin"
	if mirror {
		wantSource = "served_from=mirror"
	}
	if got := countLogLines(logs.String(), wantSource); got != 1 {
		t.Fatalf("want exactly one %s line, got %d in:\n%s", wantSource, got, logs)
	}
	return countGitInvocations(t, counterPath)
}

// seedClaimRefs seeds size refs under refs/claims/success/ in one update-ref
// process, cycling through four distinct claim commits.
func seedClaimRefs(t *testing.T, bare string, size int) {
	t.Helper()
	messages := []string{
		`{"cluster":"c1","task_id":"t1","lease_until":"2026-10-25T00:00:00Z","nonce":"n1"}`,
		`{"cluster":"c2","task_id":"t2","lease_until":"2026-10-25T00:00:00Z","nonce":"n2"}`,
		`{"cluster":"c3","task_id":"t3","lease_until":"2026-10-25T00:00:00Z","nonce":"n3"}`,
		`{"cluster":"c4","task_id":"t4","lease_until":"2026-10-25T00:00:00Z","nonce":"n4"}`,
	}
	shas := make([]string, 0, len(messages))
	for _, message := range messages {
		shas = append(shas, strings.TrimSpace(mustGit(
			t, bare, "commit-tree", claimEmptyTreeSHA, "-m", message,
		)))
	}
	var input strings.Builder
	for i := 1; i <= size; i++ {
		fmt.Fprintf(
			&input,
			"create refs/claims/success/task%d/phase%d/%d %s\n",
			i, i, i, shas[i%len(shas)],
		)
	}
	mustGitInput(t, bare, input.String(), "update-ref", "--stdin")
}

// mustGitInput runs git in dir with stdin and fails the test on a non-zero exit.
func mustGitInput(t *testing.T, dir, input string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, out)
	}
	return string(out)
}

// writeGitShim writes an executable git shim that appends its arguments to
// counterPath and execs the real git binary.
func writeGitShim(t *testing.T, path, realGit, counterPath string) {
	t.Helper()
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + shellQuote(counterPath) + "\n" +
		"exec " + shellQuote(realGit) + " \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write git shim: %v", err)
	}
}

// countGitInvocations reads the shim counter and counts all lines and the
// lines that start with cat-file.
func countGitInvocations(t *testing.T, counterPath string) gitProcessCounts {
	t.Helper()
	data, err := os.ReadFile(counterPath)
	if err != nil {
		t.Fatalf("read git counter: %v", err)
	}
	var counts gitProcessCounts
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		counts.total++
		if strings.HasPrefix(line, "cat-file") {
			counts.catFile++
		}
	}
	return counts
}

// TestClaimMirrorWalkIsPrefixScoped proves the mirror listing hands the prefix
// to for-each-ref instead of walking the whole mirror namespace. A process
// count cannot prove this: the unscoped walk is also exactly one process, so
// the test asserts the recorded argv itself.
func TestClaimMirrorWalkIsPrefixScoped(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("locate git: %v", err)
	}
	shimDir := t.TempDir()
	counterPath := filepath.Join(shimDir, "counter")
	writeGitShim(t, filepath.Join(shimDir, "git"), realGit, counterPath)

	fixture := newClaimFixture(t)
	configureClaimIdentity(t, fixture.bare)
	seedClaimRefs(t, fixture.bare, 80)

	client := newClaimClient(fixture.cloneA)
	ctx := context.Background()
	if err := client.ConfigureClaimMirror(ctx); err != nil {
		t.Fatalf("ConfigureClaimMirror: %v", err)
	}
	if err := client.Pull(ctx); err != nil {
		t.Fatalf("Pull: %v", err)
	}

	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", shimDir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatalf("prepend git shim to PATH: %v", err)
	}
	if err := os.WriteFile(counterPath, nil, 0o600); err != nil {
		t.Fatalf("truncate git counter: %v", err)
	}
	logs, restore := captureSlogLogs()
	defer restore()
	h := libhttp.NewJSONErrorHandler(handler.NewRefsGetHandler(client))
	// task7 must match task7/... only, never task70..task79.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/refs?prefix=claims%2Fsuccess%2Ftask7", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if err := os.Setenv("PATH", oldPath); err != nil {
		t.Fatalf("restore PATH: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("GET status %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var claims []git.ClaimRef
	if err := json.Unmarshal(rec.Body.Bytes(), &claims); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("want 1 ref under claims/success/task7, got %d: %+v", len(claims), claims)
	}
	if got := countLogLines(logs.String(), "served_from=mirror"); got != 1 {
		t.Fatalf("want exactly one served_from=mirror line, got %d in:\n%s", got, logs)
	}

	data, err := os.ReadFile(counterPath)
	if err != nil {
		t.Fatalf("read git counter: %v", err)
	}
	var walks []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "for-each-ref") {
			walks = append(walks, line)
		}
	}
	if len(walks) != 1 {
		t.Fatalf("want exactly one for-each-ref, got %d: %q", len(walks), walks)
	}
	const want = " refs/claim-mirror/success/task7"
	if !strings.HasSuffix(walks[0], want) {
		t.Fatalf("for-each-ref not scoped to the prefix: %q, want suffix %q", walks[0], want)
	}
}

// TestClaimMirrorUpdateFailureSwallowed proves a failed local mirror update
// never fails the request: the origin write stands and the failure is logged.
func TestClaimMirrorUpdateFailureSwallowed(t *testing.T) {
	fixture := newClaimFixture(t)
	configureClaimIdentity(t, fixture.bare)
	ctx := context.Background()

	realClient := newClaimClient(fixture.cloneA)
	if err := realClient.ConfigureClaimMirror(ctx); err != nil {
		t.Fatalf("ConfigureClaimMirror: %v", err)
	}
	if err := realClient.Pull(ctx); err != nil {
		t.Fatalf("Pull: %v", err)
	}

	blocker := filepath.Join(fixture.cloneA, ".git", "refs", "claim-mirror")
	if err := os.RemoveAll(blocker); err != nil {
		t.Fatalf("clear mirror namespace: %v", err)
	}
	if err := os.WriteFile(blocker, []byte("blocked\n"), 0o600); err != nil {
		t.Fatalf("block the mirror namespace with a regular file: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(blocker) })

	fake := &mocks.FakeClaimOrigin{}
	g := git.NewGitWithClaimMirrorForTest(fixture.cloneA, fake)
	git.SetClaimMirrorFreshAtForTest(g, time.Now())

	logs, restore := captureSlogLogs()
	defer restore()

	sha, err := g.PutClaimRef(ctx, "claims/task/fail", git.ClaimRefRequest{
		Cluster:    "nuke",
		TaskID:     "fail",
		LeaseUntil: claimLeaseUntil,
	})
	if err != nil {
		t.Fatalf("PutClaimRef must succeed despite a failed mirror update: %v", err)
	}
	if sha == "" {
		t.Fatal("want a non-empty sha")
	}
	if got := strings.TrimSpace(
		mustGit(t, fixture.bare, "rev-parse", "refs/claims/task/fail"),
	); got != sha {
		t.Fatalf("want the origin ref at %s, got %q", sha, got)
	}
	if got := countLogLines(logs.String(), "updating the local claim mirror after a write failed"); got < 1 {
		t.Fatalf("want a mirror-update failure log line, got %d in:\n%s", got, logs)
	}
	if got := strings.TrimSpace(
		mustGit(t, fixture.cloneA, "for-each-ref", "refs/claim-mirror/task/fail"),
	); got != "" {
		t.Fatalf("want no local mirror ref, got %q", got)
	}
}
