#!/usr/bin/env bash
# Wrapper script to run the Dify multi-workspace setup tool.
# Usage: ./scripts/setup_dify_workspaces.sh
#
# Required environment variables:
#   POSTGRES_URL     — PostgreSQL connection string
#   DIFY_ADMIN_URL   — Dify Console API base URL (e.g. http://dify:5001/console/api)
#   DIFY_ADMIN_TOKEN — Bearer token for Dify Console API authentication

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"

cd "$ROOT_DIR/router"
exec go run ./cmd/setup_dify_workspaces "$@"
