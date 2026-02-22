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
	"fmt"
	"path"
	"reflect"
	"regexp"

	devicev1alpha1 "github.com/nvidia/nvsentinel/api/device/v1alpha1"
	pb "github.com/nvidia/nvsentinel/internal/generated/device/v1alpha1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/storage"
	"k8s.io/apiserver/pkg/storage/storagebackend/factory"
	"k8s.io/klog/v2"
)

type gpuService struct {
	pb.UnimplementedGpuServiceServer
	storage     storage.Interface
	destroyFunc factory.DestroyFunc
}

// NewGPUService creates a new GPU gRPC service backed by the provided storage.
func NewGPUService(storage storage.Interface, destroyFunc factory.DestroyFunc) *gpuService {
	return &gpuService{
		storage:     storage,
		destroyFunc: destroyFunc,
	}
}

// Name returns the fully qualified gRPC service name.
func (s *gpuService) Name() string {
	return pb.GpuService_ServiceDesc.ServiceName
}

// IsReady reports whether the underlying storage backend is healthy.
func (s *gpuService) IsReady() bool {
	if s.storage == nil {
		return false
	}
	return s.storage.ReadinessCheck() == nil
}

// Cleanup shuts down the storage backend.
func (s *gpuService) Cleanup() {
	if s.destroyFunc != nil {
		klog.V(2).InfoS("Shutting down storage backend", "service", s.Name())
		s.destroyFunc()
	}
}

// normalizeNamespace returns "default" if ns is empty.
func normalizeNamespace(ns string) string {
	if ns == "" {
		return "default"
	}
	return ns
}

// validateNamespace checks that ns does not exceed the K8s maximum namespace length.
// An empty namespace is valid (it defaults to "default" elsewhere).
func validateNamespace(ns string) error {
	if ns == "" {
		return nil
	}
	if len(ns) > 253 { // K8s namespace max length
		return status.Error(codes.InvalidArgument, "namespace exceeds maximum length of 253 characters")
	}
	return nil
}

// gpuUUIDPattern matches NVIDIA GPU UUIDs
// (e.g., GPU-12345678-1234-1234-1234-123456789abc).
var gpuUUIDPattern = regexp.MustCompile(
	`^GPU-[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-` +
		`[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`,
)

// validateGPUName checks that name is non-empty and matches
// the NVIDIA GPU UUID format.
func validateGPUName(name string) error {
	if name == "" {
		return status.Error(codes.InvalidArgument, "name is required")
	}

	if !gpuUUIDPattern.MatchString(name) {
		return status.Errorf(codes.InvalidArgument,
			"name must be a valid GPU UUID "+
				"(GPU-xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx), got %q",
			name)
	}

	return nil
}

func (s *gpuService) storageKey(ns string, name string) string {
	base := path.Join("/", devicev1alpha1.SchemeGroupVersion.Group, "gpus")
	if name != "" {
		ns = normalizeNamespace(ns)
	}
	return path.Join(base, ns, name)
}

// GetGpu retrieves a single GPU resource.
func (s *gpuService) GetGpu(ctx context.Context, req *pb.GetGpuRequest) (*pb.GetGpuResponse, error) {
	logger := klog.FromContext(ctx)

	if err := validateGPUName(req.GetName()); err != nil {
		return nil, err
	}
	if err := validateNamespace(req.GetNamespace()); err != nil {
		return nil, err
	}

	key := s.storageKey(req.GetNamespace(), req.GetName())
	opts := storage.GetOptions{
		ResourceVersion: req.GetOpts().GetResourceVersion(),
	}

	gpu := &devicev1alpha1.GPU{}
	if err := s.storage.Get(ctx, key, opts, gpu); err != nil {
		if storage.IsNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "GPU %q not found", req.GetName())
		}
		logger.V(3).Error(err, "storage backend error during Get", "key", key)
		return nil, status.Error(codes.Internal, "internal server error")
	}

	logger.V(4).Info("Retrieved GPU", "name", req.GetName(), "namespace", req.GetNamespace())

	return &pb.GetGpuResponse{
		Gpu: devicev1alpha1.ToProto(gpu),
	}, nil
}

