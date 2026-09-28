// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package memprof

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

//counterfeiter:generate -o ../../mocks/reporter.go --fake-name FakeReporter . Reporter

// Reporter periodically inspects the process's memory and, once it crosses a
// threshold, logs where that memory is going.
//
// It exists because a container that is OOMKilled at its cgroup limit is
// otherwise opaque: the process dies without saying what grew, and the runtime
// metrics on /metrics say only *how much*, never *which code*. A heap profile
// would answer that, but only if something can reach the debug endpoint — which
// is exactly what is unavailable when the pod is crash-looping behind a
// cluster-internal Service. This reporter writes the same information to the
// service log, which is readable with nothing but `kubectl logs`.
type Reporter interface {
	Run(ctx context.Context) error
}

// New returns a Reporter that ticks every interval and, when the Go heap in use
// or the container's cgroup usage reaches thresholdBytes, logs a breakdown.
//
// Both signals are checked because they fail independently: the cgroup figure
// includes page cache, so a container can sit at its limit with a tiny heap,
// while a runaway allocation can exceed the threshold long before the cgroup
// notices. Reporting only one would miss the other's failure mode.
//
// topN bounds how many allocation sites are logged per report, largest first.
func New(interval time.Duration, thresholdBytes uint64, topN int) Reporter {
	return &reporter{
		interval:       interval,
		thresholdBytes: thresholdBytes,
		topN:           topN,
	}
}

type reporter struct {
	interval       time.Duration
	thresholdBytes uint64
	topN           int
}

// Run ticks until ctx is cancelled, reporting on each tick that crosses the
// threshold. A tick below the threshold is silent — the reporter must not
// generate steady-state log volume on a healthy process.
func (r *reporter) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			r.report(ctx)
		}
	}
}

func (r *reporter) report(ctx context.Context) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	cg := readCgroupMemory()

	if max(ms.HeapInuse, cg.currentBytes) < r.thresholdBytes {
		return
	}

	slog.WarnContext(
		ctx,
		"git-rest: memory threshold exceeded",
		"threshold_bytes", r.thresholdBytes,
		"heap_inuse_bytes", ms.HeapInuse,
		"heap_alloc_bytes", ms.HeapAlloc,
		"heap_sys_bytes", ms.HeapSys,
		"heap_objects", ms.HeapObjects,
		"stack_inuse_bytes", ms.StackInuse,
		"num_gc", ms.NumGC,
		"goroutines", runtime.NumGoroutine(),
		"cgroup_current_bytes", cg.currentBytes,
		"cgroup_max_bytes", cg.maxBytes,
		"cgroup_anon_bytes", cg.anonBytes,
		"cgroup_file_bytes", cg.fileBytes,
		"cgroup_inactive_file_bytes", cg.inactiveFileBytes,
	)

	for _, site := range topSites(r.topN) {
		slog.WarnContext(
			ctx,
			"git-rest: memory allocation site",
			"function", site.function,
			"file", site.file,
			"line", site.line,
			"inuse_bytes", site.inuseBytes,
			"inuse_objects", site.inuseObjects,
		)
	}
}

// allocSite is one attributed allocation, symbolized to a source location.
type allocSite struct {
	function     string
	file         string
	line         int
	inuseBytes   int64
	inuseObjects int64
}

