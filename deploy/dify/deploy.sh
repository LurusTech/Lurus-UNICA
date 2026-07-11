#!/bin/bash
# Deploy Dify services to K3s cluster
# Usage: bash deploy.sh [apply|delete|status]
#
# Prerequisites:
#   - K3s cluster running with kubectl configured
#   - Namespace 'unica-dev' created (deploy/namespace.yaml)
#   - PostgreSQL running (STORY-001)
#   - Redis running (STORY-001)
#   - Secret values updated in dify-secrets.yaml
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NAMESPACE="unica-dev"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info()  { echo -e "${GREEN}[INFO]${NC}  $*"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC}  $*"; }
log_error() { echo -e "${RED}[ERROR]${NC} $*"; }

check_prerequisites() {
    log_info "Checking prerequisites..."

    if ! command -v kubectl &>/dev/null; then
        log_error "kubectl not found. Please install kubectl."
        exit 1
    fi

    # Check namespace exists
    if ! kubectl get namespace "$NAMESPACE" &>/dev/null; then
        log_error "Namespace '$NAMESPACE' not found. Apply deploy/namespace.yaml first."
        exit 1
    fi

    # Check PostgreSQL is running
    if ! kubectl -n "$NAMESPACE" get pods -l app.kubernetes.io/name=postgresql --field-selector=status.phase=Running 2>/dev/null | grep -q Running; then
        log_warn "PostgreSQL does not appear to be running. Dify DB init may fail."
    fi

    # Check Redis is running
    if ! kubectl -n "$NAMESPACE" get pods -l app.kubernetes.io/name=redis --field-selector=status.phase=Running 2>/dev/null | grep -q Running; then
        log_warn "Redis does not appear to be running. Dify services may fail to start."
    fi

    # Check secrets have been customized
    if grep -q "CHANGE_ME" "$SCRIPT_DIR/dify-secrets.yaml"; then
        log_warn "dify-secrets.yaml still contains placeholder values (CHANGE_ME)."
        log_warn "Update the secret values before deploying to production."
    fi

    log_info "Prerequisites check complete."
}

apply() {
    check_prerequisites

    log_info "Deploying Dify to namespace '$NAMESPACE'..."

    # Step 1: Apply secrets
    log_info "Step 1/7: Applying secrets..."
    kubectl apply -f "$SCRIPT_DIR/dify-secrets.yaml"

    # Step 2: Apply PVCs
    log_info "Step 2/7: Applying persistent volume claims..."
    kubectl apply -f "$SCRIPT_DIR/dify-pvc.yaml"

    # Step 3: Initialize database
    log_info "Step 3/7: Initializing Dify database..."
    # Delete previous job if exists (jobs are immutable)
    kubectl -n "$NAMESPACE" delete job dify-db-init --ignore-not-found=true
    kubectl apply -f "$SCRIPT_DIR/dify-db-init.yaml"

    log_info "Waiting for DB init job to complete (timeout: 120s)..."
    if ! kubectl -n "$NAMESPACE" wait --for=condition=complete job/dify-db-init --timeout=120s; then
        log_error "DB init job failed. Check logs with:"
        log_error "  kubectl -n $NAMESPACE logs job/dify-db-init"
        exit 1
    fi
    log_info "Database initialized successfully."

    # Step 4: Deploy SSRF proxy (needed by sandbox and API)
    log_info "Step 4/7: Deploying SSRF proxy..."
    kubectl apply -f "$SCRIPT_DIR/dify-ssrf-proxy.yaml"

    # Step 5: Deploy sandbox (needed by API)
    log_info "Step 5/7: Deploying sandbox..."
    kubectl apply -f "$SCRIPT_DIR/dify-sandbox.yaml"

    # Step 6: Deploy API, Worker, Web
    log_info "Step 6/7: Deploying API, Worker, and Web..."
    kubectl apply -f "$SCRIPT_DIR/dify-api.yaml"
    kubectl apply -f "$SCRIPT_DIR/dify-worker.yaml"
    kubectl apply -f "$SCRIPT_DIR/dify-web.yaml"

    # Step 7: Deploy Nginx reverse proxy
    log_info "Step 7/7: Deploying Nginx reverse proxy..."
    kubectl apply -f "$SCRIPT_DIR/dify-nginx.yaml"

    log_info "All Dify manifests applied."
    log_info ""
    log_info "Waiting for pods to be ready (this may take 2-5 minutes)..."
    kubectl -n "$NAMESPACE" rollout status deployment/dify-ssrf-proxy --timeout=120s || true
    kubectl -n "$NAMESPACE" rollout status deployment/dify-sandbox --timeout=120s || true
    kubectl -n "$NAMESPACE" rollout status deployment/dify-api --timeout=300s || true
    kubectl -n "$NAMESPACE" rollout status deployment/dify-worker --timeout=300s || true
    kubectl -n "$NAMESPACE" rollout status deployment/dify-web --timeout=120s || true
    kubectl -n "$NAMESPACE" rollout status deployment/dify-nginx --timeout=120s || true

    log_info ""
    log_info "===== Dify Deployment Complete ====="
    status
}

delete() {
    log_info "Removing Dify from namespace '$NAMESPACE'..."

    kubectl delete -f "$SCRIPT_DIR/dify-nginx.yaml" --ignore-not-found=true
    kubectl delete -f "$SCRIPT_DIR/dify-web.yaml" --ignore-not-found=true
    kubectl delete -f "$SCRIPT_DIR/dify-worker.yaml" --ignore-not-found=true
    kubectl delete -f "$SCRIPT_DIR/dify-api.yaml" --ignore-not-found=true
    kubectl delete -f "$SCRIPT_DIR/dify-sandbox.yaml" --ignore-not-found=true
    kubectl delete -f "$SCRIPT_DIR/dify-ssrf-proxy.yaml" --ignore-not-found=true
    kubectl -n "$NAMESPACE" delete job dify-db-init --ignore-not-found=true

    log_info "Dify services removed."
    log_warn "PVC (dify-storage) and secrets (dify-secrets) are NOT deleted."
    log_warn "To delete data: kubectl -n $NAMESPACE delete pvc dify-storage"
    log_warn "To delete secrets: kubectl -n $NAMESPACE delete secret dify-secrets"
}

status() {
    echo ""
    log_info "Dify pods status:"
    kubectl -n "$NAMESPACE" get pods -l app.kubernetes.io/component=dify -o wide 2>/dev/null || echo "No Dify pods found."

    echo ""
    log_info "Dify services:"
    kubectl -n "$NAMESPACE" get svc -l app.kubernetes.io/component=dify 2>/dev/null || echo "No Dify services found."

    echo ""
    log_info "Access Dify Web UI:"
    # Get node IP
    NODE_IP=$(kubectl get nodes -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}' 2>/dev/null || echo "NODE_IP")
    echo "  http://${NODE_IP}:30001"
    echo ""
    log_info "First-time setup: visit the URL above to create an admin account."
}

# Main
case "${1:-status}" in
    apply)  apply ;;
    delete) delete ;;
    status) status ;;
    *)
        echo "Usage: $0 [apply|delete|status]"
        exit 1
        ;;
esac
