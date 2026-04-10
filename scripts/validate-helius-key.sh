#!/usr/bin/env bash
# validate-helius-key.sh — strict pre-flight check for HELIUS_API_KEY.
#
# Usage: validate-helius-key.sh <key>
#
# Exits 0 on valid key, 1 on any failure.
# Never prints the key value — only length and masked confirmation.
#
# Rejection criteria:
#   - empty
#   - shorter than 20 characters
#   - matches any known placeholder pattern (case-insensitive)

set -euo pipefail

KEY="${1:-}"
MIN_LEN=20

# Known placeholder patterns (lowercase comparison)
PLACEHOLDERS=(
  "__paste_your_helius_api_key_here__"
  "paste_your_helius_api_key"
  "replace_me"
  "missing"
  "dummy"
  "test"
  "your_api_key"
  "your-api-key"
  "helius_api_key"
)

if [ -z "$KEY" ]; then
  echo "ERROR: HELIUS_API_KEY is empty. Run 'make setup-live-env'." >&2
  exit 1
fi

key_len=${#KEY}

if [ "$key_len" -lt "$MIN_LEN" ]; then
  echo "ERROR: HELIUS_API_KEY looks invalid or placeholder (length=${key_len} < ${MIN_LEN}). Run 'make setup-live-env'." >&2
  exit 1
fi

# Lowercase the key for pattern matching (never printed)
key_lower=$(echo "$KEY" | tr '[:upper:]' '[:lower:]')

for pat in "${PLACEHOLDERS[@]}"; do
  if [ "$key_lower" = "$pat" ] || echo "$key_lower" | grep -qF "$pat"; then
    echo "ERROR: HELIUS_API_KEY looks invalid or placeholder. Run 'make setup-live-env'." >&2
    exit 1
  fi
done

echo "PASS: HELIUS_API_KEY looks valid (length=${key_len})"
