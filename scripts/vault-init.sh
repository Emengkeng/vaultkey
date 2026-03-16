#!/bin/sh
# vault-init.sh
# Runs once at startup. Initializes Vault if not already initialized,
# unseals it, and prints the root token and unseal keys.
# On subsequent runs it just unseals using the stored keys.
#
# IMPORTANT: The unseal keys and root token are stored in /vault/data/init.json
# This file must be backed up securely by the operator. Loss of unseal keys
# means permanent loss of access to all encrypted keys.

set -e

VAULT_ADDR="${VAULT_ADDR:-http://vault:8200}"
INIT_FILE="/vault/data/init.json"
MAX_RETRIES=30

wait_for_vault() {
  echo "Waiting for Vault to start..."
  i=0
  # Use query params to make health endpoint return 200 even when sealed/uninitialized
  until curl -sf "${VAULT_ADDR}/v1/sys/health?standbyok=true&sealedcode=200&uninitcode=200" > /dev/null 2>&1 || [ $i -ge $MAX_RETRIES ]; do
    echo "  Attempt $((i+1))/$MAX_RETRIES..."
    sleep 1
    i=$((i+1))
  done
  if [ $i -ge $MAX_RETRIES ]; then
    echo "ERROR: Vault did not start in time"
    exit 1
  fi
  echo "Vault is up"
}

init_vault() {
  echo "Initializing Vault..."
  # 3 key shares, threshold of 2
  # For customer-managed unseal: operator provides their own KMS here
  
  echo "DEBUG: Attempting to initialize vault at ${VAULT_ADDR}/v1/sys/init"
  INIT_RESPONSE=$(curl -sf \
    --request POST \
    --data '{"secret_shares": 3, "secret_threshold": 2}' \
    "${VAULT_ADDR}/v1/sys/init" 2>&1)
  
  CURL_EXIT=$?
  if [ $CURL_EXIT -ne 0 ]; then
    echo "ERROR: Vault initialization failed with exit code $CURL_EXIT"
    echo "Response: $INIT_RESPONSE"
    exit 1
  fi
  
  echo "DEBUG: Initialization response received, saving to $INIT_FILE"
  echo "$INIT_RESPONSE" > "$INIT_FILE"
  chmod 600 "$INIT_FILE"

  echo ""
  echo "=========================================================="
  echo "  VAULT INITIALIZED - SAVE THESE KEYS SECURELY"
  echo "=========================================================="
  echo "$INIT_RESPONSE" | python3 -c "
import json, sys
data = json.load(sys.stdin)
for i, key in enumerate(data['keys']):
    print(f'  Unseal Key {i+1}: {key}')
print(f'  Root Token:   {data[\"root_token\"]}')
"
  echo "=========================================================="
  echo "  These keys are also saved to: $INIT_FILE"
  echo "  Back this file up. Loss = permanent loss of wallet access."
  echo "=========================================================="
  echo ""
}

unseal_vault() {
  if [ ! -f "$INIT_FILE" ]; then
    echo "ERROR: $INIT_FILE not found. Was Vault initialized?"
    exit 1
  fi

  echo "Unsealing Vault..."
  # Use first 2 unseal keys (threshold = 2)
  KEY1=$(python3 -c "import json; d=json.load(open('$INIT_FILE')); print(d['keys'][0])")
  KEY2=$(python3 -c "import json; d=json.load(open('$INIT_FILE')); print(d['keys'][1])")

  curl -sf --request POST \
    --data "{\"key\": \"$KEY1\"}" \
    "${VAULT_ADDR}/v1/sys/unseal" > /dev/null

  curl -sf --request POST \
    --data "{\"key\": \"$KEY2\"}" \
    "${VAULT_ADDR}/v1/sys/unseal" > /dev/null

  echo "Vault unsealed"
}

enable_transit() {
  ROOT_TOKEN=$(python3 -c "import json; d=json.load(open('$INIT_FILE')); print(d['root_token'])")
  MOUNT_PATH="${VAULT_MOUNT_PATH:-transit}"
  KEY_NAME="${VAULT_KEY_NAME:-vaultkey-master}"

  # check if transit is already enabled
  STATUS=$(curl -sf \
    -H "X-Vault-Token: $ROOT_TOKEN" \
    "${VAULT_ADDR}/v1/sys/mounts/${MOUNT_PATH}" 2>&1 || true)

  if echo "$STATUS" | grep -q "aes256-gcm96\|transit"; then
    echo "Transit secrets engine already enabled"
  else
    echo "Enabling transit secrets engine..."
    curl -sf \
      --request POST \
      -H "X-Vault-Token: $ROOT_TOKEN" \
      --data "{\"type\": \"transit\"}" \
      "${VAULT_ADDR}/v1/sys/mounts/${MOUNT_PATH}" > /dev/null

    echo "Creating encryption key: $KEY_NAME"
    curl -sf \
      --request POST \
      -H "X-Vault-Token: $ROOT_TOKEN" \
      --data '{"type": "aes256-gcm96", "exportable": false}' \
      "${VAULT_ADDR}/v1/${MOUNT_PATH}/keys/${KEY_NAME}" > /dev/null
  fi

  echo "Transit engine ready"

  # write the root token to a known location for the app container to pick up
  ROOT_TOKEN_FILE="/vault/data/root_token"
  echo "$ROOT_TOKEN" > "$ROOT_TOKEN_FILE"
  chmod 600 "$ROOT_TOKEN_FILE"
}

main() {
  wait_for_vault

  echo "DEBUG: Checking if Vault is initialized..."
  INIT_STATUS=$(curl -sf "${VAULT_ADDR}/v1/sys/init" | python3 -c "import json,sys; print(json.load(sys.stdin)['initialized'])")
  echo "DEBUG: Vault initialized status: $INIT_STATUS"

  if [ "$INIT_STATUS" = "False" ]; then
    init_vault
  else
    echo "Vault already initialized"
  fi

  echo "DEBUG: Checking if Vault is sealed..."
  SEAL_STATUS=$(curl -sf "${VAULT_ADDR}/v1/sys/seal-status" | python3 -c "import json,sys; print(json.load(sys.stdin)['sealed'])")
  echo "DEBUG: Vault sealed status: $SEAL_STATUS"

  if [ "$SEAL_STATUS" = "True" ]; then
    unseal_vault
  else
    echo "Vault already unsealed"
  fi

  enable_transit
  echo "Vault ready"
}

main