#!/usr/bin/env sh
set -e

REPO="GabrielVidal1/zipgo"
BINARY="zipgo"
INSTALL_DIR="${PWD}"

# ── colours ────────────────────────────────────────────────────────────────
if [ -t 1 ]; then
  BOLD=$'\033[1m';  RESET=$'\033[0m'
  GREEN=$'\033[32m'; CYAN=$'\033[36m'; YELLOW=$'\033[33m'; RED=$'\033[31m'; GREY=$'\033[90m'
else
  BOLD=""; RESET=""; GREEN=""; CYAN=""; YELLOW=""; RED=""; GREY=""
fi

info()    { printf "${CYAN}  ->  ${RESET}%s\n" "$*"; }
success() { printf "${GREEN}  ok  ${RESET}%s\n" "$*"; }
warn()    { printf "${YELLOW}  !   ${RESET}%s\n" "$*"; }
fatal()   { printf "${RED}  err ${RESET}%s\n" "$*" >&2; exit 1; }

# ── banner ─────────────────────────────────────────────────────────────────
printf "\n"
printf "%s\n" "  ${BOLD}zipgo installer${RESET}"
printf "%s\n" "  ${GREY}one binary, many sites${RESET}"
printf "\n"
