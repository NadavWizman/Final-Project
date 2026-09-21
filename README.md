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
| `locustfile.py` | Locust | Load-testing scenario |

Each node keeps its **own** append-only chain on disk (`nodes/chain_node1.jsonl`,
`chain_node2.jsonl`, `chain_node3.jsonl`). The chain is replayed on startup, so a
node that restarts resumes from where it left off.

---

## Features

| Feature | Description |
|---------|-------------|
| **Stock trading** | Market and limit BUY/SELL on 54 S&P 500 tickers, executed through consensus |
| **CFD trading** | Leveraged long/short positions with live P&L and margin tracking |
| **Options** | CALL/PUT contracts with a real chain from Yahoo Finance; premium verified server-side |
| **Stop Loss / Take Profit** | Multiple SL/TP levels per position, with per-level quantity and P&L preview |
| **AI news & chat** | Per-ticker summaries and Q&A via Google Gemini, grounded in live market data |
| **Consensus** | Every order signed with ECDSA and approved by ≥ 2 of 3 independent nodes |

A background thread (`trading/sltp.py`, started from `trading/apps.py`) polls every
15 seconds and triggers any SL/TP level whose price has been reached. Stock and CFD
triggers both create a **signed order at status `SUBMITTED`**, so an automatic close
goes through node consensus exactly like a manual trade. Option expiry settles
directly — it is an expiry event rather than a trade the user is authorising.

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
   - checks chain continuity (index + prev_hash) against **its own** chain
   - queries the Oracle and verifies price is within 1 % of the Leader's price
   - checks the price is less than 60 seconds old
   - verifies the user's ECDSA signature
4. **≥ 2/3 approve** → Leader calls `/execute_order/` on Django, broadcasts `Commit` to Validators  
5. **CONFIRMED** — balances updated, block appended to all three chains  
6. **REJECTED** — limit price violated, insufficient funds, or consensus not reached

An order never stays queued indefinitely. If consensus fails, the Leader calls
`/reject_order/` so the order leaves the queue instead of being re-proposed forever.
If the Oracle is unreachable, the Leader retries quietly for 60 seconds and then
rejects the order with `price feed unavailable`.

---

## Consensus & Fault Tolerance

**Node recovery.** A node that is offline during a commit falls behind. On its next
vote it replies `DESYNC head=<n>`; the Leader replays the blocks it missed and asks
it to vote again, so it rejoins consensus automatically instead of rejecting every
future proposal. Validators also verify a block on `Commit`, not just on `Propose`,
so a block that would leave a gap is refused rather than appended blindly.

**What consensus protects — and what it does not.** Worth being precise about:

| Attack | Caught? | Why |
|--------|---------|-----|
| Leader reports a manipulated price | ✅ | Validators query the Oracle themselves; > 1 % divergence is rejected |
| Order fields altered in the database (`quantity`, `stock`, `order_type`, `nonce`) | ✅ | Those fields are covered by the user's ECDSA signature |
| Forged or replaced signature | ✅ | Verified independently by each Validator |
| Tampering with committed chain history | ✅ | Each block hashes the previous one |
| Wallet balance edited directly in the database | ❌ | Never signed, never seen by the nodes |
| Order status flipped in the database | ❌ | Status is not part of the signed payload |
| Compromised Oracle service | ❌ | All nodes query the same source and would agree on a bad price |
| All three nodes colluding | ❌ | 2/3 majority assumes at most one faulty node |

In short: consensus protects the **integrity of the order as the user authorized it**
and the **price it executes at**. It does not make Django's database tamper-proof —
Django remains the trusted source of truth for balances and holdings.

---

## Prerequisites

- Python 3.11+
- Go 1.26+ (only needed if rebuilding the binary — a pre-built arm64 macOS binary is included)

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
> Then **restart all three nodes**. Running a mix of old and new binaries produces
> confusing behaviour — for example, an old Leader will not understand a new
> Validator's resync request.

---

## Demonstrating Consensus

Four ways to show the system refusing a trade it should not execute.

**1. Limit price violation** — a business rule, enforced by Django after consensus.  
Place a BUY with a limit far below market. The nodes approve the block (it is
cryptographically valid), then Django refuses to execute it.

**2. Node offline → 2/3, then automatic recovery.**  
Stop node3, place an order (consensus succeeds 2/3), restart node3, place another.
node3 reports `DESYNC`, the Leader replays the block it missed, and the tally
returns to 3/3.

**3. Rogue Oracle — a Leader fed a manipulated price.**  
`oracle_service/rogue_oracle.py` speaks the same gRPC interface but reports a price
of your choosing.

```bash
# Terminal 6 — dishonest Oracle on port 8002
cd oracle_service && python3 rogue_oracle.py

# Terminal 2 — point ONLY the Leader at it
cd nodes && ORACLE_URL=127.0.0.1:8002 NODE_NAME=node1 NODE_PASS=node1pass IS_LEADER=true ./nodes_bin
```

The Validators still query the honest Oracle on 8001, see the divergence, and reject:
`price divergence 34161.00% > 1%` → **1/3 approvals**. Restart the Leader without
`ORACLE_URL` to restore normal operation.

