// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package memprof

// Exported for testing only. The reporter's value is in its parsing and
// attribution behaviour, neither of which is reachable through Run() without
// driving the process over a real memory threshold.

var (
	ParseUint        = parseUint
	ReadKeyValues    = readKeyValues
	ReadUintFromFile = readUintFromFile
	ReadCgroupMemory = readCgroupMemory
)

// TestSite is an allocation site with exported fields, so the external test
// package can assert on what topSites attributed.
type TestSite struct {
	Function     string
	File         string
	Line         int
	InuseBytes   int64
	InuseObjects int64
}

// TopSitesForTest exposes topSites with its result reshaped for assertions.
func TopSitesForTest(n int) []TestSite {
	sites := topSites(n)
	result := make([]TestSite, 0, len(sites))
	for _, site := range sites {
		result = append(result, TestSite{
			Function:     site.function,
			File:         site.file,
			Line:         site.line,
			InuseBytes:   site.inuseBytes,
			InuseObjects: site.inuseObjects,
		})
	}
	return result
}
