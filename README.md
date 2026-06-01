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
- Go 1.21+

---

## Quick Start

### 1 — Clone and install Python dependencies

```bash
git clone https://github.com/NadavWizman/Final-Project.git
cd Final-Project

pip install -r requirements.txt
```

### 2 — Configure the Django backend

```bash
cd backend
cp .env.example .env          # edit SECRET_KEY and DEBUG as needed
python3 manage.py migrate
python3 manage.py createsuperuser   # optional — for the /admin panel
```

Create node users so the Leader can authenticate with Django:

```bash
python3 manage.py shell -c "
from django.contrib.auth.models import User
User.objects.create_user('node1', password='node1pass', is_staff=True)
"
```

### 3 — Start the Oracle service

```bash
cd oracle_service
python3 oracle_server.py
# Listening on 0.0.0.0:8001
```

### 4 — Start the blockchain nodes (three separate terminals)

```bash
# Terminal A — Leader (node1)
cd nodes
NODE_NAME=node1 NODE_PASS=node1pass IS_LEADER=true ./nodes_bin

# Terminal B — Validator (node2)
NODE_NAME=node2 NODE_PASS=node2pass IS_LEADER=false LISTEN_PORT=9002 ./nodes_bin

# Terminal C — Validator (node3)
NODE_NAME=node3 NODE_PASS=node3pass IS_LEADER=false LISTEN_PORT=9003 ./nodes_bin
```

To recompile the binary after source changes:

```bash
cd nodes
go build -o nodes_bin .
```

### 5 — Start Django

```bash
cd backend
python3 manage.py runserver
# Open http://127.0.0.1:8000
```

---

## Environment Variables

### Django (`backend/.env`)

| Variable | Default | Description |
|----------|---------|-------------|
| `SECRET_KEY` | insecure dev value | Django secret key — **change in production** |
| `DEBUG` | `True` | Set to `False` in production |
| `ALLOWED_HOSTS` | `127.0.0.1,localhost` | Comma-separated allowed host names |

### Nodes (`nodes/.env.example`)

| Variable | Default | Description |
|----------|---------|-------------|
| `NODE_NAME` | `node1` | Identifies the node (`node1`, `node2`, `node3`) |
| `NODE_PASS` | `node1pass` | Basic auth password for Django requests |
| `IS_LEADER` | `false` | Set `true` for the leader node only |
| `LISTEN_PORT` | auto (9002/9003) | gRPC port for validators |
| `DJANGO_URL` | `http://127.0.0.1:8000/api` | Backend API base URL |
| `ORACLE_URL` | `127.0.0.1:8001` | Oracle gRPC address |
| `VALIDATOR_ADDRESSES` | `localhost:9002,localhost:9003` | Comma-separated validator addresses |

---

## Running Tests

```bash
cd backend
python3 manage.py test trading --verbosity=2
```

32 tests covering: crypto utilities, registration, portfolio, deposits, order creation, submission, and execution.

---

## API Reference

All endpoints require HTTP Basic Auth except `/api/register/`.

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/register/` | Create account (returns ECDSA public key) |
| GET | `/api/portfolio/` | Balance and holdings |
| POST | `/api/deposit/` | Add USD to wallet |
| GET | `/api/orders/` | List orders (own only; staff sees all) |
| POST | `/api/orders/` | Create DRAFT order |
| POST | `/api/orders/{id}/submit/` | Sign and submit for consensus |
| POST | `/api/orders/{id}/execute_order/` | Called by Leader after consensus |
| GET | `/api/price/{ticker}/` | Live price via Oracle gRPC |
| GET | `/api/history/{ticker}/` | Historical OHLC data |
