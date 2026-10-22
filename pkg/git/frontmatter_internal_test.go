// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git

import (
	"testing"
)

func TestHasFrontmatter(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name:    "LF frontmatter",
			content: "---\ntitle: t\n---\nbody\n",
			want:    true,
		},
		{
			name:    "CRLF frontmatter",
			content: "---\r\ntitle: t\r\n---\r\nbody\r\n",
			want:    true,
		},
		{
			name:    "empty body",
			content: "",
			want:    false,
		},
		{
			name:    "bare error string",
			content: "error: something went wrong",
			want:    false,
		},
		{
			name:    "leading blank line",
			content: "\n---\ntitle: t\n---\nbody\n",
			want:    false,
		},
		{
			name:    "opening delimiter without closing line",
			content: "---\ntitle: t\nbody\n",
			want:    false,
		},
		{
			name:    "terminated with ellipsis instead of delimiter",
			content: "---\ntitle: t\n...\nbody\n",
			want:    false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := hasFrontmatter([]byte(test.content)); got != test.want {
				t.Fatalf("hasFrontmatter(%q) = %v, want %v", test.content, got, test.want)
			}
		})
	}
}

func TestIsTaskFilePath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{
			name: "tasks directory",
			path: "tasks/x.md",
			want: true,
		},
		{
			name: "25 Tasks directory",
			path: "25 Tasks/x.md",
			want: true,
		},
		{
			name: "repository root",
			path: "README.md",
			want: false,
		},
		{
			name: "archive directory with matching prefix",
			path: "25 Tasks Archive/x.md",
			want: false,
		},
		{
			name: "deeper nesting",
			path: "tasks/sub/x.md",
			want: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isTaskFilePath(test.path); got != test.want {
				t.Fatalf("isTaskFilePath(%q) = %v, want %v", test.path, got, test.want)
			}
		})
	}
}
