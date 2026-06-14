BINARY      := zipgo
DOMAINS_DIR := $(abspath domains)

# Load .env if present
-include .env
export

DIST             := dist

.PHONY: build run run-local run-prod clean format build-install-scripts deploy deploy-pages

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

# --- Deploy ---------------------------------------------------------------
# Rebuild the zipgo image on raspy2 from docker/Dockerfile (pulls the latest
# release binary from GitHub) and recreate the container. No registry needed.
# Override host/dir/service via DEPLOY_HOST / DEPLOY_DIR / DEPLOY_SERVICE.
deploy:
	bash scripts/deploy.sh

# Sync the zipgo.xyz landing pages (domains/zipgo.xyz/) to the host; zipgo
# hot-reloads, no restart. Override via DEPLOY_HOST / DEPLOY_DIR / DEPLOY_DOMAIN.
deploy-pages:
	bash scripts/deploy-pages.sh
