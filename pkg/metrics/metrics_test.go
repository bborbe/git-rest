// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package metrics_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bborbe/git-rest/pkg/metrics"
)

// gatherCounter returns the value of an unlabeled counter from the prometheus
// default registry. Returns 0 if the counter is not registered or has not been
// incremented.
func gatherCounter(name string) float64 {
	mfs, err := prometheus.DefaultGatherer.Gather()
	Expect(err).NotTo(HaveOccurred())
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			return m.GetCounter().GetValue()
		}
	}
	return 0
}

// gatherCounterVecLabelValue returns the value of a CounterVec with one label,
// for the matching label value. Returns 0 if not found.
func gatherCounterVecLabelValue(name, labelName, labelValue string) float64 {
	mfs, err := prometheus.DefaultGatherer.Gather()
	Expect(err).NotTo(HaveOccurred())
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == labelName && l.GetValue() == labelValue {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

var _ = Describe("QuarantinedFilesTotal", func() {
	It("is registered with value 0 in the default registry at init() time", func() {
		Expect(gatherCounter("git_rest_quarantined_files_total")).To(Equal(0.0))
	})

	It("increments when IncQuarantinedFiles is called via the interface", func() {
		// Use a fresh metrics implementation to avoid bleeding counter state into
		// other test cases that share the default registry. (The default registry
		// is process-global; the unlabeled counter at init() is the baseline.)
		before := gatherCounter("git_rest_quarantined_files_total")
		m := metrics.NewMetrics()
		m.IncQuarantinedFiles()
		m.IncQuarantinedFiles()
		after := gatherCounter("git_rest_quarantined_files_total")
		Expect(after - before).To(Equal(2.0))
	})
})

var _ = Describe("ResolverFailuresTotal git_mv_failed label", func() {
	It("is pre-initialised to 0 alongside the existing five label values", func() {
		Expect(gatherCounterVecLabelValue(
			"git_rest_resolver_failures_total", "category", "git_mv_failed",
		)).To(Equal(0.0))
	})
})
