#!/usr/bin/env sh
set -e

REPO="GabrielVidal1/zipgo"
BINARY="zipgo"
INSTALL_DIR="${PWD}"

# ── colours ────────────────────────────────────────────────────────────────
if [ -t 1 ]; then
  BOLD=$'\033[1m';  RESET=$'\033[0m'
  GREEN=$'\033[32m'; CYAN=$'\033[36m'; YELLOW=$'\033[33m'; RED=$'\033[31m'; GREY=$'\033[90m'
else
  BOLD=""; RESET=""; GREEN=""; CYAN=""; YELLOW=""; RED=""; GREY=""
fi

info()    { printf "${CYAN}  ->  ${RESET}%s\n" "$*"; }
success() { printf "${GREEN}  ok  ${RESET}%s\n" "$*"; }
warn()    { printf "${YELLOW}  !   ${RESET}%s\n" "$*"; }
fatal()   { printf "${RED}  err ${RESET}%s\n" "$*" >&2; exit 1; }

# ── banner ─────────────────────────────────────────────────────────────────
printf "\n"
printf "%s\n" "  ${BOLD}zipgo installer${RESET}"
printf "%s\n" "  ${GREY}one binary, many sites${RESET}"
printf "\n"

# ── detect arch ────────────────────────────────────────────────────────────
GOOS="linux"
ARCH="$(uname -m)"

case "$ARCH" in
  x86_64|amd64)  GOARCH="amd64" ;;
  arm64|aarch64) GOARCH="arm64" ;;
  *)             fatal "Unsupported architecture: $ARCH" ;;
esac

EXT=""
info "Platform: ${BOLD}${GOOS}/${GOARCH}${RESET}"

# ── resolve latest zipgo release tag ──────────────────────────────────────
info "Fetching latest zipgo release…"
if command -v curl >/dev/null 2>&1; then
  LATEST=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
elif command -v wget >/dev/null 2>&1; then
  LATEST=$(wget -qO- "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
else
  fatal "curl or wget is required"
fi

[ -z "$LATEST" ] && fatal "Could not determine latest release. Check https://github.com/${REPO}/releases"
info "Latest zipgo: ${BOLD}${LATEST}${RESET}"

# ── build zipgo download URL ───────────────────────────────────────────────
ASSET="zipgo-${GOOS}-${GOARCH}${EXT}"
URL="https://github.com/${REPO}/releases/download/${LATEST}/${ASSET}"
CHECKSUMS_URL="https://github.com/${REPO}/releases/download/${LATEST}/checksums.txt"

# ── confirm install location ───────────────────────────────────────────────
printf "\n"
printf "%s\n" "  Install zipgo into: ${BOLD}${INSTALL_DIR}${RESET}"
printf "  Continue? [Y/n] "
read -r CONFIRM </dev/tty
case "$CONFIRM" in
  [nN]*) info "Aborted."; exit 0 ;;
esac
printf "\n"

# ── download zipgo binary ──────────────────────────────────────────────────
DEST="${INSTALL_DIR}/${BINARY}${EXT}"
info "Downloading ${ASSET}…"

if [ -f "$DEST" ]; then
  info "Binary already exists at ${DEST}, skipping download"
else
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --progress-bar "$URL" -o "$DEST"
  elif command -v wget >/dev/null 2>&1; then
    wget -q --show-progress "$URL" -O "$DEST"
  else
    fatal "curl or wget is required to download zipgo"
  fi
fi

# ── verify the download against the release's checksums.txt ──────────────
info "Verifying checksum…"
if command -v curl >/dev/null 2>&1; then
  SUMS=$(curl -fsSL "$CHECKSUMS_URL")
else
  SUMS=$(wget -qO- "$CHECKSUMS_URL")
