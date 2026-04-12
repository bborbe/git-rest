// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package metrics

import (
	libtime "github.com/bborbe/time"
	"github.com/prometheus/client_golang/prometheus"
)

// BuildInfoMetrics records build information as a Prometheus gauge.
//
//counterfeiter:generate -o ../../mocks/build_info_metrics.go --fake-name BuildInfoMetrics . BuildInfoMetrics
type BuildInfoMetrics interface {
	SetBuildInfo(buildDate *libtime.DateTime)
}

// NewBuildInfoMetrics returns a BuildInfoMetrics that registers a git_rest_build_info gauge.
func NewBuildInfoMetrics(version, commit string) BuildInfoMetrics {
	return &buildInfoMetrics{
		gauge:   buildInfoGauge,
		version: version,
		commit:  commit,
	}
}

type buildInfoMetrics struct {
	gauge   *prometheus.GaugeVec
	version string
	commit  string
}

func (b *buildInfoMetrics) SetBuildInfo(buildDate *libtime.DateTime) {
	date := ""
	if buildDate != nil {
		date = buildDate.String()
	}
	b.gauge.With(prometheus.Labels{
		"version": b.version,
		"commit":  b.commit,
		"date":    date,
	}).Set(1)
}

// buildInfoGauge tracks build metadata.
var buildInfoGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Name: "git_rest_build_info",
	Help: "Build information for the git-rest service.",
}, []string{"version", "commit", "date"})

func init() {
	prometheus.MustRegister(buildInfoGauge)
}
