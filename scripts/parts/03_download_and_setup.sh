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
