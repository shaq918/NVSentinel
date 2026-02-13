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

package apiserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nvidia/nvsentinel/pkg/controlplane/apiserver/api"
	"github.com/nvidia/nvsentinel/pkg/controlplane/apiserver/metrics"
	"github.com/nvidia/nvsentinel/pkg/storage/storagebackend"
	netutils "github.com/nvidia/nvsentinel/pkg/util/net"
	"github.com/nvidia/nvsentinel/pkg/version"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/admin"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
)

type DeviceAPIServer struct {
	BindAddress          string
	HealthAddress        string
	ServiceMonitorPeriod time.Duration
	MetricsAddress       string
	ShutdownGracePeriod  time.Duration

	DeviceServer     *grpc.Server
	HealthServer     *health.Server
	AdminServer      *grpc.Server
	AdminCleanup     func()
	Metrics          *metrics.ServerMetrics
	Storage          *storagebackend.Storage
	ServiceProviders []api.ServiceProvider
	mu               sync.RWMutex
	// services is guarded by mu
	services []api.Service

	wg sync.WaitGroup
}

func (c *CompletedConfig) New(s *storagebackend.Storage) (*DeviceAPIServer, error) {
	klog.V(4).InfoS("Creating new Device Server",
		"bind", c.BindAddress,
		"serverOptionsCount", len(c.ServerOptions))

	deviceSrv := grpc.NewServer(c.ServerOptions...)

	adminSrv := grpc.NewServer()

	return &DeviceAPIServer{
		BindAddress:          c.BindAddress,
		HealthAddress:        c.HealthAddress,
		ServiceMonitorPeriod: c.ServiceMonitorPeriod,
		MetricsAddress:       c.MetricsAddress,
		ShutdownGracePeriod:  c.ShutdownGracePeriod,
		DeviceServer:         deviceSrv,
		AdminServer:          adminSrv,
		Metrics:              c.ServerMetrics,
		Storage:              s,
		ServiceProviders:     c.ServiceProviders,
	}, nil
}

type preparedDeviceAPIServer struct {
	*DeviceAPIServer
}

func (s *DeviceAPIServer) PrepareRun(ctx context.Context) (preparedDeviceAPIServer, error) {
	if s.HealthAddress != "" {
		s.HealthServer = health.NewServer()
		healthpb.RegisterHealthServer(s.AdminServer, s.HealthServer)
		// Also register on DeviceServer so sidecar providers connecting via
		// unix socket can perform health checks without a separate connection.
		healthpb.RegisterHealthServer(s.DeviceServer, s.HealthServer)
		s.HealthServer.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
	}

	// Enable gRPC reflection on both servers. This is intentional:
	// - DeviceServer: allows grpcurl/grpc_cli debugging
	// - AdminServer: required for channelz and admin tooling
	// To restrict in production, use NetworkPolicy on the admin port.
	reflection.Register(s.DeviceServer)
	reflection.Register(s.AdminServer)

	adminCleanup, err := admin.Register(s.AdminServer)
	if err != nil {
		return preparedDeviceAPIServer{}, fmt.Errorf("failed to register gRPC admin services: %w", err)
	}
	s.AdminCleanup = adminCleanup

	if s.Metrics != nil {
		s.Metrics.InitializeMetrics(s.DeviceServer)
		s.Metrics.InitializeMetrics(s.AdminServer)
	}

	klog.FromContext(ctx).V(3).Info("gRPC services registered",
		"health", s.HealthAddress != "",
		"reflection", true,
		"admin/channelz", true,
		"metrics", s.Metrics != nil)

	return preparedDeviceAPIServer{s}, nil
}

func (s *preparedDeviceAPIServer) Run(ctx context.Context) error {
	return s.run(ctx)
}

