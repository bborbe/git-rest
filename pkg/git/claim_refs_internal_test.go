// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git

import (
	"testing"
)

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
