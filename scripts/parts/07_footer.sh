    ;;
esac

# ── done ───────────────────────────────────────────────────────────────────
printf "\n"
printf "%s\n" "  ${GREEN}${BOLD}zipgo ${LATEST} is ready!${RESET}"
printf "\n"
printf "%s\n" "  Next steps:"
printf "\n"
printf "%s\n" "  ${CYAN}1.${RESET}  Point your domain's DNS at this machine, then"
printf "%s\n" "      mkdir domains/yourdomain.com && cp -r dist/* domains/yourdomain.com/"
printf "%s\n" "      ${GREY}(a folder ending in a dot is a subdomain: domains/yourdomain.com/docs./)${RESET}"
printf "\n"
printf "%s\n" "  ${CYAN}2.${RESET}  No domain yet? With no domain folder zipgo serves"
printf "%s\n" "      everything at ${BOLD}http://localhost:9000${RESET}"
printf "\n"
printf "%s\n" "  Docs → ${BOLD}https://github.com/${REPO}#readme${RESET}"
printf "\n"
