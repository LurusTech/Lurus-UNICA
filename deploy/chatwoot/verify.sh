#!/bin/bash
# Verify Chatwoot deployment on K3s cluster.
#
# Checks:
#   1. Pods are running
#   2. Service is accessible
#   3. Database connectivity
#   4. REST API responds
#   5. WebSocket endpoint available
#
# Usage:
#   bash deploy/chatwoot/verify.sh

set -euo pipefail

NAMESPACE="unica-dev"
PASS=0
FAIL=0
WARN=0

check() {
  local desc="$1"
  shift
  if "$@" > /dev/null 2>&1; then
    echo "  [PASS] $desc"
    PASS=$((PASS + 1))
  else
    echo "  [FAIL] $desc"
    FAIL=$((FAIL + 1))
  fi
}

warn_check() {
  local desc="$1"
  shift
  if "$@" > /dev/null 2>&1; then
    echo "  [PASS] $desc"
    PASS=$((PASS + 1))
  else
    echo "  [WARN] $desc (non-critical)"
    WARN=$((WARN + 1))
  fi
}

echo "=== Chatwoot Deployment Verification ==="
echo ""

# 1. Check pods
echo "[1] Pod Status:"
check "chatwoot-web pod is Running" \
  kubectl get pods -n "$NAMESPACE" -l "app.kubernetes.io/name=chatwoot,app.kubernetes.io/component=web" \
  -o jsonpath='{.items[0].status.phase}' | grep -q Running

check "chatwoot-sidekiq pod is Running" \
  kubectl get pods -n "$NAMESPACE" -l "app.kubernetes.io/name=chatwoot,app.kubernetes.io/component=sidekiq" \
  -o jsonpath='{.items[0].status.phase}' | grep -q Running
echo ""

# 2. Check service
echo "[2] Service Status:"
check "chatwoot Service exists" \
  kubectl get svc chatwoot -n "$NAMESPACE"
echo ""

# 3. Check database
echo "[3] Database:"
check "chatwoot_db exists in PostgreSQL" \
  kubectl exec -n "$NAMESPACE" postgresql-0 -- \
  env PGPASSWORD="$(kubectl get secret unica-db-credentials -n "$NAMESPACE" -o jsonpath='{.data.POSTGRES_PASSWORD}' | base64 -d)" \
  psql -h localhost -U postgres -lqt | grep -q chatwoot_db
echo ""

# 4. Check REST API via port-forward (temporary)
echo "[4] REST API (via internal pod exec):"
CW_POD=$(kubectl get pods -n "$NAMESPACE" -l "app.kubernetes.io/name=chatwoot,app.kubernetes.io/component=web" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")

if [ -n "$CW_POD" ]; then
  check "GET /auth/sign_in returns HTTP 200" \
    kubectl exec -n "$NAMESPACE" "$CW_POD" -- \
    curl -sf -o /dev/null -w '%{http_code}' http://localhost:3000/auth/sign_in | grep -q 200

  warn_check "GET /api/v1/profile responds" \
    kubectl exec -n "$NAMESPACE" "$CW_POD" -- \
    curl -sf -o /dev/null http://localhost:3000/api/v1/profile
else
  echo "  [FAIL] No chatwoot-web pod found"
  FAIL=$((FAIL + 1))
fi
echo ""

# 5. Check WebSocket endpoint
echo "[5] WebSocket Endpoint:"
if [ -n "$CW_POD" ]; then
  warn_check "ActionCable endpoint is available (/cable)" \
    kubectl exec -n "$NAMESPACE" "$CW_POD" -- \
    curl -sf -o /dev/null -w '%{http_code}' http://localhost:3000/cable
fi
echo ""

# 6. Check PVC
echo "[6] Storage:"
check "chatwoot-storage PVC is Bound" \
  kubectl get pvc chatwoot-storage -n "$NAMESPACE" -o jsonpath='{.status.phase}' | grep -q Bound
echo ""

# 7. Check Jobs completed
echo "[7] Init Jobs:"
warn_check "chatwoot-db-init job completed" \
  kubectl get job chatwoot-db-init -n "$NAMESPACE" -o jsonpath='{.status.conditions[?(@.type=="Complete")].status}' | grep -q True

warn_check "chatwoot-migrate job completed" \
  kubectl get job chatwoot-migrate -n "$NAMESPACE" -o jsonpath='{.status.conditions[?(@.type=="Complete")].status}' | grep -q True

warn_check "chatwoot-admin-init job completed" \
  kubectl get job chatwoot-admin-init -n "$NAMESPACE" -o jsonpath='{.status.conditions[?(@.type=="Complete")].status}' | grep -q True
echo ""

# Summary
echo "=== Verification Summary ==="
echo "  Passed:   $PASS"
echo "  Failed:   $FAIL"
echo "  Warnings: $WARN"
echo ""

if [ "$FAIL" -gt 0 ]; then
  echo "Some checks FAILED. Review the output above."
  echo ""
  echo "Troubleshooting:"
  echo "  kubectl get pods -n $NAMESPACE -l app.kubernetes.io/name=chatwoot"
  echo "  kubectl logs -n $NAMESPACE -l app.kubernetes.io/name=chatwoot,app.kubernetes.io/component=web --tail=50"
  echo "  kubectl describe pod -n $NAMESPACE -l app.kubernetes.io/name=chatwoot,app.kubernetes.io/component=web"
  exit 1
else
  echo "All critical checks passed!"
  echo ""
  echo "Next steps:"
  echo "  1. Port-forward to access web UI:"
  echo "     kubectl port-forward svc/chatwoot -n $NAMESPACE 3000:3000"
  echo "  2. Open http://localhost:3000 and login with admin credentials"
  echo "  3. Create an Account via super admin panel (/super_admin)"
  echo "  4. Test Custom Channel inbox creation via API"
  exit 0
fi
