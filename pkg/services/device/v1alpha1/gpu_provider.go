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
	"fmt"

	devicev1alpha1 "github.com/nvidia/nvsentinel/api/device/v1alpha1"
	pb "github.com/nvidia/nvsentinel/internal/generated/device/v1alpha1"
	"github.com/nvidia/nvsentinel/pkg/controlplane/apiserver/api"
	"github.com/nvidia/nvsentinel/pkg/controlplane/apiserver/registry"
	"github.com/nvidia/nvsentinel/pkg/storage/memory"
	"google.golang.org/grpc"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apiserver/pkg/storage/storagebackend"
)

func init() {
	registry.Register(NewGPUServiceProvider())
}

type gpuServiceProvider struct {
	groupVersion schema.GroupVersion
}

// NewGPUServiceProvider returns a ServiceProvider that installs the GPU gRPC service.
func NewGPUServiceProvider() api.ServiceProvider {
	return &gpuServiceProvider{
		groupVersion: devicev1alpha1.SchemeGroupVersion,
	}
}

// Install creates the in-memory storage backend and registers the GPU service
// on the provided gRPC server.
func (p *gpuServiceProvider) Install(svr *grpc.Server, cfg storagebackend.Config) (api.Service, error) {
	// Currently only in-memory storage is supported. The cfg parameter is
	// accepted for future extensibility but not used for backend selection.
	_ = cfg

	gv := p.groupVersion.String()

	scheme := runtime.NewScheme()
	if err := devicev1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add %q to scheme: %w", gv, err)
	}

	codecs := serializer.NewCodecFactory(scheme)
	info, ok := runtime.SerializerInfoForMediaType(codecs.SupportedMediaTypes(), runtime.ContentTypeJSON)
	if !ok {
		return nil, fmt.Errorf("no serializer found for %s in %s", runtime.ContentTypeJSON, gv)
	}
	codec := codecs.CodecForVersions(info.Serializer, info.Serializer, schema.GroupVersions{p.groupVersion}, schema.GroupVersions{p.groupVersion})

	s, destroyFunc, err := memory.CreateStorage(codec)
	if err != nil {
		return nil, fmt.Errorf("failed to create in-memory storage for %s: %w", gv, err)
	}

	service := NewGPUService(s, destroyFunc)
	pb.RegisterGpuServiceServer(svr, service)

	return service, nil
}