**4. Database tampering.**  
Stop the Leader, place an order, then alter the signed fields:

```python
# python3 manage.py shell
from trading.models import Order
o = Order.objects.filter(status='SUBMITTED').latest('id')
o.quantity = 1000          # attacker inflates the order
o.save()                   # signature is NOT regenerated
```

Restart the Leader: both Validators report `ECDSA verification failed` → **1/3**.

---

## Environment Variables

### Django (`backend/.env`)

| Variable | Default | Description |
|----------|---------|-------------|
| `SECRET_KEY` | insecure dev value | Django secret key — **change in production** |
| `DEBUG` | `True` | Set to `False` in production |
| `ALLOWED_HOSTS` | `127.0.0.1,localhost` | Comma-separated allowed host names |
| `ORACLE_URL` | `127.0.0.1:8001` | Oracle gRPC address used by the price proxy |
| `GEMINI_API_KEY` | *(empty)* | Enables AI news & chat — get a free key at [aistudio.google.com/app/apikey](https://aistudio.google.com/app/apikey) |

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

### Rogue Oracle (`oracle_service/rogue_oracle.py`)

| Variable | Default | Description |
|----------|---------|-------------|
| `ROGUE_PRICE` | `1.00` | Fabricated price to report |
| `ROGUE_PORT` | `8002` | Port to listen on |

---

## Management Commands

| Command | Description |
|---------|-------------|
| `python3 manage.py migrate` | Apply all database migrations |
| `python3 manage.py seed_stocks` | Populate the Stock table with 54 S&P 500 tickers |
| `python3 manage.py create_node_users` | Create node1/node2/node3 users with `is_staff=True` |
| `python3 manage.py create_test_users` | Create N test users for load testing (default 1000) |
| `python3 manage.py test trading` | Run the Django test suite |
| `python3 manage.py createsuperuser` | Create an admin user for `/admin` |

All seed/create commands are **idempotent** — safe to run multiple times.

---

## Running Tests

### Django

```bash
cd backend
python3 manage.py test trading --verbosity=2
```

45 tests, all passing, covering: crypto utilities, registration, portfolio, deposits,
order creation (whitelist, nonce, quantity validation), order submission
(ECDSA signature), full order execution (BUY/SELL, limit price, balance checks,
stale timestamp), consensus rejection (node-only permission, balance left
untouched, already-confirmed orders protected), partial CFD closes (proportional
margin, LONG/SHORT P&L, quantity capping), and SL/TP consensus routing.

> **Note on 403 vs 401:** unauthenticated requests receive `403`, not `401`.
> `trading/auth.py` defines `SilentBasicAuthentication`, which omits the
> `WWW-Authenticate` header so browsers never show their native credential
> dialog — the SPA handles login itself. DRF returns `403` whenever the
> authenticator supplies no challenge header, so this is intended behaviour.

### Go nodes

```bash
cd nodes
go test ./...
```

Covers desync detection and parsing, block replay ranges, commit gap rejection,
commit idempotency, and full recovery of a node that fell behind.

---

## Load Testing

```bash
pip3 install locust
cd backend && python3 manage.py create_test_users --count 1000
# start Django + Oracle + all three nodes, then:
locust -f locustfile.py --host http://127.0.0.1:8000
```

Open http://localhost:8089 and set users = 1000, spawn rate = 50.
The scenario mixes portfolio reads, stock buys and sells, and CFD open/close
in roughly realistic proportions.

---

## API Reference

All endpoints require HTTP Basic Auth except `/api/register/`.

### Accounts & portfolio

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/api/register/` | Create account — returns ECDSA public key and $10 000 wallet |
| `GET` | `/api/portfolio/` | Balance and holdings |
| `POST` | `/api/deposit/` | Add USD to wallet |

### Orders & consensus

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/api/orders/` | List orders (own only; staff/nodes see all) |
| `POST` | `/api/orders/` | Create DRAFT order |
| `POST` | `/api/orders/{id}/submit/` | Sign order with ECDSA and submit for consensus |
| `POST` | `/api/orders/{id}/execute_order/` | Called by the Leader after consensus is reached (nodes only) |
| `POST` | `/api/orders/{id}/reject_order/` | Called by the Leader when consensus fails (nodes only) |

### Market data

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/api/price/{ticker}/` | Live price via Oracle gRPC |
| `GET` | `/api/history/{ticker}/` | Historical OHLC data (supports `period` and `interval` params) |

### Positions

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/api/cfd/` | Open and closed CFD positions with live P&L |
| `GET` | `/api/options/` | Option positions |
| `GET` | `/api/options/chain/{ticker}/` | Option chain (strikes, expiries, bid/ask) |
| `GET` | `/api/sltp/` | Stop-loss / take-profit levels |
| `POST` | `/api/sltp/` | Create an SL/TP level on a position or CFD |
| `DELETE` | `/api/sltp/{id}/` | Delete an SL/TP level |

### AI

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/api/ai-news/{ticker}/` | Gemini-generated news summary for a ticker |
| `POST` | `/api/ai-chat/{ticker}/` | Ask a question about a ticker, grounded in live price data |
