    # ── Windows: Task Scheduler via schtasks ───────────────────────────
    TASK_NAME="zipgo"
    BAT="${INSTALL_DIR}/zipgo-start.bat"
    cat > "$BAT" <<BAT
@echo off
set ZIPGO_PASS=${ZIPGO_PASS}
set VINCE_MANAGED=1
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

    if [ -n "$VINCE_DEST" ]; then
      VINCE_BAT="${INSTALL_DIR}/vince-start.bat"
      cat > "$VINCE_BAT" <<BAT
@echo off
"${VINCE_DEST}" serve --data "${VINCE_DATA}" --listen 127.0.0.1:8899
BAT
      schtasks //Create //F \
        //TN "vince" \
        //TR "\"${VINCE_BAT}\"" \
        //SC ONLOGON //RL HIGHEST //RU "$(whoami)" 2>/dev/null \
        && success "Vince Task Scheduler entry created" \
        || warn "Could not register Vince — run as Administrator"
      info "Start:   schtasks /Run /TN vince"
      info "Stop:    schtasks /End /TN vince"
    fi
