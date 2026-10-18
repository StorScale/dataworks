#!/usr/bin/env bash
# What went wrong with the platform on kind: its pods, Jobs and events, and
# each pod's logs, with a crashed container's run before its restart. CI
# runs it when the platform doesn't start or a suite fails.
#
#   deploy/kind/diagnose.sh [namespace]     (default: storscale)
ns=${1:-storscale}
k() { kubectl --context "${KUBECTL_CONTEXT:-kind-storscale}" -n "$ns" "$@"; }
k get pods,jobs,projects -o wide
k get events --sort-by=.lastTimestamp | tail -40
k describe jobs | grep -E '^Name:|Pods Statuses|^Events|  (Normal|Warning)' || true
for p in $(k get pods -o name); do
  echo "=== $p"; k logs "$p" --all-containers --tail 60 || true
  # the run before a restart, for a container that crashed
  k logs "$p" --all-containers --previous --tail 40 2>/dev/null | sed 's/^/  (before its restart) /' || true
done
# Containers that ended before (OOMKilled, an exit code): what their logs can't say.
for p in $(k get pods -o name); do
  k get "$p" -o jsonpath='{range .status.containerStatuses[?(@.lastState.terminated)]}{.name}{": "}{.lastState.terminated.reason}{" (exit "}{.lastState.terminated.exitCode}{") at "}{.lastState.terminated.finishedAt}{"\n"}{end}' 2>/dev/null |
    sed "s|^|restarted: $p, container |" || true
done
