BINARY      := zipgo
DOMAINS_DIR := $(abspath domains)

.PHONY: build run run-local clean format



format:
	gofmt -w .

build-install-scripts:
	bash scripts/populate_script.sh domains/zipgo.xyz/install

build: build-install-scripts
	go mod tidy
	CGO_ENABLED=0 go build -o $(BINARY) .
	@if [ "$$(uname)" = "Darwin" ]; then codesign --force --sign - $(BINARY); fi

run: build
	ZIPGO_PASS=$${ZIPGO_PASS:-dev} sudo -E ./$(BINARY) $(DOMAINS_DIR)

run-local: build
	ZIPGO_PASS=$${ZIPGO_PASS:-dev} ZIPGO_LOCALHOST=1 ./$(BINARY) $(DOMAINS_DIR)

clean:
	rm -f $(BINARY)