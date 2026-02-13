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

package version

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGet(t *testing.T) {
	info := Get()

	if info.Version != Version {
		t.Errorf("expected Version %s, got %s", Version, info.Version)
	}

	if info.GoVersion == "" || info.Platform == "" {
		t.Error("runtime info (GoVersion/Platform) should not be empty")
	}
}

func TestUserAgent(t *testing.T) {
	ua := UserAgent()
	expectedPrefix := "nvidia-device-api/" + Version

	if !strings.HasPrefix(ua, expectedPrefix) {
		t.Errorf("UserAgent %s does not start with %s", ua, expectedPrefix)
	}
}

func TestHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	w := httptest.NewRecorder()

	Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", ct)
	}

	var info Info
	if err := json.NewDecoder(w.Body).Decode(&info); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	if info.Version != Version {
		t.Errorf("expected version %s in response, got %s", Version, info.Version)
	}
}
