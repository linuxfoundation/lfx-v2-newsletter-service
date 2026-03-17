#!/usr/bin/env bash
# Copyright The Linux Foundation and each contributor to LFX.
# SPDX-License-Identifier: MIT

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP_PORT="${AUTH_SMOKE_PORT:-18080}"
JWKS_PORT="${AUTH_SMOKE_JWKS_PORT:-18081}"
TEST_TAG="${AUTH_SMOKE_TAG:-Weekly ED Newsletter}"

TMP_DIR="$(mktemp -d)"
JWKS_SERVER_PID=""
APP_PID=""

cleanup() {
  if [[ -n "${APP_PID}" ]] && kill -0 "${APP_PID}" >/dev/null 2>&1; then
    kill "${APP_PID}" >/dev/null 2>&1 || true
    wait "${APP_PID}" 2>/dev/null || true
  fi

  if [[ -n "${JWKS_SERVER_PID}" ]] && kill -0 "${JWKS_SERVER_PID}" >/dev/null 2>&1; then
    kill "${JWKS_SERVER_PID}" >/dev/null 2>&1 || true
    wait "${JWKS_SERVER_PID}" 2>/dev/null || true
  fi

  rm -rf "${TMP_DIR}"
}
trap cleanup EXIT

if lsof -nP -iTCP:"${APP_PORT}" -sTCP:LISTEN >/dev/null 2>&1; then
  echo "auth-smoke: app port ${APP_PORT} is already in use"
  exit 1
fi

if lsof -nP -iTCP:"${JWKS_PORT}" -sTCP:LISTEN >/dev/null 2>&1; then
  echo "auth-smoke: JWKS port ${JWKS_PORT} is already in use"
  exit 1
fi

cat > "${TMP_DIR}/jwks_server.go" <<'EOF'
package main

import (
  "crypto/rand"
  "crypto/rsa"
  "encoding/base64"
  "encoding/json"
  "log"
  "math/big"
  "net/http"
  "os"
  "time"

  "github.com/golang-jwt/jwt/v5"
)

type jwk struct {
  Kty string `json:"kty"`
  Kid string `json:"kid"`
  Alg string `json:"alg"`
  Use string `json:"use"`
  N   string `json:"n"`
  E   string `json:"e"`
}

type jwks struct {
  Keys []jwk `json:"keys"`
}

func b64url(b []byte) string {
  return base64.RawURLEncoding.EncodeToString(b)
}

func main() {
  port := os.Getenv("AUTH_SMOKE_JWKS_PORT")
  tokenPath := os.Getenv("AUTH_SMOKE_TOKEN_PATH")

  priv, err := rsa.GenerateKey(rand.Reader, 2048)
  if err != nil {
    log.Fatal(err)
  }

  kid := "auth-smoke-key"
  set := jwks{Keys: []jwk{{
    Kty: "RSA",
    Kid: kid,
    Alg: "PS256",
    Use: "sig",
    N:   b64url(priv.PublicKey.N.Bytes()),
    E:   b64url(big.NewInt(int64(priv.PublicKey.E)).Bytes()),
  }}}

  claims := jwt.MapClaims{
    "iss":       "heimdall",
    "aud":       "lfx-v2-newsletter-service",
    "principal": "auth-smoke-user",
    "iat":       time.Now().Add(-1 * time.Minute).Unix(),
    "exp":       time.Now().Add(30 * time.Minute).Unix(),
  }

  tok := jwt.NewWithClaims(jwt.SigningMethodPS256, claims)
  tok.Header["kid"] = kid

  signed, err := tok.SignedString(priv)
  if err != nil {
    log.Fatal(err)
  }

  if err := os.WriteFile(tokenPath, []byte(signed), 0600); err != nil {
    log.Fatal(err)
  }

  http.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(set)
  })

  log.Fatal(http.ListenAndServe("127.0.0.1:"+port, nil))
}
EOF

