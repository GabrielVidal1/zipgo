BINARY   := zipgo
APPS_DIR := $(abspath apps)

.PHONY: build run run-local clean format



format:
	gofmt -w .

build-install-scripts:
	bash scripts/populate_script.sh apps/install

build: build-install-scripts
	go mod tidy
	CGO_ENABLED=0 go build -o $(BINARY) .
	@if [ "$$(uname)" = "Darwin" ]; then codesign --force --sign - $(BINARY); fi

run: build
	ZIPGO_PASS=$${ZIPGO_PASS:-dev} sudo -E ./$(BINARY) $(APPS_DIR)

run-local: build
	ZIPGO_PASS=$${ZIPGO_PASS:-dev} ./$(BINARY) $(APPS_DIR)

clean:
	rm -f $(BINARY)