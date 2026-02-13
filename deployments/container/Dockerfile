# Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Multi-target Dockerfile for NVSentinel components
#
# Targets:
#   device-api-server  - Pure Go server (no NVML, uses sidecar provider)
#   nvml-provider      - NVML provider sidecar (CGO, requires RuntimeClass nvidia)
#
# Build examples:
#   # Build device-api-server (default, pure Go)
#   docker build --target device-api-server -t nvsentinel/device-api-server .
#
#   # Build nvml-provider sidecar
#   docker build --target nvml-provider -t nvsentinel/nvml-provider .
#
# Note: NVML provider requires glibc runtime (Debian) for RTLD_DEEPBIND support

# TODO: Add Cosign image signing and SBOM generation to CI/CD pipeline.
# See: https://docs.sigstore.dev/signing/quickstart/
# Steps:
# 1. Sign images with cosign: cosign sign --key <key> <image>
# 2. Generate SBOM: syft <image> -o cyclonedx-json > sbom.json
# 3. Attach SBOM: cosign attach sbom --sbom sbom.json <image>

# ==============================================================================
# Build Arguments
# ==============================================================================

ARG GOLANG_VERSION=1.25
ARG VERSION=dev
ARG GIT_COMMIT=unknown
ARG GIT_TREE_STATE=dirty
ARG BUILD_DATE

# ==============================================================================
# Base Builder - Pure Go (Alpine)
# ==============================================================================

FROM golang:${GOLANG_VERSION}-alpine AS builder-alpine

ARG VERSION
ARG GIT_COMMIT
ARG GIT_TREE_STATE
ARG BUILD_DATE

WORKDIR /workspace

# Install build dependencies
RUN apk add --no-cache git make

# Copy go mod files first for caching
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Version package path
ARG VERSION_PKG=github.com/nvidia/nvsentinel/pkg/version

# Build device-api-server (CGO disabled, pure Go)
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-s -w \
        -X ${VERSION_PKG}.Version=${VERSION} \
        -X ${VERSION_PKG}.GitCommit=${GIT_COMMIT} \
        -X ${VERSION_PKG}.GitTreeState=${GIT_TREE_STATE} \
        -X ${VERSION_PKG}.BuildDate=${BUILD_DATE}" \
    -o /build/device-api-server \
    ./cmd/device-api-server

# ==============================================================================
# Base Builder - CGO (Debian/glibc)
# ==============================================================================

FROM golang:${GOLANG_VERSION}-bookworm AS builder-debian

ARG VERSION
ARG GIT_COMMIT
ARG GIT_TREE_STATE
ARG BUILD_DATE

WORKDIR /workspace

# Install build dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    git \
    make \
    && rm -rf /var/lib/apt/lists/*

# Copy go mod files first for caching
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Version package path
ARG VERSION_PKG=github.com/nvidia/nvsentinel/pkg/version

# Build nvml-provider (CGO enabled for go-nvml)
RUN CGO_ENABLED=1 go build \
    -tags=nvml \
    -ldflags "-s -w \
        -X ${VERSION_PKG}.Version=${VERSION} \
        -X ${VERSION_PKG}.GitCommit=${GIT_COMMIT} \
        -X ${VERSION_PKG}.GitTreeState=${GIT_TREE_STATE} \
        -X ${VERSION_PKG}.BuildDate=${BUILD_DATE}" \
    -o /build/nvml-provider \
    ./cmd/nvml-provider

# ==============================================================================
# Target: device-api-server
# ==============================================================================
# Pure Go server with no NVML dependencies. Uses sidecar provider for GPU access.
# Small image size, fast startup, works on any architecture.

# Pinned to digest for reproducible builds. Update with:
# docker manifest inspect alpine:3.21 | jq '.manifests[] | select(.platform.architecture=="amd64") | .digest'
FROM alpine:3.21@sha256:22e0ec13c0db6b3e1ba3280e831fc50ba7bffe58e81f31670a64b1afede247bc AS device-api-server

LABEL org.opencontainers.image.source="https://github.com/nvidia/nvsentinel"
LABEL org.opencontainers.image.description="NVSentinel Device API Server - Node-local GPU device state cache"
LABEL org.opencontainers.image.licenses="Apache-2.0"
LABEL org.opencontainers.image.title="device-api-server"

# Add ca-certificates for HTTPS
RUN apk add --no-cache ca-certificates

WORKDIR /

COPY --from=builder-alpine --chmod=755 /build/device-api-server /device-api-server

# Run as non-root user (nobody)
USER 65534:65534

# Health probe port (configurable via --health-probe-bind-address)
EXPOSE 8081
# Metrics port (configurable via --metrics-bind-address)
EXPOSE 9090

ENTRYPOINT ["/device-api-server"]

# ==============================================================================
# Target: nvml-provider
# ==============================================================================
# NVML provider sidecar for GPU enumeration and health monitoring.
# Requires glibc runtime (Debian) for RTLD_DEEPBIND support.
# Must run with RuntimeClass: nvidia to access NVML libraries.

# Pinned to digest for reproducible builds. Update with:
# docker manifest inspect debian:bookworm-slim | jq '.manifests[] | select(.platform.architecture=="amd64") | .digest'
FROM debian:bookworm-slim@sha256:6458e6ce2b6448e31bfdced4be7d8aa88d389e6694ab09f5a718a694abe147f4 AS nvml-provider

LABEL org.opencontainers.image.source="https://github.com/nvidia/nvsentinel"
LABEL org.opencontainers.image.description="NVSentinel NVML Provider - GPU enumeration and health monitoring sidecar"
LABEL org.opencontainers.image.licenses="Apache-2.0"
LABEL org.opencontainers.image.title="nvml-provider"

# Add ca-certificates for HTTPS
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /

COPY --from=builder-debian --chmod=755 /build/nvml-provider /nvml-provider

# Run as non-root user
USER 65534:65534

# Health check port
EXPOSE 8082

ENTRYPOINT ["/nvml-provider"]
