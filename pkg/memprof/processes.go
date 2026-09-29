// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package memprof

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// procRoot is the procfs mount point. It is a constant, with the root passed
// explicitly to the readers below, so a test can point at a fixture tree without
// a mutable package-level variable.
const procRoot = "/proc"

// processMemory is one live process in the container, as procfs reports it.
//
// Both figures are kept because they answer different questions. rssBytes is what
// the process holds right now; peakBytes is the high-water mark it has ever
// reached. A process that spiked and settled appears only in the peak, while one
// holding the cgroup at its limit appears in both.
type processMemory struct {
	pid       int
	command   string
	rssBytes  uint64
	peakBytes uint64
}

// topProcesses returns the n live processes holding the most resident memory,
// largest first.
//
// It exists because the cgroup's anonymous memory is not attributable from the Go
// heap alone. A container can sit at its limit with a single-digit-MiB heap — in
// which case the memory belongs to a child process (git, for this service) or to
// memory the heap profile does not cover at all. Naming the processes is what
// turns "anonymous memory grew" into "this command grew", which is the difference
// between a diagnosis and a measurement.
func topProcesses(n int) []processMemory {
	return topProcessesIn(procRoot, n)
}

func topProcessesIn(root string, n int) []processMemory {
	if n <= 0 {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}

	processes := make([]processMemory, 0, len(entries))
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue // not a pid directory
		}
		if process, ok := readProcessMemory(root, pid); ok {
			processes = append(processes, process)
		}
	}

	sort.Slice(processes, func(i, j int) bool {
		return processes[i].rssBytes > processes[j].rssBytes
	})
	if len(processes) > n {
		processes = processes[:n]
	}
	return processes
}

// readProcessMemory reads one process's status. It reports false for a process
// that has already exited (procfs is racy by design), for a kernel thread, and
// for anything whose status cannot be parsed — a diagnostic must degrade to
// silence rather than to a misleading zero entry.
func readProcessMemory(root string, pid int) (processMemory, bool) {
	dir := filepath.Join(root, strconv.Itoa(pid))
	raw, err := os.ReadFile(filepath.Join(dir, "status")) //nolint:gosec // fixed procfs path
	if err != nil {
		return processMemory{}, false
	}

	process := processMemory{pid: pid}
	for line := range strings.SplitSeq(string(raw), "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		switch key {
		case "Name":
			process.command = strings.TrimSpace(value)
		case "VmRSS":
			process.rssBytes = parseKilobytes(value)
		case "VmHWM":
			process.peakBytes = parseKilobytes(value)
		}
	}
	if process.rssBytes == 0 && process.peakBytes == 0 {
		return processMemory{}, false
	}

	process.command = readCommandLine(root, pid, process.command)
	return process, true
}

// parseKilobytes parses procfs's "<number> kB" form, returning bytes. An
// unparseable value yields zero rather than an error: these files are diagnostic
// and must never be the reason a report fails.
func parseKilobytes(value string) uint64 {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return 0
	}
	kilobytes, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0
	}
	return kilobytes * 1024
}

// readCommandLine returns the process's command line, falling back to its short
// name. argv is NUL-separated; only the first two fields are kept so that a git
// invocation is recognisable without logging a full repository path.
func readCommandLine(root string, pid int, fallback string) string {
	path := filepath.Join(root, strconv.Itoa(pid), "cmdline")
	raw, err := os.ReadFile(path) //nolint:gosec // fixed procfs path
	if err != nil {
		return fallback
	}
	args := strings.Fields(strings.ReplaceAll(string(raw), "\x00", " "))
	if len(args) == 0 {
		return fallback
	}
	if len(args) > 2 {
		args = args[:2]
	}
	return strings.Join(args, " ")
}
