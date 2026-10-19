// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package git_test

import (
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/format"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6@v6.12.2 -generate

func TestSuite(t *testing.T) {
	time.Local = time.UTC
	format.TruncatedDiff = false
	RegisterFailHandler(Fail)
	suiteConfig, reporterConfig := GinkgoConfiguration()
	// The suite has grown to 121 real-git specs; at 120s it ran only 63 of them
	// on a local run before the deadline. 300s restores headroom without hiding
	// a genuine hang.
	suiteConfig.Timeout = 300 * time.Second
	RunSpecs(t, "Git Test Suite", suiteConfig, reporterConfig)
}
