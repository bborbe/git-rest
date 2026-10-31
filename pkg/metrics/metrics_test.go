// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package metrics_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

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

// gatherGauge returns the value of an unlabeled gauge from the prometheus default
// registry, and whether the series was present.
func gatherGauge(name string) (float64, bool) {
	mfs, err := prometheus.DefaultGatherer.Gather()
	Expect(err).NotTo(HaveOccurred())
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			return m.GetGauge().GetValue(), true
		}
	}
	return 0, false
}

var _ = Describe("QuarantinedBacklog", func() {
	It("is pre-initialised to 0 and exposed before any pull refreshes it", func() {
		value, found := gatherGauge("git_rest_quarantined_backlog")
		Expect(found).To(BeTrue(), "the gauge series must exist at init() time")
		Expect(value).To(Equal(0.0))
	})
})

// gatherCounterPresent returns the value of an unlabeled counter from the
// prometheus default registry, and whether the series was present. Mirrors the
// existing gatherGauge helper: gatherCounter returns 0 both for an absent series
// and for one present with value 0, so it cannot prove pre-registration.
func gatherCounterPresent(name string) (float64, bool) {
	mfs, err := prometheus.DefaultGatherer.Gather()
	Expect(err).NotTo(HaveOccurred())
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			return m.GetCounter().GetValue(), true
		}
	}
	return 0, false
}

var _ = Describe("PullRescuesTotal", func() {
	It("is registered with value 0 in the default registry at init() time", func() {
		value, found := gatherCounterPresent("git_rest_pull_rescues_total")
		Expect(found).To(BeTrue(), "the counter series must exist at init() time")
		Expect(value).To(Equal(0.0))
	})

	It("increments when IncPullRescue is called via the interface", func() {
		// Use a fresh metrics implementation to avoid bleeding counter state into
		// other test cases that share the default registry. (The default registry
		// is process-global; the unlabeled counter at init() is the baseline.)
		before := gatherCounter("git_rest_pull_rescues_total")
		m := metrics.NewMetrics()
		m.IncPullRescue()
		after := gatherCounter("git_rest_pull_rescues_total")
		Expect(after - before).To(Equal(1.0))
	})
})

var _ = Describe("AutoresolvedTotal", func() {
	It("is registered with value 0 in the default registry at init() time", func() {
		value, found := gatherCounterPresent("git_rest_autoresolved_total")
		Expect(found).To(BeTrue(), "the counter series must exist at init() time")
		Expect(value).To(Equal(0.0))
	})

	It("increments when IncAutoresolved is called via the interface", func() {
		before := gatherCounter("git_rest_autoresolved_total")
		m := metrics.NewMetrics()
		m.IncAutoresolved()
		after := gatherCounter("git_rest_autoresolved_total")
		Expect(after - before).To(Equal(1.0))
	})
})

var _ = Describe("ResolverFailuresTotal quarantine_io_failed label", func() {
	It("is pre-initialised to 0 alongside the existing five label values", func() {
		Expect(gatherCounterVecLabelValue(
			"git_rest_resolver_failures_total", "category", "quarantine_io_failed",
		)).To(Equal(0.0))
	})
})

var _ = Describe("ResolverFailuresTotal nested_source label", func() {
	It("is pre-initialised to 0 alongside the existing label values", func() {
		Expect(gatherCounterVecLabelValue(
			"git_rest_resolver_failures_total", "category", "nested_source",
		)).To(Equal(0.0))
	})
})

var _ = Describe("RefRequestsTotal", func() {
	It("exposes all thirteen op/result series at 0 before any ref request", func() {
		mfs, err := prometheus.DefaultGatherer.Gather()
		Expect(err).NotTo(HaveOccurred())

		seen := make(map[string]bool)
		count := 0
		for _, mf := range mfs {
			if mf.GetName() != "git_rest_ref_requests_total" {
				continue
			}
			for _, metric := range mf.GetMetric() {
				count++
				labels := make(map[string]string)
				for _, label := range metric.GetLabel() {
					labels[label.GetName()] = label.GetValue()
				}
				key := labels["op"] + "/" + labels["result"]
				seen[key] = true
				Expect(metric.GetCounter().GetValue()).To(Equal(0.0), key)
			}
		}
		Expect(count).To(Equal(13))
		for _, op := range []string{"get", "put", "delete"} {
			for _, result := range []string{"200", "400", "409", "500"} {
				Expect(seen[op+"/"+result]).To(BeTrue(), op+"/"+result)
			}
		}
		Expect(seen["put/413"]).To(BeTrue(), "put/413")
	})
})

