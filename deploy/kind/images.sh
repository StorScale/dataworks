#!/usr/bin/env bash
# Builds the platform's own images from this checkout, as `storscale up --dir
# stack` does, tags them as the chart names them (ghcr.io/storscale/<name>:dev)
# and loads them into a kind cluster.
#
#   deploy/kind/images.sh [cluster]        (default: storscale)
#
# Then: helm install ... --set images.tag=dev
set -euo pipefail
cd "$(dirname "$0")/../.."
cluster=${1:-storscale}

docker compose --project-directory stack -f stack/compose.yaml -f compose.dev.yaml build \
  platformd setup notebook jupyterhub superset-init airflow-init openmetadata-search semanticd catalog-sync mlflow

# The image Compose builds, and the name the chart gives it.
while read -r local name; do
  docker tag "$local" "ghcr.io/storscale/$name:dev"
  echo "loading $name" >&2
  kind load docker-image --name "$cluster" "ghcr.io/storscale/$name:dev" >/dev/null
done <<'EOF'
storscale-platformd-dev platformd
storscale-tools platform-tools
storscale-airflow platform-airflow
storscale-superset platform-superset
storscale-jupyterhub platform-jupyterhub
storscale-notebook platform-notebook
storscale-opensearch platform-opensearch
storscale-semanticd platform-semanticd
storscale-catalog-sync platform-catalog-sync
storscale-mlflow platform-mlflow
EOF
