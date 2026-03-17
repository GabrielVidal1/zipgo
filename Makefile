BINARY      := sitehost
INSTALL_DIR := /usr/local/bin
SERVICE     := sitehost
APPS_DIR    := $(abspath apps)
UNIT_FILE   := /etc/systemd/system/$(SERVICE).service
ENV_FILE    := /etc/sitehost/env

.PHONY: build install uninstall up down restart status logs run run-local clean help

## help: show available commands
help:
	@printf '\n  \033[1msitehost\033[0m\n\n'
	@printf '  \033[36mmake build\033[0m       compile the binary\n'
	@printf '  \033[36mmake install\033[0m     build + install binary + systemd service\n'
	@printf '  \033[36mmake uninstall\033[0m   stop service and remove everything\n\n'
	@printf '  \033[36mmake up\033[0m          start the service\n'
	@printf '  \033[36mmake down\033[0m        stop the service\n'
	@printf '  \033[36mmake restart\033[0m     restart (picks up new sites automatically)\n'
	@printf '  \033[36mmake status\033[0m      show service status\n'
	@printf '  \033[36mmake logs\033[0m        follow live logs\n\n'
	@printf '  \033[36mmake run\033[0m         run in foreground with real domain (needs sudo)\n'
	@printf '  \033[36mmake run-local\033[0m   run on localhost, no domain, no sudo\n'
	@printf '  \033[36mmake clean\033[0m       remove compiled binary\n\n'

## build: compile the binary
## On macOS, codesign is required to add LC_UUID so dyld doesn't warn/crash.
build:
	go mod tidy
	CGO_ENABLED=0 go build -o $(BINARY) .
	@if [ "$$(uname)" = "Darwin" ]; then \
		codesign --force --sign - $(BINARY); \
	fi

## run: run in foreground with a real domain (needs sudo for ports 80/443)
run: build
	@if [ -z "$$SITEHOST_PASS" ]; then \
		read -s -p "Backoffice password: " pass; echo; \
		SITEHOST_PASS="$$pass" sudo -E ./$(BINARY) $(APPS_DIR); \
	else \
		sudo -E ./$(BINARY) $(APPS_DIR); \
	fi

## run-local: run on localhost with no domain and no sudo needed
##   Sites served on http://localhost:9000, 9001, 9002, ...
##   Backoffice on http://localhost:8999
##   No apps/root.txt needed.
run-local: build
	SITEHOST_PASS=$${SITEHOST_PASS:-dev} ./$(BINARY) $(APPS_DIR)

## install: build, install binary, create systemd service (prompts for password)
install: build
	@echo "→ Installing binary to $(INSTALL_DIR)/$(BINARY)"
	sudo install -m 755 $(BINARY) $(INSTALL_DIR)/$(BINARY)

	@sudo mkdir -p /etc/sitehost
	@if sudo test -f $(ENV_FILE); then \
		echo "→ Keeping existing credentials in $(ENV_FILE)"; \
	else \
		read -s -p "Set backoffice password: " pass; echo; \
		printf 'SITEHOST_USER=admin\nSITEHOST_PASS=%s\n' "$$pass" | sudo tee $(ENV_FILE) > /dev/null; \
		sudo chmod 600 $(ENV_FILE); \
		echo "→ Credentials written to $(ENV_FILE)"; \
	fi

	@echo "→ Writing systemd unit $(UNIT_FILE)"
	@printf '[Unit]\nDescription=sitehost static site server\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\nEnvironmentFile=%s\nExecStart=%s/%s %s\nRestart=on-failure\nRestartSec=5s\nAmbientCapabilities=CAP_NET_BIND_SERVICE\nNoNewPrivileges=true\nEnvironment=HOME=/var/lib/%s\n\n[Install]\nWantedBy=multi-user.target\n' \
		$(ENV_FILE) $(INSTALL_DIR) $(BINARY) $(APPS_DIR) $(SERVICE) \
		| sudo tee $(UNIT_FILE) > /dev/null

	@sudo mkdir -p /var/lib/$(SERVICE)
	@sudo systemctl daemon-reload
	@sudo systemctl enable $(SERVICE)
	@printf '\n\033[32m✅  Installed.\033[0m Run \033[36mmake up\033[0m to start.\n\n'

## uninstall: stop service and remove binary (apps/ and certs kept)
uninstall:
	-sudo systemctl stop $(SERVICE)
	-sudo systemctl disable $(SERVICE)
	sudo rm -f $(UNIT_FILE) $(INSTALL_DIR)/$(BINARY)
	sudo systemctl daemon-reload
	@printf '\n\033[32m✅  Uninstalled.\033[0m apps/ and TLS certs untouched.\n\n'

## up: start the service
up:
	sudo systemctl start $(SERVICE)
	@printf '\033[32m✅  $(SERVICE) started.\033[0m\n'

## down: stop the service
down:
	sudo systemctl stop $(SERVICE)
	@printf '\033[33m🛑  $(SERVICE) stopped.\033[0m\n'

## restart: restart the service
restart:
	sudo systemctl restart $(SERVICE)
	@printf '\033[32m🔄  $(SERVICE) restarted.\033[0m\n'

## status: show service status
status:
	systemctl status $(SERVICE)

## logs: follow live logs
logs:
	journalctl -fu $(SERVICE)

## clean: remove compiled binary
clean:
	rm -f $(BINARY)