// ListGpus retrieves a list of GPU resources.
func (s *gpuService) ListGpus(ctx context.Context, req *pb.ListGpusRequest) (*pb.ListGpusResponse, error) {
	logger := klog.FromContext(ctx)

	if err := validateNamespace(req.GetNamespace()); err != nil {
		return nil, err
	}

	var gpus devicev1alpha1.GPUList

	opts := storage.ListOptions{
		ResourceVersion: req.GetOpts().GetResourceVersion(),
		Recursive:       true,
		Predicate:       storage.Everything,
	}

	key := s.storageKey(req.GetNamespace(), "")
	if err := s.storage.GetList(ctx, key, opts, &gpus); err != nil {
		if storage.IsNotFound(err) {
			rv, _ := s.storage.GetCurrentResourceVersion(ctx)
			rvStr := fmt.Sprintf("%d", rv)
			if rv == 0 {
				rvStr = req.GetOpts().GetResourceVersion()
			}
			return &pb.ListGpusResponse{
				GpuList: &pb.GpuList{
					Metadata: &pb.ListMeta{
						ResourceVersion: rvStr,
					},
					Items: []*pb.Gpu{},
				},
			}, nil
		}
		logger.V(3).Error(err, "storage backend error during List", "key", key)
		return nil, status.Error(codes.Internal, "internal server error")
	}

	logger.V(4).Info("Listed GPUs",
		"namespace", req.GetNamespace(),
		"count", len(gpus.Items),
		"resourceVersion", gpus.GetListMeta().GetResourceVersion(),
	)

	return &pb.ListGpusResponse{
		GpuList: devicev1alpha1.ToProtoList(&gpus),
	}, nil
}

// WatchGpus streams lifecycle events for GPU resources.
func (s *gpuService) WatchGpus(req *pb.WatchGpusRequest, stream grpc.ServerStreamingServer[pb.WatchGpusResponse]) error {
	ctx := stream.Context()
	logger := klog.FromContext(ctx)

	ns := req.GetNamespace()
	rv := req.GetOpts().GetResourceVersion()

	key := s.storageKey(req.GetNamespace(), "")
	w, err := s.storage.Watch(ctx, key, storage.ListOptions{
		ResourceVersion: rv,
		Recursive:       true,
		Predicate:       storage.Everything,
	})
	if err != nil {
		if storage.IsInvalidError(err) {
			return status.Errorf(codes.OutOfRange, "%v", err)
		}
		logger.Error(err, "failed to initialize storage watch", "key", key)
		return status.Error(codes.Internal, "internal server error")
	}
	defer w.Stop()

	logger.V(3).Info("Started watch stream", "namespace", ns, "resourceVersion", rv)

	for {
		select {
		case <-ctx.Done():
			logger.V(3).Info("Watch stream closed by client", "namespace", ns)
			return ctx.Err()
		case event, ok := <-w.ResultChan():
			if !ok {
				logger.V(3).Info("Watch stream closed by storage backend", "namespace", ns)
				return nil
			}

			if event.Type == watch.Error {
				if statusObj, ok := event.Object.(*metav1.Status); ok {
					if statusObj.Code == 410 || statusObj.Reason == metav1.StatusReasonExpired {
						logger.V(4).Info("Watch stream expired", "namespace", ns, "resourceVersion", rv)
						return status.Errorf(codes.OutOfRange, "required resource version %s is too old: %s", rv, statusObj.Message)
					}
					logger.Error(nil, "watch stream storage status error", "status", statusObj.Message)
					return status.Error(codes.Internal, "internal server error")
				}

				if errObj, ok := event.Object.(error); ok && storage.IsInvalidError(errObj) {
					return status.Errorf(codes.OutOfRange, "%v", errObj)
				}
				logger.Error(nil, "unexpected storage error during watch", "object", event.Object)
				return status.Error(codes.Internal, "internal server error")
			}

			obj, ok := event.Object.(*devicev1alpha1.GPU)
			if !ok {
				logger.V(4).Info("Watch received unexpected object type", "type", reflect.TypeOf(event.Object))
				continue
			}

			logger.V(6).Info("Sending watch event",
				"type", event.Type,
				"name", obj.Name,
				"resourceVersion", obj.ResourceVersion,
			)

			resp := &pb.WatchGpusResponse{
				Type:   string(event.Type),
				Object: devicev1alpha1.ToProto(obj),
			}
			if err := stream.Send(resp); err != nil {
				logger.V(3).Info("Watch stream send error (client likely disconnected)", "err", err)
				return err
			}
		}
	}
}

