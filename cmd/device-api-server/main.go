// Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package main implements the Device API Server.
//
// The Device API Server is a node-local gRPC cache server deployed as a
// Kubernetes DaemonSet. It acts as an intermediary between providers
// (health monitors) that update GPU device states and consumers
// (device plugins, DRA drivers) that read device states.
//
// Key features:
//   - Read-blocking semantics: Reads are blocked during provider updates
//     to prevent consumers from reading stale data
//   - Multiple provider support: Multiple health monitors can update
//     different conditions on the same GPUs
//   - Multiple consumer support: Device plugins, DRA drivers, and other
//     consumers can read and watch GPU states
//   - Observability: Prometheus metrics, structured logging with klog/v2
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/klog/v2"

	"github.com/nvidia/nvsentinel/pkg/controlplane/apiserver"
	"github.com/nvidia/nvsentinel/pkg/controlplane/apiserver/options"
	"github.com/nvidia/nvsentinel/pkg/storage/storagebackend"
	"github.com/nvidia/nvsentinel/pkg/version"

	// Import service providers so their init() functions register them.
	_ "github.com/nvidia/nvsentinel/pkg/services/device/v1alpha1"
)

const (
	// ComponentName is the name of this component for logging.
	ComponentName = "device-api-server"
)

func main() {
	opts := options.NewOptions()

	fss := cliflag.NamedFlagSets{}
	opts.AddFlags(&fss)

	// Add a version flag to the global flag set.
	showVersion := pflag.Bool("version", false, "Show version and exit")

	// Merge all named flag sets into the global pflag command line.
	for _, fs := range fss.FlagSets {
		pflag.CommandLine.AddFlagSet(fs)
	}

	pflag.Parse()

	// Handle version flag before any other initialization.
	if *showVersion {
		v := version.Get()
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(v); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to encode version: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	// Set up signal handling for graceful shutdown.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Complete fills in defaults and resolves environment overrides.
	completedOpts, err := opts.Complete(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to complete options: %v\n", err)
		os.Exit(1)
	}

	// Validate rejects invalid flag combinations.
	if errs := completedOpts.Validate(); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "Invalid configuration: %v\n", e)
		}
		os.Exit(1)
	}

	// Create root logger with component name.
	logger := klog.Background().WithName(ComponentName)
	ctx = klog.NewContext(ctx, logger)

	versionInfo := version.Get()
	logger.Info("Starting server",
		"version", versionInfo.Version,
		"commit", versionInfo.GitCommit,
		"buildDate", versionInfo.BuildDate,
	)

	// Build the apiserver configuration from completed options.
	apiserverConfig, err := apiserver.NewConfig(ctx, completedOpts)
	if err != nil {
		logger.Error(err, "Failed to create apiserver config")
		os.Exit(1)
	}

	completedAPIServerConfig, err := apiserverConfig.Complete()
	if err != nil {
		logger.Error(err, "Failed to complete apiserver config")
		os.Exit(1)
	}

	// Build the storage backend configuration from completed options.
	storageConfig, err := storagebackend.NewConfig(ctx, completedOpts.Storage)
	if err != nil {
		logger.Error(err, "Failed to create storage config")
		os.Exit(1)
	}

	completedStorageConfig, err := storageConfig.Complete()
	if err != nil {
		logger.Error(err, "Failed to complete storage config")
		os.Exit(1)
	}

	storage, err := completedStorageConfig.New()
	if err != nil {
		logger.Error(err, "Failed to create storage backend")
		os.Exit(1)
	}

	preparedStorage, err := storage.PrepareRun(ctx)
	if err != nil {
		logger.Error(err, "Failed to prepare storage backend")
		os.Exit(1)
	}

	// Create, prepare the device API server before starting the run loop.
	server, err := completedAPIServerConfig.New(storage)
	if err != nil {
		logger.Error(err, "Failed to create device API server")
		os.Exit(1)
	}

	prepared, err := server.PrepareRun(ctx)
	if err != nil {
		logger.Error(err, "Failed to prepare device API server")
		os.Exit(1)
	}

	// Run storage and server concurrently. If either fails, the errgroup
	// cancels the shared context so the other component shuts down.
	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		return preparedStorage.Run(gctx)
	})

	g.Go(func() error {
		return prepared.Run(gctx)
	})

	if err := g.Wait(); err != nil {
		logger.Error(err, "Server error")
		os.Exit(1)
	}

	logger.Info("Server stopped gracefully")
}
