#!/bin/bash
# NVML Provider Sidecar Demo
# Demonstrates the NVML provider sidecar architecture for GPU enumeration
#
# Prerequisites:
#   - kubectl configured with GPU cluster access
#   - docker with buildx for building images
#   - helm 3.x installed
#   - GPU nodes with RuntimeClass 'nvidia'
#
# Usage: ./demos/nvml-sidecar-demo.sh [kubeconfig]
#
# Environment Variables (all optional):
#   KUBECONFIG       - Path to kubeconfig file (default: $HOME/.kube/config)
#   NAMESPACE        - Kubernetes namespace (default: device-api)
#   RELEASE_NAME     - Helm release name (default: device-api-server)
#   IMAGE_REGISTRY   - Container registry (default: ttl.sh)
#   IMAGE_TAG        - Image tag (default: 2h for ttl.sh expiry)
#   SERVER_IMAGE     - Full device-api-server image (default: $IMAGE_REGISTRY/device-api-server:$IMAGE_TAG)
#   SIDECAR_IMAGE    - Full sidecar image (default: $IMAGE_REGISTRY/device-api-server-sidecar:$IMAGE_TAG)
#   BUILD_PLATFORM   - Target platform for builds (default: linux/amd64)
#   GPU_NODE_SELECTOR - Label selector for GPU nodes (default: nvidia.com/gpu.present=true)
#   CHART_PATH       - Path to Helm chart (default: deployments/helm/device-api-server)
#   VALUES_FILE      - Path to values file (default: deployments/helm/values-sidecar-test.yaml)
#   DOCKERFILE       - Path to Dockerfile (default: deployments/container/Dockerfile)
#   APP_NAME         - Helm chart app name for pod selectors (default: device-api-server)
#   CONTAINER_NAME   - Main container name (default: device-api-server)
#   SIDECAR_CONTAINER_NAME - Sidecar container name (default: nvml-provider)
#   INTERACTIVE      - Enable interactive mode with prompts (default: true)
#   SKIP_DESTRUCTIVE - Skip destructive ops in non-interactive mode (default: true)
#   SKIP_BUILD       - Skip image building entirely (default: false)
#
# Examples:
#   # Use default settings with ttl.sh
#   ./demos/nvml-sidecar-demo.sh
#
#   # Use custom kubeconfig
#   KUBECONFIG=~/.kube/config-aws-gpu ./demos/nvml-sidecar-demo.sh
#
#   # Use custom registry
#   IMAGE_REGISTRY=ghcr.io/nvidia IMAGE_TAG=latest ./demos/nvml-sidecar-demo.sh
#
#   # Non-interactive mode (for CI/automation)
#   INTERACTIVE=false KUBECONFIG=~/.kube/config ./demos/nvml-sidecar-demo.sh

set -euo pipefail

# ==============================================================================
# Configuration (all values configurable via environment variables)
# ==============================================================================

# Kubernetes configuration
KUBECONFIG="${KUBECONFIG:-${1:-$HOME/.kube/config}}"
NAMESPACE="${NAMESPACE:-device-api}"
RELEASE_NAME="${RELEASE_NAME:-device-api-server}"

# Paths (relative to repo root)
CHART_PATH="${CHART_PATH:-deployments/helm/device-api-server}"
VALUES_FILE="${VALUES_FILE:-deployments/helm/values-sidecar-test.yaml}"
DOCKERFILE="${DOCKERFILE:-deployments/container/Dockerfile}"

# Image registry settings
IMAGE_REGISTRY="${IMAGE_REGISTRY:-ttl.sh}"
IMAGE_TAG="${IMAGE_TAG:-2h}"

# Image names (using ttl.sh ephemeral registry by default - images expire based on tag)
SERVER_IMAGE="${SERVER_IMAGE:-${IMAGE_REGISTRY}/device-api-server:${IMAGE_TAG}}"
SIDECAR_IMAGE="${SIDECAR_IMAGE:-${IMAGE_REGISTRY}/device-api-server-sidecar:${IMAGE_TAG}}"

# Build settings
BUILD_PLATFORM="${BUILD_PLATFORM:-linux/amd64}"

