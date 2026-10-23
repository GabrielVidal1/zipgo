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
