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
