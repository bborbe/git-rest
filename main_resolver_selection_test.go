// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"reflect"
	"testing"

	"github.com/bborbe/git-rest/pkg/metrics"
)

func TestSelectResolver_Default_Uses_MarkerResolver(t *testing.T) {
	app := &application{Repo: "/tmp/repo-default", VaultWrite: false}
	r := app.selectResolver(metrics.NewMetrics())
	got := reflect.TypeOf(r).String()
	if got != "*git.markerResolver" {
		t.Fatalf("expected *git.markerResolver, got %s", got)
	}
}

func TestSelectResolver_VaultWrite_Uses_YAMLMergeResolver(t *testing.T) {
	app := &application{Repo: "/tmp/repo-vault", VaultWrite: true}
	r := app.selectResolver(metrics.NewMetrics())
	got := reflect.TypeOf(r).String()
	if got != "*git.yamlMergeResolver" {
		t.Fatalf("expected *git.yamlMergeResolver, got %s", got)
	}
}
