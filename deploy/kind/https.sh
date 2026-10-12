#!/usr/bin/env bash
# Puts HTTPS in front of the platform on a kind cluster, as on a real one:
# an ingress (ingress-nginx) ends TLS with a certificate from a private CA
# that only the platform's settings know of. CI uses it to test the chart's
# HTTPS and private-CA support (deploy/kind/https.yaml).
#
#   deploy/kind/https.sh [domain] [namespace]     (default: storscale.test storscale)
#
# Run it once the kind cluster exists and before `storscale up --k8s`:
#   - a throwaway CA, and a certificate from it for the domain and catalog.,
#     access., s3. under it: the Secret storscale-tls, and the CA in the
#     ConfigMap storscale-test-ca (global.ca.configMap);
#   - ingress-nginx, inside the cluster only;
#   - the platform's names resolving to it, inside the cluster (dns.sh).
#
# Not a .localhost domain: some clients (libcurl) take those for loopback
# without asking DNS. KUBECTL_CONTEXT: the cluster's kubectl context.
set -euo pipefail
cd "$(dirname "$0")"
kubectl() { command kubectl ${KUBECTL_CONTEXT:+--context "$KUBECTL_CONTEXT"} "$@"; }
helm() { command helm ${KUBECTL_CONTEXT:+--kube-context "$KUBECTL_CONTEXT"} "$@"; }
domain=${1:-storscale.test}
ns=${2:-storscale}
ingress_nginx=4.15.1   # the chart's version

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
openssl req -x509 -newkey rsa:2048 -nodes -days 7 -subj "/CN=StorScale test CA" \
  -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign" \
  -keyout "$work/ca.key" -out "$work/ca.crt" 2>/dev/null
openssl req -newkey rsa:2048 -nodes -subj "/CN=$domain" -keyout "$work/tls.key" -out "$work/tls.csr" 2>/dev/null
openssl x509 -req -in "$work/tls.csr" -CA "$work/ca.crt" -CAkey "$work/ca.key" -CAcreateserial -days 7 \
  -extfile <(printf 'subjectAltName=DNS:%s,DNS:catalog.%s,DNS:access.%s,DNS:s3.%s\nextendedKeyUsage=serverAuth\n' \
    "$domain" "$domain" "$domain" "$domain") -out "$work/tls.crt" 2>/dev/null

kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl -n "$ns" create secret tls storscale-tls --cert "$work/tls.crt" --key "$work/tls.key" \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl -n "$ns" create configmap storscale-test-ca --from-file=ca.crt="$work/ca.crt" \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null
echo "a test CA, and its certificate for $domain in the Secret storscale-tls"

helm upgrade --install ingress-nginx ingress-nginx --repo https://kubernetes.github.io/ingress-nginx \
  --version "$ingress_nginx" -n ingress-nginx --create-namespace \
  --set controller.service.type=ClusterIP --set controller.admissionWebhooks.enabled=false \
  --wait --timeout 10m >/dev/null
echo "ingress-nginx $ingress_nginx, inside the cluster"

./dns.sh "$domain" "$ns" "ingress-nginx-controller.ingress-nginx.svc.cluster.local"
