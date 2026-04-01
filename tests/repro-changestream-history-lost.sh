#!/bin/bash
# Copyright (c) 2025, NVIDIA CORPORATION.  All rights reserved.
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

# Reproduction script for ChangeStreamHistoryLost in event-exporter.
#
# This script reproduces the issue where the event-exporter falls behind
# the MongoDB oplog due to sequential publishing, causing the resume token
# to become stale and triggering an unrecoverable ChangeStreamHistoryLost error.
#
# Prerequisites:
#   - Kind cluster with NVSentinel deployed (make cluster-create && tilt up)
#   - event-exporter-mock deployed with RESPONSE_DELAY env var support
#   - mongosh installed locally
#
# Usage:
#   ./tests/repro-changestream-history-lost.sh [OPTIONS]
#
# Options:
#   --response-delay    Sink response delay (default: 300ms)
#   --insert-rate       Events per batch per second (default: 25)
#   --insert-batches    Number of batches to insert (default: 500)
#   --namespace         Kubernetes namespace (default: nvsentinel)
#   --skip-setup        Skip oplog resize and mock delay configuration
#   --cleanup           Remove test data and restore settings

set -euo pipefail

RESPONSE_DELAY="${RESPONSE_DELAY:-300ms}"
INSERT_RATE="${INSERT_RATE:-25}"
INSERT_BATCHES="${INSERT_BATCHES:-500}"
NAMESPACE="${NAMESPACE:-nvsentinel}"
SKIP_SETUP=false
CLEANUP=false

MONGO_POD_NAME="mongodb-0"
MONGO_LOCAL_PORT="27117"
MONGO_SECRET="mongo-app-client-cert-secret"
MONGO_DATABASE="HealthEventsDatabase"
OPLOG_SIZE_MB=990
PF_PID=""
CERT_DIR=""

log() {
    echo "[$(date +'%Y-%m-%d %H:%M:%S')] $*"
}

error() {
    log "ERROR: $*" >&2
    teardown_mongo
    exit 1
}

usage() {
    head -35 "$0" | tail -12
    exit 0
}

while [[ $# -gt 0 ]]; do
    case $1 in
        --response-delay) RESPONSE_DELAY="$2"; shift 2 ;;
        --insert-rate)    INSERT_RATE="$2"; shift 2 ;;
        --insert-batches) INSERT_BATCHES="$2"; shift 2 ;;
        --namespace)      NAMESPACE="$2"; shift 2 ;;
        --skip-setup)     SKIP_SETUP=true; shift ;;
        --cleanup)        CLEANUP=true; shift ;;
        --help|-h)        usage ;;
        *) error "Unknown option: $1" ;;
    esac
done

# --- MongoDB connection helpers ---

setup_mongo() {
    log "Setting up MongoDB connection (TLS + port-forward)..."

    if ! command -v mongosh &> /dev/null; then
        error "mongosh is not installed. See: https://www.mongodb.com/docs/mongodb-shell/install/"
    fi

    CERT_DIR=$(mktemp -d)

    kubectl get secret "$MONGO_SECRET" -n "$NAMESPACE" -o jsonpath='{.data.ca\.crt}' | base64 -d > "$CERT_DIR/ca.crt"
    kubectl get secret "$MONGO_SECRET" -n "$NAMESPACE" -o jsonpath='{.data.tls\.crt}' | base64 -d > "$CERT_DIR/tls.crt"
    kubectl get secret "$MONGO_SECRET" -n "$NAMESPACE" -o jsonpath='{.data.tls\.key}' | base64 -d > "$CERT_DIR/tls.key"
    cat "$CERT_DIR/tls.crt" "$CERT_DIR/tls.key" > "$CERT_DIR/creds.pem"
    chmod 600 "$CERT_DIR/creds.pem" "$CERT_DIR/ca.crt"

    kubectl port-forward "$MONGO_POD_NAME" "$MONGO_LOCAL_PORT:27017" -n "$NAMESPACE" &>/dev/null &
    PF_PID=$!
    sleep 3

    if ! kill -0 "$PF_PID" 2>/dev/null; then
        error "Port forward to MongoDB failed"
    fi

    log "MongoDB connection ready (port-forward PID: $PF_PID)"
}

teardown_mongo() {
    if [ -n "$PF_PID" ]; then
        kill "$PF_PID" 2>/dev/null || true
        PF_PID=""
    fi
    if [ -n "$CERT_DIR" ] && [ -d "$CERT_DIR" ]; then
        rm -rf "$CERT_DIR"
        CERT_DIR=""
    fi
}

trap teardown_mongo EXIT INT TERM

# App-level mongosh (X509 auth) — for database operations
mongosh_eval() {
    local script="$1"
    mongosh "mongodb://localhost:${MONGO_LOCAL_PORT}/${MONGO_DATABASE}?directConnection=true&authMechanism=MONGODB-X509&authSource=\$external&tls=true&tlsCAFile=${CERT_DIR}/ca.crt&tlsCertificateKeyFile=${CERT_DIR}/creds.pem&tlsAllowInvalidHostnames=true" \
        --quiet --eval "$script"
}

