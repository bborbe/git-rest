// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"reflect"
	"testing"

	"github.com/bborbe/git-rest/pkg/git"
	"github.com/bborbe/git-rest/pkg/metrics"
)

// Type references resolved at runtime so a rename of the unexported struct on
// either side flips both reference and assertion together — no hardcoded type-name
// string that silently breaks on rename.
var (
	markerResolverType = reflect.TypeOf(git.NewMarkerResolver("/tmp/refresolver"))
	yamlResolverType   = reflect.TypeOf(
		git.NewYAMLMergeResolver("/tmp/refresolver", metrics.NewMetrics()),
	)
)

func TestSelectResolver_Default_Uses_MarkerResolver(t *testing.T) {
	app := &application{Repo: "/tmp/repo-default", VaultWrite: false}
	r := app.selectResolver(metrics.NewMetrics())
	if got := reflect.TypeOf(r); got != markerResolverType {
		t.Fatalf("expected %s, got %s", markerResolverType, got)
	}
}

func TestSelectResolver_VaultWrite_Uses_YAMLMergeResolver(t *testing.T) {
	app := &application{Repo: "/tmp/repo-vault", VaultWrite: true}
	r := app.selectResolver(metrics.NewMetrics())
	if got := reflect.TypeOf(r); got != yamlResolverType {
		t.Fatalf("expected %s, got %s", yamlResolverType, got)
	}
}