//nolint:cyclop
func (s *DeviceAPIServer) run(ctx context.Context) error {
	logger := klog.FromContext(ctx)
	socketPath := strings.TrimPrefix(s.BindAddress, "unix://")

	if s.HealthServer != nil {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()

			s.serveHealth(ctx)
		}()

		s.wg.Add(1)
		go func() {
			defer s.wg.Done()

			defer func() {
				if r := recover(); r != nil {
					klog.ErrorS(nil, "Health monitor panicked, setting NOT_SERVING", "panic", r)

					if s.HealthServer != nil {
						s.HealthServer.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
					}
				}
			}()

			s.monitorServiceHealth(ctx)
		}()
	}

	if s.MetricsAddress != "" {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()

			s.serveMetrics(ctx)
		}()
	}

	if err := s.waitForStorage(ctx); err != nil {
		return fmt.Errorf("failed to wait for storage backend readiness: %w", err)
	}

	if err := s.installAPIServices(ctx); err != nil {
		return err
	}

	// Sync serving status with backend and service readiness immediately.
	s.checkServiceHealth()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		<-ctx.Done()

		logger.V(2).Info("Received termination signal, starting graceful shutdown")

		if s.HealthServer != nil {
			s.HealthServer.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
		}

		s.DeviceServer.GracefulStop()

		if s.AdminServer != nil {
			adminDone := make(chan struct{})
			go func() {
				s.AdminServer.GracefulStop()
				close(adminDone)
			}()

			select {
			case <-adminDone:
			case <-time.After(s.ShutdownGracePeriod):
				logger.V(2).Info("AdminServer graceful stop timed out, forcing stop")
				s.AdminServer.Stop()
			}
		}

		if s.AdminCleanup != nil {
			s.AdminCleanup()
		}
	}()

	lis, cleanup, err := netutils.CreateUDSListener(ctx, socketPath, 0666)
	if err != nil {
		return err
	}
	defer cleanup()

	logger.Info("Starting Device API Server", "address", s.BindAddress)

	serveErr := s.DeviceServer.Serve(lis)
	if serveErr != nil && !errors.Is(serveErr, grpc.ErrServerStopped) {
		logger.Error(serveErr, "Server exited unexpectedly")
	}

	s.wg.Wait()

	return serveErr
}

func (s *DeviceAPIServer) serveHealth(ctx context.Context) {
	logger := klog.FromContext(ctx)

	lc := net.ListenConfig{}
	lis, err := lc.Listen(ctx, "tcp", s.HealthAddress) //nolint:wsl_v5
	if err != nil {
		logger.Error(err, "Failed to listen on health port", "address", s.HealthAddress)
		return
	}

	// Shutdown listener immediately on cancellation
	// to unblock Serve and reject new conns.
	go func() {
		<-ctx.Done()

		if err := lis.Close(); err != nil {
			logger.Error(err, "Failed to close health listener", "address", s.HealthAddress)
		}
	}()

	logger.V(2).Info("Starting health server", "address", s.HealthAddress)

	serveErr := s.AdminServer.Serve(lis)
	if serveErr != nil && !errors.Is(serveErr, grpc.ErrServerStopped) && !errors.Is(serveErr, net.ErrClosed) {
		logger.Error(serveErr, "Health server stopped unexpectedly")
	}
}

func (s *DeviceAPIServer) serveMetrics(ctx context.Context) {
	logger := klog.FromContext(ctx)

	gatherers := prometheus.Gatherers{
		prometheus.DefaultGatherer,            // System, Go, Kine
		metrics.DefaultServerMetrics.Registry, // Device API gRPC
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(gatherers, promhttp.HandlerOpts{}))
	mux.Handle("/version", version.Handler())

	metricsSrv := &http.Server{
		Addr:              s.MetricsAddress,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	lc := net.ListenConfig{}
	lis, err := lc.Listen(ctx, "tcp", s.MetricsAddress) //nolint:wsl_v5
	if err != nil {
		logger.Error(err, "Failed to listen on metrics port", "address", s.MetricsAddress)
		return
	}

	go func() {
		<-ctx.Done()

		logger.V(2).Info("Shutting down metrics server", "address", s.MetricsAddress)

		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.ShutdownGracePeriod)
		defer cancel()

		if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
			logger.Error(err, "Metrics server graceful shutdown failed; forcing close", "address", s.MetricsAddress)
			metricsSrv.Close()
		}
	}()

	logger.V(2).Info("Starting metrics server", "address", s.MetricsAddress)

	serveErr := metricsSrv.Serve(lis)
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) && !errors.Is(serveErr, net.ErrClosed) {
		logger.Error(serveErr, "Metrics server stopped unexpectedly", "address", s.MetricsAddress)
	}
}

