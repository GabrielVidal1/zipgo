BINARY      := zipgo
DOMAINS_DIR := $(abspath domains)

.PHONY: build run run-local run-prod clean format build-install-scripts

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
