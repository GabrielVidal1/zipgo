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
