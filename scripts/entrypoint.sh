#!/bin/sh
# If VAULT_TOKEN_FILE is set, read the token from the file.
# This avoids exposing the token as a plain environment variable.
if [ -n "$VAULT_TOKEN_FILE" ] && [ -f "$VAULT_TOKEN_FILE" ]; then
  export VAULT_TOKEN=$(cat "$VAULT_TOKEN_FILE")
fi

exec /app/vaultkey "$@"
