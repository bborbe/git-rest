// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	libtime "github.com/bborbe/time"

	"github.com/bborbe/git-rest/mocks"
	"github.com/bborbe/git-rest/pkg/git"
)

const (
	claimEmptyTreeSHA = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	claimLeaseUntil   = "2026-10-21T12:05:00Z"
)

// mustGit runs git in dir and fails the test on a non-zero exit.
func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, out)
	}
	return string(out)
}

// claimFixture is a bare origin plus two independent clones and clients.
type claimFixture struct {
	bare    string
	cloneA  string
	cloneB  string
	clientA git.Git
	clientB git.Git
}

func newClaimFixture(t *testing.T) *claimFixture {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	mustGit(t, root, "init", "--bare", bare)

	seed := filepath.Join(root, "seed")
	mustGit(t, root, "init", seed)
	configureClaimIdentity(t, seed)
	mustGit(t, seed, "checkout", "-b", "main")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("seed\n"), 0600); err != nil {
		t.Fatalf("write seed file: %v", err)
	}
	mustGit(t, seed, "add", "README.md")
	mustGit(t, seed, "commit", "-m", "seed")
	mustGit(t, seed, "remote", "add", "origin", bare)
	mustGit(t, seed, "push", "origin", "main")
	mustGit(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")

	cloneA := filepath.Join(root, "clone-a")
	cloneB := filepath.Join(root, "clone-b")
	mustGit(t, root, "clone", bare, cloneA)
	mustGit(t, root, "clone", bare, cloneB)
	configureClaimIdentity(t, cloneA)
	configureClaimIdentity(t, cloneB)

	return &claimFixture{
		bare:    bare,
		cloneA:  cloneA,
		cloneB:  cloneB,
		clientA: newClaimClient(cloneA),
		clientB: newClaimClient(cloneB),
	}
}

func configureClaimIdentity(t *testing.T, dir string) {
	t.Helper()
	mustGit(t, dir, "config", "user.name", "Claim User")
	mustGit(t, dir, "config", "user.email", "claim@example.com")
}

func newClaimClient(repoPath string) git.Git {
	return git.New(repoPath, &noopMetrics{}, libtime.NewCurrentDateTime(), "", nil)
}

// claimResult is one outcome of a concurrent claim operation.
type claimResult struct {
	sha string
	err error
}

// putPair runs two PutClaimRef calls concurrently against one origin.
func putPair(
	ctx context.Context,
	a, b git.Git,
	key string,
	reqA, reqB git.ClaimRefRequest,
) (claimResult, claimResult) {
	results := make([]claimResult, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		results[0].sha, results[0].err = a.PutClaimRef(ctx, key, reqA)
	}()
	go func() {
		defer wg.Done()
		results[1].sha, results[1].err = b.PutClaimRef(ctx, key, reqB)
	}()
	wg.Wait()
	return results[0], results[1]
}

// deletePair runs two DeleteClaimRef calls concurrently against one origin.
func deletePair(
	ctx context.Context,
	a, b git.Git,
	key, expectedSHA string,
) (claimResult, claimResult) {
	results := make([]claimResult, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		results[0].err = a.DeleteClaimRef(ctx, key, expectedSHA)
	}()
	go func() {
		defer wg.Done()
		results[1].err = b.DeleteClaimRef(ctx, key, expectedSHA)
	}()
	wg.Wait()
	return results[0], results[1]
}

