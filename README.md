# TradeDesk — Consensus-Backed Trading Platform

A decentralized stock trading system built as a final project.  
Every trade is signed with ECDSA and confirmed by a 3-node blockchain consensus before execution.

---

## Architecture

```
Browser (SPA)
    │ HTTP/JSON
    ▼
Django Backend  ──gRPC──▶  Oracle Service (yfinance)
    │                           ▲
    │ (Leader polls)            │ (all nodes query independently)
    ▼                           │
Node 1 (Leader) ──gRPC──▶  Node 2 (Validator)
                 ──gRPC──▶  Node 3 (Validator)
```

| Component | Technology | Purpose |
|-----------|------------|---------|
| `backend/` | Django + DRF | REST API, user auth, order lifecycle, web UI |
| `nodes/` | Go + gRPC | 3-node blockchain consensus (1 leader + 2 validators) |
| `oracle_service/` | Python + gRPC | Live stock prices via Yahoo Finance |
| `blockchain/proto/` | Protobuf | gRPC message definitions |

---

## Order Lifecycle

```
DRAFT → SUBMITTED → CONFIRMED
                 └→ REJECTED
```

1. **DRAFT** — user creates an order (ticker, quantity, optional limit price)  
2. **SUBMITTED** — backend signs the order with the user's ECDSA private key  
3. **Consensus** — the Leader node polls for SUBMITTED orders, queries the Oracle for price,
   and sends a `Propose` gRPC to both Validators.  
   Each Validator independently:
   - checks chain continuity (index + prev_hash)
   - queries the Oracle and verifies price is within 1 % of the Leader's price
   - verifies the user's ECDSA signature
4. **≥ 2/3 approve** → Leader calls `/execute_order/` on Django, broadcasts `Commit` to Validators  
5. **CONFIRMED** — balances updated, block appended to all three chains  
6. **REJECTED** — limit price violated or insufficient funds

---

## Prerequisites

- Python 3.11+
- Go 1.21+ (only needed if rebuilding the binary)

---

## Quick Start

### Option A — one command (recommended)

```bash
git clone https://github.com/NadavWizman/Final-Project.git
cd Final-Project
bash setup.sh
```

`setup.sh` will:
1. Install all Python dependencies from `requirements.txt`
2. Create `backend/.env` from the example file
3. Run Django migrations
4. Seed the S&P 500 stock catalog (54 tickers)
5. Create the three node users (`node1`, `node2`, `node3`)
6. Rebuild the Go binary (if Go is installed)

### Option B — manual steps

```bash
# 1. Install Python dependencies
pip3 install -r requirements.txt

# 2. Configure Django
cd backend
cp .env.example .env        # edit SECRET_KEY if needed

# 3. Database
python3 manage.py migrate
python3 manage.py seed_stocks          # populate stock catalog
python3 manage.py create_node_users    # create node1/node2/node3
```

---

## Running the Project

Open **five** terminal windows from the project root:

```bash
# Terminal 1 — Oracle service (must start first)
cd oracle_service && python3 oracle_server.py

# Terminal 2 — Node 1: Leader
cd nodes && NODE_NAME=node1 NODE_PASS=node1pass IS_LEADER=true ./nodes_bin

# Terminal 3 — Node 2: Validator
cd nodes && NODE_NAME=node2 NODE_PASS=node2pass IS_LEADER=false LISTEN_PORT=9002 ./nodes_bin

# Terminal 4 — Node 3: Validator
cd nodes && NODE_NAME=node3 NODE_PASS=node3pass IS_LEADER=false LISTEN_PORT=9003 ./nodes_bin

# Terminal 5 — Django backend
cd backend && python3 manage.py runserver
```

Open **http://127.0.0.1:8000** in your browser.

> **Rebuild the Go binary after source changes:**
> ```bash
> cd nodes && go build -o nodes_bin .
> ```

---

## Environment Variables

### Django (`backend/.env`)

| Variable | Default | Description |
|----------|---------|-------------|
| `SECRET_KEY` | insecure dev value | Django secret key — **change in production** |
| `DEBUG` | `True` | Set to `False` in production |
| `ALLOWED_HOSTS` | `127.0.0.1,localhost` | Comma-separated allowed host names |
| `ORACLE_URL` | `127.0.0.1:8001` | Oracle gRPC address used by the price proxy |

### Nodes (`nodes/.env.example`)

| Variable | Default | Description |
|----------|---------|-------------|
| `NODE_NAME` | `node1` | Identifies the node (`node1`, `node2`, `node3`) |
| `NODE_PASS` | `node1pass` | Basic auth password matching the Django user |
| `IS_LEADER` | `false` | Set `true` for the leader node only |
| `LISTEN_PORT` | auto (9002/9003) | gRPC port for validators |
| `DJANGO_URL` | `http://127.0.0.1:8000/api` | Backend API base URL |
| `ORACLE_URL` | `127.0.0.1:8001` | Oracle gRPC address |
| `VALIDATOR_ADDRESSES` | `localhost:9002,localhost:9003` | Comma-separated validator addresses (Leader only) |

---

## Management Commands

| Command | Description |
|---------|-------------|
| `python3 manage.py migrate` | Apply all database migrations |
| `python3 manage.py seed_stocks` | Populate the Stock table with 54 S&P 500 tickers |
| `python3 manage.py create_node_users` | Create node1/node2/node3 users with `is_staff=True` |
| `python3 manage.py test trading` | Run the full test suite (32 tests) |
| `python3 manage.py createsuperuser` | Create an admin user for `/admin` |

All seed/create commands are **idempotent** — safe to run multiple times.

---

## Running Tests

```bash
cd backend
python3 manage.py test trading --verbosity=2
```

32 tests covering: crypto utilities, registration, portfolio, deposits,
order creation (whitelist, nonce, quantity validation), order submission
(ECDSA signature), and full order execution (BUY/SELL, limit price,
balance checks, stale timestamp).

---

## API Reference

All endpoints require HTTP Basic Auth except `/api/register/`.

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/api/register/` | Create account — returns ECDSA public key and $10 000 wallet |
| `GET` | `/api/portfolio/` | Balance and holdings |
| `POST` | `/api/deposit/` | Add USD to wallet |
| `GET` | `/api/orders/` | List orders (own only; staff/nodes see all) |
| `POST` | `/api/orders/` | Create DRAFT order |
| `POST` | `/api/orders/{id}/submit/` | Sign order with ECDSA and submit for consensus |
| `POST` | `/api/orders/{id}/execute_order/` | Called by the Leader after consensus is reached |
| `GET` | `/api/price/{ticker}/` | Live price via Oracle gRPC |
| `GET` | `/api/history/{ticker}/` | Historical OHLC data (supports `period` and `interval` params) |
