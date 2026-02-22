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

package nvml

import (
	"fmt"
	"os"
	"path/filepath"
)

// Condition constants for NVML provider.
const (
	// ConditionTypeNVMLReady is the condition type for NVML health status.
	ConditionTypeNVMLReady = "NVMLReady"

	// ConditionSourceNVML is the source identifier for conditions set by NVML provider.
	ConditionSourceNVML = "nvml-provider"

	// ConditionStatusTrue indicates the condition is met.
	ConditionStatusTrue = "True"

	// ConditionStatusFalse indicates the condition is not met.
	ConditionStatusFalse = "False"

	// ConditionStatusUnknown indicates the condition status is unknown.
	ConditionStatusUnknown = "Unknown"
)

// FormatBytes formats bytes to a human-readable string.
func FormatBytes(bytes uint64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)

	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.1f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.1f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// FindDriverLibrary locates the NVML library in the driver root.
//
// It searches common paths where libnvidia-ml.so.1 might be located.
// Returns empty string if not found (will use system default).
func FindDriverLibrary(driverRoot string) string {
	if driverRoot == "" {
		return ""
	}

	searchPaths := []string{
		filepath.Join(driverRoot, "usr/lib64/libnvidia-ml.so.1"),
		filepath.Join(driverRoot, "usr/lib/x86_64-linux-gnu/libnvidia-ml.so.1"),
		filepath.Join(driverRoot, "usr/lib/libnvidia-ml.so.1"),
		filepath.Join(driverRoot, "lib64/libnvidia-ml.so.1"),
		filepath.Join(driverRoot, "lib/libnvidia-ml.so.1"),
	}

	for _, path := range searchPaths {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	return ""
}
