#!/usr/bin/env sh
set -eu

repo_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
compose_file="$repo_root/deploy/compose/compose.yaml"
compose_env="$repo_root/deploy/compose/fixtures/compose.env"
helm_image=${MEM_HELM_IMAGE:-alpine/helm:3.17.1}
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM

docker compose --env-file "$compose_env" -f "$compose_file" config \
  --format json >"$tmp_dir/compose.json"

python3 - "$tmp_dir/compose.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    config = json.load(handle)

published = {
    name: service.get("ports", [])
    for name, service in config["services"].items()
    if service.get("ports")
}
if set(published) != {"web"}:
    raise SystemExit(f"only web may publish a host port, got: {sorted(published)}")
port = published["web"][0]
if port.get("host_ip") != "127.0.0.1" or int(port.get("published", 0)) != 8080:
    raise SystemExit(f"default web bind must be 127.0.0.1:8080, got: {port}")

backend = config["networks"].get("backend", {})
if not backend.get("internal"):
    raise SystemExit("the single-node backend network must be internal")
PY

if grep -En '(^|[[:space:]])(FROM|image:|--from=)[^#]*:latest([[:space:]]|$)' \
  "$repo_root/server/Dockerfile" \
  "$repo_root/worker/Dockerfile" \
  "$repo_root/web/Dockerfile" ||
  grep -REn '(^|[[:space:]])(FROM|image:|--from=)[^#]*:latest([[:space:]]|$)' \
    "$repo_root/deploy"; then
  echo "mutable latest image tag found in production deployment files" >&2
  exit 1
fi

run_helm() {
  if command -v helm >/dev/null 2>&1; then
    helm "$@"
  else
    docker run --rm \
      -v "$repo_root:/src:ro" \
      -w /src \
      "$helm_image" "$@"
  fi
}

run_helm lint deploy/helm/mem
run_helm template mem deploy/helm/mem \
  --namespace mem-system \
  --kube-version 1.28.0 >"$tmp_dir/default.yaml"
run_helm template mem deploy/helm/mem \
  --namespace mem-system \
  --kube-version 1.28.0 \
  --values deploy/helm/mem/values-production.example.yaml \
  --set ingress.enabled=true \
  --set worker.autoscaling.enabled=true \
  --set web.autoscaling.enabled=true >"$tmp_dir/production.yaml"
run_helm template mem deploy/helm/mem \
  --namespace mem-system \
  --kube-version 1.28.0 \
  --set worker.enabled=false >"$tmp_dir/no-worker.yaml"

if grep -q 'app.kubernetes.io/component: worker' "$tmp_dir/no-worker.yaml"; then
  echo "worker.enabled=false still rendered a Worker workload" >&2
  exit 1
fi
if run_helm template mem deploy/helm/mem \
  --namespace mem-system \
  --kube-version 1.28.0 \
  --set images.server.tag=latest >"$tmp_dir/latest.yaml" 2>&1; then
  echo "Helm schema accepted the mutable latest image tag" >&2
  exit 1
fi

python3 - "$tmp_dir/default.yaml" "$tmp_dir/production.yaml" <<'PY'
import re
import sys


def documents(path):
    with open(path, encoding="utf-8") as handle:
        return handle.read().split("\n---\n")


def component_document(items, kind, component):
    matches = [
        item
        for item in items
        if re.search(rf"(?m)^kind: {re.escape(kind)}$", item)
        and re.search(
            rf"(?m)^[ \t]+app\.kubernetes\.io/component: {re.escape(component)}$",
            item,
        )
    ]
    if len(matches) != 1:
        raise SystemExit(
            f"expected one {component} {kind}, rendered {len(matches)}"
        )
    return matches[0]


default_documents = documents(sys.argv[1])
production_documents = documents(sys.argv[2])
for profile, items in (
    ("default", default_documents),
    ("production", production_documents),
):
    memd = component_document(items, "Deployment", "memd")
    if not re.search(r"(?m)^  replicas: 1$", memd):
        raise SystemExit(
            f"{profile} memd Deployment must render exactly one replica"
        )
    if not re.search(r"(?m)^  strategy:\n    type: Recreate$", memd):
        raise SystemExit(
            f"{profile} memd Deployment must use a non-overlapping Recreate rollout"
        )
if any(
    re.search(r"(?m)^kind: HorizontalPodAutoscaler$", item)
    and re.search(r"(?m)^[ \t]+app\.kubernetes\.io/component: memd$", item)
    for item in production_documents
):
    raise SystemExit("production example must not render a memd HPA")
component_document(production_documents, "HorizontalPodAutoscaler", "worker")
component_document(production_documents, "HorizontalPodAutoscaler", "web")
PY

for kind in Deployment Service Job PodDisruptionBudget NetworkPolicy Ingress HorizontalPodAutoscaler; do
  if ! grep -q "^kind: $kind$" "$tmp_dir/production.yaml"; then
    echo "production Helm render is missing kind: $kind" >&2
    exit 1
  fi
done

if [ "${MEM_VALIDATE_BUILD_IMAGES:-0}" = "1" ]; then
  docker build --tag mem-server:deploy-validation "$repo_root/server"
  docker build --tag mem-worker:deploy-validation "$repo_root/worker"
  docker build --tag mem-web:deploy-validation "$repo_root/web"
fi

echo "PASS: production Compose and Helm deployment validation"
