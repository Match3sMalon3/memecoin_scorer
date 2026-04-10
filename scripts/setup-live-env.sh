#!/usr/bin/env bash
# setup-live-env.sh — idiot-proof local secret bootstrap for the live ingestor.
#
# What it does:
#   1. Creates .env from .env.example if .env does not already exist.
#   2. Detects whether HELIUS_API_KEY is missing or still the placeholder.
#   3. Prompts the operator with silent input (key is NOT echoed to the terminal).
#   4. Writes the key into .env, preserving all other values.
#   5. Prints a masked confirmation (length only, never the full key).
#
# Usage:
#   scripts/setup-live-env.sh
#   make setup-live-env
#
# Safe to re-run: if the key is already set and not the placeholder, it asks
# whether to overwrite.

set -euo pipefail

PLACEHOLDER="__PASTE_YOUR_HELIUS_API_KEY_HERE__"
ENV_FILE=".env"
EXAMPLE_FILE=".env.example"

# ---- Create .env from .env.example if absent ----
if [ ! -f "${ENV_FILE}" ]; then
  if [ ! -f "${EXAMPLE_FILE}" ]; then
    echo "ERROR: ${EXAMPLE_FILE} not found. Cannot create ${ENV_FILE}." >&2
    exit 1
  fi
  cp "${EXAMPLE_FILE}" "${ENV_FILE}"
  echo "Created ${ENV_FILE} from ${EXAMPLE_FILE}."
fi

# ---- Strip PUBLIC_BASE_URL from .env (it is per-session, must not live in .env) ----
# This migrates any .env that was created from an older .env.example which included
# a blank PUBLIC_BASE_URL= stub that would poison Make's variable namespace.
if grep -q "^PUBLIC_BASE_URL=" "${ENV_FILE}" 2>/dev/null; then
  if sed --version 2>/dev/null | grep -q GNU; then
    sed -i '/^PUBLIC_BASE_URL=/d' "${ENV_FILE}"
  else
    sed -i '' '/^PUBLIC_BASE_URL=/d' "${ENV_FILE}"
  fi
  echo "Removed PUBLIC_BASE_URL= from ${ENV_FILE} (it must be exported in your shell, not stored here)."
fi

# ---- Read current value ----
current_key=""
if grep -q "^HELIUS_API_KEY=" "${ENV_FILE}" 2>/dev/null; then
  current_key=$(grep "^HELIUS_API_KEY=" "${ENV_FILE}" | head -1 | cut -d'=' -f2-)
fi

# ---- Check if already set and not placeholder ----
if [ -n "${current_key}" ] && [ "${current_key}" != "${PLACEHOLDER}" ]; then
  key_len=${#current_key}
  echo "HELIUS_API_KEY is already set in ${ENV_FILE} (length=${key_len})."
  printf "Overwrite? [y/N] "
  read -r overwrite
  if [ "${overwrite}" != "y" ] && [ "${overwrite}" != "Y" ]; then
    echo "Keeping existing key. No changes made."
    exit 0
  fi
fi

# ---- Prompt for key (silent input) ----
echo ""
printf "Enter HELIUS_API_KEY (input is hidden): "
new_key=""
if [ -t 0 ]; then
  # Interactive terminal — use silent read
  read -rs new_key
  echo ""  # newline after hidden input
else
  # Non-interactive (piped) — read normally
  read -r new_key
fi

if [ -z "${new_key}" ]; then
  echo "ERROR: HELIUS_API_KEY cannot be empty." >&2
  exit 1
fi

if [ "${new_key}" = "${PLACEHOLDER}" ]; then
  echo "ERROR: You pasted the placeholder value. Enter your real Helius API key." >&2
  exit 1
fi

# ---- Write key into .env (replace or append) ----
if grep -q "^HELIUS_API_KEY=" "${ENV_FILE}"; then
  # Replace existing line (portable sed — works on macOS and Linux)
  if sed --version 2>/dev/null | grep -q GNU; then
    sed -i "s|^HELIUS_API_KEY=.*|HELIUS_API_KEY=${new_key}|" "${ENV_FILE}"
  else
    # macOS sed requires an extension argument
    sed -i '' "s|^HELIUS_API_KEY=.*|HELIUS_API_KEY=${new_key}|" "${ENV_FILE}"
  fi
else
  # Append new line
  echo "HELIUS_API_KEY=${new_key}" >> "${ENV_FILE}"
fi

key_len=${#new_key}
echo "HELIUS_API_KEY saved to ${ENV_FILE} (length=${key_len})"
echo ""
echo "Next steps:"
echo "  make prove-go-live    — verify the key is present and start strict live mode"
echo "  make clean-start      — start services (uses .env automatically)"
