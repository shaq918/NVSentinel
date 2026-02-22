//  Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

package metrics

import (
	"sync"

	grpcprom "github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus"
	"github.com/nvidia/nvsentinel/pkg/version"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"k8s.io/klog/v2"
)

// ServerMetrics wraps gRPC Prometheus metrics and a dedicated registry.
// Using a dedicated registry prevents collisions with global metrics from
// other components within the same process (e.g., Kine or etcd).
type ServerMetrics struct {
	Registry            *prometheus.Registry
	Collectors          *grpcprom.ServerMetrics
	ServiceHealthStatus *prometheus.GaugeVec
	mu                  sync.Mutex
	buildInfoLabels     prometheus.Labels
	registerOnce        sync.Once
}

// WithBuildInfo populates the metadata labels used by the build_info metric.
// Must be called before Register() and only from a single goroutine (typically during init).
func (m *ServerMetrics) WithBuildInfo(info version.Info) *ServerMetrics {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.buildInfoLabels = prometheus.Labels{
		"version":    info.Version,
		"revision":   info.GitCommit,
		"build_date": info.BuildDate,
		"goversion":  info.GoVersion,
		"compiler":   info.Compiler,
		"platform":   info.Platform,
	}

	return m
}

var (
	// DefaultServerMetrics is the default instance of ServerMetrics using a
	// dedicated registry and standard gRPC interceptor metrics.
	DefaultServerMetrics = &ServerMetrics{
		Registry: prometheus.NewRegistry(),
		Collectors: grpcprom.NewServerMetrics(
			grpcprom.WithServerHandlingTimeHistogram(),
		),
		ServiceHealthStatus: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "device_apiserver_service_status",
				Help: "Whether the service is serving (1) or not (0).",
			},
			[]string{"service"},
		),
	}
)

// Register initializes the metrics registration exactly once. It registers
// gRPC collectors and the device_apiserver_build_info gauge.
func (m *ServerMetrics) Register() {
	m.registerOnce.Do(func() {
		if err := m.Registry.Register(m.Collectors); err != nil {
			klog.ErrorS(err, "Failed to register gRPC metrics")
		}

		if err := m.Registry.Register(m.ServiceHealthStatus); err != nil {
			klog.ErrorS(err, "Failed to register service health metrics")
		}

		m.mu.Lock()
		labels := m.buildInfoLabels
		m.mu.Unlock()

		if labels != nil {
			version := prometheus.NewGauge(prometheus.GaugeOpts{
				Name:        "device_apiserver_build_info",
				Help:        "Build information about the device-apiserver binary.",
				ConstLabels: labels,
			})
			version.Set(1)

			if err := m.Registry.Register(version); err != nil {
				klog.ErrorS(err, "Failed to register build info metric")
			}
		}
	})
}

// InitializeMetrics configures the gRPC metrics for the provided server.
func (m *ServerMetrics) InitializeMetrics(svr *grpc.Server) {
	if m.Collectors != nil {
		m.Collectors.InitializeMetrics(svr)
	}
}

// GetGatherer returns the prometheus.Gatherer for the dedicated registry.
func (m *ServerMetrics) GetGatherer() prometheus.Gatherer {
	return m.Registry
}
