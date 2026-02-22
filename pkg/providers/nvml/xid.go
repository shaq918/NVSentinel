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
	"strconv"
	"strings"
)

// XID errors documentation:
// https://docs.nvidia.com/deploy/xid-errors/index.html

// defaultIgnoredXids contains XID error codes that are typically caused by
// application errors rather than hardware failures. These are ignored by
// default to avoid false positives in health monitoring.
//
// Reference: https://docs.nvidia.com/deploy/xid-errors/index.html#topic_4
var defaultIgnoredXids = map[uint64]bool{
	// Application errors - GPU should still be healthy
	13:  true, // Graphics Engine Exception
	31:  true, // GPU memory page fault
	43:  true, // GPU stopped processing
	45:  true, // Preemptive cleanup, due to previous errors
	68:  true, // Video processor exception
	109: true, // Context Switch Timeout Error
}

// criticalXids contains XID error codes that indicate critical hardware
// failures requiring immediate attention.
var criticalXids = map[uint64]bool{
	// Memory errors
	48: true, // Double Bit ECC Error
	63: true, // Row remapping failure
	64: true, // Uncontained ECC error
	74: true, // NVLink error
	79: true, // GPU has fallen off the bus

	// Fatal errors
	94:  true, // Contained ECC error (severe)
	95:  true, // Uncontained ECC error
	119: true, // GSP (GPU System Processor) error
	120: true, // GSP firmware error
}

// XidDescriptions provides human-readable descriptions for common XIDs.
var XidDescriptions = map[uint64]string{
	// Application errors (typically ignored)
	13:  "Graphics Engine Exception",
	31:  "GPU memory page fault",
	43:  "GPU stopped processing",
	45:  "Preemptive cleanup",
	68:  "Video processor exception",
	109: "Context Switch Timeout",

	// Memory errors
	48: "Double Bit ECC Error",
	63: "Row remapping failure",
	64: "Uncontained ECC error",
	74: "NVLink error",
	79: "GPU has fallen off the bus",
	94: "Contained ECC error",
	95: "Uncontained ECC error",

	// Other notable XIDs
	8:   "GPU not accessible",
	32:  "Invalid or corrupted push buffer stream",
	38:  "Driver firmware error",
	56:  "Display engine error",
	57:  "Error programming video memory interface",
	62:  "Internal micro-controller halt (non-fatal)",
	69:  "Graphics engine accessor error",
	119: "GSP error",
	120: "GSP firmware error",
}

// IsDefaultIgnored returns true if the XID is in the default ignored set.
func IsDefaultIgnored(xid uint64) bool {
	return defaultIgnoredXids[xid]
}

// IsCritical returns true if the XID is in the critical set.
func IsCritical(xid uint64) bool {
	return criticalXids[xid]
}

// DefaultIgnoredXidsList returns a copy of the default ignored XID set.
func DefaultIgnoredXidsList() map[uint64]bool {
	out := make(map[uint64]bool, len(defaultIgnoredXids))
	for k, v := range defaultIgnoredXids {
		out[k] = v
	}
	return out
}

// isIgnoredXid returns true if the XID should be ignored for health purposes.
//
// An XID is ignored if it's in the default ignored list OR in the additional
// ignored map provided by the user. The map is built once at provider startup
// from the config slice for O(1) lookup.
func isIgnoredXid(xid uint64, additionalIgnored map[uint64]bool) bool {
	if defaultIgnoredXids[xid] {
		return true
	}

	return additionalIgnored[xid]
}

// IsCriticalXid returns true if the XID indicates a critical hardware failure.
func IsCriticalXid(xid uint64) bool {
	return criticalXids[xid]
}

// xidToString returns a human-readable description for an XID.
func xidToString(xid uint64) string {
	if desc, ok := XidDescriptions[xid]; ok {
		return desc
	}

	return "Unknown XID"
}

// ParseIgnoredXids parses a comma-or-space-separated string of XID values.
// Non-numeric tokens are silently skipped.
func ParseIgnoredXids(input string) []uint64 {
	if input == "" {
		return nil
	}

	var result []uint64

	tokens := strings.FieldsFunc(input, func(r rune) bool {
		return r == ',' || r == ' '
	})

	for _, tok := range tokens {
		v, err := strconv.ParseUint(tok, 10, 64)
		if err != nil {
			continue
		}

		result = append(result, v)
	}

	if len(result) == 0 {
		return nil
	}

	return result
}

// XidSeverity represents the severity level of an XID error.
type XidSeverity int

const (
	// XidSeverityUnknown indicates the XID severity is unknown.
	XidSeverityUnknown XidSeverity = iota
	// XidSeverityIgnored indicates the XID is typically caused by applications.
	XidSeverityIgnored
	// XidSeverityWarning indicates the XID may indicate a problem.
	XidSeverityWarning
	// XidSeverityCritical indicates the XID indicates a critical hardware failure.
	XidSeverityCritical
)

// Severity string constants.
const (
	severityUnknown  = "unknown"
	severityIgnored  = "ignored"
	severityWarning  = "warning"
	severityCritical = "critical"
)

// GetXidSeverity returns the severity level for an XID.
func GetXidSeverity(xid uint64) XidSeverity {
	if defaultIgnoredXids[xid] {
		return XidSeverityIgnored
	}

	if criticalXids[xid] {
		return XidSeverityCritical
	}

	// XIDs not in either list are treated as warnings
	return XidSeverityWarning
}

// String returns a string representation of XidSeverity.
func (s XidSeverity) String() string {
	switch s {
	case XidSeverityUnknown:
		return severityUnknown
	case XidSeverityIgnored:
		return severityIgnored
	case XidSeverityWarning:
		return severityWarning
	case XidSeverityCritical:
		return severityCritical
	default:
		return severityUnknown
	}
}
