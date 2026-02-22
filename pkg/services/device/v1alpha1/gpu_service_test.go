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

package v1alpha1

import (
	"context"
	"strings"
	"testing"

	devicev1alpha1 "github.com/nvidia/nvsentinel/api/device/v1alpha1"
	pb "github.com/nvidia/nvsentinel/internal/generated/device/v1alpha1"
	"github.com/nvidia/nvsentinel/pkg/storage/memory"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
)

func newTestService(t *testing.T) *gpuService {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := devicev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	codecs := serializer.NewCodecFactory(scheme)
	gv := devicev1alpha1.SchemeGroupVersion
	info, _ := runtime.SerializerInfoForMediaType(codecs.SupportedMediaTypes(), runtime.ContentTypeJSON)
	codec := codecs.CodecForVersions(info.Serializer, info.Serializer, schema.GroupVersions{gv}, schema.GroupVersions{gv})

	s, destroy, err := memory.CreateStorage(codec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(destroy)

	return NewGPUService(s, destroy)
}

func createTestGpu(t *testing.T, svc *gpuService, name string) *pb.Gpu {
	t.Helper()

	gpu, err := svc.CreateGpu(context.Background(), &pb.CreateGpuRequest{
		Gpu: &pb.Gpu{
			Metadata: &pb.ObjectMeta{
				Name:      name,
				Namespace: "default",
			},
			Spec: &pb.GpuSpec{
				Uuid: name,
			},
		},
	})
	if err != nil {
		t.Fatalf("failed to create GPU %q: %v", name, err)
	}

	return gpu
}

func TestGPUService_CreateAndGet(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	const gpuName = "GPU-00000000-0000-0000-0000-000000000000"
	created := createTestGpu(t, svc, gpuName)

	if created.GetMetadata().GetName() != gpuName {
		t.Errorf("expected name %q, got %q", gpuName, created.GetMetadata().GetName())
	}
	if created.GetMetadata().GetUid() == "" {
		t.Error("expected UID to be set on created GPU")
	}

	resp, err := svc.GetGpu(ctx, &pb.GetGpuRequest{
		Name:      gpuName,
		Namespace: "default",
	})
	if err != nil {
		t.Fatalf("GetGpu failed: %v", err)
	}

	got := resp.GetGpu()
	if got.GetMetadata().GetName() != gpuName {
		t.Errorf("expected name %q, got %q", gpuName, got.GetMetadata().GetName())
	}
	if got.GetMetadata().GetUid() != created.GetMetadata().GetUid() {
		t.Errorf("UID mismatch: expected %q, got %q",
			created.GetMetadata().GetUid(), got.GetMetadata().GetUid())
	}
}

func TestGPUService_CreateDuplicate(t *testing.T) {
	svc := newTestService(t)

	const gpuName = "GPU-11111111-1111-1111-1111-111111111111"
	createTestGpu(t, svc, gpuName)

	_, err := svc.CreateGpu(context.Background(), &pb.CreateGpuRequest{
		Gpu: &pb.Gpu{
			Metadata: &pb.ObjectMeta{
				Name:      gpuName,
				Namespace: "default",
			},
			Spec: &pb.GpuSpec{
				Uuid: gpuName,
			},
		},
	})
	if err == nil {
		t.Fatal("expected error for duplicate create, got nil")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}
	if st.Code() != codes.AlreadyExists {
		t.Errorf("expected code %v, got %v: %s", codes.AlreadyExists, st.Code(), st.Message())
	}
}

func TestGPUService_List(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	createTestGpu(t, svc, "GPU-aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	createTestGpu(t, svc, "GPU-bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")

	resp, err := svc.ListGpus(ctx, &pb.ListGpusRequest{
		Namespace: "default",
	})
	if err != nil {
		t.Fatalf("ListGpus failed: %v", err)
	}

	count := len(resp.GetGpuList().GetItems())
	if count != 2 {
		t.Errorf("expected 2 GPUs, got %d", count)
	}
}

func TestGPUService_Delete(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	const gpuName = "GPU-22222222-2222-2222-2222-222222222222"
	createTestGpu(t, svc, gpuName)

	_, err := svc.DeleteGpu(ctx, &pb.DeleteGpuRequest{
		Name:      gpuName,
		Namespace: "default",
	})
	if err != nil {
		t.Fatalf("DeleteGpu failed: %v", err)
	}

	_, err = svc.GetGpu(ctx, &pb.GetGpuRequest{
		Name:      gpuName,
		Namespace: "default",
	})
	if err == nil {
		t.Fatal("expected NotFound after delete, got nil")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}
	if st.Code() != codes.NotFound {
		t.Errorf("expected code %v, got %v: %s", codes.NotFound, st.Code(), st.Message())
	}
}

func TestGPUService_DeleteNotFound(t *testing.T) {
	svc := newTestService(t)

	_, err := svc.DeleteGpu(context.Background(), &pb.DeleteGpuRequest{
		Name:      "GPU-ffffffff-ffff-ffff-ffff-ffffffffffff",
		Namespace: "default",
	})
	if err == nil {
		t.Fatal("expected NotFound error, got nil")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}
	if st.Code() != codes.NotFound {
		t.Errorf("expected code %v, got %v: %s", codes.NotFound, st.Code(), st.Message())
	}
}

func TestGPUService_Update(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	const gpuName = "GPU-33333333-3333-3333-3333-333333333333"
	created := createTestGpu(t, svc, gpuName)

	updated, err := svc.UpdateGpu(ctx, &pb.UpdateGpuRequest{
		Gpu: &pb.Gpu{
			Metadata: &pb.ObjectMeta{
				Name:      gpuName,
				Namespace: "default",
			},
			Spec: &pb.GpuSpec{
				Uuid: "GPU-new-uuid",
			},
		},
	})
	if err != nil {
		t.Fatalf("UpdateGpu failed: %v", err)
	}

	if updated.GetSpec().GetUuid() != "GPU-new-uuid" {
		t.Errorf("expected spec.uuid %q, got %q", "GPU-new-uuid", updated.GetSpec().GetUuid())
	}
	if updated.GetMetadata().GetGeneration() != created.GetMetadata().GetGeneration()+1 {
		t.Errorf("expected generation %d, got %d",
			created.GetMetadata().GetGeneration()+1, updated.GetMetadata().GetGeneration())
	}
}

func TestGPUService_UpdateStatus(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	const gpuName = "GPU-44444444-4444-4444-4444-444444444444"
	created := createTestGpu(t, svc, gpuName)

	updated, err := svc.UpdateGpuStatus(ctx, &pb.UpdateGpuStatusRequest{
		Gpu: &pb.Gpu{
			Metadata: &pb.ObjectMeta{
				Name:      gpuName,
				Namespace: "default",
			},
			Status: &pb.GpuStatus{
				RecommendedAction: "drain",
			},
		},
	})
	if err != nil {
		t.Fatalf("UpdateGpuStatus failed: %v", err)
	}

	if updated.GetStatus().GetRecommendedAction() != "drain" {
		t.Errorf("expected recommended action %q, got %q",
			"drain", updated.GetStatus().GetRecommendedAction())
	}

	// Generation must NOT change on status-only updates.
	if updated.GetMetadata().GetGeneration() != created.GetMetadata().GetGeneration() {
		t.Errorf("expected generation %d (unchanged), got %d",
			created.GetMetadata().GetGeneration(), updated.GetMetadata().GetGeneration())
	}
}

func TestGPUService_UpdateStatus_StaleResourceVersion(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	const gpuName = "GPU-55555555-5555-5555-5555-555555555555"
	created := createTestGpu(t, svc, gpuName)
	staleRV := created.GetMetadata().GetResourceVersion()

	// Update spec to increment the resource version.
	_, err := svc.UpdateGpu(ctx, &pb.UpdateGpuRequest{
		Gpu: &pb.Gpu{
			Metadata: &pb.ObjectMeta{
				Name:      gpuName,
				Namespace: "default",
			},
			Spec: &pb.GpuSpec{
				Uuid: "GPU-updated-uuid",
			},
		},
	})
	if err != nil {
		t.Fatalf("UpdateGpu failed: %v", err)
	}

	// Attempt status update with the stale resource version.
	_, err = svc.UpdateGpuStatus(ctx, &pb.UpdateGpuStatusRequest{
		Gpu: &pb.Gpu{
			Metadata: &pb.ObjectMeta{
				Name:            gpuName,
				Namespace:       "default",
				ResourceVersion: staleRV,
			},
			Status: &pb.GpuStatus{
				RecommendedAction: "drain",
			},
		},
	})
	if err == nil {
		t.Fatal("expected error for stale resource version, got nil")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}
	if st.Code() != codes.Aborted {
		t.Errorf("expected code %v, got %v: %s", codes.Aborted, st.Code(), st.Message())
	}
}

func TestGPUService_UpdateStatus_NilStatus(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	const gpuName = "GPU-66666666-6666-6666-6666-666666666666"
	createTestGpu(t, svc, gpuName)

	_, err := svc.UpdateGpuStatus(ctx, &pb.UpdateGpuStatusRequest{
		Gpu: &pb.Gpu{
			Metadata: &pb.ObjectMeta{
				Name:      gpuName,
				Namespace: "default",
			},
			Status: nil,
		},
	})
	if err == nil {
		t.Fatal("expected error for nil status, got nil")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Errorf("expected code %v, got %v: %s", codes.InvalidArgument, st.Code(), st.Message())
	}
}

func TestGPUService_UpdateStatus_EmptyConditions(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	const gpuName = "GPU-77777777-7777-7777-7777-777777777777"
	createTestGpu(t, svc, gpuName)

	// First set a condition.
	_, err := svc.UpdateGpuStatus(ctx, &pb.UpdateGpuStatusRequest{
		Gpu: &pb.Gpu{
			Metadata: &pb.ObjectMeta{
				Name:      gpuName,
				Namespace: "default",
			},
			Status: &pb.GpuStatus{
				Conditions: []*pb.Condition{
					{
						Type:   "Ready",
						Status: "True",
						Reason: "TestReason",
					},
				},
				RecommendedAction: "drain",
			},
		},
	})
	if err != nil {
		t.Fatalf("UpdateGpuStatus (set condition) failed: %v", err)
	}

	// Now update with empty conditions to clear them.
	updated, err := svc.UpdateGpuStatus(ctx, &pb.UpdateGpuStatusRequest{
		Gpu: &pb.Gpu{
			Metadata: &pb.ObjectMeta{
				Name:      gpuName,
				Namespace: "default",
			},
			Status: &pb.GpuStatus{
				Conditions:        []*pb.Condition{},
				RecommendedAction: "none",
			},
		},
	})
	if err != nil {
		t.Fatalf("UpdateGpuStatus (clear conditions) failed: %v", err)
	}

	if len(updated.GetStatus().GetConditions()) != 0 {
		t.Errorf("expected 0 conditions after clearing, got %d", len(updated.GetStatus().GetConditions()))
	}
	if updated.GetStatus().GetRecommendedAction() != "none" {
		t.Errorf("expected recommended action %q, got %q", "none", updated.GetStatus().GetRecommendedAction())
	}
}

func TestGPUService_CreateValidation(t *testing.T) {
	svc := newTestService(t)

	tests := []struct {
		name string
		req  *pb.CreateGpuRequest
	}{
		{
			name: "nil gpu body",
			req:  &pb.CreateGpuRequest{},
		},
		{
			name: "nil metadata",
			req: &pb.CreateGpuRequest{
				Gpu: &pb.Gpu{
					Spec: &pb.GpuSpec{Uuid: "GPU-test"},
				},
			},
		},
		{
			name: "empty name",
			req: &pb.CreateGpuRequest{
				Gpu: &pb.Gpu{
					Metadata: &pb.ObjectMeta{Name: ""},
					Spec:     &pb.GpuSpec{Uuid: "GPU-test"},
				},
			},
		},
		{
			name: "invalid GPU UUID format",
			req: &pb.CreateGpuRequest{
				Gpu: &pb.Gpu{
					Metadata: &pb.ObjectMeta{Name: "not-a-gpu-uuid"},
					Spec:     &pb.GpuSpec{Uuid: "GPU-test"},
				},
			},
		},
		{
			name: "path traversal in name",
			req: &pb.CreateGpuRequest{
				Gpu: &pb.Gpu{
					Metadata: &pb.ObjectMeta{Name: "../../etc/passwd"},
					Spec:     &pb.GpuSpec{Uuid: "GPU-test"},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.CreateGpu(context.Background(), tc.req)
			if err == nil {
				t.Fatal("expected InvalidArgument error, got nil")
			}

			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("expected gRPC status error, got %T: %v", err, err)
			}
			if st.Code() != codes.InvalidArgument {
				t.Errorf("expected code %v, got %v: %s", codes.InvalidArgument, st.Code(), st.Message())
			}
		})
	}
}

func TestGPUService_NamespaceValidation(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	longNS := strings.Repeat("a", 254)

	_, err := svc.GetGpu(ctx, &pb.GetGpuRequest{
		Name:      "GPU-00000000-0000-0000-0000-000000000000",
		Namespace: longNS,
	})
	if err == nil {
		t.Fatal("expected InvalidArgument for long namespace, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Errorf("expected code %v, got %v: %s", codes.InvalidArgument, st.Code(), st.Message())
	}
}
