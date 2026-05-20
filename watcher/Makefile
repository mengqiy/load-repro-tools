.PHONY: build docker-build docker-push deploy delete logs clean

# Variables
IMAGE_NAME ?= mengqiyu-configmap-watcher
IMAGE_TAG ?= latest
REGISTRY ?= 953421922360.dkr.ecr.us-west-2.amazonaws.com
FULL_IMAGE = $(REGISTRY)/$(IMAGE_NAME):$(IMAGE_TAG)
NAMESPACE = mengqiyu-watcher

# Build the Go binary locally
build:
	go build -o configmap-watcher main.go

# Build Docker image (standard)
docker-build:
	docker build -t $(IMAGE_NAME):$(IMAGE_TAG) .
	docker tag $(IMAGE_NAME):$(IMAGE_TAG) $(FULL_IMAGE)

# Push Docker image to registry
docker-push: docker-build
	docker push $(FULL_IMAGE)

# Update deployment.yaml with the correct image
update-image:
	sed -i 's|image:.*|image: $(FULL_IMAGE)|' deployment.yaml

# Deploy to Kubernetes
deploy: update-image
	kubectl apply -f deployment.yaml

# Delete deployment from Kubernetes
delete:
	kubectl delete -f deployment.yaml

# View logs
logs:
	kubectl logs -n $(NAMESPACE) -l app=configmap-watcher -f --all-containers=true

# Scale deployment
scale:
	@read -p "Enter number of replicas: " replicas; \
	kubectl scale deployment configmap-watcher -n $(NAMESPACE) --replicas=$$replicas

# Get pod status
status:
	kubectl get pods -n $(NAMESPACE) -l app=configmap-watcher

# Clean up local build artifacts
clean:
	rm -f configmap-watcher

# Build and deploy in one command
all: docker-build docker-push deploy

# Help
help:
	@echo "Available targets:"
	@echo "  build                - Build Go binary locally"
	@echo "  docker-build         - Build Docker image (standard)"
	@echo "  docker-push          - Build and push Docker image to registry"
	@echo "  deploy               - Deploy to Kubernetes cluster"
	@echo "  delete               - Delete deployment from cluster"
	@echo "  logs                 - Follow logs from all pods"
	@echo "  scale                - Scale the deployment (interactive)"
	@echo "  status               - Get pod status"
	@echo "  clean                - Remove local build artifacts"
	@echo "  all                  - Build, push, and deploy"
	@echo ""
	@echo "Variables:"
	@echo "  IMAGE_NAME    - Docker image name (default: configmap-watcher)"
	@echo "  IMAGE_TAG     - Docker image tag (default: latest)"
	@echo "  REGISTRY      - Docker registry (default: localhost:5000)"
	@echo ""
	@echo "Example usage:"
	@echo "  make docker-build"
	@echo "  make deploy"
	@echo "  make REGISTRY=myregistry.io IMAGE_TAG=v1.0.0 all"
