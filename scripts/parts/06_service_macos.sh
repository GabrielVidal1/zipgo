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
