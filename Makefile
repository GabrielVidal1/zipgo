BINARY      := zipgo
DOMAINS_DIR := $(abspath domains)

# Load .env (DOCKER_IMAGE, DOCKER_TAG, DOCKER_USERNAME, DOCKER_PASSWORD) if present
-include .env
export

DOCKER_IMAGE     ?= $(DOCKER_USERNAME)/zipgo
DOCKER_TAG       ?= latest
DOCKER_PLATFORMS ?= linux/amd64,linux/arm64
DIST             := dist

.PHONY: build run run-local run-prod clean format build-install-scripts \
        docker-login docker-binaries docker-build docker-push docker-buildx

format:
	gofmt -w .

build-install-scripts:
	bash scripts/populate_script.sh domains/zipgo.xyz/install.

build: build-install-scripts
	go mod tidy
	CGO_ENABLED=0 go build -o $(BINARY) .
	@if [ "$$(uname)" = "Darwin" ]; then codesign --force --sign - $(BINARY); fi

run: build
	ZIPGO_DOMAINS_FOLDER=$(DOMAINS_DIR) sudo -E ./$(BINARY) serve

run-local: build
	ZIPGO_DOMAINS_FOLDER=$(DOMAINS_DIR) ZIPGO_LOCALHOST=1 ./$(BINARY) serve

run-prod: build
	sudo setcap 'cap_net_bind_service=+ep' $(BINARY)
	ZIPGO_DOMAINS_FOLDER=$(DOMAINS_DIR) ./$(BINARY) serve

clean:
	rm -f $(BINARY)
	rm -rf $(DIST)

# --- Docker ---------------------------------------------------------------
# Requires DOCKER_USERNAME / DOCKER_PASSWORD (and optionally DOCKER_IMAGE,
# DOCKER_TAG) in .env. DOCKER_PASSWORD should be a Docker Hub access token.

docker-login:
	@test -n "$(DOCKER_USERNAME)" || { echo "DOCKER_USERNAME is not set (add it to .env)"; exit 1; }
	@test -n "$(DOCKER_PASSWORD)" || { echo "DOCKER_PASSWORD is not set (add it to .env)"; exit 1; }
	@echo "$(DOCKER_PASSWORD)" | docker login -u "$(DOCKER_USERNAME)" --password-stdin

# Cross-compile static binaries on the host, one per target arch. The Dockerfile
# just COPYs these in, so the image build never needs QEMU emulation.
docker-binaries: build-install-scripts
	go mod tidy
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o $(DIST)/zipgo-linux-amd64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o $(DIST)/zipgo-linux-arm64 .

docker-build: docker-binaries
	docker build -t $(DOCKER_IMAGE):$(DOCKER_TAG) .

docker-push: docker-login docker-build
	docker push $(DOCKER_IMAGE):$(DOCKER_TAG)

# Single-step multi-arch build + push (amd64 + arm64 for the Pi) via buildx
docker-buildx: docker-login docker-binaries
	docker buildx build --platform $(DOCKER_PLATFORMS) \
		-t $(DOCKER_IMAGE):$(DOCKER_TAG) --push .
