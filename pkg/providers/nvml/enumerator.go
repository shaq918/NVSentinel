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

//go:build nvml

package nvml

import (
	"fmt"
	"time"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	devicev1alpha1 "github.com/nvidia/nvsentinel/api/device/v1alpha1"
)

// enumerateDevices discovers all GPUs via NVML and registers them via gRPC.
//
// For each GPU found, it extracts device information and creates a GPU entry
// via the GpuService API with an initial "NVMLReady" condition set to True.
//
// Returns the number of GPUs discovered.
func (p *Provider) enumerateDevices() (int, error) {
	count, ret := p.nvmllib.DeviceGetCount()
	if ret != nvml.SUCCESS {
		return 0, fmt.Errorf("failed to get device count: %v", nvml.ErrorString(ret))
	}

	if count == 0 {
		p.logger.Info("No GPUs found on this node")
		return 0, nil
	}

	p.logger.V(1).Info("Enumerating GPUs", "count", count)

	successCount := 0
	uuids := make([]string, 0, count)

	for i := 0; i < count; i++ {
		device, ret := p.nvmllib.DeviceGetHandleByIndex(i)
		if ret != nvml.SUCCESS {
			p.logger.Error(nil, "Failed to get device handle", "index", i, "error", nvml.ErrorString(ret))

			continue
		}

		gpu, productName, memoryBytes, err := p.deviceToGpu(i, device)
		if err != nil {
			p.logger.Error(err, "Failed to get GPU info", "index", i)

			continue
		}

		// Register GPU via typed client (Create is idempotent -- returns existing GPU if already registered)
		_, err = p.client.Create(p.ctx, gpu, metav1.CreateOptions{})
		if err != nil {
			p.logger.Error(err, "Failed to create GPU via gRPC", "uuid", gpu.Name)

			continue
		}

		// Track UUID for health monitoring
		uuids = append(uuids, gpu.Name)

		p.logger.Info("GPU registered",
			"uuid", gpu.Name,
			"productName", productName,
			"memory", FormatBytes(memoryBytes),
		)

		successCount++
	}

	// Assign tracked UUIDs atomically (caller holds p.mu)
	p.gpuUUIDs = uuids

	return successCount, nil
}

// deviceToGpu extracts GPU information from an NVML device handle.
// Returns the GPU object, product name, and memory bytes (for logging).
func (p *Provider) deviceToGpu(index int, device Device) (*devicev1alpha1.GPU, string, uint64, error) {
	// Get UUID (required)
	uuid, ret := device.GetUUID()
	if ret != nvml.SUCCESS {
		return nil, "", 0, fmt.Errorf("failed to get UUID: %v", nvml.ErrorString(ret))
	}

	// Get memory info (for logging)
	var memoryBytes uint64

	memInfo, ret := device.GetMemoryInfo()
	if ret == nvml.SUCCESS {
		memoryBytes = memInfo.Total
	}

	// Get product name (for logging)
	productName, ret := device.GetName()
	if ret != nvml.SUCCESS {
		productName = "Unknown"
	}

	// Build GPU object using K8s-native types
	now := metav1.Now()
	gpu := &devicev1alpha1.GPU{
		ObjectMeta: metav1.ObjectMeta{
			Name: uuid,
		},
		Spec: devicev1alpha1.GPUSpec{
			UUID: uuid,
		},
		Status: devicev1alpha1.GPUStatus{
			Conditions: []metav1.Condition{
				{
					Type:               ConditionTypeNVMLReady,
					Status:             metav1.ConditionStatus(ConditionStatusTrue),
					Reason:             "Initialized",
					Message:            fmt.Sprintf("GPU enumerated via NVML: %s (%s)", productName, FormatBytes(memoryBytes)),
					LastTransitionTime: now,
				},
			},
		},
	}

	return gpu, productName, memoryBytes, nil
}

// UpdateCondition updates a single condition on a GPU via the typed client.
//
// This method:
// 1. Gets the current GPU state
// 2. Updates/adds the condition in the status
// 3. Sends the updated status via UpdateStatus (status subresource)
//
// The condition's LastTransitionTime is set to the current time.
func (p *Provider) UpdateCondition(
	uuid string,
	conditionType string,
	conditionStatus string,
	reason, message string,
) error {
	// Get current GPU state
	gpu, err := p.client.Get(p.ctx, uuid, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get GPU %s: %w", uuid, err)
	}

	if gpu == nil {
		return fmt.Errorf("Get returned nil for %s", uuid)
	}

	// Build the new condition
	condition := metav1.Condition{
		Type:               conditionType,
		Status:             metav1.ConditionStatus(conditionStatus),
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.NewTime(time.Now()),
	}

	// Find and replace existing condition, or append
	found := false
	for i, existing := range gpu.Status.Conditions {
		if existing.Type == conditionType {
			gpu.Status.Conditions[i] = condition
			found = true
			break
		}
	}
	if !found {
		gpu.Status.Conditions = append(gpu.Status.Conditions, condition)
	}

	// Cap conditions to prevent unbounded growth
	const maxConditions = 100
	if len(gpu.Status.Conditions) > maxConditions {
		gpu.Status.Conditions = gpu.Status.Conditions[len(gpu.Status.Conditions)-maxConditions:]
	}

	// Update the GPU status via the status subresource
	_, err = p.client.UpdateStatus(p.ctx, gpu, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update GPU status %s: %w", uuid, err)
	}

	return nil
}
