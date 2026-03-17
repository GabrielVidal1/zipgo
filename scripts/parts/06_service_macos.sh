    # ── macOS: launchd ─────────────────────────────────────────────────
    PLIST_DIR="$HOME/Library/LaunchAgents"
    LOG_DIR="$HOME/Library/Logs/zipgo"
    mkdir -p "$PLIST_DIR" "$LOG_DIR"

    # zipgo plist
    PLIST_FILE="${PLIST_DIR}/com.zipgo.plist"
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
    <key>VINCE_MANAGED</key>   <string>1</string>
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

    # vince plist (only if binary was downloaded)
    if [ -n "$VINCE_DEST" ]; then
      VINCE_PLIST="${PLIST_DIR}/com.vince.plist"
      cat > "$VINCE_PLIST" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>             <string>com.vince</string>
  <key>ProgramArguments</key>
  <array>
    <string>${VINCE_DEST}</string>
    <string>serve</string>
    <string>--data</string>    <string>${VINCE_DATA}</string>
    <string>--listen</string>  <string>127.0.0.1:8899</string>
  </array>
  <key>RunAtLoad</key>         <true/>
  <key>KeepAlive</key>         <true/>
  <key>StandardOutPath</key>   <string>${LOG_DIR}/vince.log</string>
  <key>StandardErrorPath</key> <string>${LOG_DIR}/vince.err</string>
</dict>
</plist>
PLIST
      launchctl unload "$VINCE_PLIST" 2>/dev/null || true
      launchctl load -w "$VINCE_PLIST"
      success "Vince launchd agent registered → ${VINCE_PLIST}"
      info "Stop:    launchctl unload ${VINCE_PLIST}"
      info "Start:   launchctl load -w ${VINCE_PLIST}"
    fi
