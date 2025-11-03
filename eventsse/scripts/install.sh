#!/bin/bash


# Deploy EventRouter
kubectl apply -f ./manifests/ns.yaml
kubectl apply -f ./manifests/sa.yaml
kubectl apply -f ./manifests/deploy-eventsse.yaml
kubectl apply -f ./manifests/registration.yaml
kubectl apply -f ./manifests/service.yaml
