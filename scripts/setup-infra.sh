#!/usr/bin/env bash
# UNICA Infrastructure Setup Script
# Sets up K3s namespace, PostgreSQL, and Redis for development
#
# Prerequisites:
#   - K3s cluster running and kubectl configured
#   - Helm 3 installed
#   - Secrets in deploy/secrets/unica-secrets.yaml updated with real passwords
#
# Usage: bash scripts/setup-infra.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$SCRIPT_DIR/../deploy"
NAMESPACE="unica-dev"

echo "=== UNICA Infrastructure Setup ==="
echo ""

# Pre-flight checks
echo "--- Pre-flight Checks ---"
command -v kubectl >/dev/null 2>&1 || { echo "ERROR: kubectl not found"; exit 1; }
command -v helm >/dev/null 2>&1 || { echo "ERROR: helm not found"; exit 1; }
kubectl cluster-info >/dev/null 2>&1 || { echo "ERROR: Cannot connect to K3s cluster"; exit 1; }
echo "[OK] kubectl and helm available, cluster reachable"
echo ""

# Step 1: Create namespace
echo "--- Step 1: Create Namespace ($NAMESPACE) ---"
kubectl apply -f "$DEPLOY_DIR/namespace.yaml"
echo ""

# Step 2: Apply secrets
echo "--- Step 2: Apply Secrets ---"
if grep -q "CHANGE_ME" "$DEPLOY_DIR/secrets/unica-secrets.yaml"; then
  echo "WARNING: Secrets file contains placeholder values (CHANGE_ME)."
  echo "         Update deploy/secrets/unica-secrets.yaml before production use."
  echo "         Proceeding with placeholder values for dev..."
fi
kubectl apply -f "$DEPLOY_DIR/secrets/unica-secrets.yaml"
echo ""

# Step 3: Add Helm repos
echo "--- Step 3: Add Helm Repositories ---"
helm repo add bitnami https://charts.bitnami.com/bitnami 2>/dev/null || true
helm repo update
echo ""

# Step 4: Install PostgreSQL
echo "--- Step 4: Install PostgreSQL ---"
if helm status postgresql -n "$NAMESPACE" >/dev/null 2>&1; then
  echo "PostgreSQL already installed, upgrading..."
  helm upgrade postgresql bitnami/postgresql \
    -n "$NAMESPACE" \
    -f "$DEPLOY_DIR/postgresql/values.yaml" \
    --wait --timeout 5m
else
  helm install postgresql bitnami/postgresql \
    -n "$NAMESPACE" \
    -f "$DEPLOY_DIR/postgresql/values.yaml" \
    --wait --timeout 5m
fi
echo ""

# Step 5: Install Redis
echo "--- Step 5: Install Redis ---"
if helm status redis -n "$NAMESPACE" >/dev/null 2>&1; then
  echo "Redis already installed, upgrading..."
  helm upgrade redis bitnami/redis \
    -n "$NAMESPACE" \
    -f "$DEPLOY_DIR/redis/values.yaml" \
    --wait --timeout 5m
else
  helm install redis bitnami/redis \
    -n "$NAMESPACE" \
    -f "$DEPLOY_DIR/redis/values.yaml" \
    --wait --timeout 5m
fi
echo ""

echo "=== Infrastructure Setup Complete ==="
echo ""
echo "Next steps:"
echo "  1. Run 'bash scripts/verify-infra.sh' to verify all services"
echo "  2. Or apply test pod: kubectl apply -f deploy/test-pod.yaml"
echo ""
