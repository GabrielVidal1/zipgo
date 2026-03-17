    ;;
esac

# ── done ───────────────────────────────────────────────────────────────────
printf "\n"
printf "%s\n" "  ${GREEN}${BOLD}zipgo ${LATEST} is ready!${RESET}"
printf "\n"
printf "%s\n" "  Next steps:"
printf "\n"
printf "%s\n" "  ${CYAN}1.${RESET}  Edit apps/root.txt with your domain"
printf "%s\n" "      ${GREY}(leave empty for localhost mode)${RESET}"
printf "\n"
printf "%s\n" "  ${CYAN}2.${RESET}  Backoffice → ${BOLD}http://localhost:8999${RESET}"
printf "\n"

if [ -n "$VINCE_DEST" ]; then
  printf "%s\n" "  ${CYAN}3.${RESET}  Vince analytics → ${BOLD}http://localhost:8899${RESET}"
  printf "%s\n" "      Login: ${BOLD}${VINCE_ADMIN}${RESET} / ${VINCE_PASS}"
  printf "\n"
  printf "%s\n" "      ${GREY}In domain mode: https://analytics.<your-domain>${RESET}"
  printf "\n"
fi