AUTH_SMOKE_JWKS_PORT="${JWKS_PORT}" AUTH_SMOKE_TOKEN_PATH="${TMP_DIR}/token.txt" \
  go run "${TMP_DIR}/jwks_server.go" >"${TMP_DIR}/jwks.log" 2>&1 &
JWKS_SERVER_PID="$!"

for _ in {1..60}; do
  if curl -fsS "http://127.0.0.1:${JWKS_PORT}/jwks" >/dev/null 2>&1; then
    break
  fi
  sleep 0.2
done

if ! curl -fsS "http://127.0.0.1:${JWKS_PORT}/jwks" >/dev/null 2>&1; then
  echo "auth-smoke: JWKS server did not become ready"
  cat "${TMP_DIR}/jwks.log" || true
  exit 1
fi

set +u
set -a
if [[ -f "${ROOT_DIR}/.env" ]]; then
  # shellcheck source=/dev/null
  source "${ROOT_DIR}/.env"
fi
set +a
set -u

export PORT="${APP_PORT}"
export JWKS_URL="http://127.0.0.1:${JWKS_PORT}/jwks"
export JWT_AUDIENCE="lfx-v2-newsletter-service"
export JWT_ISSUER="heimdall"
export JWT_AUTH_DISABLED_MOCK_LOCAL_PRINCIPAL=""

pushd "${ROOT_DIR}" >/dev/null

go run ./cmd/newsletter-api >"${TMP_DIR}/app.log" 2>&1 &
APP_PID="$!"

for _ in {1..100}; do
  if curl -fsS "http://127.0.0.1:${APP_PORT}/livez" >/dev/null 2>&1; then
    break
  fi
  sleep 0.2
done

if ! curl -fsS "http://127.0.0.1:${APP_PORT}/livez" >/dev/null 2>&1; then
  echo "auth-smoke: app did not become ready"
  cat "${TMP_DIR}/app.log" || true
  popd >/dev/null
  exit 1
fi

livez_status="$(curl -s -o /dev/null -w "%{http_code}" "http://127.0.0.1:${APP_PORT}/livez")"
readyz_status="$(curl -s -o /dev/null -w "%{http_code}" "http://127.0.0.1:${APP_PORT}/readyz")"

no_token_status="$(curl -s -o /dev/null -w "%{http_code}" "http://127.0.0.1:${APP_PORT}/newsletters?tag=${TEST_TAG// /%20}")"
invalid_token_status="$(curl -s -o /dev/null -w "%{http_code}" -H "Authorization: Bearer not-a-real-token" "http://127.0.0.1:${APP_PORT}/newsletters?tag=${TEST_TAG// /%20}")"
valid_token="$(cat "${TMP_DIR}/token.txt")"
valid_token_status="$(curl -s -o /dev/null -w "%{http_code}" -H "Authorization: Bearer ${valid_token}" "http://127.0.0.1:${APP_PORT}/newsletters?tag=${TEST_TAG// /%20}")"

popd >/dev/null

echo "auth-smoke: /livez=${livez_status}, /readyz=${readyz_status}, no-token=${no_token_status}, invalid-token=${invalid_token_status}, valid-token=${valid_token_status}"

if [[ "${livez_status}" != "200" ]]; then
  echo "auth-smoke: expected /livez=200"
  exit 1
fi

if [[ "${readyz_status}" != "200" ]]; then
  echo "auth-smoke: expected /readyz=200"
  exit 1
fi

if [[ "${no_token_status}" != "401" ]]; then
  echo "auth-smoke: expected missing token status 401, got ${no_token_status}"
  exit 1
fi

if [[ "${invalid_token_status}" != "401" ]]; then
  echo "auth-smoke: expected invalid token status 401, got ${invalid_token_status}"
  exit 1
fi

if [[ "${valid_token_status}" == "401" ]]; then
  echo "auth-smoke: expected valid token to pass auth layer (non-401), got 401"
  exit 1
fi

echo "auth-smoke: PASS"
