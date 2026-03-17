# ── OS-specific background service setup ──────────────────────────────────
printf "\n"
printf "  Register zipgo as a background service that starts on boot? [Y/n] "
read -r SETUP_SERVICE </dev/tty
printf "\n"

case "$SETUP_SERVICE" in
  [nN]*) info "Skipping service setup." ;;
  *)
