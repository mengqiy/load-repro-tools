#!/bin/bash

set -e

# Configuration
IMAGE_NAME="configmap-watcher"
IMAGE_TAG="${IMAGE_TAG:-latest}"
REGISTRY="${REGISTRY:-localhost:5000}"
FULL_IMAGE="${REGISTRY}/${IMAGE_NAME}:${IMAGE_TAG}"

echo "Building Docker image: ${FULL_IMAGE}"
docker build -t "${IMAGE_NAME}:${IMAGE_TAG}" .
docker tag "${IMAGE_NAME}:${IMAGE_TAG}" "${FULL_IMAGE}"

echo ""
echo "Pushing image to registry..."
docker push "${FULL_IMAGE}"

echo ""
echo "Updating deployment.yaml with image: ${FULL_IMAGE}"
sed -i.bak "s|image:.*|image: ${FULL_IMAGE}|" deployment.yaml

echo ""
echo "Deploying to Kubernetes..."
kubectl apply -f deployment.yaml

echo ""
echo "Deployment complete! Checking status..."
kubectl get pods -n mengqiyu-watcher -l app=configmap-watcher

echo ""
echo "To view logs, run:"
echo "  kubectl logs -n mengqiyu-watcher -l app=configmap-watcher -f"
