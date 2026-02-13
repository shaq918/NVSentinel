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
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc"

	"k8s.io/apiserver/pkg/storage/storagebackend"
	"k8s.io/component-base/logs"
	logsapi "k8s.io/component-base/logs/api/v1"

	"github.com/nvidia/nvsentinel/pkg/controlplane/apiserver/api"
	"github.com/nvidia/nvsentinel/pkg/controlplane/apiserver/metrics"
	"github.com/nvidia/nvsentinel/pkg/controlplane/apiserver/options"
	"github.com/nvidia/nvsentinel/pkg/controlplane/apiserver/registry"
	"github.com/nvidia/nvsentinel/pkg/version"
)

type Config struct {
	NodeName             string
	BindAddress          string
	HealthAddress        string
	ServiceMonitorPeriod time.Duration
	MetricsAddress       string
	ShutdownGracePeriod  time.Duration

	ServerOptions    []grpc.ServerOption
	ServerMetrics    *metrics.ServerMetrics
	StorageConfig    storagebackend.Config
	ServiceProviders []api.ServiceProvider
	LogOptions       *logs.Options
}

type CompletedConfig struct {
	*Config
}

func NewConfig(ctx context.Context, opts options.CompletedOptions) (*Config, error) {
	serverMetrics := metrics.DefaultServerMetrics.WithBuildInfo(version.Get())
	serverMetrics.Register()

	config := &Config{
		NodeName:             opts.NodeName,
		HealthAddress:        opts.HealthAddress,
		ServiceMonitorPeriod: opts.ServiceMonitorPeriod,
		MetricsAddress:       opts.MetricsAddress,
		ShutdownGracePeriod:  opts.ShutdownGracePeriod,
		ServerOptions:        []grpc.ServerOption{},
		ServerMetrics:        serverMetrics,
		LogOptions:           opts.Logs,
	}

	config.ServiceProviders = append(config.ServiceProviders, registry.List()...)
	if len(config.ServiceProviders) == 0 {
		return nil, fmt.Errorf("no API services were discovered; at least one is required")
	}

	config.ServerOptions = append(config.ServerOptions,
		grpc.ChainUnaryInterceptor(serverMetrics.Collectors.UnaryServerInterceptor()),
		grpc.ChainStreamInterceptor(serverMetrics.Collectors.StreamServerInterceptor()),
	)

	if err := logsapi.ValidateAndApply(opts.Logs, nil); err != nil {
		if !strings.Contains(err.Error(), "already applied") {
			return nil, fmt.Errorf("failed to apply logging configuration: %w", err)
		}
	}

	if err := opts.GRPC.ApplyTo(&config.BindAddress, &config.ServerOptions); err != nil {
		return nil, fmt.Errorf("failed to apply grpc options: %w", err)
	}

	if err := opts.Storage.ApplyTo(&config.StorageConfig); err != nil {
		return nil, fmt.Errorf("failed to apply storage options: %w", err)
	}

	return config, nil
}

func (c *Config) Complete() (CompletedConfig, error) {
	if c == nil {
		return CompletedConfig{}, nil
	}

	return CompletedConfig{c}, nil
}
