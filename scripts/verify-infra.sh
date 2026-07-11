#!/usr/bin/env bash
# UNICA Infrastructure Verification Script
# Verifies PostgreSQL, Redis, pgvector, and Redis Streams are functional
#
# Usage: bash scripts/verify-infra.sh

set -euo pipefail

NAMESPACE="unica-dev"
PASS=0
FAIL=0

check() {
  local name="$1"
  shift
  if "$@" >/dev/null 2>&1; then
    echo "[PASS] $name"
    ((PASS++))
  else
    echo "[FAIL] $name"
    ((FAIL++))
  fi
}

echo "=== UNICA Infrastructure Verification ==="
echo ""

# 1. Namespace
echo "--- Namespace ---"
check "Namespace $NAMESPACE exists" kubectl get namespace "$NAMESPACE"
echo ""

# 2. Pods running
echo "--- Pod Status ---"
kubectl get pods -n "$NAMESPACE" -o wide 2>/dev/null || echo "No pods found"
echo ""

# 3. PostgreSQL
echo "--- PostgreSQL ---"
PG_POD=$(kubectl get pods -n "$NAMESPACE" -l app.kubernetes.io/name=postgresql -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
if [ -n "$PG_POD" ]; then
  check "PostgreSQL pod running" kubectl get pod "$PG_POD" -n "$NAMESPACE" -o jsonpath='{.status.phase}' | grep -q Running

  # Connection test
  check "PostgreSQL accepts connections" \
    kubectl exec "$PG_POD" -n "$NAMESPACE" -- \
    bash -c 'PGPASSWORD="$POSTGRES_PASSWORD" psql -U postgres -d unica_core -c "SELECT 1;"'

  # pgvector test
  echo ""
  echo "--- pgvector Extension ---"
  PG_VECTOR_OUT=$(kubectl exec "$PG_POD" -n "$NAMESPACE" -- \
    bash -c 'PGPASSWORD="$POSTGRES_PASSWORD" psql -U postgres -d unica_core -t -c "SELECT extversion FROM pg_extension WHERE extname='"'"'vector'"'"';"' 2>/dev/null || echo "")
  if [ -n "$(echo "$PG_VECTOR_OUT" | tr -d '[:space:]')" ]; then
    echo "[PASS] pgvector installed (version: $(echo "$PG_VECTOR_OUT" | tr -d '[:space:]'))"
    ((PASS++))
  else
    # Try creating it
    kubectl exec "$PG_POD" -n "$NAMESPACE" -- \
      bash -c 'PGPASSWORD="$POSTGRES_PASSWORD" psql -U postgres -d unica_core -c "CREATE EXTENSION IF NOT EXISTS vector;"' >/dev/null 2>&1
    check "pgvector extension created" \
      kubectl exec "$PG_POD" -n "$NAMESPACE" -- \
      bash -c 'PGPASSWORD="$POSTGRES_PASSWORD" psql -U postgres -d unica_core -c "SELECT extversion FROM pg_extension WHERE extname='"'"'vector'"'"';"'
  fi

  # PVC test
  echo ""
  echo "--- PostgreSQL Persistence ---"
  check "PostgreSQL PVC bound" \
    kubectl get pvc -n "$NAMESPACE" -l app.kubernetes.io/name=postgresql -o jsonpath='{.items[0].status.phase}' | grep -q Bound
else
  echo "[FAIL] PostgreSQL pod not found"
  ((FAIL++))
fi
echo ""

# 4. Redis
echo "--- Redis ---"
REDIS_POD=$(kubectl get pods -n "$NAMESPACE" -l app.kubernetes.io/name=redis -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
if [ -n "$REDIS_POD" ]; then
  check "Redis pod running" kubectl get pod "$REDIS_POD" -n "$NAMESPACE" -o jsonpath='{.status.phase}' | grep -q Running

  # Connection test
  check "Redis accepts connections (PING)" \
    kubectl exec "$REDIS_POD" -n "$NAMESPACE" -- \
    redis-cli -a "$REDIS_PASSWORD" ping 2>/dev/null | grep -q PONG

  # Streams test
  echo ""
  echo "--- Redis Streams ---"
  kubectl exec "$REDIS_POD" -n "$NAMESPACE" -- \
    redis-cli -a "$REDIS_PASSWORD" XADD unica:test_stream '*' key value >/dev/null 2>&1
  STREAM_OUT=$(kubectl exec "$REDIS_POD" -n "$NAMESPACE" -- \
    redis-cli -a "$REDIS_PASSWORD" XREAD COUNT 1 STREAMS unica:test_stream 0 2>/dev/null || echo "")
  if echo "$STREAM_OUT" | grep -q "key"; then
    echo "[PASS] Redis Streams functional (XADD/XREAD)"
    ((PASS++))
  else
    echo "[FAIL] Redis Streams not functional"
    ((FAIL++))
  fi
  # Cleanup test stream
  kubectl exec "$REDIS_POD" -n "$NAMESPACE" -- \
    redis-cli -a "$REDIS_PASSWORD" DEL unica:test_stream >/dev/null 2>&1

  # PVC test
  echo ""
  echo "--- Redis Persistence ---"
  check "Redis PVC bound" \
    kubectl get pvc -n "$NAMESPACE" -l app.kubernetes.io/name=redis -o jsonpath='{.items[0].status.phase}' | grep -q Bound
else
  echo "[FAIL] Redis pod not found"
  ((FAIL++))
fi
echo ""

# 5. Secrets
echo "--- Secrets ---"
check "unica-db-credentials secret exists" \
  kubectl get secret unica-db-credentials -n "$NAMESPACE"
echo ""

# Summary
echo "=== Verification Summary ==="
echo "Passed: $PASS"
echo "Failed: $FAIL"
echo ""
if [ "$FAIL" -eq 0 ]; then
  echo "All checks passed! Infrastructure is ready."
  exit 0
else
  echo "Some checks failed. Review output above."
  exit 1
fi
