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

package v1alpha1_test

import (
	"io"
	"testing"
	"time"

	pb "github.com/nvidia/nvsentinel/internal/generated/device/v1alpha1"
	"github.com/nvidia/nvsentinel/pkg/testutil"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestIntegration_CRUD performs a full Create→Get→List→Update→Delete cycle over gRPC.
func TestIntegration_CRUD(t *testing.T) {
	client := testutil.NewTestGPUClient(t)
	ctx := t.Context()

	const gpuName = "GPU-12345678-1234-1234-1234-123456789abc"

	// Create a GPU
	created, err := client.CreateGpu(ctx, &pb.CreateGpuRequest{
		Gpu: &pb.Gpu{
			Metadata: &pb.ObjectMeta{
				Name:      gpuName,
				Namespace: "default",
			},
			Spec: &pb.GpuSpec{
				Uuid: "GPU-1234",
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateGpu failed: %v", err)
	}

	if created.GetMetadata().GetName() != gpuName {
		t.Errorf("expected name %q, got %q", gpuName, created.GetMetadata().GetName())
	}
	if created.GetSpec().GetUuid() != "GPU-1234" {
		t.Errorf("expected UUID %q, got %q", "GPU-1234", created.GetSpec().GetUuid())
	}
	if created.GetMetadata().GetUid() == "" {
		t.Error("expected UID to be set")
	}

	// Get it back
	getResp, err := client.GetGpu(ctx, &pb.GetGpuRequest{
		Name:      gpuName,
		Namespace: "default",
	})
	if err != nil {
		t.Fatalf("GetGpu failed: %v", err)
	}

	got := getResp.GetGpu()
	if got.GetSpec().GetUuid() != "GPU-1234" {
		t.Errorf("expected UUID %q, got %q", "GPU-1234", got.GetSpec().GetUuid())
	}

	// List namespace "default"
	listResp, err := client.ListGpus(ctx, &pb.ListGpusRequest{
		Namespace: "default",
	})
	if err != nil {
		t.Fatalf("ListGpus failed: %v", err)
	}

	if len(listResp.GetGpuList().GetItems()) != 1 {
		t.Errorf("expected 1 GPU, got %d", len(listResp.GetGpuList().GetItems()))
	}

	// Update the spec (change UUID to "GPU-5678")
	got.Spec.Uuid = "GPU-5678"
	updated, err := client.UpdateGpu(ctx, &pb.UpdateGpuRequest{
		Gpu: got,
	})
	if err != nil {
		t.Fatalf("UpdateGpu failed: %v", err)
	}

	if updated.GetSpec().GetUuid() != "GPU-5678" {
		t.Errorf("expected UUID %q, got %q", "GPU-5678", updated.GetSpec().GetUuid())
	}

	// Verify change persists
	getResp2, err := client.GetGpu(ctx, &pb.GetGpuRequest{
		Name:      gpuName,
		Namespace: "default",
	})
	if err != nil {
		t.Fatalf("GetGpu (after update) failed: %v", err)
	}

	if getResp2.GetGpu().GetSpec().GetUuid() != "GPU-5678" {
		t.Errorf("expected UUID %q after update, got %q", "GPU-5678", getResp2.GetGpu().GetSpec().GetUuid())
	}

	// Delete it
	_, err = client.DeleteGpu(ctx, &pb.DeleteGpuRequest{
		Name:      gpuName,
		Namespace: "default",
	})
	if err != nil {
		t.Fatalf("DeleteGpu failed: %v", err)
	}

	// List again, verify count=0
	listResp2, err := client.ListGpus(ctx, &pb.ListGpusRequest{
		Namespace: "default",
	})
	if err != nil {
		t.Fatalf("ListGpus (after delete) failed: %v", err)
	}

	if len(listResp2.GetGpuList().GetItems()) != 0 {
		t.Errorf("expected 0 GPUs after delete, got %d", len(listResp2.GetGpuList().GetItems()))
	}
}

// TestIntegration_Watch tests the streaming WatchGpus RPC.
func TestIntegration_Watch(t *testing.T) {
	client := testutil.NewTestGPUClient(t)
	ctx := t.Context()

	const gpuName = "GPU-aabbccdd-1122-3344-5566-778899aabbcc"

	// Start a watch stream
	stream, err := client.WatchGpus(ctx, &pb.WatchGpusRequest{
		Namespace: "default",
	})
	if err != nil {
		t.Fatalf("WatchGpus failed to start: %v", err)
	}

	// Create a GPU in a separate goroutine after a brief delay.
	// The WatchGpus RPC returns a stream only after the server-side watch
	// is established. However, the gRPC client dial and server handler setup
	// may not be fully synchronized, so a small delay ensures the watch is
	// ready to receive events. The main goroutine uses a 5s timeout on Recv
	// as the real synchronization mechanism.
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		time.Sleep(100 * time.Millisecond)
		_, err := client.CreateGpu(ctx, &pb.CreateGpuRequest{
			Gpu: &pb.Gpu{
				Metadata: &pb.ObjectMeta{
					Name:      gpuName,
					Namespace: "default",
				},
				Spec: &pb.GpuSpec{
					Uuid: "GPU-WATCH-1",
				},
			},
		})
		if err != nil {
			t.Errorf("CreateGpu in watch test failed: %v", err)
		}
	}()

	// Wait for the ADDED event
	timeout := time.After(5 * time.Second)
	receivedEvent := false

	for !receivedEvent {
		select {
		case <-timeout:
			t.Fatal("timeout waiting for watch event")
		default:
			event, err := stream.Recv()
			if err == io.EOF {
				t.Fatal("stream closed before receiving event")
			}
			if err != nil {
				t.Fatalf("stream.Recv() failed: %v", err)
			}

			if event.GetType() == "ADDED" && event.GetObject().GetMetadata().GetName() == gpuName {
				receivedEvent = true
				if event.GetObject().GetSpec().GetUuid() != "GPU-WATCH-1" {
					t.Errorf("expected UUID %q, got %q", "GPU-WATCH-1", event.GetObject().GetSpec().GetUuid())
				}
			}
		}
	}

	// Wait for the create goroutine to finish
	<-doneCh

	// Clean up
	_, err = client.DeleteGpu(ctx, &pb.DeleteGpuRequest{
		Name:      gpuName,
		Namespace: "default",
	})
	if err != nil {
		t.Errorf("cleanup DeleteGpu failed: %v", err)
	}
}

// TestIntegration_WatchWithResourceVersion_OutOfRange verifies that requesting
// a watch from a specific ResourceVersion returns codes.OutOfRange, because the
// in-memory store does not support watch resume.
func TestIntegration_WatchWithResourceVersion_OutOfRange(t *testing.T) {
	client := testutil.NewTestGPUClient(t)
	ctx := t.Context()

	stream, err := client.WatchGpus(ctx, &pb.WatchGpusRequest{
		Namespace: "default",
		Opts: &pb.ListOptions{
			ResourceVersion: "1",
		},
	})
	if err != nil {
		t.Fatalf("WatchGpus failed to open stream: %v", err)
	}

	// In gRPC server streaming, handler errors surface on Recv.
	_, err = stream.Recv()
	if err == nil {
		t.Fatal("expected OutOfRange error for non-empty ResourceVersion, got nil")
	}
	if status.Code(err) != codes.OutOfRange {
		t.Errorf("expected codes.OutOfRange, got %v: %v", status.Code(err), err)
	}
}

// TestIntegration_UpdateStatus tests the status subresource update.
func TestIntegration_UpdateStatus(t *testing.T) {
	client := testutil.NewTestGPUClient(t)
	ctx := t.Context()

	const gpuName = "GPU-55667788-aabb-ccdd-eeff-001122334455"

	// Create a GPU
	created, err := client.CreateGpu(ctx, &pb.CreateGpuRequest{
		Gpu: &pb.Gpu{
			Metadata: &pb.ObjectMeta{
				Name:      gpuName,
				Namespace: "default",
			},
			Spec: &pb.GpuSpec{
				Uuid: "GPU-STATUS-1",
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateGpu failed: %v", err)
	}

	// Update the status with a condition
	updatedGpu, err := client.UpdateGpuStatus(ctx, &pb.UpdateGpuStatusRequest{
		Gpu: &pb.Gpu{
			Metadata: &pb.ObjectMeta{
				Name:            gpuName,
				Namespace:       "default",
				ResourceVersion: created.GetMetadata().GetResourceVersion(),
			},
			Status: &pb.GpuStatus{
				Conditions: []*pb.Condition{
					{
						Type:               "Ready",
						Status:             "True",
						LastTransitionTime: timestamppb.Now(),
						Reason:             "TestReason",
						Message:            "Test message",
					},
				},
				RecommendedAction: "No action needed",
			},
		},
	})
	if err != nil {
		t.Fatalf("UpdateGpuStatus failed: %v", err)
	}

	if len(updatedGpu.GetStatus().GetConditions()) != 1 {
		t.Errorf("expected 1 condition, got %d", len(updatedGpu.GetStatus().GetConditions()))
	}

	// Get the GPU and verify status was updated
	getResp, err := client.GetGpu(ctx, &pb.GetGpuRequest{
		Name:      gpuName,
		Namespace: "default",
	})
	if err != nil {
		t.Fatalf("GetGpu failed: %v", err)
	}

	gpu := getResp.GetGpu()
	if len(gpu.GetStatus().GetConditions()) != 1 {
		t.Errorf("expected 1 condition in retrieved GPU, got %d", len(gpu.GetStatus().GetConditions()))
	}

	cond := gpu.GetStatus().GetConditions()[0]
	if cond.GetType() != "Ready" {
		t.Errorf("expected condition type %q, got %q", "Ready", cond.GetType())
	}
	if cond.GetStatus() != "True" {
		t.Errorf("expected condition status %q, got %q", "True", cond.GetStatus())
	}
	if cond.GetReason() != "TestReason" {
		t.Errorf("expected condition reason %q, got %q", "TestReason", cond.GetReason())
	}
	if gpu.GetStatus().GetRecommendedAction() != "No action needed" {
		t.Errorf("expected recommended action %q, got %q", "No action needed", gpu.GetStatus().GetRecommendedAction())
	}

	// Clean up
	_, err = client.DeleteGpu(ctx, &pb.DeleteGpuRequest{
		Name:      gpuName,
		Namespace: "default",
	})
	if err != nil {
		t.Errorf("cleanup DeleteGpu failed: %v", err)
	}
}

// TestIntegration_ErrorCodes verifies correct gRPC error codes are returned.
func TestIntegration_ErrorCodes(t *testing.T) {
	client := testutil.NewTestGPUClient(t)
	ctx := t.Context()

	const gpuName = "GPU-deadbeef-dead-beef-dead-beefdeadbeef"

	// Get non-existent GPU → codes.NotFound
	_, err := client.GetGpu(ctx, &pb.GetGpuRequest{
		Name:      gpuName,
		Namespace: "default",
	})
	if err == nil {
		t.Fatal("expected error for non-existent GPU")
	}
	if status.Code(err) != codes.NotFound {
		t.Errorf("expected codes.NotFound, got %v", status.Code(err))
	}

	// Create a GPU
	_, err = client.CreateGpu(ctx, &pb.CreateGpuRequest{
		Gpu: &pb.Gpu{
			Metadata: &pb.ObjectMeta{
				Name:      gpuName,
				Namespace: "default",
			},
			Spec: &pb.GpuSpec{
				Uuid: "GPU-ERROR-1",
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateGpu failed: %v", err)
	}

	// Create duplicate → codes.AlreadyExists
	_, err = client.CreateGpu(ctx, &pb.CreateGpuRequest{
		Gpu: &pb.Gpu{
			Metadata: &pb.ObjectMeta{
				Name:      gpuName,
				Namespace: "default",
			},
			Spec: &pb.GpuSpec{
				Uuid: "GPU-ERROR-2",
			},
		},
	})
	if err == nil {
		t.Fatal("expected error for duplicate GPU creation")
	}
	if status.Code(err) != codes.AlreadyExists {
		t.Errorf("expected codes.AlreadyExists, got %v", status.Code(err))
	}

	// Delete the GPU
	_, err = client.DeleteGpu(ctx, &pb.DeleteGpuRequest{
		Name:      gpuName,
		Namespace: "default",
	})
	if err != nil {
		t.Fatalf("DeleteGpu failed: %v", err)
	}

	// Delete non-existent → codes.NotFound
	_, err = client.DeleteGpu(ctx, &pb.DeleteGpuRequest{
		Name:      gpuName,
		Namespace: "default",
	})
	if err == nil {
		t.Fatal("expected error for deleting non-existent GPU")
	}
	if status.Code(err) != codes.NotFound {
		t.Errorf("expected codes.NotFound for delete, got %v", status.Code(err))
	}
}
