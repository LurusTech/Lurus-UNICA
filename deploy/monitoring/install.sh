#!/usr/bin/env bash
# UNICA Monitoring Stack Installation Script
# Deploys Prometheus, Grafana, and Loki to K3s via Helm.
#
# Prerequisites:
#   - kubectl configured for K3s cluster
#   - Helm 3.x installed
#   - unica namespace exists (kubectl create namespace unica)
#
# Usage: bash deploy/monitoring/install.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DASHBOARD_DIR="${SCRIPT_DIR}/../grafana/dashboards"

echo "=== UNICA Monitoring Stack Installation ==="

# Add Helm repos
echo "[1/6] Adding Helm repositories..."
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts 2>/dev/null || true
helm repo add grafana https://grafana.github.io/helm-charts 2>/dev/null || true
helm repo update

# Create monitoring namespace
echo "[2/6] Creating monitoring namespace..."
kubectl create namespace monitoring 2>/dev/null || true

# Create dashboard ConfigMap from JSON files
echo "[3/6] Creating Grafana dashboard ConfigMap..."
kubectl create configmap unica-grafana-dashboards \
  --from-file=system-overview.json="${DASHBOARD_DIR}/system-overview.json" \
  --from-file=gateway-channel-status.json="${DASHBOARD_DIR}/gateway-channel-status.json" \
  --from-file=router-ai-pipeline.json="${DASHBOARD_DIR}/router-ai-pipeline.json" \
  --from-file=queue-backlog.json="${DASHBOARD_DIR}/queue-backlog.json" \
  --from-file=agent-workspace.json="${DASHBOARD_DIR}/agent-workspace.json" \
  --from-file=channel-traffic.json="${DASHBOARD_DIR}/channel-traffic.json" \
  -n monitoring --dry-run=client -o yaml | kubectl apply -f -

# Install kube-prometheus-stack (Prometheus + Grafana)
echo "[4/6] Installing kube-prometheus-stack..."
helm upgrade --install monitoring prometheus-community/kube-prometheus-stack \
  -n monitoring \
  -f "${SCRIPT_DIR}/prometheus-values.yaml" \
  --wait --timeout 5m

# Install Loki stack
echo "[5/6] Installing Loki stack..."
helm upgrade --install loki-stack grafana/loki-stack \
  -n monitoring \
  -f "${SCRIPT_DIR}/loki-values.yaml" \
  --wait --timeout 5m

# Apply ServiceMonitors for UNICA services
echo "[6/8] Applying ServiceMonitors..."
kubectl apply -f "${SCRIPT_DIR}/servicemonitors.yaml"

# Apply Prometheus alert rules
echo "[7/8] Applying Prometheus alert rules..."
kubectl apply -f "${SCRIPT_DIR}/alert-rules.yaml"

# Deploy webhook adapter
echo "[8/8] Deploying webhook adapter..."
kubectl apply -f "${SCRIPT_DIR}/../alertmanager/deployment.yaml"

echo ""
echo "=== Installation Complete ==="
echo "Grafana:       http://grafana.unica.local (admin / unica-grafana-admin)"
echo "Prometheus:    kubectl port-forward -n monitoring svc/monitoring-kube-prometheus-prometheus 9090:9090"
echo "AlertManager:  kubectl port-forward -n monitoring svc/monitoring-kube-prometheus-alertmanager 9093:9093"
echo ""
echo "Verify targets: open Prometheus UI -> Status -> Targets"
echo "Verify dashboards: open Grafana -> Dashboards -> UNICA folder"
echo "Verify alert rules: open Prometheus UI -> Alerts"
echo ""
echo "IMPORTANT: Update webhook URLs in deploy/alertmanager/deployment.yaml Secret before alerts will work."
