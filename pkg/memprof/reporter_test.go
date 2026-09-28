// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package memprof_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/git-rest/pkg/memprof"
)

var _ = Describe("Reporter", func() {
	Describe("Run", func() {
		It("returns context.Canceled when the context is cancelled", func() {
			ctx, cancel := context.WithCancel(context.Background())
			// Threshold above any real heap so no report is attempted: this
			// spec is about lifecycle, not reporting.
			reporter := memprof.New(time.Hour, 1<<62, 5)

			done := make(chan error, 1)
			go func() { done <- reporter.Run(ctx) }()

			cancel()

			Eventually(done).Should(Receive(MatchError(context.Canceled)))
		})

		It("returns context.DeadlineExceeded when the deadline passes", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()

			reporter := memprof.New(time.Hour, 1<<62, 5)

			Expect(reporter.Run(ctx)).To(MatchError(context.DeadlineExceeded))
		})

		It("survives reporting with a threshold low enough to always fire", func() {
			// A threshold of 1 byte forces the full report path — cgroup read,
			// memstats, symbolisation — on every tick. This is the only spec
			// that exercises report(), and it asserts it neither panics nor
			// returns early.
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()

			reporter := memprof.New(10*time.Millisecond, 1, 3)

			Expect(reporter.Run(ctx)).To(MatchError(context.DeadlineExceeded))
		})
	})

	Describe("TopSitesForTest", func() {
		It("returns nothing when asked for no sites", func() {
			Expect(memprof.TopSitesForTest(0)).To(BeEmpty())
		})

		It("attributes live allocations to non-runtime frames", func() {
			// Held live past the assertion so the sampler cannot have freed it.
			held := make([]byte, 8*1024*1024)
			for i := range held {
				held[i] = byte(i)
			}
			defer runtime.KeepAlive(held)

			sites := memprof.TopSitesForTest(10)

			Expect(sites).ToNot(BeEmpty())

			// Every site must be named — an anonymous entry would make the
			// report useless precisely when it matters.
			attributed := 0
			for _, site := range sites {
				Expect(site.Function).ToNot(BeEmpty())
				if !strings.HasPrefix(site.Function, "runtime.") {
					attributed++
				}
			}
			// At least one site must resolve to application code, which is what
			// proves the runtime-frame skip is doing its job rather than every
			// stack falling through to the innermost-frame fallback.
			Expect(attributed).To(BeNumerically(">", 0))
		})

		It("returns at most the requested number of sites", func() {
			held := make([]byte, 8*1024*1024)
			for i := range held {
				held[i] = byte(i)
			}
			defer runtime.KeepAlive(held)

			Expect(len(memprof.TopSitesForTest(2))).To(BeNumerically("<=", 2))
		})

		It("orders sites largest first", func() {
			held := make([]byte, 8*1024*1024)
			for i := range held {
				held[i] = byte(i)
			}
			defer runtime.KeepAlive(held)

			sites := memprof.TopSitesForTest(10)
			for i := 1; i < len(sites); i++ {
				Expect(sites[i-1].InuseBytes).To(BeNumerically(">=", sites[i].InuseBytes))
			}
		})
	})

	Describe("ParseUint", func() {
		DescribeTable("parses",
			func(input string, expected uint64) {
				Expect(memprof.ParseUint(input)).To(Equal(expected))
			},
			Entry("a number", "1234", uint64(1234)),
			Entry("zero", "0", uint64(0)),
			Entry("cgroup v2's max sentinel", "max", uint64(0)),
			Entry("empty", "", uint64(0)),
			Entry("garbage", "not-a-number", uint64(0)),
			Entry("negative", "-1", uint64(0)),
		)
	})

	Describe("ReadKeyValues", func() {
		var dir string

		BeforeEach(func() { dir = GinkgoT().TempDir() })

		It("parses cgroup v2 key/value lines", func() {
			path := filepath.Join(dir, "memory.stat")
			Expect(os.WriteFile(path, []byte("anon 1234\nfile 5678\n"), 0600)).To(Succeed())

			values := memprof.ReadKeyValues(path)

			Expect(values).To(HaveKeyWithValue("anon", uint64(1234)))
			Expect(values).To(HaveKeyWithValue("file", uint64(5678)))
		})

		It("skips malformed lines without failing the read", func() {
			path := filepath.Join(dir, "memory.stat")
			Expect(
				os.WriteFile(path, []byte("anon 1234\n\nbroken\nfile 5678\n"), 0600),
			).To(Succeed())

			values := memprof.ReadKeyValues(path)

			Expect(values).To(HaveKeyWithValue("anon", uint64(1234)))
			Expect(values).To(HaveKeyWithValue("file", uint64(5678)))
			Expect(values).ToNot(HaveKey("broken"))
		})

		It("returns an empty map for a missing file", func() {
			Expect(memprof.ReadKeyValues(filepath.Join(dir, "absent"))).To(BeEmpty())
		})
	})

	Describe("ReadUintFromFile", func() {
		var dir string

		BeforeEach(func() { dir = GinkgoT().TempDir() })

		It("reads a trailing-newline integer", func() {
			path := filepath.Join(dir, "memory.current")
			Expect(os.WriteFile(path, []byte("4096\n"), 0600)).To(Succeed())

			Expect(memprof.ReadUintFromFile(path)).To(Equal(uint64(4096)))
		})

		It("reports 0 for cgroup v2's unlimited sentinel", func() {
			path := filepath.Join(dir, "memory.max")
			Expect(os.WriteFile(path, []byte("max\n"), 0600)).To(Succeed())

			Expect(memprof.ReadUintFromFile(path)).To(Equal(uint64(0)))
		})

		It("reports 0 for a missing file rather than erroring", func() {
			Expect(memprof.ReadUintFromFile(filepath.Join(dir, "absent"))).To(Equal(uint64(0)))
		})
	})

	Describe("ReadCgroupMemory", func() {
		It("returns without panicking outside a cgroup", func() {
			// On a host without cgroup v2 at the expected path every field reads
			// zero. The contract is that this is a value, never a failure — the
			// reporter must not be the reason a service fails to start.
			Expect(func() { _ = memprof.ReadCgroupMemory() }).ToNot(Panic())
		})
	})
})
