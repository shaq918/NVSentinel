# ADR-033: gRPC Sink Connector for Platform-Connectors

## Context

Platform-connectors receives health events from all NVSentinel health monitors
via a Unix domain socket and distributes them to registered connectors using a
ring buffer fanout pattern. Today there are two connectors:

- **Store connector** — persists events to MongoDB or PostgreSQL for downstream
  modules (fault-quarantine, node-drainer, health-events-analyzer) that watch
  change streams.
- **K8s connector** — updates Kubernetes node conditions so operators can see
  GPU health status via `kubectl describe node`.

Both connectors serve NVSentinel's internal architecture. There is no mechanism
to forward the full structured HealthEvent proto to components outside of
NVSentinel.

## Problem

External systems that want to consume NVSentinel health events have two options
today, both lossy:

1. **Watch Kubernetes node conditions** — node conditions flatten the rich
   HealthEvent proto (17 fields) into a condition type, boolean status, and a
   truncated message string. Entity-level detail (GPU UUID, GPC/TPC/SM,
   register values), the full error code array, recommended action enum,
   processing strategy, and metadata are lost or truncated at the 4KB
   Kubernetes message limit.

2. **Query the database** — requires access to the NVSentinel MongoDB or
   PostgreSQL instance, coupling the external system to NVSentinel's internal
   storage layer.

## Solution

Add a third connector — the **gRPC sink connector** — that forwards the full
HealthEvent proto to an external gRPC server. The external server implements
the existing `PlatformConnector` service:

```protobuf
service PlatformConnector {
  rpc HealthEventOccurredV1(HealthEvents) returns (google.protobuf.Empty) {}
}
```

No new proto definitions are needed. The external server receives the same
structured data that the store and K8s connectors receive — full fidelity,
no truncation.

## Architecture

```
Health Monitors (5+ types)
        |
    [UDS Socket]
        |
[Platform-Connectors gRPC Server]
        |
    [Pipeline: Transform Events]
        |
    [Ring Buffer Fanout]
        |--- K8s Ring Buffer ---> K8s Connector (node conditions)
        |--- Store Ring Buffer -> Store Connector (MongoDB/PostgreSQL)
        |--- gRPC Ring Buffer --> gRPC Sink Connector (external server)  <-- NEW
```

Each connector has its own ring buffer with independent retry logic. If the
gRPC sink target is unreachable, only its buffer backs up — the store and K8s
connectors are unaffected.

## Configuration

The connector is disabled by default and configured via Helm values:

```yaml
platformConnector:
  grpcSinkConnector:
    enabled: false
    target: ""         # gRPC server address, e.g. "my-service.example.com:50051"
    maxRetries: 3      # Retry attempts with exponential backoff before dropping
```

## Use Cases

- **Custom remediation pipelines** — receive structured fault data and trigger
  organization-specific remediation workflows without depending on NVSentinel's
  internal modules.
- **External analytics and dashboards** — stream GPU health events to a central
  analytics platform with full entity-level detail (GPU UUID, PCI address,
  error codes, recommended actions).
- **Multi-cluster aggregation** — forward events from multiple clusters to a
  central service for fleet-wide fault correlation and pattern detection.
- **Integration with existing infrastructure** — connect NVSentinel's detection
  layer to existing operational tooling without modifying NVSentinel's internal
  architecture.
