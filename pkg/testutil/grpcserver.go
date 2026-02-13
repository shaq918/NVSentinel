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

// Package testutil provides shared test infrastructure for gRPC integration tests.
package testutil

import (
	"context"
	"net"
	"testing"

	clientset "github.com/nvidia/nvsentinel/pkg/client-go/client/versioned"
	gpuclient "github.com/nvidia/nvsentinel/pkg/client-go/client/versioned/typed/device/v1alpha1"

	pb "github.com/nvidia/nvsentinel/internal/generated/device/v1alpha1"
	svc "github.com/nvidia/nvsentinel/pkg/services/device/v1alpha1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	apistorage "k8s.io/apiserver/pkg/storage/storagebackend"
)

// NewTestGPUClient creates a bufconn-backed gRPC client for testing.
// It spins up a real gRPC server with the GPU service backed by in-memory storage.
// All resources are cleaned up when t finishes.
func NewTestGPUClient(t *testing.T) pb.GpuServiceClient {
	t.Helper()

	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()

	provider := svc.NewGPUServiceProvider()
	service, err := provider.Install(srv, apistorage.Config{})
	if err != nil {
		t.Fatalf("failed to install GPU service: %v", err)
	}

	go func() {
		if err := srv.Serve(lis); err != nil {
			t.Logf("server stopped: %v", err)
		}
	}()

	conn, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to create gRPC client: %v", err)
	}

	t.Cleanup(func() {
		conn.Close()
		service.Cleanup()
		srv.Stop()
		lis.Close()
	})

	return pb.NewGpuServiceClient(conn)
}

// NewTestGPUTypedClient creates a bufconn-backed typed GPU client for testing.
// It spins up a real gRPC server with the GPU service backed by in-memory storage,
// and returns a GPUInterface from the generated client SDK.
// All resources are cleaned up when t finishes.
func NewTestGPUTypedClient(t *testing.T) gpuclient.GPUInterface {
	t.Helper()

	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()

	provider := svc.NewGPUServiceProvider()
	service, err := provider.Install(srv, apistorage.Config{})
	if err != nil {
		t.Fatalf("failed to install GPU service: %v", err)
	}

	go func() {
		if err := srv.Serve(lis); err != nil {
			t.Logf("server stopped: %v", err)
		}
	}()

	conn, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to create gRPC client: %v", err)
	}

	t.Cleanup(func() {
		conn.Close()
		service.Cleanup()
		srv.Stop()
		lis.Close()
	})

	cs := clientset.New(conn)
	return cs.DeviceV1alpha1().GPUs()
}