// CreateGpu creates a single GPU resource.
func (s *gpuService) CreateGpu(ctx context.Context, req *pb.CreateGpuRequest) (*pb.Gpu, error) {
	logger := klog.FromContext(ctx)

	if req.GetGpu() == nil {
		return nil, status.Error(codes.InvalidArgument, "resource body is required")
	}
	if req.GetGpu().GetMetadata() == nil {
		return nil, status.Error(codes.InvalidArgument, "metadata.name: Required value")
	}
	if err := validateGPUName(req.GetGpu().GetMetadata().GetName()); err != nil {
		return nil, err
	}

	name := req.GetGpu().GetMetadata().GetName()
	ns := normalizeNamespace(req.GetGpu().GetMetadata().GetNamespace())
	key := s.storageKey(ns, name)

	gpu := devicev1alpha1.FromProto(req.Gpu)
	gpu.SetNamespace(ns)
	gpu.SetUID(uuid.NewUUID())
	now := metav1.Now()
	gpu.SetCreationTimestamp(now)
	gpu.SetGeneration(1)
	for i := range gpu.Status.Conditions {
		if gpu.Status.Conditions[i].LastTransitionTime.IsZero() {
			gpu.Status.Conditions[i].LastTransitionTime = now
		}
	}

	out := &devicev1alpha1.GPU{}
	if err := s.storage.Create(ctx, key, gpu, out, 0); err != nil {
		logger.Error(err, "Failed to create GPU", "name", name, "namespace", ns)
		if storage.IsExist(err) {
			return nil, status.Errorf(codes.AlreadyExists, "GPU %q already exists", req.GetGpu().GetMetadata().GetName())
		}
		return nil, status.Error(codes.Internal, "internal server error")
	}

	logger.V(2).Info("Successfully created GPU", "name", name, "namespace", ns, "uid", out.UID)

	return devicev1alpha1.ToProto(out), nil
}

// UpdateGpu updates a single GPU resource (spec only).
func (s *gpuService) UpdateGpu(ctx context.Context, req *pb.UpdateGpuRequest) (*pb.Gpu, error) {
	logger := klog.FromContext(ctx)

	if req.GetGpu() == nil {
		return nil, status.Error(codes.InvalidArgument, "resource body is required")
	}
	if req.GetGpu().GetMetadata() == nil {
		return nil, status.Error(codes.InvalidArgument, "metadata.name: Required value")
	}
	if err := validateGPUName(req.GetGpu().GetMetadata().GetName()); err != nil {
		return nil, err
	}

	name := req.GetGpu().GetMetadata().GetName()
	ns := req.GetGpu().GetMetadata().GetNamespace()
	key := s.storageKey(ns, name)
	updatedGpu := &devicev1alpha1.GPU{}

	err := s.storage.GuaranteedUpdate(
		ctx,
		key,
		updatedGpu,
		false,
		nil,
		func(input runtime.Object, res storage.ResponseMeta) (runtime.Object, *uint64, error) {
			curr := input.(*devicev1alpha1.GPU)
			incoming := devicev1alpha1.FromProto(req.GetGpu())

			if incoming.ResourceVersion != "" && incoming.ResourceVersion != curr.ResourceVersion {
				return nil, nil, storage.NewResourceVersionConflictsError(key, 0)
			}

			if incoming.UID != "" && incoming.UID != curr.UID {
				return nil, nil, status.Errorf(codes.InvalidArgument,
					"GPU %q is invalid: metadata.uid: field is immutable", name)
			}

			if incoming.Namespace != "" && incoming.Namespace != curr.Namespace {
				return nil, nil, status.Errorf(codes.InvalidArgument,
					"GPU %q is invalid: metadata.namespace: field is immutable", name)
			}

			if reflect.DeepEqual(curr.Spec, incoming.Spec) {
				return curr, nil, nil
			}

			clone := curr.DeepCopy()
			clone.Spec = incoming.Spec
			clone.Generation++

			return clone, nil, nil
		},
		nil,
	)

	if err != nil {
		if storage.IsNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "GPU %q not found", req.GetGpu().GetMetadata().GetName())
		}
		if storage.IsConflict(err) {
			logger.V(3).Info("Update conflict", "name", name, "namespace", ns, "err", err)
			return nil, status.Errorf(codes.Aborted,
				"operation cannot be fulfilled on GPUs %q: the object has been modified; please apply your changes to the latest version and try again", name)
		}
		logger.Error(err, "failed to update GPU", "name", name, "namespace", ns)
		return nil, status.Error(codes.Internal, "internal server error")
	}

	logger.V(2).Info("Successfully updated GPU",
		"name", name,
		"namespace", ns,
		"resourceVersion", updatedGpu.ResourceVersion,
		"generation", updatedGpu.Generation,
	)

	return devicev1alpha1.ToProto(updatedGpu), nil
}

