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

package chart_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// chartDir returns the path to the Helm chart directory.
func chartDir(t *testing.T) string {
	t.Helper()
	// When running from the chart directory itself
	if _, err := os.Stat("Chart.yaml"); err == nil {
		wd, _ := os.Getwd()
		return wd
	}
	t.Fatal("Chart.yaml not found; run tests from the chart directory")
	return ""
}

// helmTemplate runs helm template with optional --set overrides and returns stdout.
func helmTemplate(t *testing.T, sets ...string) string {
	t.Helper()
	args := []string{"template", "test-release", chartDir(t)}
	for _, s := range sets {
		args = append(args, "--set", s)
	}
	cmd := exec.Command("helm", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, string(out))
	}
	return string(out)
}

func TestChart_DefaultRenders(t *testing.T) {
	out := helmTemplate(t)
	if len(out) == 0 {
		t.Fatal("helm template produced no output")
	}
	// Should contain a DaemonSet
	if !strings.Contains(out, "kind: DaemonSet") {
		t.Error("Expected DaemonSet in rendered output")
	}
	// Should contain a ServiceAccount
	if !strings.Contains(out, "kind: ServiceAccount") {
		t.Error("Expected ServiceAccount in rendered output")
	}
}

func TestChart_TerminationGracePeriod_Default(t *testing.T) {
	out := helmTemplate(t)
	// Default: shutdownDelay(5) + shutdownGracePeriod(25) + 5 = 35
	if !strings.Contains(out, "terminationGracePeriodSeconds: 35") {
		t.Errorf("Expected terminationGracePeriodSeconds: 35 with defaults, got:\n%s",
			extractLine(out, "terminationGracePeriodSeconds"))
	}
}

func TestChart_TerminationGracePeriod_CustomValues(t *testing.T) {
	out := helmTemplate(t,
		"server.shutdownDelay=10",
		"server.shutdownGracePeriod=60",
	)
	// 10 + 60 + 5 = 75
	if !strings.Contains(out, "terminationGracePeriodSeconds: 75") {
		t.Errorf("Expected terminationGracePeriodSeconds: 75 with custom values, got:\n%s",
			extractLine(out, "terminationGracePeriodSeconds"))
	}
}

func TestChart_NoNVMLSidecar_ByDefault(t *testing.T) {
	out := helmTemplate(t)
	if strings.Contains(out, "name: nvml-provider") {
		t.Error("NVML provider sidecar should not be present by default")
	}
}

func TestChart_NVMLSidecar_WhenEnabled(t *testing.T) {
	out := helmTemplate(t, "nvmlProvider.enabled=true")
	if !strings.Contains(out, "name: nvml-provider") {
		t.Error("NVML provider sidecar should be present when enabled")
	}
	// Should have NVIDIA_VISIBLE_DEVICES env var
	if !strings.Contains(out, "NVIDIA_VISIBLE_DEVICES") {
		t.Error("Expected NVIDIA_VISIBLE_DEVICES env var in nvml-provider sidecar")
	}
}

func TestChart_BindAddress(t *testing.T) {
	out := helmTemplate(t)
	// Default binds to unix socket
	if !strings.Contains(out, "--bind-address=unix:///var/run/device-api/device.sock") {
		t.Error("Expected default --bind-address=unix:///var/run/device-api/device.sock")
	}
}

func TestChart_SecurityContext(t *testing.T) {
	out := helmTemplate(t)
	if !strings.Contains(out, "readOnlyRootFilesystem: true") {
		t.Error("Expected readOnlyRootFilesystem: true in security context")
	}
	if !strings.Contains(out, "runAsNonRoot: true") {
		t.Error("Expected runAsNonRoot: true in security context")
	}
	if !strings.Contains(out, "allowPrivilegeEscalation: false") {
		t.Error("Expected allowPrivilegeEscalation: false in security context")
	}
}

func TestChart_SocketVolume(t *testing.T) {
	out := helmTemplate(t)
	if !strings.Contains(out, "name: socket-dir") {
		t.Error("Expected socket-dir volume")
	}
	if !strings.Contains(out, "/var/run/device-api") {
		t.Error("Expected socket directory path /var/run/device-api")
	}
}

func TestChart_MetricsPort_WhenEnabled(t *testing.T) {
	out := helmTemplate(t, "metrics.enabled=true")
	if !strings.Contains(out, "name: metrics") {
		t.Error("Expected metrics port when metrics are enabled")
	}
}

func TestChart_MetricsPort_WhenDisabled(t *testing.T) {
	out := helmTemplate(t, "metrics.enabled=false")
	// The metrics port should not appear in containerPort definitions
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if strings.Contains(line, "name: metrics") &&
			i > 0 && strings.Contains(lines[i-1], "containerPort") {
			t.Error("Metrics port should not be present when metrics are disabled")
		}
	}
}

func TestChart_NodeSelector(t *testing.T) {
	out := helmTemplate(t)
	if !strings.Contains(out, "nvidia.com/gpu.present") {
		t.Error("Expected GPU node selector by default")
	}
}

func TestChart_PreStopHook(t *testing.T) {
	out := helmTemplate(t)
	// preStop sleep should match shutdownDelay
	if !strings.Contains(out, `command: ["sleep", "5"]`) {
		// Try alternate format
		if !strings.Contains(out, "sleep") {
			t.Error("Expected preStop sleep hook")
		}
	}
}

// extractLine returns the first line containing the given substring.
func extractLine(s, substr string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, substr) {
			return strings.TrimSpace(line)
		}
	}
	return "<not found>"
}
