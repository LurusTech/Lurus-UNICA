#!/bin/bash
# Deploy Chatwoot to K3s cluster (unica-dev namespace).
#
# Prerequisites:
#   - K3s cluster running with kubectl configured
#   - PostgreSQL and Redis deployed (STORY-001)
#   - Secrets in chatwoot-secrets.yaml updated with real values
#
# Usage:
#   cd deploy/chatwoot
#   bash deploy.sh [--skip-db-init] [--skip-migrate] [--skip-admin]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NAMESPACE="unica-dev"

SKIP_DB_INIT=false
SKIP_MIGRATE=false
SKIP_ADMIN=false

for arg in "$@"; do
  case $arg in
    --skip-db-init) SKIP_DB_INIT=true ;;
    --skip-migrate) SKIP_MIGRATE=true ;;
    --skip-admin)   SKIP_ADMIN=true ;;
    *) echo "Unknown argument: $arg"; exit 1 ;;
  esac
done

echo "=== Deploying Chatwoot to namespace: $NAMESPACE ==="

# Step 1: Apply secrets and config
echo ""
echo "[1/7] Applying secrets and ConfigMap..."
kubectl apply -f "$SCRIPT_DIR/chatwoot-secrets.yaml"
kubectl apply -f "$SCRIPT_DIR/chatwoot-env.yaml"

# Step 2: Create PVC
echo ""
echo "[2/7] Creating PersistentVolumeClaim..."
kubectl apply -f "$SCRIPT_DIR/chatwoot-pvc.yaml"

# Step 3: Initialize database
if [ "$SKIP_DB_INIT" = false ]; then
  echo ""
  echo "[3/7] Initializing Chatwoot database..."
  # Delete previous job if exists (jobs are immutable)
  kubectl delete job chatwoot-db-init -n "$NAMESPACE" --ignore-not-found=true
  kubectl apply -f "$SCRIPT_DIR/chatwoot-db-init.yaml"
  echo "Waiting for database init job to complete..."
  kubectl wait --for=condition=complete job/chatwoot-db-init -n "$NAMESPACE" --timeout=120s
  echo "Database init completed."
else
  echo ""
  echo "[3/7] Skipping database init (--skip-db-init)"
fi

# Step 4: Run migrations
if [ "$SKIP_MIGRATE" = false ]; then
  echo ""
  echo "[4/7] Running Chatwoot migrations..."
  kubectl delete job chatwoot-migrate -n "$NAMESPACE" --ignore-not-found=true
  kubectl apply -f "$SCRIPT_DIR/chatwoot-migrate.yaml"
  echo "Waiting for migration job to complete (this may take a few minutes)..."
  kubectl wait --for=condition=complete job/chatwoot-migrate -n "$NAMESPACE" --timeout=600s
  echo "Migrations completed."
else
  echo ""
  echo "[4/7] Skipping migrations (--skip-migrate)"
fi

# Step 5: Deploy web server
echo ""
echo "[5/7] Deploying Chatwoot web server..."
kubectl apply -f "$SCRIPT_DIR/chatwoot-web.yaml"

# Step 6: Deploy Sidekiq worker
echo ""
echo "[6/7] Deploying Chatwoot Sidekiq worker..."
kubectl apply -f "$SCRIPT_DIR/chatwoot-sidekiq.yaml"

# Step 7: Wait for web to be ready, then create admin
echo ""
echo "[7/7] Waiting for Chatwoot web to be ready..."
kubectl rollout status deployment/chatwoot-web -n "$NAMESPACE" --timeout=300s

if [ "$SKIP_ADMIN" = false ]; then
  echo ""
  echo "Creating super admin account..."
  kubectl delete job chatwoot-admin-init -n "$NAMESPACE" --ignore-not-found=true
  kubectl apply -f "$SCRIPT_DIR/chatwoot-admin-init.yaml"
  kubectl wait --for=condition=complete job/chatwoot-admin-init -n "$NAMESPACE" --timeout=120s
  echo "Admin account created."
else
  echo ""
  echo "Skipping admin creation (--skip-admin)"
fi

echo ""
echo "=== Chatwoot deployment complete! ==="
echo ""
echo "To access Chatwoot web UI:"
echo "  kubectl port-forward svc/chatwoot -n $NAMESPACE 3000:3000"
echo "  Then open: http://localhost:3000"
echo ""
echo "To verify the deployment:"
echo "  bash $SCRIPT_DIR/verify.sh"
