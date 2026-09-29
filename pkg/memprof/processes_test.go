// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package memprof_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/git-rest/pkg/memprof"
)

var _ = Describe("Processes", func() {
	var root string

	// writeProcess lays out a procfs-shaped pid directory.
	writeProcess := func(pid, status, cmdline string) {
		dir := filepath.Join(root, pid)
		Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o600)).To(Succeed())
		if cmdline != "" {
			Expect(
				os.WriteFile(filepath.Join(dir, "cmdline"), []byte(cmdline), 0o600),
			).To(Succeed())
		}
	}

	BeforeEach(func() {
		var err error
		root, err = os.MkdirTemp("", "memprof-proc")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = os.RemoveAll(root) })
	})

	It("orders processes by resident memory, largest first", func() {
		writeProcess("10", "Name:\tgit\nVmRSS:\t  4096 kB\nVmHWM:\t  8192 kB\n", "git\x00push\x00")
		writeProcess(
			"20",
			"Name:\tgit-rest\nVmRSS:\t   512 kB\nVmHWM:\t  1024 kB\n",
			"git-rest\x00",
		)

		processes := memprof.TopProcessesInForTest(root, 10)

		Expect(processes).To(HaveLen(2))
		Expect(processes[0].PID).To(Equal(10))
		Expect(processes[0].RSSBytes).To(Equal(uint64(4096 * 1024)))
		Expect(processes[0].PeakBytes).To(Equal(uint64(8192 * 1024)))
		Expect(processes[1].PID).To(Equal(20))
	})

	It("keeps the peak separate from the current figure", func() {
		// A process that spiked and settled is visible only in the high-water
		// mark, which is what makes a short-lived git invocation catchable.
		writeProcess("10", "Name:\tgit\nVmRSS:\t   100 kB\nVmHWM:\t400000 kB\n", "git\x00push\x00")

		processes := memprof.TopProcessesInForTest(root, 10)

		Expect(processes).To(HaveLen(1))
		Expect(processes[0].RSSBytes).To(Equal(uint64(100 * 1024)))
		Expect(processes[0].PeakBytes).To(Equal(uint64(400000 * 1024)))
	})

	It("names the process by its first two argv fields", func() {
		writeProcess(
			"10",
			"Name:\tgit\nVmRSS:\t  1024 kB\nVmHWM:\t  1024 kB\n",
			"git\x00-C\x00/data\x00push\x00origin\x00master\x00",
		)

		processes := memprof.TopProcessesInForTest(root, 10)

		Expect(processes).To(HaveLen(1))
		Expect(processes[0].Command).To(Equal("git -C"))
	})

	It("falls back to the process name when argv is absent", func() {
		writeProcess("10", "Name:\tgit\nVmRSS:\t  1024 kB\nVmHWM:\t  1024 kB\n", "")

		processes := memprof.TopProcessesInForTest(root, 10)

		Expect(processes).To(HaveLen(1))
		Expect(processes[0].Command).To(Equal("git"))
	})

	It("falls back to the process name when argv is only NUL bytes", func() {
		writeProcess("10", "Name:\tgit\nVmRSS:\t  1024 kB\nVmHWM:\t  1024 kB\n", "\x00\x00")

		processes := memprof.TopProcessesInForTest(root, 10)

		Expect(processes).To(HaveLen(1))
		Expect(processes[0].Command).To(Equal("git"))
	})

	It("skips entries that are not pid directories", func() {
		writeProcess("10", "Name:\tgit\nVmRSS:\t  1024 kB\nVmHWM:\t  1024 kB\n", "git\x00")
		Expect(os.MkdirAll(filepath.Join(root, "sys"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(root, "uptime"), []byte("1"), 0o600)).To(Succeed())

		processes := memprof.TopProcessesInForTest(root, 10)

		Expect(processes).To(HaveLen(1))
		Expect(processes[0].PID).To(Equal(10))
	})

	It("skips a pid directory whose status is unreadable", func() {
		Expect(os.MkdirAll(filepath.Join(root, "10"), 0o755)).To(Succeed())

		Expect(memprof.TopProcessesInForTest(root, 10)).To(BeEmpty())
	})

	It("skips a kernel thread that reports no memory", func() {
		writeProcess("10", "Name:\tkthreadd\n", "")

		Expect(memprof.TopProcessesInForTest(root, 10)).To(BeEmpty())
	})

	It("reports a malformed memory line as zero rather than dropping the process", func() {
		writeProcess("10", "Name:\tgit\nVmRSS:\tnot-a-number kB\nVmHWM:\t  1024 kB\n", "git\x00")

		processes := memprof.TopProcessesInForTest(root, 10)

		Expect(processes).To(HaveLen(1))
		Expect(processes[0].RSSBytes).To(BeZero())
		Expect(processes[0].PeakBytes).To(Equal(uint64(1024 * 1024)))
	})

	It("treats an empty memory value as zero rather than dropping the process", func() {
		writeProcess("10", "Name:\tgit\nVmRSS:\t\nVmHWM:\t  1024 kB\n", "git\x00")

		processes := memprof.TopProcessesInForTest(root, 10)

		Expect(processes).To(HaveLen(1))
		Expect(processes[0].RSSBytes).To(BeZero())
		Expect(processes[0].PeakBytes).To(Equal(uint64(1024 * 1024)))
	})

	It("truncates the list to n", func() {
		writeProcess("10", "Name:\tgit\nVmRSS:\t  4096 kB\nVmHWM:\t  4096 kB\n", "git\x00")
		writeProcess("20", "Name:\tgit\nVmRSS:\t  2048 kB\nVmHWM:\t  2048 kB\n", "git\x00")
		writeProcess("30", "Name:\tgit\nVmRSS:\t  1024 kB\nVmHWM:\t  1024 kB\n", "git\x00")

		Expect(memprof.TopProcessesInForTest(root, 2)).To(HaveLen(2))
	})

	It("returns nothing for a non-positive n", func() {
		writeProcess("10", "Name:\tgit\nVmRSS:\t  1024 kB\nVmHWM:\t  1024 kB\n", "git\x00")

		Expect(memprof.TopProcessesInForTest(root, 0)).To(BeEmpty())
		Expect(memprof.TopProcessesInForTest(root, -1)).To(BeEmpty())
	})

	It("returns nothing when the procfs root does not exist", func() {
		Expect(memprof.TopProcessesInForTest(filepath.Join(root, "missing"), 10)).To(BeEmpty())
	})
})
