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

// TestProcess is a process with exported fields, so the external test package
// can assert on what topProcessesIn attributed.
type TestProcess struct {
	PID       int
	Command   string
	RSSBytes  uint64
	PeakBytes uint64
}

// TopProcessesInForTest exposes topProcessesIn against an arbitrary procfs root,
// so the parsing can be exercised on a fixture tree rather than on the host's
// live process table.
func TopProcessesInForTest(root string, n int) []TestProcess {
	processes := topProcessesIn(root, n)
	result := make([]TestProcess, 0, len(processes))
	for _, process := range processes {
		result = append(result, TestProcess{
			PID:       process.pid,
			Command:   process.command,
			RSSBytes:  process.rssBytes,
			PeakBytes: process.peakBytes,
		})
	}
	return result
}

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
