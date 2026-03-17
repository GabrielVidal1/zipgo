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
