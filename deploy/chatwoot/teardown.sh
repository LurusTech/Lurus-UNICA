#!/bin/bash
# Remove Chatwoot deployment from K3s cluster.
#
# Usage:
#   bash deploy/chatwoot/teardown.sh [--keep-db]
#
# Options:
#   --keep-db   Keep the chatwoot_db database (only remove K3s resources)

set -euo pipefail

NAMESPACE="unica-dev"
KEEP_DB=false

for arg in "$@"; do
  case $arg in
    --keep-db) KEEP_DB=true ;;
    *) echo "Unknown argument: $arg"; exit 1 ;;
  esac
done

echo "=== Removing Chatwoot from namespace: $NAMESPACE ==="

echo "Deleting deployments..."
kubectl delete deployment chatwoot-web chatwoot-sidekiq -n "$NAMESPACE" --ignore-not-found=true

echo "Deleting services..."
kubectl delete svc chatwoot -n "$NAMESPACE" --ignore-not-found=true

echo "Deleting jobs..."
kubectl delete job chatwoot-db-init chatwoot-migrate chatwoot-admin-init -n "$NAMESPACE" --ignore-not-found=true

echo "Deleting ConfigMap and Secrets..."
kubectl delete configmap chatwoot-env -n "$NAMESPACE" --ignore-not-found=true
kubectl delete secret chatwoot-secrets -n "$NAMESPACE" --ignore-not-found=true

echo "Deleting PVC (data will be lost)..."
kubectl delete pvc chatwoot-storage -n "$NAMESPACE" --ignore-not-found=true

if [ "$KEEP_DB" = false ]; then
  echo "Dropping chatwoot_db database..."
  PG_PASS=$(kubectl get secret unica-db-credentials -n "$NAMESPACE" -o jsonpath='{.data.POSTGRES_PASSWORD}' | base64 -d)
  kubectl exec -n "$NAMESPACE" postgresql-0 -- \
    env PGPASSWORD="$PG_PASS" psql -h localhost -U postgres -c "DROP DATABASE IF EXISTS chatwoot_db;"
  kubectl exec -n "$NAMESPACE" postgresql-0 -- \
    env PGPASSWORD="$PG_PASS" psql -h localhost -U postgres -c "DROP ROLE IF EXISTS chatwoot;"
  echo "Database dropped."
else
  echo "Keeping chatwoot_db database (--keep-db)"
fi

echo ""
echo "=== Chatwoot removed successfully ==="
