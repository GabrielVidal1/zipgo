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
