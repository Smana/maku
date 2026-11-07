#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# task demo:down: stop the room page's port-forward and delete the cluster task demo created (never
# the current kubeconfig's). Keeps .demo/app (the GitHub App's id and key) unless --all.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
# shellcheck source=hack/demo/lib.sh
. hack/demo/lib.sh
DEMO_DIR="$PWD/.demo"
NAME=maku-demo
all=0
[[ ${1:-} != --all ]] || all=1
[[ -d $DEMO_DIR ]] || { echo "nothing to do: no .demo directory"; exit 0; }

if [[ -s $DEMO_DIR/port-forward.pid ]]; then
  pid=$(<"$DEMO_DIR/port-forward.pid")
  if demo_pid_is_forward "$pid"; then kill "$pid" && echo "stopped the port-forward (pid $pid)"; fi
fi

kind_made=''
[[ ! -s $DEMO_DIR/cluster ]] || kind_made=$(<"$DEMO_DIR/cluster")
case $kind_made in
k3d) k3d cluster delete "$NAME" ;;
kind) kind delete cluster --name "$NAME" --kubeconfig "$DEMO_DIR/kubeconfig" ;;
current) echo "left the current kubeconfig's cluster alone: uninstall with  helm uninstall af -n maku  and  kubectl delete ns maku agents" ;;
*) echo "no cluster was created by task demo" ;;
esac

if [[ $all == 1 ]]; then
  [[ ! -s $DEMO_DIR/app/slug ]] || echo "delete the GitHub App on GitHub too: https://github.com/settings/apps/$(<"$DEMO_DIR/app/slug")/advanced"
  rm -rf "$DEMO_DIR"
  echo "removed .demo"
else
  rm -rf "${DEMO_DIR:?}/bin"
  for f in state cluster context kubeconfig values.yaml port-forward.pid port-forward.log answers.env; do rm -f "${DEMO_DIR:?}/$f"; done
  echo "kept .demo/app (task demo:down -- --all removes it)"
fi
