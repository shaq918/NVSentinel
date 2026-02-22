# NVIDIA Device API

The NVIDIA Device API provides a Kubernetes-idiomatic Go SDK and Protobuf definitions for interacting with NVIDIA device resources.

**Node-local GPU device state management for Kubernetes**

The NVIDIA Device API provides a standardized gRPC interface for observing and managing GPU device states in Kubernetes environments. It enables coordination between:

- **Providers** (health monitors like NVSentinel, DCGM) that detect GPU health issues
- **Consumers** (device plugins, DRA drivers) that need GPU health status for scheduling

## Overview

The Device API Server is a pure Go gRPC server with no hardware dependencies.
GPU enumeration and health monitoring is provided by external providers (sidecars).

```
┌─────────────────────────────────────────────────────────────┐
│                        GPU Node                              │
│                                                              │
│  ┌─────────────────────────────────────────────────────────┐│
│  │              Device API Server (DaemonSet)              ││
│  │                                                         ││
│  │  ┌─────────────────────────────────────────────────┐   ││
│  │  │               GpuService (unified)              │   ││
│  │  │  Read:  GetGpu, ListGpus, WatchGpus             │   ││
│  │  │  Write: CreateGpu, UpdateGpuStatus, DeleteGpu   │   ││
│  │  └────────────────────┬────────────────────────────┘   ││
│  │                       ▼                                 ││
│  │  ┌──────────────────────────────────────────────────┐  ││
│  │  │                 GPU Cache (RWMutex)               │  ││
│  │  └──────────────────────────────────────────────────┘  ││
│  └─────────────────────────────────────────────────────────┘│
│                                                              │
│  Providers (gRPC clients):                                   │
│  ├── nvml-provider sidecar ─► CreateGpu, UpdateGpuStatus    │
│  ├── NVSentinel ────────────► CreateGpu, UpdateGpuStatus    │
│  └── Custom providers ──────► CreateGpu, UpdateGpuStatus    │
│                                                              │
│  Consumers (gRPC clients):                                   │
│  ├── Device Plugins ────────► GetGpu, ListGpus, WatchGpus   │
│  └── DRA Drivers ───────────► GetGpu, ListGpus, WatchGpus   │
└─────────────────────────────────────────────────────────────┘
```

## Key Features

- **Pure Go server**: No hardware dependencies; providers run as separate sidecars
- **Read-blocking semantics**: Consumer reads block during provider updates to prevent stale data
- **Multiple provider support**: Aggregate health status from NVSentinel, DCGM, or custom providers
- **Watch streams**: Real-time GPU state change notifications
- **Prometheus metrics**: Full observability with alerting rules
- **Helm chart**: Production-ready Kubernetes deployment

## Repository Structure

| Module | Description |
| :--- | :--- |
| [`api/`](./api) | Protobuf definitions and Go types for the Device API. |
| [`client-go/`](./client-go) | Kubernetes-style generated clients, informers, and listers. |
| [`code-generator/`](./code-generator) | Tools for generating NVIDIA-specific client logic. |
| [`cmd/device-api-server/`](./cmd/device-api-server) | Device API Server binary |
| [`pkg/deviceapiserver/`](./pkg/deviceapiserver) | Server implementation |
| [`charts/`](./charts) | Helm chart for Kubernetes deployment |

---

## Quick Start

### Deploy Device API Server

```bash
# Install with Helm
helm install device-api-server ./deployments/helm/device-api-server \
  --namespace device-api --create-namespace
```

For GPU enumeration and health monitoring, deploy the nvml-provider sidecar.
See the [nvml-sidecar demo](demos/nvml-sidecar-demo.sh) for an example deployment.

### Using the Go Client

```bash
go get github.com/nvidia/device-api/api@latest
```

```go
import (
    v1alpha1 "github.com/nvidia/device-api/api/gen/go/device/v1alpha1"
)
```

### Example: List GPUs

```go
package main

import (
    "context"
    "log"

    v1alpha1 "github.com/nvidia/device-api/api/gen/go/device/v1alpha1"
    "google.golang.org/grpc"
    "google.golang.org/grpc/credentials/insecure"
)

func main() {
    // Connect via Unix socket (recommended for node-local access)
    conn, err := grpc.NewClient(
        "unix:///var/run/device-api/device.sock",
        grpc.WithTransportCredentials(insecure.NewCredentials()),
    )
    if err != nil {
        log.Fatalf("failed to connect: %v", err)
    }
    defer conn.Close()

    client := v1alpha1.NewGpuServiceClient(conn)

    // List all GPUs
    resp, err := client.ListGpus(context.Background(), &v1alpha1.ListGpusRequest{})
    if err != nil {
        log.Fatalf("failed to list GPUs: %v", err)
    }

    for _, gpu := range resp.GpuList.Items {
        log.Printf("GPU: %s (UUID: %s)", gpu.Name, gpu.Spec.Uuid)
        for _, cond := range gpu.Status.Conditions {
            log.Printf("  %s: %s (%s)", cond.Type, cond.Status, cond.Reason)
        }
    }
}
```

### Using grpcurl

```bash
# List GPUs
grpcurl -plaintext localhost:50051 nvidia.device.v1alpha1.GpuService/ListGpus

# Watch for changes
grpcurl -plaintext localhost:50051 nvidia.device.v1alpha1.GpuService/WatchGpus
```

## API Overview

### GpuService

The unified `GpuService` follows Kubernetes API conventions with standard CRUD methods:

**Read Operations** (for consumers like device plugins and DRA drivers):

| Method | Description |
|--------|-------------|
| `GetGpu` | Retrieves a single GPU resource by its unique name |
| `ListGpus` | Retrieves a list of all GPU resources |
| `WatchGpus` | Streams lifecycle events (ADDED, MODIFIED, DELETED) for GPU resources |

**Write Operations** (for providers like health monitors):

| Method | Description |
|--------|-------------|
| `CreateGpu` | Register a new GPU with the server |
| `UpdateGpu` | Replace entire GPU resource |
| `UpdateGpuStatus` | Update GPU status only (acquires write lock) |
| `DeleteGpu` | Remove a GPU from the server |

---

## Development

### Prerequisites

- **Go**: `v1.25+`
- **Protoc**: Required for protobuf generation
- **golangci-lint**: Required for code quality checks
- **Make**: Used for orchestrating build and generation tasks
- **Helm 3.0+**: For chart development

### Build

```bash
# Build everything
make build

# Build server only
make build-server

# Generate protobuf code
make code-gen
```

### Test

```bash
# Run all tests
make test

# Run server tests only
make test-server
```

### Lint

```bash
make lint
```

---

## Documentation

- **[API Reference](docs/api/device-api-server.md)** - Complete gRPC API documentation
- **[Operations Guide](docs/operations/device-api-server.md)** - Deployment, configuration, monitoring
- **[Helm Chart](deployments/helm/device-api-server/README.md)** - Chart configuration reference
- **[Design Documents](docs/design/)** - Architecture and design decisions

The `client-go` module includes several examples for how to use the generated clients:

* **Standard Client**: Basic CRUD operations.
* **Shared Informers**: High-performance caching for controllers.
* **Watch**: Real-time event streaming via gRPC.

See the [examples](./client-go/examples) directory for details.

---

## Contributing

We welcome contributions! Please see:

- [Code of Conduct](CODE_OF_CONDUCT.md)
- [Development Guide](DEVELOPMENT.md)

All contributors must sign their commits (DCO).

--- 

## License

This project is licensed under the Apache License 2.0 - see the [LICENSE](LICENSE) file for details.

---

*Built by NVIDIA for GPU infrastructure management*
