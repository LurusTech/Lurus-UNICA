#!/bin/bash
# Verify Dify deployment on K3s cluster
# Checks all acceptance criteria for STORY-004
#
set -euo pipefail

NAMESPACE="unica-dev"
PASS=0
FAIL=0
WARN=0

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "  ${GREEN}[PASS]${NC} $*"; ((PASS++)); }
fail() { echo -e "  ${RED}[FAIL]${NC} $*"; ((FAIL++)); }
warn() { echo -e "  ${YELLOW}[WARN]${NC} $*"; ((WARN++)); }
info() { echo -e "  [INFO] $*"; }

echo "================================================"
echo "  Dify Deployment Verification (STORY-004)"
echo "================================================"
echo ""

# --- 1. Check all pods are running ---
echo "1. Checking Dify pods..."
for DEPLOY in dify-api dify-worker dify-web dify-sandbox dify-ssrf-proxy dify-nginx; do
    STATUS=$(kubectl -n "$NAMESPACE" get deployment "$DEPLOY" -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
    if [ "${STATUS:-0}" -ge 1 ]; then
        pass "$DEPLOY is running (ready replicas: $STATUS)"
    else
        fail "$DEPLOY is NOT ready"
    fi
done

echo ""

# --- 2. Check services ---
echo "2. Checking Dify services..."
for SVC in dify-api dify-web dify-sandbox dify-ssrf-proxy dify-nginx; do
    if kubectl -n "$NAMESPACE" get svc "$SVC" &>/dev/null; then
        pass "Service $SVC exists"
    else
        fail "Service $SVC not found"
    fi
done

echo ""

# --- 3. Check database ---
echo "3. Checking Dify database (pgvector)..."
DB_POD=$(kubectl -n "$NAMESPACE" get pods -l app.kubernetes.io/name=postgresql -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
if [ -n "$DB_POD" ]; then
    # Check dify_db exists
    DB_EXISTS=$(kubectl -n "$NAMESPACE" exec "$DB_POD" -- bash -c "PGPASSWORD=\$POSTGRES_PASSWORD psql -U postgres -tAc \"SELECT 1 FROM pg_database WHERE datname='dify_db'\"" 2>/dev/null || echo "")
    if [ "$DB_EXISTS" = "1" ]; then
        pass "Database 'dify_db' exists"
    else
        fail "Database 'dify_db' not found"
    fi

    # Check pgvector extension
    PGVECTOR=$(kubectl -n "$NAMESPACE" exec "$DB_POD" -- bash -c "PGPASSWORD=\$POSTGRES_PASSWORD psql -U postgres -d dify_db -tAc \"SELECT 1 FROM pg_extension WHERE extname='vector'\"" 2>/dev/null || echo "")
    if [ "$PGVECTOR" = "1" ]; then
        pass "pgvector extension is enabled"
    else
        fail "pgvector extension not found in dify_db"
    fi

    # Check dify user
    DIFY_USER=$(kubectl -n "$NAMESPACE" exec "$DB_POD" -- bash -c "PGPASSWORD=\$POSTGRES_PASSWORD psql -U postgres -tAc \"SELECT 1 FROM pg_roles WHERE rolname='dify'\"" 2>/dev/null || echo "")
    if [ "$DIFY_USER" = "1" ]; then
        pass "Database user 'dify' exists"
    else
        fail "Database user 'dify' not found"
    fi
else
    fail "PostgreSQL pod not found"
fi

echo ""

# --- 4. Check Dify API health ---
echo "4. Checking Dify API health..."
API_POD=$(kubectl -n "$NAMESPACE" get pods -l app.kubernetes.io/name=dify-api -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
if [ -n "$API_POD" ]; then
    HEALTH=$(kubectl -n "$NAMESPACE" exec "$API_POD" -- curl -s -o /dev/null -w "%{http_code}" http://localhost:5001/health 2>/dev/null || echo "000")
    if [ "$HEALTH" = "200" ]; then
        pass "Dify API /health returns 200"
    else
        fail "Dify API /health returned $HEALTH (expected 200)"
    fi
else
    fail "Dify API pod not found"
fi

echo ""

# --- 5. Check Dify Web UI accessibility ---
echo "5. Checking Dify Web UI..."
NODE_IP=$(kubectl get nodes -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}' 2>/dev/null || echo "localhost")
WEB_STATUS=$(curl -s -o /dev/null -w "%{http_code}" "http://${NODE_IP}:30001" --connect-timeout 5 2>/dev/null || echo "000")
if [ "$WEB_STATUS" = "200" ] || [ "$WEB_STATUS" = "302" ]; then
    pass "Dify Web UI accessible at http://${NODE_IP}:30001 (HTTP $WEB_STATUS)"
else
    warn "Dify Web UI returned HTTP $WEB_STATUS at http://${NODE_IP}:30001"
    info "If running remotely, test access from a browser on the cluster network."
fi

echo ""

# --- 6. Check PVC ---
echo "6. Checking persistent storage..."
PVC_STATUS=$(kubectl -n "$NAMESPACE" get pvc dify-storage -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
if [ "$PVC_STATUS" = "Bound" ]; then
    pass "PVC 'dify-storage' is Bound"
else
    fail "PVC 'dify-storage' status: ${PVC_STATUS:-not found}"
fi

echo ""

# --- 7. Pod restart resilience (check restart count) ---
echo "7. Checking pod stability (restart counts)..."
RESTARTS=$(kubectl -n "$NAMESPACE" get pods -l app.kubernetes.io/component=dify -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{range .status.containerStatuses[*]}{.restartCount}{end}{"\n"}{end}' 2>/dev/null || echo "")
if [ -n "$RESTARTS" ]; then
    HIGH_RESTARTS=0
    while IFS=$'\t' read -r POD COUNT; do
        if [ "${COUNT:-0}" -gt 3 ]; then
            warn "$POD has $COUNT restarts"
            HIGH_RESTARTS=1
        fi
    done <<< "$RESTARTS"
    if [ "$HIGH_RESTARTS" -eq 0 ]; then
        pass "All Dify pods have acceptable restart counts"
    fi
else
    warn "Could not check pod restarts"
fi

echo ""

# --- Summary ---
echo "================================================"
echo "  Verification Summary"
echo "================================================"
echo -e "  ${GREEN}PASSED: $PASS${NC}"
echo -e "  ${RED}FAILED: $FAIL${NC}"
echo -e "  ${YELLOW}WARNINGS: $WARN${NC}"
echo ""

if [ "$FAIL" -eq 0 ]; then
    echo -e "${GREEN}All critical checks passed!${NC}"
    echo ""
    echo "Next steps (manual):"
    echo "  1. Open http://${NODE_IP}:30001 in browser"
    echo "  2. Create admin account on first visit"
    echo "  3. Configure LLM provider in Settings > Model Provider"
    echo "  4. Create a test workspace and agent"
    echo "  5. Test chat and knowledge base upload"
    exit 0
else
    echo -e "${RED}Some checks failed. Review the output above.${NC}"
    echo ""
    echo "Debug commands:"
    echo "  kubectl -n $NAMESPACE get pods -l app.kubernetes.io/component=dify"
    echo "  kubectl -n $NAMESPACE logs deployment/dify-api"
    echo "  kubectl -n $NAMESPACE logs deployment/dify-worker"
    echo "  kubectl -n $NAMESPACE describe pod -l app.kubernetes.io/name=dify-api"
    exit 1
fi