// gatherCounterVecLabelPresent reports whether a CounterVec series with the
// given label value is present in the default registry. A value-only lookup
// cannot distinguish "registered at 0" from "not registered", so presence is
// asserted by walking the gathered metrics.
func gatherCounterVecLabelPresent(name, labelName, labelValue string) bool {
	mfs, err := prometheus.DefaultGatherer.Gather()
	Expect(err).NotTo(HaveOccurred())
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == labelName && l.GetValue() == labelValue {
					return true
				}
			}
		}
	}
	return false
}

var _ = Describe("WriteFlushesTotal", func() {
	It("exposes both outcome series at 0 in the default registry at init() time", func() {
		for _, outcome := range []string{"ok", "error"} {
			Expect(
				gatherCounterVecLabelPresent(
					"git_rest_write_flushes_total",
					"outcome",
					outcome,
				),
			).To(BeTrue(), "outcome="+outcome+" must be registered at init() time")
			Expect(gatherCounterVecLabelValue(
				"git_rest_write_flushes_total", "outcome", outcome,
			)).To(Equal(0.0))
		}
	})

	It("increments the matching outcome when IncWriteFlush is called", func() {
		before := gatherCounterVecLabelValue("git_rest_write_flushes_total", "outcome", "ok")
		m := metrics.NewMetrics()
		m.IncWriteFlush("ok")
		after := gatherCounterVecLabelValue("git_rest_write_flushes_total", "outcome", "ok")
		Expect(after - before).To(Equal(1.0))
	})
})

var _ = Describe("WriteCoalescedTotal", func() {
	It("is registered with value 0 in the default registry at init() time", func() {
		value, found := gatherCounterPresent("git_rest_write_coalesced_total")
		Expect(found).To(BeTrue(), "the counter series must exist at init() time")
		Expect(value).To(Equal(0.0))
	})

	It("increments when IncWriteCoalesced is called via the interface", func() {
		before := gatherCounter("git_rest_write_coalesced_total")
		m := metrics.NewMetrics()
		m.IncWriteCoalesced()
		m.IncWriteCoalesced()
		after := gatherCounter("git_rest_write_coalesced_total")
		Expect(after - before).To(Equal(2.0))
	})
})

var _ = Describe("WritePending", func() {
	It("is pre-initialised to 0 and exposed at init() time", func() {
		value, found := gatherGauge("git_rest_write_pending")
		Expect(found).To(BeTrue(), "the gauge series must exist at init() time")
		Expect(value).To(Equal(0.0))
	})

	It("sets the gauge when SetWritePending is called via the interface", func() {
		m := metrics.NewMetrics()
		m.SetWritePending(3)
		value, found := gatherGauge("git_rest_write_pending")
		Expect(found).To(BeTrue())
		Expect(value).To(Equal(3.0))
		// Reset the process-global gauge so later tests see the baseline.
		m.SetWritePending(0)
	})
})

var _ = Describe("PushRetriesTotal", func() {
	It("is registered with value 0 in the default registry at init() time", func() {
		value, found := gatherCounterPresent("git_rest_push_retries_total")
		Expect(found).To(BeTrue(), "the counter series must exist at init() time")
		Expect(value).To(Equal(0.0))
	})

	It("increments when IncPushRetry is called via the interface", func() {
		before := gatherCounter("git_rest_push_retries_total")
		m := metrics.NewMetrics()
		m.IncPushRetry()
		after := gatherCounter("git_rest_push_retries_total")
		Expect(after - before).To(Equal(1.0))
	})
})

var _ = Describe("GitOperationDuration buckets", func() {
	It("keeps a sample above 30 s in a finite bucket", func() {
		// A label value used by no other test in the package keeps this sample
		// isolated in the process-global registry.
		const label = "bucket-test-31s"
		observer, err := metrics.GitOperationDuration.GetMetricWithLabelValues(label)
		Expect(err).NotTo(HaveOccurred())
		hist, ok := observer.(prometheus.Histogram)
		Expect(ok).To(BeTrue(), "the observer must be a prometheus.Histogram")

		hist.Observe(31.0)

		var dtoMetric dto.Metric
		Expect(hist.Write(&dtoMetric)).To(Succeed())

		var countAt30, countAt60 uint64
		found10, found30, found60 := false, false, false
		for _, bucket := range dtoMetric.GetHistogram().GetBucket() {
			switch bucket.GetUpperBound() {
			case 10:
				found10 = true
			case 30:
				found30 = true
				countAt30 = bucket.GetCumulativeCount()
			case 60:
				found60 = true
				countAt60 = bucket.GetCumulativeCount()
			}
		}
		Expect(found10).To(BeTrue(), "the original 10 s boundary must remain")
		Expect(found30).To(BeTrue(), "the 30 s boundary must be present")
		Expect(found60).To(BeTrue(), "the 60 s boundary must be present")
		Expect(countAt30).To(Equal(uint64(0)),
			"a 31 s sample must not fall in the 30 s bucket")
		Expect(countAt60).To(Equal(uint64(1)),
			"a 31 s sample must land inside the finite 60 s bucket")
	})
})
