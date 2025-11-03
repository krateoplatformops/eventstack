#!/bin/bash

set -euo pipefail


kubectl apply -f ./manifests/ns.yaml
kubectl apply -f ./manifests/sa.yaml
kubectl apply -f ./manifests/svc.yaml
kubectl apply -f ./manifests/deploy-sweeper.yaml

kubectl apply -f ./manifests/etcd-flooder-job.yaml
