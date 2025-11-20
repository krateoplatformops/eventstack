#!/bin/bash


kind get kubeconfig >/dev/null 2>&1 || \
    cat <<EOF | kind create cluster --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  extraPortMappings:
    - containerPort: 9091   # porta del server echo nel pod
      hostPort: 9091        # porta sul tuo Mac
EOF
