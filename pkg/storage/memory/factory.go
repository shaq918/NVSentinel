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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/storage"
	"k8s.io/apiserver/pkg/storage/storagebackend/factory"
)

// CreateStorage returns a new in-memory storage.Interface, a DestroyFunc, and any error.
// This mirrors the signature of storagebackend/factory.Create() so it can be
// used as a drop-in replacement in ServiceProvider.Install().
func CreateStorage(codec runtime.Codec) (storage.Interface, factory.DestroyFunc, error) {
	store := NewStore(codec)
	destroy := func() {
		// No resources to release for in-memory storage.
	}
	return store, destroy, nil
}
