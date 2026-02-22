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

package memory

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apiserver/pkg/storage"
)

func TestCreateStorage(t *testing.T) {
	s, destroy, err := CreateStorage(codec)
	if err != nil {
		t.Fatalf("CreateStorage failed: %v", err)
	}
	defer destroy()

	if s == nil {
		t.Fatal("expected non-nil storage.Interface")
	}

	// Verify it's functional by doing a basic Create + Get.
	ctx := context.Background()
	obj := newTestObject("factory-gpu", "default")
	if err := s.Create(ctx, "/test/factory-gpu", obj, nil, 0); err != nil {
		t.Fatalf("Create via factory storage failed: %v", err)
	}

	got := &unstructured.Unstructured{}
	if err := s.Get(ctx, "/test/factory-gpu", storage.GetOptions{}, got); err != nil {
		t.Fatalf("Get via factory storage failed: %v", err)
	}

	if got.GetName() != "factory-gpu" {
		t.Errorf("expected name factory-gpu, got %s", got.GetName())
	}
}

func TestCreateStorage_DestroyIsIdempotent(t *testing.T) {
	_, destroy, err := CreateStorage(codec)
	if err != nil {
		t.Fatal(err)
	}

	// Should not panic when called multiple times.
	destroy()
	destroy()
}
