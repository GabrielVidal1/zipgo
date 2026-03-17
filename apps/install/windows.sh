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
GOOS="windows"
ARCH="$(uname -m)"

case "$ARCH" in
  x86_64|amd64)  GOARCH="amd64" ;;
  arm64|aarch64) GOARCH="arm64" ;;
  *)             fatal "Unsupported architecture: $ARCH" ;;
esac

EXT=".exe"
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

# ── prompt for zipgo password ──────────────────────────────────────────────
if [ -z "$ZIPGO_PASS" ]; then
  printf "  Set a backoffice password (leave empty to auto-generate): "
  read -rs ZIPGO_PASS </dev/tty
  printf "\n"

  if [ -z "$ZIPGO_PASS" ]; then
    ZIPGO_PASS=$(openssl rand -base64 12 | tr -d '=+/')
    printf "  Generated zipgo password: %s\n" "$ZIPGO_PASS"
  fi
fi

# ── install vince analytics ────────────────────────────────────────────────
printf "\n"
info "Installing Vince analytics sidecar…"

VINCE_DEST=""
VINCE_DATA="${INSTALL_DIR}/vince-data"
VINCE_INSTALL_OK=1

# Vince's installer puts the binary on PATH (typically ~/.local/bin or /usr/local/bin).
# We then copy it next to the zipgo binary so startVinceSidecar() can find it.
if command -v curl >/dev/null 2>&1; then
  curl -fsSL https://vinceanalytics.com/install.sh | bash || VINCE_INSTALL_OK=0
elif command -v wget >/dev/null 2>&1; then
  wget -qO- https://vinceanalytics.com/install.sh | bash || VINCE_INSTALL_OK=0
else
  warn "curl or wget required for Vince install — skipping"
  VINCE_INSTALL_OK=0
fi

if [ "$VINCE_INSTALL_OK" = "0" ]; then
  warn "Vince installation failed — analytics sidecar will be skipped"
else
  # Locate the vince binary that was just installed.
  VINCE_BIN="$(command -v vince 2>/dev/null || true)"
  if [ -z "$VINCE_BIN" ]; then
    # Common non-PATH locations the upstream installer may use.
    for candidate in "$HOME/.local/bin/vince" "/usr/local/bin/vince" "./vince"; do
      if [ -x "$candidate" ]; then
        VINCE_BIN="$candidate"
        break
      fi
    done
  fi

  if [ -z "$VINCE_BIN" ]; then
    warn "Could not locate vince binary after install — analytics sidecar will be skipped"
  else
    # Copy next to zipgo so the sidecar launcher can find it by relative path.
    VINCE_DEST="${INSTALL_DIR}/vince${EXT}"
    if [ "$VINCE_BIN" != "$VINCE_DEST" ]; then
      cp "$VINCE_BIN" "$VINCE_DEST"
      chmod +x "$VINCE_DEST"
    fi
    success "Vince binary ready → ${VINCE_DEST}"

    # ── create vince admin account ─────────────────────────────────────
    VINCE_ADMIN="${ZIPGO_USER:-admin}"
    if [ -z "$VINCE_PASS" ]; then
      VINCE_PASS=$(openssl rand -base64 12 | tr -d '=+/')
    fi

    info "Creating Vince admin account…"
    mkdir -p "$VINCE_DATA"
    "$VINCE_DEST" admin \
      --data     "$VINCE_DATA" \
      --name     "$VINCE_ADMIN" \
      --password "$VINCE_PASS" \
      && success "Vince admin created: ${BOLD}${VINCE_ADMIN}${RESET}" \
      || warn "Could not create Vince admin — run manually later:
         ${VINCE_DEST} admin --data ${VINCE_DATA} --name admin --password <pass>"
  fi
fi

# ── OS-specific background service setup ──────────────────────────────────
printf "\n"
printf "  Register zipgo as a background service that starts on boot? [Y/n] "
read -r SETUP_SERVICE </dev/tty
printf "\n"

case "$SETUP_SERVICE" in
  [nN]*) info "Skipping service setup." ;;
  *)

    # ── Windows: Task Scheduler via schtasks ───────────────────────────
    TASK_NAME="zipgo"
    BAT="${INSTALL_DIR}/zipgo-start.bat"
    cat > "$BAT" <<BAT
@echo off
set ZIPGO_PASS=${ZIPGO_PASS}
set VINCE_MANAGED=1
"${DEST}" "${INSTALL_DIR}/apps"
BAT
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

    if [ -n "$VINCE_DEST" ]; then
      VINCE_BAT="${INSTALL_DIR}/vince-start.bat"
      cat > "$VINCE_BAT" <<BAT
@echo off
"${VINCE_DEST}" serve --data "${VINCE_DATA}" --listen 127.0.0.1:8899
BAT
      schtasks //Create //F \
        //TN "vince" \
        //TR "\"${VINCE_BAT}\"" \
        //SC ONLOGON //RL HIGHEST //RU "$(whoami)" 2>/dev/null \
        && success "Vince Task Scheduler entry created" \
        || warn "Could not register Vince — run as Administrator"
      info "Start:   schtasks /Run /TN vince"
      info "Stop:    schtasks /End /TN vince"
    fi

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

if [ -n "$VINCE_DEST" ]; then
  printf "%s\n" "  ${CYAN}3.${RESET}  Vince analytics → ${BOLD}http://localhost:8899${RESET}"
  printf "%s\n" "      Login: ${BOLD}${VINCE_ADMIN}${RESET} / ${VINCE_PASS}"
  printf "\n"
  printf "%s\n" "      ${GREY}In domain mode: https://analytics.<your-domain>${RESET}"
  printf "\n"
fi