# Node selection (for listing GPU nodes)
GPU_NODE_SELECTOR="${GPU_NODE_SELECTOR:-nvidia.com/gpu.present=true}"

# Interactive mode (set to false for CI/automated runs)
INTERACTIVE="${INTERACTIVE:-true}"

# Skip destructive demos in non-interactive mode
SKIP_DESTRUCTIVE="${SKIP_DESTRUCTIVE:-true}"

# Skip image building entirely (use pre-built images)
SKIP_BUILD="${SKIP_BUILD:-false}"

# Helm chart app name (used for pod selectors and container names)
APP_NAME="${APP_NAME:-device-api-server}"
CONTAINER_NAME="${CONTAINER_NAME:-device-api-server}"
SIDECAR_CONTAINER_NAME="${SIDECAR_CONTAINER_NAME:-nvml-provider}"

# ==============================================================================
# Terminal Colors (buildah-style)
# ==============================================================================

if [[ -t 1 ]]; then
    red=$(tput setaf 1)
    green=$(tput setaf 2)
    yellow=$(tput setaf 3)
    blue=$(tput setaf 4)
    magenta=$(tput setaf 5)
    cyan=$(tput setaf 6)
    white=$(tput setaf 7)
    bold=$(tput bold)
    reset=$(tput sgr0)
else
    red=""
    green=""
    yellow=""
    blue=""
    magenta=""
    cyan=""
    white=""
    bold=""
    reset=""
fi

# ==============================================================================
# Helper Functions
# ==============================================================================

banner() {
    echo ""
    echo "${bold}${blue}============================================================${reset}"
    echo "${bold}${blue}  $1${reset}"
    echo "${bold}${blue}============================================================${reset}"
    echo ""
}

step() {
    echo ""
    echo "${bold}${green}>>> $1${reset}"
    echo ""
}

info() {
    echo "${cyan}    $1${reset}"
}

warn() {
    echo "${yellow}    WARNING: $1${reset}"
}

error() {
    echo "${red}    ERROR: $1${reset}"
}

run_cmd() {
    echo "${magenta}    \$ $*${reset}"
    "$@"
}

pause() {
    if [[ "${INTERACTIVE}" == "true" ]]; then
        echo ""
        read -r -p "${yellow}Press ENTER to continue...${reset}"
        echo ""
    fi
}

confirm() {
    if [[ "${INTERACTIVE}" != "true" ]]; then
        # Auto-confirm in non-interactive mode
        info "Auto-confirming: $1"
        return 0
    fi
    echo ""
    read -r -p "${yellow}$1 [y/N] ${reset}" response
    case "$response" in
        [yY][eE][sS]|[yY]) return 0 ;;
        *) return 1 ;;
    esac
}

# Confirm for destructive operations (skipped in non-interactive mode if SKIP_DESTRUCTIVE=true)
confirm_destructive() {
    if [[ "${INTERACTIVE}" != "true" && "${SKIP_DESTRUCTIVE}" == "true" ]]; then
        info "Skipping destructive operation in non-interactive mode: $1"
        return 1
    fi
    confirm "$1"
}

