#!/usr/bin/env bash
# UNICA WSL2 Development Environment Setup
# Run this script INSIDE WSL2 (Ubuntu)
#
# Usage:
#   1. Open WSL terminal
#   2. cd /mnt/e/kefu
#   3. bash scripts/setup-wsl-env.sh
#
# What this script does:
#   - Installs Docker (if not present)
#   - Installs kubectl, helm, k3d
#   - Creates a k3d cluster named "unica-dev"
#   - Deploys PostgreSQL + Redis via setup-infra.sh

set -euo pipefail

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*"; exit 1; }

# Check we're in WSL
if ! grep -qi microsoft /proc/version 2>/dev/null; then
  error "This script must be run inside WSL2. Open your WSL terminal first."
fi

echo "============================================"
echo "  UNICA WSL2 Environment Setup"
echo "============================================"
echo ""

# -------------------------------------------
# Step 1: Docker
# -------------------------------------------
info "Step 1/5: Docker"

if command -v docker &>/dev/null && docker info &>/dev/null; then
  info "Docker already installed and running"
else
  if command -v docker &>/dev/null; then
    warn "Docker installed but not running. Attempting to start..."
    sudo service docker start 2>/dev/null || true
    sleep 2
    if docker info &>/dev/null; then
      info "Docker started"
    else
      warn "Cannot start Docker service. Installing fresh..."
    fi
  fi

  if ! docker info &>/dev/null 2>&1; then
    info "Installing Docker..."
    sudo apt-get update -qq
    sudo apt-get install -y -qq ca-certificates curl gnupg lsb-release

    # Add Docker GPG key and repo
    sudo install -m 0755 -d /etc/apt/keyrings
    if [ ! -f /etc/apt/keyrings/docker.gpg ]; then
      curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo gpg --dearmor -o /etc/apt/keyrings/docker.gpg
      sudo chmod a+r /etc/apt/keyrings/docker.gpg
    fi

    DISTRO=$(. /etc/os-release && echo "$ID")
    CODENAME=$(. /etc/os-release && echo "$VERSION_CODENAME")
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/${DISTRO} ${CODENAME} stable" | \
      sudo tee /etc/apt/sources.list.d/docker.list > /dev/null

    sudo apt-get update -qq
    sudo apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-compose-plugin

    # Allow current user to use docker without sudo
    sudo usermod -aG docker "$USER"

    # Start Docker
    sudo service docker start
    sleep 3

    # Test (may need newgrp for group to take effect)
    if ! docker info &>/dev/null; then
      warn "Docker installed. You may need to run 'newgrp docker' or restart WSL."
      warn "After restart, re-run this script."
      exit 0
    fi
    info "Docker installed and running"
  fi
fi
echo ""

# -------------------------------------------
# Step 2: kubectl
# -------------------------------------------
info "Step 2/5: kubectl"

if command -v kubectl &>/dev/null; then
  info "kubectl already installed: $(kubectl version --client --short 2>/dev/null || kubectl version --client 2>&1 | head -1)"
else
  info "Installing kubectl..."
  curl -fsSLo /tmp/kubectl "https://dl.k8s.io/release/$(curl -fsSL https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl"
  sudo install -o root -g root -m 0755 /tmp/kubectl /usr/local/bin/kubectl
  rm -f /tmp/kubectl
  info "kubectl installed: $(kubectl version --client --short 2>/dev/null || echo 'ok')"
fi
echo ""

# -------------------------------------------
# Step 3: Helm
# -------------------------------------------
info "Step 3/5: Helm"

if command -v helm &>/dev/null; then
  info "Helm already installed: $(helm version --short 2>/dev/null)"
else
  info "Installing Helm 3..."
  curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
  info "Helm installed: $(helm version --short 2>/dev/null)"
fi
echo ""

# -------------------------------------------
# Step 4: k3d
# -------------------------------------------
info "Step 4/5: k3d"

if command -v k3d &>/dev/null; then
  info "k3d already installed: $(k3d version 2>/dev/null | head -1)"
else
  info "Installing k3d..."
  curl -fsSL https://raw.githubusercontent.com/k3d-io/k3d/main/install.sh | bash
  info "k3d installed: $(k3d version 2>/dev/null | head -1)"
fi
echo ""

# -------------------------------------------
# Step 5: Create k3d cluster
# -------------------------------------------
info "Step 5/5: k3d Cluster"

CLUSTER_NAME="unica-dev"

if k3d cluster list 2>/dev/null | grep -q "$CLUSTER_NAME"; then
  info "Cluster '$CLUSTER_NAME' already exists"
  # Make sure it's running
  if ! kubectl cluster-info &>/dev/null; then
    info "Starting stopped cluster..."
    k3d cluster start "$CLUSTER_NAME"
  fi
else
  info "Creating k3d cluster '$CLUSTER_NAME'..."
  k3d cluster create "$CLUSTER_NAME" \
    --agents 0 \
    --port "5432:5432@server:0" \
    --port "6379:6379@server:0" \
    --k3s-arg "--disable=traefik@server:0" \
    --wait
  info "Cluster created"
fi

# Verify cluster
kubectl cluster-info
echo ""

# -------------------------------------------
# Deploy infrastructure
# -------------------------------------------
echo "============================================"
echo "  Environment ready! Deploying infrastructure..."
echo "============================================"
echo ""

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
bash "$SCRIPT_DIR/setup-infra.sh"

echo ""
info "Running verification..."
echo ""
bash "$SCRIPT_DIR/verify-infra.sh"

echo ""
echo "============================================"
echo "  Setup Complete!"
echo "============================================"
echo ""
echo "Your development environment:"
echo "  Cluster:    k3d-${CLUSTER_NAME}"
echo "  Namespace:  unica-dev"
echo "  PostgreSQL: running (port 5432)"
echo "  Redis:      running (port 6379)"
echo ""
echo "Useful commands:"
echo "  kubectl get pods -n unica-dev          # Check pod status"
echo "  kubectl logs <pod-name> -n unica-dev   # View logs"
echo "  k3d cluster stop unica-dev             # Stop cluster"
echo "  k3d cluster start unica-dev            # Start cluster"
echo "  k3d cluster delete unica-dev           # Delete cluster"
echo ""
echo "Project path from WSL: /mnt/e/kefu"
echo ""
