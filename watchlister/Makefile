REGISTRY := 953421922360.dkr.ecr.us-west-2.amazonaws.com
IMAGE := mengqiyu-configmap-watchlister
TAG ?= latest

FULL_IMAGE := $(REGISTRY)/$(IMAGE):$(TAG)

.PHONY: build push deploy all

build:
	docker build -t $(FULL_IMAGE) .

push:
	docker push $(FULL_IMAGE)

deploy:
	sed 's|image: .*|image: $(FULL_IMAGE)|' deployment.yaml | kubectl apply -f -

all: build push deploy