check_prereqs() {
    local missing=()

    command -v kubectl &>/dev/null || missing+=("kubectl")
    command -v helm &>/dev/null || missing+=("helm")
    command -v docker &>/dev/null || missing+=("docker")

    if [[ ${#missing[@]} -gt 0 ]]; then
        error "Missing prerequisites: ${missing[*]}"
        exit 1
    fi

    # Check for buildx (required for cross-platform builds)
    if ! docker buildx version &>/dev/null; then
        warn "docker buildx not available - cross-platform builds may fail"
        warn "Run: docker buildx create --use --name multiarch"
    else
        info "Docker buildx: $(docker buildx version | head -1)"
    fi
}

# ==============================================================================
# Demo Sections
# ==============================================================================

show_intro() {
    [[ "${INTERACTIVE}" == "true" ]] && clear
    banner "NVML Provider Sidecar Architecture Demo"

    echo "${white}This demo showcases the sidecar-based NVML provider for device-api-server.${reset}"
    echo ""
    echo "${white}Architecture:${reset}"
    echo "${cyan}  ┌─────────────────────────────────────────────────────────┐${reset}"
    echo "${cyan}  │                     Pod                                 │${reset}"
    echo "${cyan}  │  ┌──────────────────┐    ┌──────────────────┐          │${reset}"
    echo "${cyan}  │  │ device-api-server│    │  nvml-provider   │          │${reset}"
    echo "${cyan}  │  │   (pure Go)      │◄───│  (CGO + NVML)    │          │${reset}"
    echo "${cyan}  │  │   Unix Socket    │gRPC│  Health :8082    │          │${reset}"
    echo "${cyan}  │  │  Health :8081    │    │  RuntimeClass:   │          │${reset}"
    echo "${cyan}  │  │  Metrics :9090   │    │    nvidia        │          │${reset}"
    echo "${cyan}  │  └──────────────────┘    └──────────────────┘          │${reset}"
    echo "${cyan}  └─────────────────────────────────────────────────────────┘${reset}"
    echo ""
    echo "${white}Benefits:${reset}"
    echo "${green}  ✓ Separation of concerns (API server vs NVML access)${reset}"
    echo "${green}  ✓ Independent scaling and updates${reset}"
    echo "${green}  ✓ Better testability (mock providers)${reset}"
    echo "${green}  ✓ Crash isolation (NVML crashes don't kill API server)${reset}"
    echo ""

    pause
}

show_config() {
    banner "Configuration"

    echo "${white}Current settings (override via environment variables):${reset}"
    echo ""
    echo "${cyan}  Kubernetes:${reset}"
    echo "    KUBECONFIG       = ${KUBECONFIG}"
    echo "    NAMESPACE        = ${NAMESPACE}"
    echo "    RELEASE_NAME     = ${RELEASE_NAME}"
    echo ""
    echo "${cyan}  Paths:${reset}"
    echo "    CHART_PATH       = ${CHART_PATH}"
    echo "    VALUES_FILE      = ${VALUES_FILE}"
    echo "    DOCKERFILE       = ${DOCKERFILE}"
    echo ""
    echo "${cyan}  Images:${reset}"
    echo "    IMAGE_REGISTRY   = ${IMAGE_REGISTRY}"
    echo "    IMAGE_TAG        = ${IMAGE_TAG}"
    echo "    SERVER_IMAGE     = ${SERVER_IMAGE}"
    echo "    SIDECAR_IMAGE    = ${SIDECAR_IMAGE}"
    echo ""
    echo "${cyan}  Build:${reset}"
    echo "    BUILD_PLATFORM     = ${BUILD_PLATFORM}"
    echo ""
    echo "${cyan}  Cluster:${reset}"
    echo "    GPU_NODE_SELECTOR  = ${GPU_NODE_SELECTOR}"
    echo ""
    echo "${cyan}  Helm Chart:${reset}"
    echo "    APP_NAME               = ${APP_NAME}"
    echo "    CONTAINER_NAME         = ${CONTAINER_NAME}"
    echo "    SIDECAR_CONTAINER_NAME = ${SIDECAR_CONTAINER_NAME}"
    echo ""
    echo "${cyan}  Mode:${reset}"
    echo "    INTERACTIVE            = ${INTERACTIVE}"
    echo "    SKIP_DESTRUCTIVE       = ${SKIP_DESTRUCTIVE}"
    echo "    SKIP_BUILD             = ${SKIP_BUILD}"
    echo ""

    pause
}

show_cluster_info() {
    banner "Step 1: Verify Cluster Connectivity"

    step "Check cluster connection"
    run_cmd kubectl --kubeconfig="${KUBECONFIG}" cluster-info

    pause

    step "List GPU nodes (selector: ${GPU_NODE_SELECTOR})"
    run_cmd kubectl --kubeconfig="${KUBECONFIG}" get nodes -l "${GPU_NODE_SELECTOR}" -o wide || {
        warn "No nodes found with selector '${GPU_NODE_SELECTOR}'"
        info "Listing all nodes instead:"
        run_cmd kubectl --kubeconfig="${KUBECONFIG}" get nodes -o wide
    }

    pause

    step "Verify nvidia RuntimeClass exists"
    run_cmd kubectl --kubeconfig="${KUBECONFIG}" get runtimeclass nvidia -o yaml || {
        warn "RuntimeClass 'nvidia' not found. GPU access may not work."
    }

    pause
}

check_image_exists() {
    local image="$1"
    # Try to inspect the manifest - if it exists, the image is available
    docker buildx imagetools inspect "${image}" &>/dev/null 2>&1
}

build_images() {
    banner "Step 2: Build Container Images"

    if [[ "${SKIP_BUILD}" == "true" ]]; then
        info "SKIP_BUILD=true, skipping image builds"
        info "Using pre-built images:"
        info "  SERVER_IMAGE:  ${SERVER_IMAGE}"
        info "  SIDECAR_IMAGE: ${SIDECAR_IMAGE}"
        return 0
    fi

    info "Building images for registry: ${IMAGE_REGISTRY}"
    info "Using unified multi-target Dockerfile at ${DOCKERFILE}"
    info "Target platform: ${BUILD_PLATFORM}"
    echo ""

    # Ensure buildx is available for cross-platform builds
    if ! docker buildx version &>/dev/null; then
        error "docker buildx is required for cross-platform builds"
        error "Install Docker Desktop or run: docker buildx create --use"
        exit 1
    fi

    # Check if images already exist
    local need_server=true
    local need_sidecar=true

    if check_image_exists "${SERVER_IMAGE}"; then
        info "Image ${SERVER_IMAGE} already exists"
        if ! confirm "Rebuild device-api-server image?"; then
            need_server=false
        fi
    fi

    if check_image_exists "${SIDECAR_IMAGE}"; then
        info "Image ${SIDECAR_IMAGE} already exists"
        if ! confirm "Rebuild device-api-server-sidecar image?"; then
            need_sidecar=false
        fi
    fi

    if [[ "${need_server}" == "true" ]]; then
        step "Build and push device-api-server image (CGO_ENABLED=0)"
        info "This is a pure Go binary with no NVML dependencies"
        info "Building for ${BUILD_PLATFORM} and pushing directly..."
        run_cmd docker buildx build \
            --platform "${BUILD_PLATFORM}" \
            --target device-api-server \
            -t "${SERVER_IMAGE}" \
            -f "${DOCKERFILE}" \
            --push \
            .
        pause
    else
        info "Skipping device-api-server build"
    fi

    if [[ "${need_sidecar}" == "true" ]]; then
        step "Build and push device-api-server-sidecar image (CGO_ENABLED=1)"
        info "This is the NVML provider sidecar with glibc runtime"
        info "Building for ${BUILD_PLATFORM} and pushing directly..."
        run_cmd docker buildx build \
            --platform "${BUILD_PLATFORM}" \
            --target nvml-provider \
            -t "${SIDECAR_IMAGE}" \
            -f "${DOCKERFILE}" \
            --push \
            .
        pause
    else
        info "Skipping device-api-server-sidecar build"
    fi
}

show_values_file() {
    banner "Step 3: Review Helm Values"

    info "The sidecar architecture is enabled via Helm values"
    echo ""

    step "Key configuration in ${VALUES_FILE}:"
    echo ""
    echo "${cyan}# Disable built-in NVML provider${reset}"
    echo "${white}nvml:${reset}"
    echo "${white}  enabled: false${reset}"
    echo ""
    echo "${cyan}# Enable NVML Provider sidecar${reset}"
    echo "${white}nvmlProvider:${reset}"
    echo "${white}  enabled: true${reset}"
    echo "${white}  image:${reset}"
    echo "${white}    repository: ${IMAGE_REGISTRY}/device-api-server-sidecar${reset}"
    echo "${white}    tag: \"${IMAGE_TAG}\"${reset}"
    echo "${white}  # Sidecar connects via shared unix socket volume${reset}"
    echo "${white}  runtimeClassName: nvidia${reset}"
    echo ""

    if [[ -f "${VALUES_FILE}" ]]; then
        step "Full values file:"
        run_cmd cat "${VALUES_FILE}"
    fi

    pause
}

deploy_sidecar() {
    banner "Step 4: Deploy with Sidecar Architecture"

    step "Create namespace if not exists"
    echo "${magenta}    \$ kubectl create namespace ${NAMESPACE} --dry-run=client -o yaml | kubectl apply -f -${reset}"
    kubectl --kubeconfig="${KUBECONFIG}" create namespace "${NAMESPACE}" --dry-run=client -o yaml | \
        kubectl --kubeconfig="${KUBECONFIG}" apply -f -

    pause

    # Check if release already exists
    # Build --set overrides to ensure Helm uses the same images we just built,
    # regardless of what the values file says.
    IMAGE_OVERRIDES=(
        --set "image.repository=${IMAGE_REGISTRY}/device-api-server"
        --set "image.tag=${IMAGE_TAG}"
        --set "nvmlProvider.image.repository=${IMAGE_REGISTRY}/device-api-server-sidecar"
        --set "nvmlProvider.image.tag=${IMAGE_TAG}"
    )

    if helm status "${RELEASE_NAME}" --kubeconfig="${KUBECONFIG}" -n "${NAMESPACE}" &>/dev/null; then
        info "Release '${RELEASE_NAME}' already exists"
        step "Upgrading existing release..."
        run_cmd helm upgrade "${RELEASE_NAME}" "${CHART_PATH}" \
            --kubeconfig="${KUBECONFIG}" \
            --namespace "${NAMESPACE}" \
            -f "${VALUES_FILE}" \
            "${IMAGE_OVERRIDES[@]}"

        step "Restarting pods to pick up changes..."
        run_cmd kubectl --kubeconfig="${KUBECONFIG}" -n "${NAMESPACE}" rollout restart daemonset "${RELEASE_NAME}"
    else
        step "Installing new release..."
        run_cmd helm install "${RELEASE_NAME}" "${CHART_PATH}" \
            --kubeconfig="${KUBECONFIG}" \
            --namespace "${NAMESPACE}" \
            -f "${VALUES_FILE}" \
            "${IMAGE_OVERRIDES[@]}"
    fi

    pause

    step "Waiting for pods to be ready (timeout 2m)..."
    if ! kubectl --kubeconfig="${KUBECONFIG}" -n "${NAMESPACE}" rollout status daemonset "${RELEASE_NAME}" --timeout=2m; then
        warn "Rollout not complete within timeout. Checking status..."
    fi

    step "Current pod status"
    run_cmd kubectl --kubeconfig="${KUBECONFIG}" -n "${NAMESPACE}" get pods -l app.kubernetes.io/name=${APP_NAME} -o wide

    pause

    step "Verify both containers are running in each pod"
    info "Each pod should have 2/2 containers ready"
    run_cmd kubectl --kubeconfig="${KUBECONFIG}" -n "${NAMESPACE}" get pods -l app.kubernetes.io/name=${APP_NAME} \
        -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.phase}{"\t"}{range .status.containerStatuses[*]}{.name}:{.ready}{" "}{end}{"\n"}{end}'

    pause
}

verify_gpu_registration() {
    banner "Step 5: Verify GPU Registration"

    step "Wait for pods to be ready"
    info "Waiting up to 60 seconds for pods to start..."
    if ! kubectl --kubeconfig="${KUBECONFIG}" -n "${NAMESPACE}" wait --for=condition=ready pod -l app.kubernetes.io/name=${APP_NAME} --timeout=60s 2>/dev/null; then
        warn "Pods may not be ready yet. Checking status..."
        run_cmd kubectl --kubeconfig="${KUBECONFIG}" -n "${NAMESPACE}" get pods -l app.kubernetes.io/name=${APP_NAME} -o wide
        run_cmd kubectl --kubeconfig="${KUBECONFIG}" -n "${NAMESPACE}" describe pods -l app.kubernetes.io/name=${APP_NAME} | tail -30
        error "Pods not ready. Check the output above for issues."
        return 1
    fi

    pause

    step "Verify DaemonSet coverage on all GPU nodes"
    local gpu_nodes_ready
    local gpu_nodes_total
    local daemonset_desired
    local daemonset_ready

    gpu_nodes_total=$(kubectl --kubeconfig="${KUBECONFIG}" get nodes -l "${GPU_NODE_SELECTOR}" --no-headers 2>/dev/null | wc -l | tr -d ' ')
    gpu_nodes_ready=$(kubectl --kubeconfig="${KUBECONFIG}" get nodes -l "${GPU_NODE_SELECTOR}" --no-headers 2>/dev/null | grep -c " Ready" || true)
    # Ensure gpu_nodes_ready is a valid number (grep -c returns 0 with exit code 1 when no matches)
    [[ -z "${gpu_nodes_ready}" ]] && gpu_nodes_ready=0
    daemonset_desired=$(kubectl --kubeconfig="${KUBECONFIG}" -n "${NAMESPACE}" get daemonset "${RELEASE_NAME}" -o jsonpath='{.status.desiredNumberScheduled}' 2>/dev/null || echo "0")
    daemonset_ready=$(kubectl --kubeconfig="${KUBECONFIG}" -n "${NAMESPACE}" get daemonset "${RELEASE_NAME}" -o jsonpath='{.status.numberReady}' 2>/dev/null || echo "0")

    echo ""
    info "GPU Nodes (total):      ${gpu_nodes_total}"
    info "GPU Nodes (Ready):      ${gpu_nodes_ready}"
    info "DaemonSet (desired):    ${daemonset_desired}"
    info "DaemonSet (ready):      ${daemonset_ready}"
    echo ""

    if [[ "${daemonset_ready}" -eq "${gpu_nodes_ready}" && "${daemonset_ready}" -gt 0 ]]; then
        echo "${green}  ✓ DaemonSet running on all ${daemonset_ready} Ready GPU nodes${reset}"
    else
        warn "DaemonSet coverage mismatch! Expected ${gpu_nodes_ready} pods, got ${daemonset_ready}"
        run_cmd kubectl --kubeconfig="${KUBECONFIG}" -n "${NAMESPACE}" get daemonset "${RELEASE_NAME}"
    fi

    pause

    step "List all pods and their nodes"
    run_cmd kubectl --kubeconfig="${KUBECONFIG}" -n "${NAMESPACE}" get pods -l app.kubernetes.io/name=${APP_NAME} -o wide

    pause

    step "Get a pod name for testing"
    POD=$(kubectl --kubeconfig="${KUBECONFIG}" -n "${NAMESPACE}" get pods -l app.kubernetes.io/name=${APP_NAME} -o jsonpath='{.items[0].metadata.name}')
    if [[ -z "${POD}" ]]; then
        error "No pods found. DaemonSet may not be scheduling on any nodes."
        run_cmd kubectl --kubeconfig="${KUBECONFIG}" -n "${NAMESPACE}" get daemonset
        return 1
    fi
    NODE=$(kubectl --kubeconfig="${KUBECONFIG}" -n "${NAMESPACE}" get pod "${POD}" -o jsonpath='{.spec.nodeName}')
    info "Using pod: ${POD} (on node: ${NODE})"

    pause

    step "Check device-api-server logs for provider connection"
    run_cmd kubectl --kubeconfig="${KUBECONFIG}" -n "${NAMESPACE}" logs "${POD}" -c "${CONTAINER_NAME}" --tail=20 || true

    pause

    step "Check nvml-provider sidecar logs"
    run_cmd kubectl --kubeconfig="${KUBECONFIG}" -n "${NAMESPACE}" logs "${POD}" -c "${SIDECAR_CONTAINER_NAME}" --tail=20 || true

    pause

    verify_gpu_uuid_match "${POD}" "${NODE}"
}

verify_gpu_uuid_match() {
    local pod="$1"
    local node="$2"

    banner "Step 5b: Verify GPU UUID Match"

    info "Comparing GPU UUIDs from nvidia-smi with device-api-server registered GPUs"
    info "Pod: ${pod} | Node: ${node}"
    echo ""

    step "Get GPU UUID from nvidia-smi on the node (via sidecar container)"
    local nvidia_smi_uuids
    nvidia_smi_uuids=$(kubectl --kubeconfig="${KUBECONFIG}" -n "${NAMESPACE}" exec "${pod}" -c "${SIDECAR_CONTAINER_NAME}" -- \
        nvidia-smi --query-gpu=uuid --format=csv,noheader 2>/dev/null || echo "")

    if [[ -z "${nvidia_smi_uuids}" ]]; then
        warn "Could not get GPU UUIDs from nvidia-smi"
        return 1
    fi

    echo "${cyan}    nvidia-smi GPU UUIDs:${reset}"
    echo "${nvidia_smi_uuids}" | while read -r uuid; do
        echo "      - ${uuid}"
    done
    echo ""

    pause

    step "Get registered GPU UUIDs from device-api-server logs"
    local registered_uuids
    registered_uuids=$(kubectl --kubeconfig="${KUBECONFIG}" -n "${NAMESPACE}" logs "${pod}" -c "${SIDECAR_CONTAINER_NAME}" 2>/dev/null | \
        grep -o 'uuid="GPU-[^"]*"' | sed 's/uuid="//;s/"$//' | sort -u || echo "")

    if [[ -z "${registered_uuids}" ]]; then
        warn "Could not find registered GPU UUIDs in logs"
        return 1
    fi

    echo "${cyan}    Registered GPU UUIDs:${reset}"
    echo "${registered_uuids}" | while read -r uuid; do
        echo "      - ${uuid}"
    done
    echo ""

    pause

    step "Compare UUIDs"
    local match_count=0
    local total_count=0

    while read -r smi_uuid; do
        [[ -z "${smi_uuid}" ]] && continue
        total_count=$((total_count + 1))
        if echo "${registered_uuids}" | grep -q "${smi_uuid}"; then
            echo "${green}    ✓ ${smi_uuid} - MATCHED${reset}"
            match_count=$((match_count + 1))
        else
            echo "${red}    ✗ ${smi_uuid} - NOT FOUND in registered GPUs${reset}"
        fi
    done <<< "${nvidia_smi_uuids}"

    echo ""
    if [[ "${match_count}" -eq "${total_count}" && "${total_count}" -gt 0 ]]; then
        echo "${green}  ✓ All ${total_count} GPU(s) from nvidia-smi are registered in device-api-server${reset}"
    else
        warn "UUID mismatch: ${match_count}/${total_count} GPUs matched"
    fi

    pause
}

demonstrate_crash_recovery() {
    banner "Step 6: Demonstrate Crash Recovery"

    info "The sidecar architecture provides crash isolation."
    info "If the NVML provider crashes, the API server continues running"
    info "and will reconnect when the provider restarts."
    echo ""

    step "Get current pod"
    POD=$(kubectl --kubeconfig="${KUBECONFIG}" -n "${NAMESPACE}" get pods -l app.kubernetes.io/name=${APP_NAME} -o jsonpath='{.items[0].metadata.name}')
    info "Using pod: ${POD}"

    pause

    if confirm_destructive "Kill the nvml-provider container to demonstrate crash recovery?"; then
        step "Killing nvml-provider container..."
        run_cmd kubectl --kubeconfig="${KUBECONFIG}" -n "${NAMESPACE}" exec "${POD}" -c "${SIDECAR_CONTAINER_NAME}" -- kill 1 || true

        info "Waiting for container restart..."
        sleep 5

        step "Check pod status (should show restart count)"
        run_cmd kubectl --kubeconfig="${KUBECONFIG}" -n "${NAMESPACE}" get pod "${POD}" -o wide

        step "Verify API server continued running"
        run_cmd kubectl --kubeconfig="${KUBECONFIG}" -n "${NAMESPACE}" logs "${POD}" -c "${CONTAINER_NAME}" --tail=10 || true

        step "Verify provider reconnected"
        run_cmd kubectl --kubeconfig="${KUBECONFIG}" -n "${NAMESPACE}" logs "${POD}" -c "${SIDECAR_CONTAINER_NAME}" --tail=10 || true
    else
        info "Skipping crash recovery demonstration"
    fi

    pause
}

show_metrics() {
    banner "Step 7: View Provider Metrics"

    step "Get pod for port-forward"
    POD=$(kubectl --kubeconfig="${KUBECONFIG}" -n "${NAMESPACE}" get pods -l app.kubernetes.io/name=${APP_NAME} -o jsonpath='{.items[0].metadata.name}')

    step "Fetch metrics from the API server"
    info "Key metrics to look for:"
    info "  - device_apiserver_service_status: Whether services are serving"
    info "  - device_apiserver_build_info: Build information"
    info "  - grpc_server_*: gRPC request/stream metrics"
    echo ""

    run_cmd kubectl --kubeconfig="${KUBECONFIG}" -n "${NAMESPACE}" exec "${POD}" -c "${CONTAINER_NAME}" -- \
        wget -qO- http://localhost:9090/metrics 2>/dev/null | grep -E "^(device_apiserver_|grpc_server_handled_total)" | sort || {
        run_cmd kubectl --kubeconfig="${KUBECONFIG}" -n "${NAMESPACE}" exec "${POD}" -c "${CONTAINER_NAME}" -- \
            curl -s http://localhost:9090/metrics 2>/dev/null | grep -E "^(device_apiserver_|grpc_server_handled_total)" | sort || true
    }

    pause
}

cleanup() {
    banner "Cleanup"

    if confirm_destructive "Remove the sidecar deployment and restore default?"; then
        step "Uninstalling Helm release..."
        run_cmd helm uninstall "${RELEASE_NAME}" \
            --kubeconfig="${KUBECONFIG}" \
            --namespace "${NAMESPACE}" || true

        info "Cleanup complete!"
    else
        info "Skipping cleanup. Release '${RELEASE_NAME}' left in namespace '${NAMESPACE}'"
    fi
}

show_summary() {
    banner "Demo Complete!"

    echo "${white}What we demonstrated:${reset}"
    echo "${green}  ✓ Built separate images for device-api-server and device-api-server-sidecar${reset}"
    echo "${green}  ✓ Deployed as sidecar architecture via Helm${reset}"
    echo "${green}  ✓ Verified DaemonSet runs on ALL GPU nodes${reset}"
    echo "${green}  ✓ Verified GPU UUIDs match between nvidia-smi and device-api-server${reset}"
    echo "${green}  ✓ Showed crash isolation and recovery${reset}"
    echo "${green}  ✓ Explored provider metrics${reset}"
    echo ""
    echo "${white}Images built:${reset}"
    echo "${cyan}  - ${SERVER_IMAGE}${reset}"
    echo "${cyan}  - ${SIDECAR_IMAGE}${reset}"
    echo ""
    echo "${white}Key files:${reset}"
    echo "${cyan}  - ${DOCKERFILE}              # Multi-target container build${reset}"
    echo "${cyan}  - ${VALUES_FILE}             # Helm values for sidecar mode${reset}"
    echo "${cyan}  - ${CHART_PATH}/             # Helm chart with sidecar support${reset}"
    echo ""
    echo "${white}Environment variables for customization:${reset}"
    echo "${cyan}  KUBECONFIG, NAMESPACE, RELEASE_NAME, IMAGE_REGISTRY, IMAGE_TAG,${reset}"
    echo "${cyan}  SERVER_IMAGE, SIDECAR_IMAGE, BUILD_PLATFORM, GPU_NODE_SELECTOR,${reset}"
    echo "${cyan}  CHART_PATH, VALUES_FILE, DOCKERFILE${reset}"
    echo ""
}

# ==============================================================================
# Main
# ==============================================================================

main() {
    export KUBECONFIG

    show_intro
    show_config
    check_prereqs
    show_cluster_info

    if confirm "Build and push container images?"; then
        build_images
    else
        info "Skipping image build. Using existing images at ${IMAGE_REGISTRY}"
    fi

    show_values_file

    if confirm "Deploy the sidecar architecture to the cluster?"; then
        deploy_sidecar
        verify_gpu_registration
        demonstrate_crash_recovery
        show_metrics
        cleanup
    else
        info "Skipping deployment"
    fi

    show_summary
}

# Run main if script is executed (not sourced)
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
    main "$@"
fi