// assertOneWinner asserts exactly one success and one conflict, returning the
// winner's sha.
func assertOneWinner(t *testing.T, results ...claimResult) string {
	t.Helper()
	winner := ""
	winners, conflicts := 0, 0
	for _, result := range results {
		switch {
		case result.err == nil:
			winners++
			winner = result.sha
		case errors.Is(result.err, git.ErrRefConflict):
			conflicts++
		default:
			t.Fatalf("unexpected result: sha=%q err=%v", result.sha, result.err)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf(
			"expected one winner and one conflict, got %d winners, %d conflicts",
			winners,
			conflicts,
		)
	}
	return winner
}

func originRef(t *testing.T, bare, ref string) string {
	t.Helper()
	return strings.TrimSpace(mustGit(t, bare, "rev-parse", ref))
}

func originRefAbsent(t *testing.T, bare, ref string) bool {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", ref)
	cmd.Dir = bare
	return cmd.Run() != nil
}

func commitMessage(t *testing.T, dir, sha string) string {
	t.Helper()
	out := mustGit(t, dir, "cat-file", "-p", sha)
	_, message, found := strings.Cut(out, "\n\n")
	if !found {
		t.Fatalf("commit %s has no message separator: %q", sha, out)
	}
	return message
}

func jsonKeys(t *testing.T, data []byte) []string {
	t.Helper()
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode %q: %v", data, err)
	}
	keys := make([]string, 0, len(decoded))
	for key := range decoded {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func listClaimRefs(t *testing.T, client git.Git, prefix string) []git.ClaimRef {
	t.Helper()
	claims, err := client.ListClaimRefs(context.Background(), prefix)
	if err != nil {
		t.Fatalf("ListClaimRefs(%q): %v", prefix, err)
	}
	return claims
}

func TestClaimCreateRace(t *testing.T) {
	fixture := newClaimFixture(t)
	ctx := context.Background()
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("claims/race/create-%d", i)
		reqA := git.ClaimRefRequest{Cluster: "cluster-a", TaskID: "t", LeaseUntil: claimLeaseUntil}
		reqB := git.ClaimRefRequest{Cluster: "cluster-b", TaskID: "t", LeaseUntil: claimLeaseUntil}
		resultA, resultB := putPair(ctx, fixture.clientA, fixture.clientB, key, reqA, reqB)
		winner := assertOneWinner(t, resultA, resultB)
		if got := originRef(t, fixture.bare, "refs/"+key); got != winner {
			t.Fatalf("iteration %d: origin ref %s, winner %s", i, got, winner)
		}
	}
}

func TestClaimRenewRace(t *testing.T) {
	fixture := newClaimFixture(t)
	ctx := context.Background()
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("claims/race/renew-%d", i)
		s0, err := fixture.clientA.PutClaimRef(ctx, key, git.ClaimRefRequest{Cluster: "seed"})
		if err != nil {
			t.Fatalf("iteration %d: create: %v", i, err)
		}
		reqA := git.ClaimRefRequest{ExpectedSHA: s0, Cluster: "cluster-a"}
		reqB := git.ClaimRefRequest{ExpectedSHA: s0, Cluster: "cluster-b"}
		resultA, resultB := putPair(ctx, fixture.clientA, fixture.clientB, key, reqA, reqB)
		winner := assertOneWinner(t, resultA, resultB)
		if got := originRef(t, fixture.bare, "refs/"+key); got != winner {
			t.Fatalf("iteration %d: origin ref %s, winner %s", i, got, winner)
		}
	}
	assertStaleRenewRejected(t, fixture)
}

