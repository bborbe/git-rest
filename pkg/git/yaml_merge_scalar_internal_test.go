// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git

import (
	"reflect"
	"testing"
)

func TestParseScalarEntry(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		wantOK   bool
		wantKey  string
		wantLine string
	}{
		{name: "integer", line: "a: 1", wantOK: true, wantKey: "a", wantLine: "a: 1"},
		{name: "null value", line: "key:", wantOK: true, wantKey: "key", wantLine: "key:"},
		{
			name:     "trailing comment",
			line:     "a: 1 # note",
			wantOK:   true,
			wantKey:  "a",
			wantLine: "a: 1 # note",
		},
		{name: "hex value", line: "k: 0x10", wantOK: true, wantKey: "k", wantLine: "k: 0x10"},
		{name: "quoted value", line: `a: "3"`, wantOK: true, wantKey: "a", wantLine: `a: "3"`},
		{name: "crlf", line: "a: 1\r", wantOK: true, wantKey: "a", wantLine: "a: 1"},
		{name: "empty", line: "", wantOK: false},
		{name: "comment", line: "# comment", wantOK: false},
		{name: "indented", line: "  a: 1", wantOK: false},
		{name: "sequence item", line: "- a", wantOK: false},
		{name: "bare dash", line: "-", wantOK: false},
		{name: "parse error", line: "a: b: c", wantOK: false},
		{name: "anchored key", line: "&x a: 1", wantOK: false},
		{name: "undefined alias", line: "key: *alias", wantOK: false},
		{name: "flow sequence", line: "tags: [a, b]", wantOK: false},
		{name: "flow mapping", line: "meta: {a: b}", wantOK: false},
		{name: "literal block", line: "desc: |", wantOK: false},
		{name: "folded block", line: "desc: >", wantOK: false},
		{name: "anchored value", line: "key: &x 1", wantOK: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			key, entry, ok := parseScalarEntry(test.line)
			if ok != test.wantOK {
				t.Fatalf("parseScalarEntry(%q) ok = %v, want %v", test.line, ok, test.wantOK)
			}
			if !test.wantOK {
				return
			}
			if key != test.wantKey {
				t.Fatalf("parseScalarEntry(%q) key = %q, want %q", test.line, key, test.wantKey)
			}
			if entry.line != test.wantLine {
				t.Fatalf(
					"parseScalarEntry(%q) line = %q, want %q",
					test.line,
					entry.line,
					test.wantLine,
				)
			}
			if entry.value == nil {
				t.Fatalf("parseScalarEntry(%q) value is nil", test.line)
			}
		})
	}
}

type conflictRegionCase struct {
	name string
	line []string
	want []conflictRegion
	ok   bool
}

var conflictRegionCases = []conflictRegionCase{
	{
		name: "single region",
		line: []string{
			"---",
			"<<<<<<< HEAD",
			"a: 1",
			"=======",
			"a: 2",
			">>>>>>> theirs",
			"---",
		},
		want: []conflictRegion{
			{
				startLine:   1,
				oursLines:   []string{"a: 1"},
				theirsLines: []string{"a: 2"},
				endLine:     5,
			},
		},
		ok: true,
	},
	{
		name: "two regions",
		line: []string{
			"<<<<<<< HEAD",
			"a: 1",
			"=======",
			"a: 2",
			">>>>>>> theirs",
			"mid",
			"<<<<<<< HEAD",
			"b: 1",
			"=======",
			"b: 2",
			">>>>>>> theirs",
		},
		want: []conflictRegion{
			{
				startLine:   0,
				oursLines:   []string{"a: 1"},
				theirsLines: []string{"a: 2"},
				endLine:     4,
			},
			{
				startLine:   6,
				oursLines:   []string{"b: 1"},
				theirsLines: []string{"b: 2"},
				endLine:     10,
			},
		},
		ok: true,
	},
	{
		name: "nested open",
		line: []string{
			"<<<<<<< HEAD",
			"a: 1",
			"<<<<<<< HEAD",
			"b: 1",
			"=======",
			"b: 2",
			">>>>>>> theirs",
		},
		ok: false,
	},
	{
		name: "separator outside region",
		line: []string{
			"=======",
			"<<<<<<< HEAD",
			"a: 1",
			"=======",
			"a: 2",
			">>>>>>> theirs",
		},
		ok: false,
	},
	{
		name: "closer outside region",
		line: []string{
			">>>>>>> theirs",
			"<<<<<<< HEAD",
			"a: 1",
			"=======",
			"a: 2",
			">>>>>>> theirs",
		},
		ok: false,
	},
	{
		name: "region without separator",
		line: []string{
			"<<<<<<< HEAD",
			"a: 1",
			">>>>>>> theirs",
		},
		ok: false,
	},
	{
		name: "open at eof",
		line: []string{
			"<<<<<<< HEAD",
			"a: 1",
			"=======",
			"a: 2",
		},
		ok: false,
	},
	{
		name: "no region",
		line: []string{
			"---",
			"a: 1",
			"---",
		},
		ok: false,
	},
}

func TestEnumerateConflictRegions(t *testing.T) {
	for _, test := range conflictRegionCases {
		t.Run(test.name, func(t *testing.T) {
			got, ok := enumerateConflictRegions(test.line)
			if ok != test.ok {
				t.Fatalf("enumerateConflictRegions ok = %v, want %v", ok, test.ok)
			}
			if !test.ok {
				return
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("enumerateConflictRegions = %#v, want %#v", got, test.want)
			}
		})
	}
}
