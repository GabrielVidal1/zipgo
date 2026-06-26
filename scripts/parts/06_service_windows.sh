    # ── Windows: Task Scheduler via schtasks ───────────────────────────
    TASK_NAME="zipgo"
    BAT="${INSTALL_DIR}/zipgo-start.bat"
    cat > "$BAT" <<BAT
@echo off
set ZIPGO_PASS=${ZIPGO_PASS}
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
