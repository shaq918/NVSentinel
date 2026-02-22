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

package testutil

import (
	"testing"

	pb "github.com/nvidia/nvsentinel/internal/generated/device/v1alpha1"
)

func TestNewTestGPUClient_CreateAndGet(t *testing.T) {
	client := NewTestGPUClient(t)
	ctx := t.Context()

	const gpuName = "GPU-01234567-89ab-cdef-0123-456789abcdef"

	created, err := client.CreateGpu(ctx, &pb.CreateGpuRequest{
		Gpu: &pb.Gpu{
			Metadata: &pb.ObjectMeta{
				Name:      gpuName,
				Namespace: "default",
			},
			Spec: &pb.GpuSpec{
				Uuid: "GPU-TEST-1",
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateGpu failed: %v", err)
	}
	if created.GetMetadata().GetName() != gpuName {
		t.Errorf("expected name %q, got %q", gpuName, created.GetMetadata().GetName())
	}

	resp, err := client.GetGpu(ctx, &pb.GetGpuRequest{
		Name:      gpuName,
		Namespace: "default",
	})
	if err != nil {
		t.Fatalf("GetGpu failed: %v", err)
	}
	if resp.GetGpu().GetSpec().GetUuid() != "GPU-TEST-1" {
		t.Errorf("expected UUID %q, got %q", "GPU-TEST-1", resp.GetGpu().GetSpec().GetUuid())
	}
}
