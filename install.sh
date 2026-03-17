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

# ── detect OS / arch ───────────────────────────────────────────────────────
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$OS" in
  linux)  GOOS="linux"  ;;
  darwin) GOOS="darwin" ;;
  msys*|mingw*|cygwin*) GOOS="windows" ;;
  *)      fatal "Unsupported OS: $OS — grab a binary from https://github.com/${REPO}/releases" ;;
esac

case "$ARCH" in
  x86_64|amd64)  GOARCH="amd64" ;;
  arm64|aarch64) GOARCH="arm64" ;;
  *)             fatal "Unsupported architecture: $ARCH" ;;
esac

info "Platform: ${BOLD}${GOOS}/${GOARCH}${RESET}"

# ── resolve latest release tag ─────────────────────────────────────────────
info "Fetching latest release…"
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
info "Latest: ${BOLD}${LATEST}${RESET}"

# ── build download URL ─────────────────────────────────────────────────────
EXT=""
[ "$GOOS" = "windows" ] && EXT=".exe"
ASSET="zipgo-${GOOS}-${GOARCH}${EXT}"
URL="https://github.com/${REPO}/releases/download/${LATEST}/${ASSET}"

# ── confirm install location ───────────────────────────────────────────────
printf "\n"
printf "%s\n" "  Install zipgo into: ${BOLD}${INSTALL_DIR}${RESET}"
printf "  Continue? [Y/n] "
read -r CONFIRM </dev/tty
case "$CONFIRM" in
  [nN]*) info "Aborted."; exit 0 ;;
esac
printf "\n"

# ── download binary ────────────────────────────────────────────────────────
DEST="${INSTALL_DIR}/${BINARY}${EXT}"
info "Downloading ${ASSET}…"

if command -v curl >/dev/null 2>&1; then
  curl -fsSL --progress-bar "$URL" -o "$DEST"
elif command -v wget >/dev/null 2>&1; then
  wget -q --show-progress "$URL" -O "$DEST"
fi

chmod +x "$DEST"
success "Binary saved → ${DEST}"

# ── create apps/ and root.txt ──────────────────────────────────────────────
if [ ! -d "${INSTALL_DIR}/apps" ]; then
  mkdir -p "${INSTALL_DIR}/apps"
  success "Created apps/"
else
  info "apps/ already exists, skipping"
fi

if [ ! -f "${INSTALL_DIR}/apps/root.txt" ]; then
  printf "" > "${INSTALL_DIR}/apps/root.txt"
  success "Created apps/root.txt  (empty = localhost mode)"
else
  info "apps/root.txt already exists, skipping"
fi

# ── prompt for password ────────────────────────────────────────────────────
if [ -z "$ZIPGO_PASS" ]; then
  printf "  Set a backoffice password: "
  read -rs ZIPGO_PASS </dev/tty
  printf "\n"
  [ -z "$ZIPGO_PASS" ] && ZIPGO_PASS="admin"
fi

# ── OS-specific background service setup ──────────────────────────────────
printf "\n"
printf "  Register zipgo as a background service that starts on boot? [Y/n] "
read -r SETUP_SERVICE </dev/tty
printf "\n"

case "$SETUP_SERVICE" in
  [nN]*) info "Skipping service setup." ;;
  *)
    case "$GOOS" in

      # ── macOS: launchd ─────────────────────────────────────────────────
      darwin)
        PLIST_DIR="$HOME/Library/LaunchAgents"
        PLIST_FILE="${PLIST_DIR}/com.zipgo.plist"
        LOG_DIR="$HOME/Library/Logs/zipgo"
        mkdir -p "$PLIST_DIR" "$LOG_DIR"

        cat > "$PLIST_FILE" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>             <string>com.zipgo</string>
  <key>ProgramArguments</key>
  <array>
    <string>${DEST}</string>
    <string>${INSTALL_DIR}/apps</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>ZIPGO_PASS</key>      <string>${ZIPGO_PASS}</string>
  </dict>
  <key>RunAtLoad</key>         <true/>
  <key>KeepAlive</key>         <true/>
  <key>StandardOutPath</key>   <string>${LOG_DIR}/zipgo.log</string>
  <key>StandardErrorPath</key> <string>${LOG_DIR}/zipgo.err</string>