func (s *DeviceAPIServer) waitForStorage(ctx context.Context) error {
	if s.Storage == nil {
		return fmt.Errorf("storage backend is not initialized")
	}

	if s.Storage.IsReady() {
		return nil
	}

	logger := klog.FromContext(ctx)
	logger.Info("Waiting for storage backend to become ready")
	startTime := time.Now()

	err := wait.PollUntilContextTimeout(ctx, 200*time.Millisecond, 60*time.Second, true,
		func(ctx context.Context) (bool, error) {
			if s.Storage.IsReady() {
				logger.V(2).Info("Storage backend is ready",
					"duration", time.Since(startTime).Round(time.Second))
				return true, nil
			}

			return false, nil
		},
	)
	if err != nil {
		return fmt.Errorf("timed out waiting for storage backend readiness: %w", err)
	}

	return nil
}

func (s *DeviceAPIServer) installAPIServices(ctx context.Context) error {
	logger := klog.FromContext(ctx)

	var services []api.Service
	for i, sp := range s.ServiceProviders {
		service, err := sp.Install(s.DeviceServer, s.Storage.StorageConfig)
		if err != nil {
			return fmt.Errorf("failed to install API service (index %d): %w", i, err)
		}

		services = append(services, service)
	}

	s.mu.Lock()
	s.services = append(s.services, services...)
	s.mu.Unlock()

	logger.V(3).Info("API services installed", "count", len(s.services))

	return nil
}

func (s *DeviceAPIServer) checkServiceHealth() {
	if s.HealthServer == nil {
		return
	}

	storageReady := s.Storage.IsReady()
	globalReady := storageReady

	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, svc := range s.services {
		name := svc.Name()
		svcReady := storageReady && svc.IsReady()
		status := healthpb.HealthCheckResponse_SERVING
		metricVal := 1.0

		if !svcReady {
			status = healthpb.HealthCheckResponse_NOT_SERVING
			metricVal = 0.0
			globalReady = false

			klog.V(2).InfoS("Service is not ready",
				"service", name,
				"serviceReady", svc.IsReady(),
				"storageReady", storageReady)
		}

		s.HealthServer.SetServingStatus(name, status)

		if s.Metrics != nil {
			s.Metrics.ServiceHealthStatus.WithLabelValues(name).Set(metricVal)
		}
	}

	globalStatus := healthpb.HealthCheckResponse_SERVING
	globalMetricVal := 1.0

	if !globalReady {
		globalStatus = healthpb.HealthCheckResponse_NOT_SERVING
		globalMetricVal = 0.0
	}

	s.HealthServer.SetServingStatus("", globalStatus)

	if s.Metrics != nil {
		s.Metrics.ServiceHealthStatus.WithLabelValues("device-apiserver").Set(globalMetricVal)
	}
}

func (s *DeviceAPIServer) monitorServiceHealth(ctx context.Context) {
	if s.ServiceMonitorPeriod <= 0 {
		klog.InfoS("Service health monitoring disabled, performing one-time check")
		s.checkServiceHealth()
		return
	}

	klog.InfoS("Starting service health monitor", "period", s.ServiceMonitorPeriod)

	ticker := time.NewTicker(s.ServiceMonitorPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			klog.InfoS("Stopping service health monitor")
			return
		case <-ticker.C:
			klog.V(4).InfoS("Triggering periodic service health check")
			s.checkServiceHealth()
		}
	}
}