# Admin-level mongosh (SCRAM root auth) — for admin commands like oplog resize
mongosh_admin_eval() {
    local script="$1"
    kubectl exec -n "$NAMESPACE" "$MONGO_POD_NAME" -c mongodb -- bash -c "
        mongosh \"mongodb://root:\${MONGODB_ROOT_PASSWORD}@\$(hostname).mongodb-headless.${NAMESPACE}.svc.cluster.local:27017/admin?directConnection=true&tls=true&tlsCertificateKeyFile=/certs/mongodb.pem&tlsCAFile=/certs/mongodb-ca-cert&authMechanism=SCRAM-SHA-256\" \
            --quiet --eval '$script'
    "
}

# --- K8s helpers ---

get_event_exporter_pod() {
    kubectl get pods -n "$NAMESPACE" -l app.kubernetes.io/name=event-exporter \
        -o jsonpath='{.items[0].metadata.name}' 2>/dev/null
}

get_mock_pod() {
    kubectl get pods -n "$NAMESPACE" -l app=event-exporter-mock \
        -o jsonpath='{.items[0].metadata.name}' 2>/dev/null
}

# --- Cleanup mode ---

if $CLEANUP; then
    log "=== Cleaning up test data ==="
    setup_mongo
    log "Removing test events..."
    mongosh_eval '
        var result = db.HealthEvents.deleteMany({
            "healthevent.checkname": "oplog-pressure-test"
        });
        print("Deleted " + result.deletedCount + " test documents");
    '
    log "Cleanup complete"
    exit 0
fi

# --- Main flow ---

log "=== ChangeStreamHistoryLost Reproduction Script ==="
log "Configuration:"
log "  Response delay:  ${RESPONSE_DELAY}"
log "  Oplog size:      ${OPLOG_SIZE_MB} MB (MongoDB minimum)"
log "  Insert rate:     ${INSERT_RATE} events/batch"
log "  Insert batches:  ${INSERT_BATCHES}"
log "  Namespace:       ${NAMESPACE}"

EXPORTER_POD=$(get_event_exporter_pod) || error "Event-exporter pod not found in namespace $NAMESPACE"
log "Event-exporter pod: ${EXPORTER_POD}"

MOCK_POD=$(get_mock_pod) || error "Mock server pod not found in namespace $NAMESPACE"
log "Mock server pod: ${MOCK_POD}"

setup_mongo

if ! $SKIP_SETUP; then
    log ""
    log "=== Phase 1: Configure mock server delay ==="
    log "Setting RESPONSE_DELAY=${RESPONSE_DELAY} on mock server deployment..."
    kubectl set env deployment/event-exporter-mock -n "$NAMESPACE" "RESPONSE_DELAY=${RESPONSE_DELAY}"
    log "Waiting for mock server rollout..."
    kubectl rollout status deployment/event-exporter-mock -n "$NAMESPACE" --timeout=60s

    log ""
    log "=== Phase 2: Shrink MongoDB oplog ==="
    log "Resizing oplog to ${OPLOG_SIZE_MB} MB (MongoDB minimum)..."
    mongosh_admin_eval "db.adminCommand({ replSetResizeOplog: 1, size: ${OPLOG_SIZE_MB} })" || \
        log "WARNING: Failed to resize oplog. Continuing with default size..."

    log ""
    log "=== Phase 3: Clean up old test data ==="
    log "Removing old test events (both camelCase and lowercase field paths)..."
    mongosh_eval '
        var r1 = db.HealthEvents.deleteMany({"healthEvent.checkName": "oplog-pressure-test"});
        var r2 = db.HealthEvents.deleteMany({"healthevent.checkname": "oplog-pressure-test"});
        print("Deleted " + (r1.deletedCount + r2.deletedCount) + " old test documents");
    ' || log "WARNING: Failed to clean up old test data"

    log ""
    log "=== Phase 4: Reset event-exporter ==="
    log "Deleting stale resume token (if any)..."
    mongosh_eval '
        var result = db.getSiblingDB("HealthEventsDatabase").ResumeTokens.deleteOne({
            clientName: "event-exporter"
        });
        print("Deleted " + result.deletedCount + " resume token(s)");
    ' || log "WARNING: Failed to delete resume token"

    log "Restarting event-exporter..."
    kubectl rollout restart deployment/event-exporter -n "$NAMESPACE" || \
        kubectl rollout restart deployment -l app.kubernetes.io/name=event-exporter -n "$NAMESPACE"
    kubectl rollout status deployment/event-exporter -n "$NAMESPACE" --timeout=120s || true
    log "Waiting for event-exporter to initialize..."
    sleep 10
fi

