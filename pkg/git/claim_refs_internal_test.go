// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestClaimRefBatchParse(t *testing.T) {
	shaA := strings.Repeat("a", 40)
	shaB := strings.Repeat("b", 40)
	contentA := "tree deadbeef\nauthor A\n\n{\"cluster\":\"one\"}"
	contentB := "tree deadbeef\n\n{\"cluster\":\"two\"}"
	noSeparator := "not json"
	tests := []struct {
		name    string
		out     string
		shas    []string
		want    map[string][]byte
		wantErr bool
	}{
		{
			name: "two valid commit objects",
			out: fmt.Sprintf(
				"%s commit %d\n%s\n%s commit %d\n%s\n",
				shaA, len(contentA), contentA,
				shaB, len(contentB), contentB,
			),
			shas: []string{shaA, shaB},
			want: map[string][]byte{
				shaA: []byte(`{"cluster":"one"}`),
				shaB: []byte(`{"cluster":"two"}`),
			},
		},
		{
			name:    "missing object",
			out:     shaA + " missing\n",
			shas:    []string{shaA},
			wantErr: true,
		},
		{
			name:    "truncated object",
			out:     shaA + " commit 100\nshort\n",
			shas:    []string{shaA},
			wantErr: true,
		},
		{
			name: "commit with no message separator",
			out: fmt.Sprintf(
				"%s commit %d\n%s\n",
				shaA, len(noSeparator), noSeparator,
			),
			shas: []string{shaA},
			want: map[string][]byte{shaA: []byte(noSeparator)},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseBatchCommitMessages([]byte(test.out), test.shas)
			if test.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseBatchCommitMessages: %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("got %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestIsLeaseConflict(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{
			name:   "stale info",
			output: " ! [rejected]        abc -> x (stale info)\nerror: failed to push some refs",
			want:   true,
		},
		{
			name:   "remote ref does not exist",
			output: "error: remote ref does not exist",
			want:   true,
		},
		{
			name:   "cannot lock ref",
			output: "error: cannot lock ref 'refs/claims/x': is at abc",
			want:   true,
		},
		{
			name:   "remote rejected failed to update ref",
			output: " ! [remote rejected] abc -> x (failed to update ref)",
			want:   true,
		},
		{
			name:   "remote rejected reference already exists",
			output: " ! [remote rejected] abc -> x (reference already exists)",
			want:   true,
		},
		{
			name:   "remote rejected reference does not exist",
			output: " ! [remote rejected] abc -> x (reference does not exist)",
			want:   true,
		},
		{
			name:   "remote rejected expected reason",
			output: " ! [remote rejected] abc -> x (expected old value)",
			want:   true,
		},
		{
			name:   "remote rejected incorrect old value provided",
			output: " ! [remote rejected] abc -> x (incorrect old value provided)",
			want:   true,
		},
		{
			name: "hook decline mentioning incorrect old value is not a conflict",
			output: "remote: incorrect old value provided\n" +
				" ! [remote rejected] abc -> x (pre-receive hook declined)",
			want: false,
		},
		{
			name: "hook decline mentioning expected is not a conflict",
			output: "remote: expected commit message\n" +
				" ! [remote rejected] abc -> x (pre-receive hook declined)",
			want: false,
		},
		{
			name:   "transport failure",
			output: "fatal: unable to access 'https://example.com/x.git': Could not resolve host",
			want:   false,
		},
		{
			name:   "nothing to push",
			output: "Everything up-to-date",
			want:   false,
		},
		{
			name:   "empty output",
			output: "",
			want:   false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isLeaseConflict(test.output); got != test.want {
				t.Fatalf("isLeaseConflict(%q) = %v, want %v", test.output, got, test.want)
			}
		})
	}
}