// topSites returns the n allocation sites holding the most live bytes, largest
// first. Sites are attributed to the first non-runtime frame, so the caller sees
// the code that allocated rather than the runtime frame that recorded it.
func topSites(n int) []allocSite {
	if n <= 0 {
		return nil
	}

	// The profile is read twice by design: the first call reports how many
	// records exist, the second fills a correctly sized slice. MemProfile
	// allocates, so a single guessed-size call would either truncate the
	// profile or overshoot the buffer.
	count, _ := runtime.MemProfile(nil, false)
	if count == 0 {
		return nil
	}
	records := make([]runtime.MemProfileRecord, count)
	filled, ok := runtime.MemProfile(records, false)
	if !ok {
		return nil
	}
	records = records[:filled]

	sort.Slice(records, func(i, j int) bool {
		return records[i].InUseBytes() > records[j].InUseBytes()
	})

	sites := make([]allocSite, 0, n)
	for _, record := range records {
		if len(sites) == n {
			break
		}
		if record.InUseBytes() == 0 {
			break // sorted descending: everything after this is zero too
		}
		function, file, line := firstUserFrame(record.Stack())
		sites = append(sites, allocSite{
			function:     function,
			file:         file,
			line:         line,
			inuseBytes:   record.InUseBytes(),
			inuseObjects: record.InUseObjects(),
		})
	}
	return sites
}

// firstUserFrame walks the stack outward until it leaves the runtime, returning
// the first frame in application code.
//
// A stack made entirely of runtime frames — which does occur, for allocations
// the runtime makes on its own behalf — falls back to the innermost frame. That
// frame is less informative than a user frame, but it still names the allocation
// site; returning nothing would log an anonymous entry, and a diagnostic that
// silently drops the largest allocation is worse than one that names it poorly.
func firstUserFrame(stack []uintptr) (function, file string, line int) {
	frames := runtime.CallersFrames(stack)

	var innermost runtime.Frame
	haveInnermost := false
	for {
		frame, more := frames.Next()
		if !haveInnermost {
			innermost, haveInnermost = frame, true
		}
		if !strings.HasPrefix(frame.Function, "runtime.") {
			return frame.Function, frame.File, frame.Line
		}
		if !more {
			break
		}
	}

	if haveInnermost {
		return innermost.Function, innermost.File, innermost.Line
	}
	return "", "", 0
}

// cgroupMemory is the container's memory accounting as the kernel sees it.
// The fields are deliberately reported separately rather than summed: `anon` is
// process memory that cannot be reclaimed, while `file` is page cache that can —
// and distinguishing them is the difference between "the process is leaking" and
// "the repository is being cached", which need opposite fixes.
type cgroupMemory struct {
	currentBytes      uint64
	maxBytes          uint64
	anonBytes         uint64
	fileBytes         uint64
	inactiveFileBytes uint64
}

const cgroupRoot = "/sys/fs/cgroup"

// readCgroupMemory reads the cgroup v2 memory files. Every field is best-effort:
// a missing or unreadable file yields zero rather than an error, because the
// reporter is diagnostic and must never be the reason a service fails to run.
func readCgroupMemory() cgroupMemory {
	var cg cgroupMemory
	cg.currentBytes = readUintFromFile(filepath.Join(cgroupRoot, "memory.current"))
	cg.maxBytes = readUintFromFile(filepath.Join(cgroupRoot, "memory.max"))

	for key, value := range readKeyValues(filepath.Join(cgroupRoot, "memory.stat")) {
		switch key {
		case "anon":
			cg.anonBytes = value
		case "file":
			cg.fileBytes = value
		case "inactive_file":
			cg.inactiveFileBytes = value
		}
	}
	return cg
}

// readUintFromFile parses a single unsigned integer from path. It returns 0 for
// a missing file, for unparseable content, and for cgroup v2's literal "max"
// (no limit set), which is not representable as a byte count.
func readUintFromFile(path string) uint64 {
	raw, err := os.ReadFile(path) //nolint:gosec // path is a fixed cgroup path, not user input
	if err != nil {
		return 0
	}
	return parseUint(strings.TrimSpace(string(raw)))
}

func parseUint(s string) uint64 {
	value, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	return value
}

// readKeyValues parses cgroup v2's "key value" per-line format. Malformed lines
// are skipped rather than failing the read.
func readKeyValues(path string) map[string]uint64 {
	result := map[string]uint64{}
	raw, err := os.ReadFile(path) //nolint:gosec // path is a fixed cgroup path, not user input
	if err != nil {
		return result
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		result[fields[0]] = parseUint(fields[1])
	}
	return result
}