log ""
log "=== Phase 5: Record baseline ==="
MOCK_EVENTS_BEFORE=$(kubectl exec -n "$NAMESPACE" "$(get_mock_pod)" -- \
    wget -qO- http://localhost:8443/metrics 2>/dev/null | grep "^mock_events_received_total" | awk '{print $2}') || MOCK_EVENTS_BEFORE=0
log "Mock events before insertion: ${MOCK_EVENTS_BEFORE}"

log ""
log "=== Phase 6: Bulk insert events (${INSERT_RATE} x ${INSERT_BATCHES} batches) ==="
TOTAL_EVENTS=$((INSERT_RATE * INSERT_BATCHES))
log "Expected total: ${TOTAL_EVENTS} events"
log "Each document includes 100KB padding to fill the ${OPLOG_SIZE_MB} MB oplog faster"
log "At ${RESPONSE_DELAY} per publish (sequential), exporter is far too slow to keep up"
log ""

START_TIME=$(date +%s)

mongosh_eval "
    var insertRate = ${INSERT_RATE};
    var batches = ${INSERT_BATCHES};
    var totalInserted = 0;
    var padding = 'x'.repeat(102400);

    for (var batch = 0; batch < batches; batch++) {
        var bulk = db.HealthEvents.initializeUnorderedBulkOp();
        var nowSec = Long(Math.floor(Date.now() / 1000));
        for (var i = 0; i < insertRate; i++) {
            bulk.insert({
                createdAt: new Date(),
                healthevent: {
                    version: 1,
                    agent: 'test-agent',
                    componentclass: 'GPU',
                    checkname: 'oplog-pressure-test',
                    isfatal: true,
                    ishealthy: false,
                    message: 'test event batch ' + batch + ' idx ' + i,
                    nodename: 'test-node-' + (i % 100),
                    generatedtimestamp: { seconds: nowSec, nanos: 0 }
                },
                healtheventstatus: {
                    userpodsevictionstatus: { status: 'NotStarted', message: '' }
                },
                _padding: padding
            });
        }
        bulk.execute();
        totalInserted += insertRate;

        if (batch % 10 === 0) {
            print('Inserted ' + totalInserted + ' events (' + batch + '/' + batches + ' batches)');
        }
        sleep(1000);
    }
    print('Total inserted: ' + totalInserted + ' events');
"

END_TIME=$(date +%s)
DURATION=$((END_TIME - START_TIME))
log "Insertion complete in ${DURATION} seconds"

log ""
log "=== Phase 7: Restart event-exporter to trigger resume from stale token ==="
log "The exporter is behind. Restarting forces it to resume from the stale token."
kubectl rollout restart deployment/event-exporter -n "$NAMESPACE"
kubectl rollout status deployment/event-exporter -n "$NAMESPACE" --timeout=120s || true
sleep 15

log ""
log "=== Phase 8: Check results ==="

MOCK_EVENTS_AFTER=$(kubectl exec -n "$NAMESPACE" "$(get_mock_pod)" -- \
    wget -qO- http://localhost:8443/metrics 2>/dev/null | grep "^mock_events_received_total" | awk '{print $2}') || MOCK_EVENTS_AFTER=0
EVENTS_EXPORTED=$((MOCK_EVENTS_AFTER - MOCK_EVENTS_BEFORE))

log "Events inserted into MongoDB: ${TOTAL_EVENTS}"
log "Events exported to mock sink: ${EVENTS_EXPORTED}"
log "Events lost/pending:          $((TOTAL_EVENTS - EVENTS_EXPORTED))"

log ""
log "=== Phase 9: Check for ChangeStreamHistoryLost ==="
EXPORTER_POD=$(get_event_exporter_pod) || true
if [ -n "$EXPORTER_POD" ]; then
    HISTORY_LOST_COUNT=$(kubectl logs "$EXPORTER_POD" -n "$NAMESPACE" --tail=1000 2>/dev/null | \
        grep -c "ChangeStreamHistoryLost" || true)
    log "ChangeStreamHistoryLost errors in logs: ${HISTORY_LOST_COUNT}"

    if [ "$HISTORY_LOST_COUNT" -gt 0 ]; then
        log ""
        log "SUCCESS: ChangeStreamHistoryLost reproduced!"
        log "The event-exporter is stuck in an infinite error loop."
        log ""
        log "Last 5 error lines:"
        kubectl logs "$EXPORTER_POD" -n "$NAMESPACE" --tail=1000 2>/dev/null | \
            grep "ChangeStreamHistoryLost" | tail -5
    else
        log ""
        log "ChangeStreamHistoryLost not yet triggered."
        log "The exporter may still be processing. Wait and re-check with:"
        log "  kubectl logs $EXPORTER_POD -n $NAMESPACE --tail=100 | grep ChangeStreamHistoryLost"
    fi
else
    log "WARNING: Could not find event-exporter pod to check logs"
fi

log ""
log "=== Summary ==="
log "Inserted:  ${TOTAL_EVENTS} events in ${DURATION}s (~$((TOTAL_EVENTS / (DURATION + 1))) events/sec)"
log "Exported:  ${EVENTS_EXPORTED} events"
log "Errors:    ${HISTORY_LOST_COUNT:-unknown} ChangeStreamHistoryLost occurrences"
log ""
log "To clean up test data: $0 --cleanup"
log "To monitor: kubectl logs -f deployment/event-exporter -n $NAMESPACE"