// UpdateGpuStatus updates only the status subresource of a GPU.
func (s *gpuService) UpdateGpuStatus(ctx context.Context, req *pb.UpdateGpuStatusRequest) (*pb.Gpu, error) {
	logger := klog.FromContext(ctx)

	if req.GetGpu() == nil {
		return nil, status.Error(codes.InvalidArgument, "resource body is required")
	}
	if req.GetGpu().GetMetadata() == nil {
		return nil, status.Error(codes.InvalidArgument, "metadata.name: Required value")
	}
	if err := validateGPUName(req.GetGpu().GetMetadata().GetName()); err != nil {
		return nil, err
	}
	if req.GetGpu().GetStatus() == nil {
		return nil, status.Error(codes.InvalidArgument, "status is required")
	}

	name := req.GetGpu().GetMetadata().GetName()
	ns := req.GetGpu().GetMetadata().GetNamespace()
	key := s.storageKey(ns, name)
	updatedGpu := &devicev1alpha1.GPU{}

	err := s.storage.GuaranteedUpdate(
		ctx,
		key,
		updatedGpu,
		false,
		nil,
		func(input runtime.Object, res storage.ResponseMeta) (runtime.Object, *uint64, error) {
			curr := input.(*devicev1alpha1.GPU)
			incoming := devicev1alpha1.FromProto(req.GetGpu())

			if incoming.ResourceVersion != "" && incoming.ResourceVersion != curr.ResourceVersion {
				return nil, nil, storage.NewResourceVersionConflictsError(key, 0)
			}

			clone := curr.DeepCopy()
			clone.Status = incoming.Status

			return clone, nil, nil
		},
		nil,
	)

	if err != nil {
		if storage.IsNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "GPU %q not found", name)
		}
		if storage.IsConflict(err) {
			return nil, status.Errorf(codes.Aborted,
				"operation cannot be fulfilled on GPUs %q: the object has been modified", name)
		}
		logger.Error(err, "failed to update GPU status", "name", name, "namespace", ns)
		return nil, status.Error(codes.Internal, "internal server error")
	}

	logger.V(2).Info("Successfully updated GPU status", "name", name, "namespace", ns, "resourceVersion", updatedGpu.ResourceVersion)

	return devicev1alpha1.ToProto(updatedGpu), nil
}

// DeleteGpu deletes a single GPU resource.
func (s *gpuService) DeleteGpu(ctx context.Context, req *pb.DeleteGpuRequest) (*emptypb.Empty, error) {
	logger := klog.FromContext(ctx)

	if err := validateGPUName(req.GetName()); err != nil {
		return nil, err
	}
	if err := validateNamespace(req.GetNamespace()); err != nil {
		return nil, err
	}

	name := req.GetName()
	ns := req.GetNamespace()
	key := s.storageKey(ns, name)
	out := &devicev1alpha1.GPU{}

	if err := s.storage.Delete(
		ctx,
		key,
		out,
		nil,
		storage.ValidateAllObjectFunc,
		nil,
		storage.DeleteOptions{},
	); err != nil {
		if storage.IsNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "GPU %q not found", name)
		}
		logger.Error(err, "Failed to delete GPU", "name", name, "namespace", ns)
		return nil, status.Error(codes.Internal, "internal server error")
	}

	logger.V(2).Info("Successfully deleted GPU",
		"name", name,
		"namespace", ns,
		"uid", out.UID,
		"resourceVersion", out.ResourceVersion,
	)

	return &emptypb.Empty{}, nil
}