fi
EXPECTED=$(printf '%s\n' "$SUMS" | awk -v a="$ASSET" '$2 == a {print $1}')
[ -z "$EXPECTED" ] && fatal "No checksum for ${ASSET} in ${CHECKSUMS_URL}"
if command -v sha256sum >/dev/null 2>&1; then
  ACTUAL=$(sha256sum "$DEST" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  ACTUAL=$(shasum -a 256 "$DEST" | awk '{print $1}')
else
  fatal "sha256sum or shasum is required to verify the download"
fi
if [ "$ACTUAL" != "$EXPECTED" ]; then
  rm -f "$DEST"
  fatal "Checksum mismatch for ${ASSET} (expected ${EXPECTED}, got ${ACTUAL}) — the file was removed. Try again, or fetch it from https://github.com/${REPO}/releases"
fi
success "Checksum OK"

chmod +x "$DEST"
success "Binary saved → ${DEST}"

# ── create domains/ directory ──────────────────────────────────────────────
DOMAINS_DIR="${INSTALL_DIR}/domains"
if [ ! -d "$DOMAINS_DIR" ]; then
  mkdir -p "$DOMAINS_DIR"
  success "Created domains/"
else
  info "domains/ already exists, skipping"
fi
info "To host a domain, create a folder: domains/<yourdomain.com>/ (no folder = localhost mode)"

# ── OS-specific background service setup ──────────────────────────────────
printf "\n"
printf "  Register zipgo as a background service that starts on boot? [Y/n] "
read -r SETUP_SERVICE </dev/tty
printf "\n"

case "$SETUP_SERVICE" in
  [nN]*) info "Skipping service setup." ;;
  *)

    # ── Linux: systemd (user service, no sudo needed) ──────────────────
    UNIT_DIR="$HOME/.config/systemd/user"
    mkdir -p "$UNIT_DIR"

    if systemctl --user daemon-reload 2>/dev/null; then
      # zipgo user service
      UNIT_FILE="${UNIT_DIR}/zipgo.service"
      cat > "$UNIT_FILE" <<UNIT
[Unit]
Description=zipgo static site server
After=network-online.target
Wants=network-online.target

[Service]
Environment=ZIPGO_DOMAINS_FOLDER=${DOMAINS_DIR}
ExecStart=${DEST} serve
Restart=on-failure
RestartSec=5s
NoNewPrivileges=true

[Install]
WantedBy=default.target
UNIT
      # A user service can't be granted CAP_NET_BIND_SERVICE; the binary
      # carries it instead so it can bind :80/:443 for domain mode.
      if command -v setcap >/dev/null 2>&1 && command -v sudo >/dev/null 2>&1; then
        sudo setcap 'cap_net_bind_service=+ep' "$DEST" 2>/dev/null \
          && info "setcap cap_net_bind_service on ${DEST} (ports 80/443 without root)" \
          || warn "Could not setcap ${DEST}: domain mode needs ports 80/443 — run: sudo setcap 'cap_net_bind_service=+ep' ${DEST}"
      fi
      systemctl --user daemon-reload
      systemctl --user enable --now zipgo
      success "systemd user service registered → ${UNIT_FILE}"
      info "Stop:    systemctl --user stop zipgo"
      info "Start:   systemctl --user start zipgo"
      info "Logs:    journalctl --user -fu zipgo"

      if command -v loginctl >/dev/null 2>&1; then
        loginctl enable-linger "$(whoami)" 2>/dev/null && \
          info "loginctl linger enabled (service persists after logout)"
      fi

    else
      # Fallback: system-level systemd (requires sudo)
      SYSTEM_UNIT="/etc/systemd/system/zipgo.service"

      cat <<UNIT | sudo tee "$SYSTEM_UNIT" > /dev/null
[Unit]
Description=zipgo static site server
After=network-online.target
Wants=network-online.target

[Service]
User=$(whoami)
Environment=ZIPGO_DOMAINS_FOLDER=${DOMAINS_DIR}
ExecStart=${DEST} serve
Restart=on-failure
RestartSec=5s
AmbientCapabilities=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=read-only
ReadWritePaths=${DOMAINS_DIR} %h/.local/share/caddy %h/.config/caddy
PrivateTmp=true

[Install]
WantedBy=multi-user.target
UNIT
      sudo systemctl daemon-reload
      sudo systemctl enable --now zipgo
      success "systemd system service registered → ${SYSTEM_UNIT}"
      info "Stop:    sudo systemctl stop zipgo"
      info "Start:   sudo systemctl start zipgo"
      info "Logs:    journalctl -fu zipgo"
    fi

    ;;
esac

# ── done ───────────────────────────────────────────────────────────────────
printf "\n"
printf "%s\n" "  ${GREEN}${BOLD}zipgo ${LATEST} is ready!${RESET}"
printf "\n"
printf "%s\n" "  Next steps:"
printf "\n"
printf "%s\n" "  ${CYAN}1.${RESET}  Point your domain's DNS at this machine, then"
printf "%s\n" "      mkdir domains/yourdomain.com && cp -r dist/* domains/yourdomain.com/"
printf "%s\n" "      ${GREY}(a folder ending in a dot is a subdomain: domains/yourdomain.com/docs./)${RESET}"
printf "\n"
printf "%s\n" "  ${CYAN}2.${RESET}  No domain yet? With no domain folder zipgo serves"
printf "%s\n" "      everything at ${BOLD}http://localhost:9000${RESET}"
printf "\n"
printf "%s\n" "  Docs → ${BOLD}https://github.com/${REPO}#readme${RESET}"
printf "\n"
