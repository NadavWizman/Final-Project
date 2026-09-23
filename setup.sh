#!/usr/bin/env bash
# setup.sh — one-shot bootstrap for TradeDesk
# Run once after cloning: bash setup.sh
set -e

ROOT="$(cd "$(dirname "$0")" && pwd)"
BACKEND="$ROOT/backend"
NODES="$ROOT/nodes"
ORACLE="$ROOT/oracle_service"

echo ""
echo "========================================"
echo "  TradeDesk — Project Setup"
echo "========================================"
echo ""

# ── 1. Python dependencies ────────────────────────────────────────
echo "[1/5] Installing Python dependencies..."
pip3 install -r "$ROOT/requirements.txt" --quiet

# ── 2. Secrets (.env files) ───────────────────────────────────────
echo "[2/5] Configuring environment and generating secrets..."
rand() { python3 -c "import secrets; print(secrets.token_urlsafe($1))"; }

if [ ! -f "$BACKEND/.env" ]; then
    sed "s|^SECRET_KEY=.*|SECRET_KEY=$(rand 50)|" "$BACKEND/.env.example" > "$BACKEND/.env"
    chmod 600 "$BACKEND/.env"
    echo "      Created backend/.env with a random SECRET_KEY"
    echo "      ℹ  Add your free Gemini API key to GEMINI_API_KEY to enable AI news & chat."
    echo "         Get one at: https://aistudio.google.com/app/apikey"
else
    echo "      backend/.env already exists — skipped."
fi

if [ ! -f "$NODES/.env" ]; then
    sed -e "s|^CLUSTER_SECRET=.*|CLUSTER_SECRET=$(rand 32)|" \
        -e "s|^NODE1_PASS=.*|NODE1_PASS=$(rand 24)|" \
        -e "s|^NODE2_PASS=.*|NODE2_PASS=$(rand 24)|" \
        -e "s|^NODE3_PASS=.*|NODE3_PASS=$(rand 24)|" \
        "$NODES/.env.example" > "$NODES/.env"
    chmod 600 "$NODES/.env"
    echo "      Created nodes/.env with a random cluster secret and node passwords"
else
    echo "      nodes/.env already exists — skipped."
fi

# ── 3. Database ───────────────────────────────────────────────────
echo "[3/5] Running Django migrations..."
python3 "$BACKEND/manage.py" migrate --run-syncdb

echo "      Seeding S&P 500 stock catalog..."
python3 "$BACKEND/manage.py" seed_stocks

echo "      Creating blockchain node users (passwords from nodes/.env)..."
python3 "$BACKEND/manage.py" create_node_users

# ── 4. Go binary ─────────────────────────────────────────────────
echo "[4/5] Building Go nodes binary..."
if command -v go &>/dev/null; then
    (cd "$NODES" && go build -o nodes_bin . && echo "      nodes_bin built successfully.")
else
    if [ -f "$NODES/nodes_bin" ]; then
        echo "      Go not found — using pre-built nodes_bin (arm64 macOS)."
    else
        echo "      ⚠  Go not installed and no pre-built binary found."
        echo "         Install Go from https://go.dev/dl/ and run: cd nodes && go build -o nodes_bin ."
    fi
fi

# ── 5. Done ───────────────────────────────────────────────────────
echo "[5/5] Setup complete!"
echo ""
echo "To start the project, open four terminals and run:"
echo ""
echo "  Terminal 1 — Oracle:"
echo "    cd oracle_service && python3 oracle_server.py"
echo ""
echo "  Terminal 2 — Node 1 (Leader):"
echo "    cd nodes && NODE_NAME=node1 IS_LEADER=true ./nodes_bin"
echo ""
echo "  Terminal 3 — Node 2 (Validator):"
echo "    cd nodes && NODE_NAME=node2 ./nodes_bin"
echo ""
echo "  Terminal 4 — Node 3 (Validator):"
echo "    cd nodes && NODE_NAME=node3 ./nodes_bin"
echo ""
echo "  Terminal 5 — Django:"
echo "    cd backend && python3 manage.py runserver"
echo ""
echo "  Then open: http://127.0.0.1:8000"
echo ""
