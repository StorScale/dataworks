#!/usr/bin/env bash
# Makes the platform's names resolve to its gateway inside a kind cluster, as
# Compose's network aliases do: every pod (the platform's own, the Buckets
# operator's servers, people's notebooks) then uses the addresses a browser
# does, and Keycloak's tokens carry one issuer.
#
#   deploy/kind/dns.sh [domain] [namespace] [target]
#                       (default: storscale.localhost storscale gateway.<namespace>.svc.cluster.local)
#
# Adds one rewrite rule to CoreDNS: <domain> and *.<domain> are answered as
# the target Service (the gateway, or an ingress in front of it, as
# deploy/kind/https.sh makes). Safe to run again: a rule for the domain is
# kept as it is. KUBECTL_CONTEXT: the cluster's kubectl context (default:
# the current one).
set -euo pipefail
kubectl() { command kubectl ${KUBECTL_CONTEXT:+--context "$KUBECTL_CONTEXT"} "$@"; }
domain=${1:-storscale.localhost}
ns=${2:-storscale}
re=$(printf '%s' "$domain" | sed 's/\./\\\\./g')
target=${3:-gateway.$ns.svc.cluster.local}
target=${target%.}.

corefile=$(kubectl -n kube-system get configmap coredns -o jsonpath='{.data.Corefile}')
if grep -q "# storscale: $domain" <<<"$corefile"; then
  echo "CoreDNS already resolves $domain to $target"
  exit 0
fi
rule="    # storscale: $domain\n    rewrite stop {\n        name regex ^(.+\\\\.)?$re\\\\.\$ $target\n        answer auto\n    }"
new=$(awk -v rule="$rule" '{print} /^\.:53 \{/ {print rule}' <<<"$corefile")
kubectl -n kube-system create configmap coredns --from-literal=Corefile="$new" --dry-run=client -o yaml |
  kubectl -n kube-system replace -f - >/dev/null
kubectl -n kube-system rollout restart deploy/coredns >/dev/null
kubectl -n kube-system rollout status deploy/coredns --timeout=120s >/dev/null
echo "CoreDNS resolves $domain and *.$domain to $target"
