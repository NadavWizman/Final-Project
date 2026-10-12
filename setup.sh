#!/usr/bin/env bash
# setup.sh — one-time setup for TradeDesk. Run once after cloning: bash setup.sh
#
#   1. a Python virtual environment in ./.venv with the dependencies
#      (gateway, oracle signers); run.sh uses it
#   2. backend/.env with a random SECRET_KEY
#   3. builds the node binary (nodes/tradedesk-node)
#   4. creates a local network in ./testnet: 4 validators, their keys,
#      one genesis file, a signing key for each of the 5 price sources and
#      the custodian's key (the only signer of deposits)
#
# Then: ./run.sh start
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT"

echo ""
echo "========================================"
echo "  TradeDesk — setup"
echo "========================================"

echo "[1/4] Python dependencies…"
command -v python3 >/dev/null || { echo "  ✗ python3 not found"; exit 1; }
python3 -c 'import sys; sys.exit(sys.version_info < (3, 11))' \
    || { echo "  ✗ Python 3.11+ is needed (found $(python3 -V 2>&1))"; exit 1; }
# a virtual environment, so the install works where the system Python is
# managed by the OS or Homebrew (PEP 668)
[ -x .venv/bin/python3 ] || python3 -m venv .venv
.venv/bin/python3 -m pip install --quiet --upgrade pip
.venv/bin/python3 -m pip install --quiet -r requirements.txt
echo "  installed into ./.venv"
PY=.venv/bin/python3

echo "[2/4] Gateway configuration…"
if [ ! -f backend/.env ]; then
    key=$("$PY" -c "import secrets; print(secrets.token_urlsafe(50))")
    sed "s|^SECRET_KEY=.*|SECRET_KEY=$key|" backend/.env.example > backend/.env
    chmod 600 backend/.env
    echo "  created backend/.env (random SECRET_KEY)"
else
    echo "  backend/.env exists — kept"
fi

echo "[3/4] Building the node…"
command -v go >/dev/null || { echo "  ✗ Go is not installed: https://go.dev/dl/ (1.26+)"; exit 1; }
(cd nodes && go build -o tradedesk-node ./cmd/tradedesk-node)
echo "  built nodes/tradedesk-node"

echo "[4/4] Local network…"
if [ -d testnet ]; then
    echo "  ./testnet exists — kept (delete it to start a fresh chain)"
else
    nodes/tradedesk-node init -dir testnet -validators 4 -oracles yahoo,nasdaq,cnbc,tradingview,google | sed 's/^/  /'
fi

echo ""
echo "Done. Start everything with:   ./run.sh start"
echo "Install the wallet once: chrome://extensions -> Developer mode -> Load unpacked -> the wallet/ folder,"
echo "then create an account in it (write down its recovery code, choose a password) and open http://127.0.0.1:8000."
echo ""