func assertStaleRenewRejected(t *testing.T, fixture *claimFixture) {
	t.Helper()
	ctx := context.Background()
	key := "claims/race/stale-renew"
	s0, err := fixture.clientA.PutClaimRef(ctx, key, git.ClaimRefRequest{Cluster: "one"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s1, err := fixture.clientA.PutClaimRef(
		ctx,
		key,
		git.ClaimRefRequest{ExpectedSHA: s0, Cluster: "two"},
	)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	_, err = fixture.clientA.PutClaimRef(
		ctx,
		key,
		git.ClaimRefRequest{ExpectedSHA: s0, Cluster: "three"},
	)
	if !errors.Is(err, git.ErrRefConflict) {
		t.Fatalf("stale renew: expected ErrRefConflict, got %v", err)
	}
	if got := originRef(t, fixture.bare, "refs/"+key); got != s1 {
		t.Fatalf("stale renew changed origin ref to %s, want %s", got, s1)
	}
}

func TestClaimDeleteRace(t *testing.T) {
	fixture := newClaimFixture(t)
	ctx := context.Background()
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("claims/race/delete-%d", i)
		s0, err := fixture.clientA.PutClaimRef(ctx, key, git.ClaimRefRequest{Cluster: "seed"})
		if err != nil {
			t.Fatalf("iteration %d: create: %v", i, err)
		}
		resultA, resultB := deletePair(ctx, fixture.clientA, fixture.clientB, key, s0)
		assertOneWinner(t, resultA, resultB)
		if !originRefAbsent(t, fixture.bare, "refs/"+key) {
			t.Fatalf("iteration %d: origin ref still present", i)
		}
	}
	assertAbsentKeyConflicts(t, fixture)
}

func assertAbsentKeyConflicts(t *testing.T, fixture *claimFixture) {
	t.Helper()
	ctx := context.Background()
	absent := "claims/race/absent"
	someSHA := strings.Repeat("a", 40)
	if _, err := fixture.clientA.PutClaimRef(
		ctx,
		absent,
		git.ClaimRefRequest{ExpectedSHA: someSHA},
	); !errors.Is(err, git.ErrRefConflict) {
		t.Fatalf("put with expected sha on absent key: expected ErrRefConflict, got %v", err)
	}
	if err := fixture.clientA.DeleteClaimRef(ctx, absent, someSHA); !errors.Is(
		err,
		git.ErrRefConflict,
	) {
		t.Fatalf("delete with expected sha on absent key: expected ErrRefConflict, got %v", err)
	}
	key := "claims/race/double-delete"
	s0, err := fixture.clientA.PutClaimRef(ctx, key, git.ClaimRefRequest{Cluster: "seed"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := fixture.clientA.DeleteClaimRef(ctx, key, s0); err != nil {
		t.Fatalf("first delete: %v", err)
	}
	if err := fixture.clientA.DeleteClaimRef(ctx, key, s0); !errors.Is(
		err,
		git.ErrRefConflict,
	) {
		t.Fatalf("second delete: expected ErrRefConflict, got %v", err)
	}
}

func TestClaimCreateRaceIdenticalBody(t *testing.T) {
	fixture := newClaimFixture(t)
	ctx := context.Background()
	request := git.ClaimRefRequest{
		Cluster:    "cluster",
		TaskID:     "t",
		LeaseUntil: claimLeaseUntil,
	}
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("claims/race/identical-create-%d", i)
		resultA, resultB := putPair(ctx, fixture.clientA, fixture.clientB, key, request, request)
		winner := assertOneWinner(t, resultA, resultB)
		if got := originRef(t, fixture.bare, "refs/"+key); got != winner {
			t.Fatalf("iteration %d: origin ref %s, winner %s", i, got, winner)
		}
	}
}

func TestClaimRenewRaceIdenticalBody(t *testing.T) {
	fixture := newClaimFixture(t)
	ctx := context.Background()
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("claims/race/identical-renew-%d", i)
		s0, err := fixture.clientA.PutClaimRef(ctx, key, git.ClaimRefRequest{Cluster: "seed"})
		if err != nil {
			t.Fatalf("iteration %d: create: %v", i, err)
		}
		request := git.ClaimRefRequest{
			ExpectedSHA: s0,
			Cluster:     "cluster",
			TaskID:      "t",
			LeaseUntil:  claimLeaseUntil,
		}
		resultA, resultB := putPair(ctx, fixture.clientA, fixture.clientB, key, request, request)
		winner := assertOneWinner(t, resultA, resultB)
		if got := originRef(t, fixture.bare, "refs/"+key); got != winner {
			t.Fatalf("iteration %d: origin ref %s, winner %s", i, got, winner)
		}
	}
}

func TestClaimRepeatIdenticalCreate(t *testing.T) {
	fixture := newClaimFixture(t)
	ctx := context.Background()
	key := "claims/repeat/create"
	request := git.ClaimRefRequest{Cluster: "c", TaskID: "t", LeaseUntil: claimLeaseUntil}
	s1, err := fixture.clientA.PutClaimRef(ctx, key, request)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := fixture.clientA.PutClaimRef(ctx, key, request); !errors.Is(
		err,
		git.ErrRefConflict,
	) {
		t.Fatalf("second create: expected ErrRefConflict, got %v", err)
	}
	if got := originRef(t, fixture.bare, "refs/"+key); got != s1 {
		t.Fatalf("origin ref %s, want %s", got, s1)
	}
}

var claimNoncePattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func claimNonce(t *testing.T, dir, sha string) string {
	t.Helper()
	var message struct {
		Nonce string `json:"nonce"`
	}
	if err := json.Unmarshal([]byte(commitMessage(t, dir, sha)), &message); err != nil {
		t.Fatalf("decode claim message: %v", err)
	}
	return message.Nonce
}

func TestClaimNonceUnique(t *testing.T) {
	fixture := newClaimFixture(t)
	ctx := context.Background()
	key := "claims/nonce/unique"
	request := git.ClaimRefRequest{Cluster: "c", TaskID: "t", LeaseUntil: claimLeaseUntil}
	s1, err := fixture.clientA.PutClaimRef(ctx, key, request)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if err := fixture.clientA.DeleteClaimRef(ctx, key, s1); err != nil {
		t.Fatalf("delete: %v", err)
	}
	s2, err := fixture.clientA.PutClaimRef(ctx, key, request)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if s1 == s2 {
		t.Fatalf("two creates produced the same sha %s", s1)
	}
	nonce1 := claimNonce(t, fixture.bare, s1)
	nonce2 := claimNonce(t, fixture.bare, s2)
	if nonce1 == nonce2 {
		t.Fatalf("nonces are equal: %s", nonce1)
	}
	if !claimNoncePattern.MatchString(nonce1) || !claimNoncePattern.MatchString(nonce2) {
		t.Fatalf("nonces are not 32 hex chars: %q, %q", nonce1, nonce2)
	}
	assertCallerNonceIgnored(t, fixture)
}

func assertCallerNonceIgnored(t *testing.T, fixture *claimFixture) {
	t.Helper()
	ctx := context.Background()
	body := `{"cluster":"c","task_id":"t","lease_until":"2026-10-21T12:05:00Z",` +
		`"nonce":"deadbeefdeadbeefdeadbeefdeadbeef"}`
	var request git.ClaimRefRequest
	if err := json.Unmarshal([]byte(body), &request); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	key := "claims/nonce/caller"
	sha, err := fixture.clientA.PutClaimRef(ctx, key, request)
	if err != nil {
		t.Fatalf("put with caller nonce: %v", err)
	}
	nonce := claimNonce(t, fixture.bare, sha)
	if !claimNoncePattern.MatchString(nonce) {
		t.Fatalf("committed nonce is not 32 hex chars: %q", nonce)
	}
	if nonce == "deadbeefdeadbeefdeadbeefdeadbeef" {
		t.Fatalf("caller-supplied nonce was used")
	}
}

func TestClaimRenewTimeoutRecovery(t *testing.T) {
	fixture := newClaimFixture(t)
	ctx := context.Background()
	key := "claims/renew/timeout"
	s0, err := fixture.clientA.PutClaimRef(ctx, key, git.ClaimRefRequest{Cluster: "one"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	renew := git.ClaimRefRequest{
		ExpectedSHA: s0,
		Cluster:     "two",
		TaskID:      "t-2",
		LeaseUntil:  claimLeaseUntil,
	}
	s1, err := fixture.clientA.PutClaimRef(ctx, key, renew)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	// The response is discarded and the identical renew is retried.
	if _, err := fixture.clientA.PutClaimRef(ctx, key, renew); !errors.Is(
		err,
		git.ErrRefConflict,
	) {
		t.Fatalf("repeated renew: expected ErrRefConflict, got %v", err)
	}
	if got := originRef(t, fixture.bare, "refs/"+key); got != s1 {
		t.Fatalf("origin ref %s, want %s", got, s1)
	}
	claims := listClaimRefs(t, fixture.clientA, "claims/")
	if len(claims) != 1 || claims[0].SHA != s1 {
		t.Fatalf("list returned %+v, want one entry with sha %s", claims, s1)
	}
	if claims[0].Cluster != "two" || claims[0].TaskID != "t-2" ||
		claims[0].LeaseUntil != claimLeaseUntil {
		t.Fatalf("listed metadata %+v does not match the holder's write", claims[0])
	}
	if _, err := fixture.clientA.PutClaimRef(
		ctx,
		key,
		git.ClaimRefRequest{ExpectedSHA: s1, Cluster: "three"},
	); err != nil {
		t.Fatalf("renew with current sha: %v", err)
	}
}

func TestClaimMetadataRoundTrip(t *testing.T) {
	fixture := newClaimFixture(t)
	ctx := context.Background()
	key := "claims/meta/round-trip"
	request := git.ClaimRefRequest{
		Cluster:    "nuke",
		TaskID:     "t-42",
		LeaseUntil: claimLeaseUntil,
	}
	sha, err := fixture.clientA.PutClaimRef(ctx, key, request)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	claims := listClaimRefs(t, fixture.clientA, "claims/")
	if len(claims) != 1 {
		t.Fatalf("expected one claim, got %d", len(claims))
	}
	claim := claims[0]
	encoded, err := json.Marshal(claim)
	if err != nil {
		t.Fatalf("marshal claim: %v", err)
	}
	keys := jsonKeys(t, encoded)
	wantKeys := []string{"cluster", "lease_until", "ref", "sha", "task_id"}
	if strings.Join(keys, ",") != strings.Join(wantKeys, ",") {
		t.Fatalf("claim JSON keys %v, want %v", keys, wantKeys)
	}
	if claim.Ref != "refs/"+key || claim.SHA != sha || claim.Cluster != "nuke" ||
		claim.TaskID != "t-42" || claim.LeaseUntil != claimLeaseUntil {
		t.Fatalf("unexpected claim: %+v", claim)
	}
	assertClaimCommitShape(t, fixture.bare, sha, request)
}

func assertClaimCommitShape(t *testing.T, dir, sha string, request git.ClaimRefRequest) {
	t.Helper()
	out := mustGit(t, dir, "cat-file", "-p", sha)
	header, message, found := strings.Cut(out, "\n\n")
	if !found {
		t.Fatalf("commit has no message separator: %q", out)
	}
	if !strings.Contains(header, "tree "+claimEmptyTreeSHA) {
		t.Fatalf("commit tree is not the empty tree: %q", header)
	}
	for _, line := range strings.Split(header, "\n") {
		if strings.HasPrefix(line, "parent ") {
			t.Fatalf("claim commit has a parent: %q", header)
		}
	}
	keys := jsonKeys(t, []byte(message))
	wantKeys := []string{"cluster", "lease_until", "nonce", "task_id"}
	if strings.Join(keys, ",") != strings.Join(wantKeys, ",") {
		t.Fatalf("message keys %v, want %v", keys, wantKeys)
	}
	var decoded struct {
		Cluster    string `json:"cluster"`
		TaskID     string `json:"task_id"`
		LeaseUntil string `json:"lease_until"`
		Nonce      string `json:"nonce"`
	}
	if err := json.Unmarshal([]byte(message), &decoded); err != nil {
		t.Fatalf("decode message: %v", err)
	}
	if decoded.Cluster != request.Cluster || decoded.TaskID != request.TaskID ||
		decoded.LeaseUntil != request.LeaseUntil {
		t.Fatalf("message %+v does not match request %+v", decoded, request)
	}
	if !claimNoncePattern.MatchString(decoded.Nonce) {
		t.Fatalf("nonce %q is not 32 hex chars", decoded.Nonce)
	}
}

// invalidClaimNames is the AC4 name table, in its percent-decoded form.
var invalidClaimNames = []string{
	"heads/main",
	"tags/x",
	"claims/../heads/main",
	"claims/",
	"claims",
	"claims/a..b",
	"claims/x.lock",
	"claims/a b",
	"claims/a~1",
	"claims/a:b",
	"claims//x",
	"claims/x/",
}

// createClaimCommit writes a parentless claim commit on the empty tree directly
// in the bare origin, then points ref at it.
func createClaimCommit(t *testing.T, bare, ref, message string) string {
	t.Helper()
	sha := strings.TrimSpace(mustGit(
		t,
		bare,
		"-c", "user.name=Claim User",
		"-c", "user.email=claim@example.com",
		"commit-tree", claimEmptyTreeSHA,
		"-m", message,
	))
	mustGit(t, bare, "update-ref", ref, sha)
	return sha
}

// metadataFromMessage decodes the caller-visible fields; non-JSON yields zeros.
func metadataFromMessage(t *testing.T, message string) (string, string, string) {
	t.Helper()
	var decoded struct {
		Cluster    string `json:"cluster"`
		TaskID     string `json:"task_id"`
		LeaseUntil string `json:"lease_until"`
	}
	if err := json.Unmarshal([]byte(message), &decoded); err != nil {
		return "", "", ""
	}
	return decoded.Cluster, decoded.TaskID, decoded.LeaseUntil
}

func assertClaimRefs(t *testing.T, claims []git.ClaimRef, wantRefs ...string) {
	t.Helper()
	got := make([]string, 0, len(claims))
	for _, claim := range claims {
		got = append(got, claim.Ref)
	}
	sort.Strings(got)
	sort.Strings(wantRefs)
	if strings.Join(got, ",") != strings.Join(wantRefs, ",") {
		t.Fatalf("claim refs %v, want %v", got, wantRefs)
	}
}

// assertListMetadataMatchesCommits asserts every listed object's metadata equals
// its commit message.
func assertListMetadataMatchesCommits(t *testing.T, bare string, claims []git.ClaimRef) {
	t.Helper()
	for _, claim := range claims {
		cluster, taskID, leaseUntil := metadataFromMessage(t, commitMessage(t, bare, claim.SHA))
		if claim.Cluster != cluster || claim.TaskID != taskID || claim.LeaseUntil != leaseUntil {
			t.Fatalf("listed metadata %+v does not match commit %s", claim, claim.SHA)
		}
	}
}

func TestClaimListRealRepo(t *testing.T) {
	fixture := newClaimFixture(t)
	t.Run("manual ref appears and disappears without a pull", func(t *testing.T) {
		testClaimManualRefAppearsAndDisappears(t, fixture)
	})
	t.Run("message forms", func(t *testing.T) {
		testClaimMessageForms(t, fixture)
	})
	t.Run("prefix matching by path component", func(t *testing.T) {
		testClaimPrefixMatching(t, fixture)
	})
	t.Run("no claim refs yields a non-nil empty slice", testClaimEmptyList)
}

func testClaimManualRefAppearsAndDisappears(t *testing.T, fixture *claimFixture) {
	t.Helper()
	sha := createClaimCommit(t, fixture.bare, "refs/claims/manual/live",
		`{"cluster":"manual","task_id":"t","lease_until":"`+claimLeaseUntil+`"}`)
	found := false
	for _, claim := range listClaimRefs(t, fixture.clientA, "claims/") {
		if claim.Ref == "refs/claims/manual/live" && claim.SHA == sha {
			found = true
		}
	}
	if !found {
		t.Fatalf("manually created ref was not listed")
	}
	mustGit(t, fixture.bare, "update-ref", "-d", "refs/claims/manual/live")
	for _, claim := range listClaimRefs(t, fixture.clientA, "claims/") {
		if claim.Ref == "refs/claims/manual/live" {
			t.Fatalf("deleted ref is still listed")
		}
	}
}

func testClaimMessageForms(t *testing.T, fixture *claimFixture) {
	t.Helper()
	createClaimCommit(t, fixture.bare, "refs/claims/meta/four",
		`{"cluster":"c1","task_id":"t1","lease_until":"`+claimLeaseUntil+`"}`)
	createClaimCommit(t, fixture.bare, "refs/claims/meta/extra",
		`{"cluster":"c2","task_id":"t2","lease_until":"`+claimLeaseUntil+`","extra":"x"}`)
	createClaimCommit(t, fixture.bare, "refs/claims/meta/legacy",
		`{"cluster":"c3","task_id":"t3","lease_until":"`+claimLeaseUntil+`"}`)
	createClaimCommit(t, fixture.bare, "refs/claims/meta/raw", "not json at all")
	claims := listClaimRefs(t, fixture.clientA, "claims/")
	if len(claims) != 4 {
		t.Fatalf("expected 4 claims, got %d", len(claims))
	}
	byRef := map[string]git.ClaimRef{}
	for _, claim := range claims {
		byRef[claim.Ref] = claim
	}
	if got := byRef["refs/claims/meta/four"]; got.Cluster != "c1" || got.TaskID != "t1" {
		t.Fatalf("four-key form: %+v", got)
	}
	if got := byRef["refs/claims/meta/extra"]; got.Cluster != "c2" || got.TaskID != "t2" {
		t.Fatalf("extra-key form: %+v", got)
	}
	if got := byRef["refs/claims/meta/legacy"]; got.Cluster != "c3" {
		t.Fatalf("legacy form: %+v", got)
	}
	if got := byRef["refs/claims/meta/raw"]; got.Cluster != "" || got.TaskID != "" ||
		got.LeaseUntil != "" {
		t.Fatalf("non-JSON form: %+v", got)
	}
	assertListMetadataMatchesCommits(t, fixture.bare, claims)
}

func testClaimPrefixMatching(t *testing.T, fixture *claimFixture) {
	t.Helper()
	for _, ref := range []string{
		"refs/claims/task/1",
		"refs/claims/task/10",
		"refs/claims/tasks/1",
		"refs/claims/repo/a/b",
	} {
		createClaimCommit(t, fixture.bare, ref,
			`{"cluster":"prefix","task_id":"t","lease_until":"`+claimLeaseUntil+`"}`)
	}
	assertClaimRefs(t, listClaimRefs(t, fixture.clientA, "claims/task/1"),
		"refs/claims/task/1")
	assertClaimRefs(t, listClaimRefs(t, fixture.clientA, "claims/task"),
		"refs/claims/task/1", "refs/claims/task/10")
	assertClaimRefs(t, listClaimRefs(t, fixture.clientA, "claims/repo/a"),
		"refs/claims/repo/a/b")
	assertListMetadataMatchesCommits(t, fixture.bare,
		listClaimRefs(t, fixture.clientA, "claims/"))
}

func testClaimEmptyList(t *testing.T) {
	t.Helper()
	empty := newClaimFixture(t)
	claims := listClaimRefs(t, empty.clientA, "claims/")
	if claims == nil || len(claims) != 0 {
		t.Fatalf("expected non-nil empty slice, got %#v", claims)
	}
}

func TestClaimServedBranchUntouched(t *testing.T) {
	fixture := newClaimFixture(t)
	ctx := context.Background()
	beforeHead := originRef(t, fixture.bare, "refs/heads/main")
	beforeRefs := mustGit(t, fixture.bare, "for-each-ref", "refs/heads", "refs/tags")

	key := "claims/served/branch"
	s0, err := fixture.clientA.PutClaimRef(ctx, key, git.ClaimRefRequest{Cluster: "one"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s1, err := fixture.clientA.PutClaimRef(
		ctx,
		key,
		git.ClaimRefRequest{ExpectedSHA: s0, Cluster: "two"},
	)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if _, err := fixture.clientA.PutClaimRef(
		ctx,
		key,
		git.ClaimRefRequest{ExpectedSHA: s0, Cluster: "stale"},
	); !errors.Is(err, git.ErrRefConflict) {
		t.Fatalf("stale renew: expected ErrRefConflict, got %v", err)
	}
	if err := fixture.clientA.DeleteClaimRef(ctx, key, s1); err != nil {
		t.Fatalf("delete: %v", err)
	}
	assertInvalidClaimNames(ctx, t)

	if got := originRef(t, fixture.bare, "refs/heads/main"); got != beforeHead {
		t.Fatalf("served branch moved from %s to %s", beforeHead, got)
	}
	if got := mustGit(t, fixture.bare, "for-each-ref", "refs/heads", "refs/tags"); got !=
		beforeRefs {
		t.Fatalf("refs changed:\nbefore:\n%s\nafter:\n%s", beforeRefs, got)
	}
}

func assertInvalidClaimNames(ctx context.Context, t *testing.T) {
	t.Helper()
	for _, name := range invalidClaimNames {
		if err := git.ValidateClaimKey(ctx, name); !errors.Is(err, git.ErrInvalidRefName) {
			t.Fatalf("ValidateClaimKey(%q): expected ErrInvalidRefName, got %v", name, err)
		}
	}
}

func TestClaimSyncStateUnchanged(t *testing.T) {
	fixture := newClaimFixture(t)
	ctx := context.Background()
	refspecA := mustGit(t, fixture.cloneA, "config", "--get-all", "remote.origin.fetch")
	refspecB := mustGit(t, fixture.cloneB, "config", "--get-all", "remote.origin.fetch")
	remotesA := mustGit(t, fixture.cloneA, "for-each-ref", "refs/remotes/origin")
	remotesB := mustGit(t, fixture.cloneB, "for-each-ref", "refs/remotes/origin")

	listClaimRefs(t, fixture.clientA, "claims/")
	listClaimRefs(t, fixture.clientB, "claims/")
	sha, err := fixture.clientA.PutClaimRef(
		ctx,
		"claims/sync/state",
		git.ClaimRefRequest{Cluster: "one"},
	)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := fixture.clientB.PutClaimRef(
		ctx,
		"claims/sync/state",
		git.ClaimRefRequest{ExpectedSHA: sha, Cluster: "two"},
	); err != nil {
		t.Fatalf("renew: %v", err)
	}

	assertUnchanged(t, "clone A fetch refspec", refspecA,
		mustGit(t, fixture.cloneA, "config", "--get-all", "remote.origin.fetch"))
	assertUnchanged(t, "clone B fetch refspec", refspecB,
		mustGit(t, fixture.cloneB, "config", "--get-all", "remote.origin.fetch"))
	assertUnchanged(t, "clone A remote refs", remotesA,
		mustGit(t, fixture.cloneA, "for-each-ref", "refs/remotes/origin"))
	assertUnchanged(t, "clone B remote refs", remotesB,
		mustGit(t, fixture.cloneB, "for-each-ref", "refs/remotes/origin"))
}

func assertUnchanged(t *testing.T, name, before, after string) {
	t.Helper()
	if before != after {
		t.Fatalf("%s changed:\nbefore:\n%s\nafter:\n%s", name, before, after)
	}
}

func TestClaimInvalidKeyRejected(t *testing.T) {
	fixture := newClaimFixture(t)
	ctx := context.Background()
	before := mustGit(t, fixture.bare, "for-each-ref")
	someSHA := strings.Repeat("a", 40)
	for _, key := range invalidClaimNames {
		if _, err := fixture.clientA.PutClaimRef(
			ctx,
			key,
			git.ClaimRefRequest{Cluster: "c"},
		); !errors.Is(err, git.ErrInvalidRefName) {
			t.Fatalf("PutClaimRef(%q): expected ErrInvalidRefName, got %v", key, err)
		}
		if err := fixture.clientA.DeleteClaimRef(ctx, key, someSHA); !errors.Is(
			err,
			git.ErrInvalidRefName,
		) {
			t.Fatalf("DeleteClaimRef(%q): expected ErrInvalidRefName, got %v", key, err)
		}
	}
	if after := mustGit(t, fixture.bare, "for-each-ref"); after != before {
		t.Fatalf("invalid keys changed the origin:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestClaimNoRemote(t *testing.T) {
	dir := t.TempDir()
	mustGit(t, dir, "init")
	configureClaimIdentity(t, dir)
	client := newClaimClient(dir)
	ctx := context.Background()
	before := mustGit(t, dir, "count-objects", "-v")

	if _, err := client.ListClaimRefs(ctx, "claims/"); !errors.Is(err, git.ErrNoRemote) {
		t.Fatalf("ListClaimRefs: expected ErrNoRemote, got %v", err)
	}
	if _, err := client.PutClaimRef(
		ctx,
		"claims/no/remote",
		git.ClaimRefRequest{Cluster: "c"},
	); !errors.Is(err, git.ErrNoRemote) {
		t.Fatalf("PutClaimRef: expected ErrNoRemote, got %v", err)
	}
	if err := client.DeleteClaimRef(ctx, "claims/no/remote", strings.Repeat("a", 40)); !errors.Is(
		err,
		git.ErrNoRemote,
	) {
		t.Fatalf("DeleteClaimRef: expected ErrNoRemote, got %v", err)
	}
	if after := mustGit(t, dir, "count-objects", "-v"); after != before {
		t.Fatalf("PutClaimRef wrote to a remote-less repo:\nbefore:\n%s\nafter:\n%s",
			before, after)
	}
}

// newClaimClientWithMetrics builds a claim client that records observations in
// fake, so the tests can assert which git operations were instrumented.
func newClaimClientWithMetrics(repoPath string, fake *mocks.FakeMetrics) git.Git {
	return git.New(repoPath, fake, libtime.NewCurrentDateTime(), "", nil)
}

// countObservedOperation returns how many times fake recorded an observation for
// operation.
func countObservedOperation(fake *mocks.FakeMetrics, operation string) int {
	count := 0
	for i := 0; i < fake.ObserveGitOperationCallCount(); i++ {
		op, _ := fake.ObserveGitOperationArgsForCall(i)
		if op == operation {
			count++
		}
	}
	return count
}

func TestClaimPutObservesPush(t *testing.T) {
	fixture := newClaimFixture(t)
	fake := &mocks.FakeMetrics{}
	client := newClaimClientWithMetrics(fixture.cloneA, fake)
	ctx := context.Background()

	if _, err := client.PutClaimRef(ctx, "claims/metrics/put", git.ClaimRefRequest{
		Cluster:    "c",
		TaskID:     "t",
		LeaseUntil: claimLeaseUntil,
	}); err != nil {
		t.Fatalf("PutClaimRef: %v", err)
	}
	if got := countObservedOperation(fake, "push"); got != 1 {
		t.Fatalf("expected exactly one push observation, got %d", got)
	}
	if got := countObservedOperation(fake, "ls-remote"); got != 0 {
		t.Fatalf("claim push must not observe ls-remote, got %d", got)
	}
}

func TestClaimPutFailureObservesPush(t *testing.T) {
	fixture := newClaimFixture(t)
	fake := &mocks.FakeMetrics{}
	client := newClaimClientWithMetrics(fixture.cloneA, fake)
	mustGit(t, fixture.cloneA, "remote", "set-url", "origin",
		filepath.Join(t.TempDir(), "absent.git"))

	if _, err := client.PutClaimRef(context.Background(), "claims/metrics/fail",
		git.ClaimRefRequest{Cluster: "c"}); err == nil {
		t.Fatal("PutClaimRef: expected an error against an absent origin")
	}
	if got := countObservedOperation(fake, "push"); got != 1 {
		t.Fatalf("expected exactly one push observation on failure, got %d", got)
	}
}

func TestClaimListObservesLsRemote(t *testing.T) {
	fixture := newClaimFixture(t)
	fake := &mocks.FakeMetrics{}
	client := newClaimClientWithMetrics(fixture.cloneA, fake)

	if _, err := client.ListClaimRefs(context.Background(), "claims/"); err != nil {
		t.Fatalf("ListClaimRefs: %v", err)
	}
	if got := countObservedOperation(fake, "ls-remote"); got != 1 {
		t.Fatalf("expected exactly one ls-remote observation, got %d", got)
	}
}

func TestClaimListFailureObservesLsRemote(t *testing.T) {
	fixture := newClaimFixture(t)
	fake := &mocks.FakeMetrics{}
	client := newClaimClientWithMetrics(fixture.cloneA, fake)
	mustGit(t, fixture.cloneA, "remote", "set-url", "origin",
		filepath.Join(t.TempDir(), "absent.git"))

	if _, err := client.ListClaimRefs(context.Background(), "claims/"); err == nil {
		t.Fatal("ListClaimRefs: expected an error against an absent origin")
	}
	if got := countObservedOperation(fake, "ls-remote"); got != 1 {
		t.Fatalf("expected exactly one ls-remote observation on failure, got %d", got)
	}
}

func TestClaimListNoRemoteObservesNothing(t *testing.T) {
	fixture := newClaimFixture(t)
	fake := &mocks.FakeMetrics{}
	client := newClaimClientWithMetrics(fixture.cloneA, fake)
	mustGit(t, fixture.cloneA, "remote", "remove", "origin")

	if _, err := client.ListClaimRefs(context.Background(), "claims/"); !errors.Is(
		err,
		git.ErrNoRemote,
	) {
		t.Fatalf("ListClaimRefs: expected ErrNoRemote, got %v", err)
	}
	if got := countObservedOperation(fake, "ls-remote"); got != 0 {
		t.Fatalf("no-remote guard must not observe ls-remote, got %d", got)
	}
}
