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
