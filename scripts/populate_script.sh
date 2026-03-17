#!/usr/bin/env bash
#
# populate_script.sh — assemble per-OS installer scripts from shared parts
#
# Usage:  ./populate_script.sh [output_dir]
#
# Each OS installer is defined as an ordered list of part files.
# Shared parts are reused across all three; OS-specific parts diverge
# only where platform detection or service setup differs.
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PARTS_DIR="${SCRIPT_DIR}/parts"
OUTPUT_DIR="${1:-${SCRIPT_DIR}/output}"

mkdir -p "$OUTPUT_DIR"

# ── manifest ───────────────────────────────────────────────────────────────
# Each entry: "target_filename|part1 part2 part3 ..."
# Parts are concatenated in order with a blank line between each.

MANIFESTS=(
  "linux.sh|00_header 01_detect_linux 02_fetch_release 03_download_and_setup 04_vince_install 05_service_prompt 06_service_linux 07_footer"
  "macos.sh|00_header 01_detect_macos 02_fetch_release 03_download_and_setup 04_vince_install 05_service_prompt 06_service_macos 07_footer"
  "windows.sh|00_header 01_detect_windows 02_fetch_release 03_download_and_setup 04_vince_install 05_service_prompt 06_service_windows 07_footer"
)

# ── build loop ─────────────────────────────────────────────────────────────
for manifest in "${MANIFESTS[@]}"; do
  TARGET="${manifest%%|*}"
  PARTS="${manifest#*|}"
  OUTFILE="${OUTPUT_DIR}/${TARGET}"

  echo "  Building ${TARGET} …"

  # Start fresh
  : > "$OUTFILE"

  FIRST=1
  for part_name in $PARTS; do
    PART_FILE="${PARTS_DIR}/${part_name}.sh"

    if [ ! -f "$PART_FILE" ]; then
      echo "  ERROR: missing part ${PART_FILE}" >&2
      exit 1
    fi

    # Blank line separator between parts (skip before the very first part)
    if [ "$FIRST" = "1" ]; then
      FIRST=0
    else
      printf "\n" >> "$OUTFILE"
    fi

    # For the header part (first file), copy as-is (includes the shebang).
    # For all other parts, strip any shebang line so we don't get duplicates.
    if [ "$part_name" = "00_header" ]; then
      cat "$PART_FILE" >> "$OUTFILE"
    else
      sed '1{/^#!/d;}' "$PART_FILE" >> "$OUTFILE"
    fi
  done

  chmod +x "$OUTFILE"
done

# ── summary ────────────────────────────────────────────────────────────────
echo ""
echo "  Done! Generated scripts:"
for manifest in "${MANIFESTS[@]}"; do
  TARGET="${manifest%%|*}"
  SIZE=$(wc -c < "${OUTPUT_DIR}/${TARGET}" | tr -d ' ')
  echo "    ${OUTPUT_DIR}/${TARGET}  (${SIZE} bytes)"
done