</dict>
</plist>
PLIST

        launchctl unload "$PLIST_FILE" 2>/dev/null || true
        launchctl load -w "$PLIST_FILE"
        success "launchd agent registered → ${PLIST_FILE}"
        info "Logs → ${LOG_DIR}/"
        info "Stop:    launchctl unload ${PLIST_FILE}"
        info "Start:   launchctl load -w ${PLIST_FILE}"
        ;;

      # ── Linux: systemd (user service, no sudo needed) ──────────────────
      linux)
        UNIT_DIR="$HOME/.config/systemd/user"
        UNIT_FILE="${UNIT_DIR}/zipgo.service"
        mkdir -p "$UNIT_DIR"

        # Try user-level systemd first; fall back to system-level if available
        if systemctl --user daemon-reload 2>/dev/null; then
          cat > "$UNIT_FILE" <<UNIT
[Unit]
Description=zipgo static site server
After=network-online.target
Wants=network-online.target

[Service]
Environment=ZIPGO_PASS=${ZIPGO_PASS}
ExecStart=${DEST} ${INSTALL_DIR}/apps
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=default.target
UNIT

          systemctl --user daemon-reload
          systemctl --user enable --now zipgo
          success "systemd user service registered → ${UNIT_FILE}"
          info "Stop:    systemctl --user stop zipgo"
          info "Start:   systemctl --user start zipgo"
          info "Logs:    journalctl --user -fu zipgo"

          # Enable linger so the service survives logout
          if command -v loginctl >/dev/null 2>&1; then
            loginctl enable-linger "$(whoami)" 2>/dev/null && \
              info "loginctl linger enabled (service persists after logout)"
          fi

        else
          # Fallback: system-level systemd (requires sudo)
          SYSTEM_UNIT="/etc/systemd/system/zipgo.service"
          ENV_FILE="/etc/zipgo/env"
          sudo mkdir -p /etc/zipgo
          printf 'ZIPGO_PASS=%s\n' "$ZIPGO_PASS" | sudo tee "$ENV_FILE" > /dev/null
          sudo chmod 600 "$ENV_FILE"

          cat <<UNIT | sudo tee "$SYSTEM_UNIT" > /dev/null
[Unit]
Description=zipgo static site server
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=${ENV_FILE}
ExecStart=${DEST} ${INSTALL_DIR}/apps
Restart=on-failure
RestartSec=5s
AmbientCapabilities=CAP_NET_BIND_SERVICE
NoNewPrivileges=true

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

      # ── Windows: Task Scheduler via schtasks ───────────────────────────
      windows)
        TASK_NAME="zipgo"
        # Write a tiny wrapper bat so env var is set
        BAT="${INSTALL_DIR}/zipgo-start.bat"
        cat > "$BAT" <<BAT
@echo off
set ZIPGO_PASS=${ZIPGO_PASS}
"${DEST}" "${INSTALL_DIR}/apps"
BAT
        # Register a task that runs at logon, hidden
        schtasks //Create //F \
          //TN "$TASK_NAME" \
          //TR "\"${BAT}\"" \
          //SC ONLOGON \
          //RL HIGHEST \
          //RU "$(whoami)" 2>/dev/null \
          && success "Task Scheduler entry created: ${TASK_NAME}" \
          || warn "Could not register Task Scheduler entry — run the command above as Administrator"

        info "Start:   schtasks /Run /TN ${TASK_NAME}"
        info "Stop:    schtasks /End /TN ${TASK_NAME}"
        info "Remove:  schtasks /Delete /F /TN ${TASK_NAME}"
        ;;
    esac
    ;;
esac

# ── done ───────────────────────────────────────────────────────────────────
printf "\n"
printf "%s\n" "  ${GREEN}${BOLD}zipgo ${LATEST} is ready!${RESET}"
printf "\n"
printf "%s\n" "  Next steps:"
printf "\n"
printf "%s\n" "  ${CYAN}1.${RESET}  Edit apps/root.txt with your domain"
printf "%s\n" "      ${GREY}(leave empty for localhost mode)${RESET}"
printf "\n"
printf "%s\n" "  ${CYAN}2.${RESET}  Backoffice → ${BOLD}http://localhost:8999${RESET}"
printf "\n"