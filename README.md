# TradeDesk — Consensus-Backed Trading Platform

A decentralized stock trading system built as a final project.  
Every trade is signed with ECDSA and must be approved by a quorum of a 3-node
blockchain cluster before Django settles it — and Django verifies that quorum
cryptographically instead of taking any single node's word for it.

---

## Architecture

```
Browser (SPA)
    │ HTTP/JSON
    ▼
Django Backend ──gRPC──▶ Oracle Service (yfinance)
    ▲    ▲                     ▲
    │    │ (every node re-reads │ (every node queries it independently)
    │    │  orders from Django) │
    │ Node 1 (Leader) ──gRPC──▶ Node 2 (Validator)
    │                 ──gRPC──▶ Node 3 (Validator)
    └── execute_order + signed votes of ≥ 2 nodes
```

| Component | Technology | Purpose |
|-----------|------------|---------|
| `backend/` | Django + DRF | REST API, user auth, order lifecycle, settlement, SL/TP monitor, web UI |
| `nodes/` | Go + gRPC | 3-node blockchain consensus (1 leader + 2 validators) |
| `oracle_service/` | Python + gRPC | Live stock prices via Yahoo Finance |
| `blockchain/proto/` | Protobuf | gRPC message definitions (`generate.sh` regenerates the code) |
| `locustfile.py` | Locust | Load-testing scenario |

Each node keeps its **own** append-only chain on disk (`nodes/chain_node1.jsonl`,
`chain_node2.jsonl`, `chain_node3.jsonl`). The chain is fully verified (genesis,
links, every block hash) when a node starts, so a node that restarts resumes
from where it left off — and a tampered or corrupt chain file stops the node
instead of being silently accepted.

---

## Features

| Feature | Description |
|---------|-------------|
| **Stock trading** | Market and limit BUY/SELL on 54 S&P 500 tickers, executed through consensus |
| **Limit orders** | Rest until the market reaches the limit, then fill at market; expire after 24 h (`LIMIT_ORDER_TTL`) |
| **CFD trading** | Leveraged long/short positions with live P&L, margin tracking and automatic liquidation |
| **Options** | CALL/PUT contracts with a real chain from Yahoo Finance; premium fetched server-side |
| **Stop Loss / Take Profit** | Multiple SL/TP levels per position — set with the order (signed with it) or later |
| **AI news & chat** | Per-ticker summaries and Q&A via Google Gemini, grounded in live market data |
| **Consensus** | Every order signed with ECDSA; settled only with Ed25519-signed votes from ≥ 2 of 3 nodes |

A background monitor (`trading/sltp.py`) runs every 15 seconds:

* **SL/TP** — triggers any level whose price has been reached. It never sells
  more than is actually held and not already pending sale.
* **CFD margin** — liquidates a CFD once its loss reaches 80 % of the margin
  posted (the liquidation price is shown next to each position).
* **Option expiry** — settles expired options at the underlying's close on the
  expiry date.

Stock and CFD triggers create a **signed order at status `SUBMITTED`**, so an
automatic close goes through node consensus exactly like a manual trade. Option
expiry settles directly — it is an expiry event rather than a trade the user is
authorising.

The monitor runs inside `manage.py runserver`. Under any other server
(gunicorn, `runserver --noreload` in production, …) run it as its own process
with `python3 manage.py run_sltp_monitor` and set `SLTP_MONITOR=external`.

---

## Order Lifecycle

```
DRAFT → SUBMITTED → CONFIRMED
                 └→ REJECTED (with a reason)
```

1. **DRAFT** — user creates an order (ticker, quantity, optional limit price, optional SL/TP)
2. **SUBMITTED** — backend signs **every trade-defining field** (stock, side, trade type,
   quantity, nonce, leverage, limit, position, option fields, SL/TP) with the user's ECDSA key
3. **Consensus** — the Leader polls `GET /orders/?status=SUBMITTED`, queries the Oracle,
   runs the validation checks itself, and sends `Propose` to both Validators in parallel.
   Every node independently:
   - checks chain continuity (index + prev_hash) against **its own** chain
   - re-reads the order **from Django with its own credentials** — it must still be
     `SUBMITTED`, and the block, the envelope and Django must agree on the trade,
     the signature and the user's public key
   - verifies the ECDSA signature with the key **from Django** (never the Leader's copy)
   - checks the price quote is less than 60 seconds old
   - queries the Oracle and verifies the **block's** price is within 1 % of its own
   - approves by signing `tradedesk-vote|<order>|<block hash>|<price>` with its Ed25519 node key
4. **≥ 2 signed approvals** → the Leader calls `/execute_order/` with the votes. Django:
   - verifies the signatures of ≥ 2 distinct registered consensus nodes
   - re-verifies the user's signature against the current row
   - re-verifies the price against the Oracle (≤ 2 % divergence, fails closed)
   - settles under row locks and records the certified block hash on the order
5. **Commit** — the Leader appends the block and broadcasts `Commit`. Validators append
   a block **only if Django shows its order CONFIRMED under exactly that block hash**.
6. **REJECTED** — insufficient funds, signature mismatch, price fails Django's
   re-verification, consensus not reached, price feed unavailable, or limit order expired.
   The reason is stored on the order and shown in the activity list.

An order never stays queued indefinitely: failed consensus is reported with
`/reject_order/`, an unreachable Oracle is retried for 60 seconds, a settlement
that keeps failing for a temporary reason is rejected after 5 minutes, and limit
orders expire.

---

## Consensus & Fault Tolerance

**Proof of consensus.** Each node has an Ed25519 signing key (`nodes/nodekey_<name>.hex`,
created on first start, never committed) and registers its public key with Django
(`POST /api/node-key/`, trust on first use — a different key later is refused until
an admin removes the old one in `/admin`). Django settles nothing without signatures
from **two distinct registered nodes** over the exact order, block hash and price.
A single compromised node — the Leader included — cannot move money on its own.

**Crash-safe settlement.** Before asking Django to settle, the Leader writes a
write-ahead record (`pending_<node>.json`). If it crashes or the response is lost,
the next cycle finishes the job: Django's `execute_order` is idempotent for the
same certified block, so a retry completes the commit instead of losing the block
or charging twice.

**Node recovery.** A Validator that was offline replies `DESYNC head=<n>`; the Leader
replays the blocks it missed (each certified by Django) and asks it to vote again.
A Leader that fell behind — for example after losing its chain file — sees
`AHEAD head=<n>` and copies the committed blocks from a Validator with the
`GetBlocks` RPC, re-certifying every block with Django before appending it.

**Authenticated channel.** Node-to-node gRPC calls carry a timestamped HMAC of the
shared `CLUSTER_SECRET` bound to the RPC method (package `nodes/clusterauth`),
valid for 30 seconds. The secret itself never travels over the wire.

**What consensus protects — and what it does not.**

| Attack | Caught? | Why |
|--------|---------|-----|
| User settles their own order directly | ✅ | `execute_order` is restricted to the `consensus_nodes` role |
| One node (e.g. the Leader) settles without the others | ✅ | Django requires Ed25519 votes from ≥ 2 registered nodes |
| Leader reports a manipulated price | ✅ | Every node queries the Oracle itself; > 1 % divergence is rejected |
| Leader forges a block or signs an order with its own key | ✅ | Nodes take the order, signature and public key from Django, not from the Leader |
| Replaying an old signed order | ✅ | The order must still be `SUBMITTED` in Django; nonces are unique |
| Insider injects a block with `Commit` | ✅ | Validators append only blocks Django certified (order CONFIRMED under that hash) |
| Stranger sends RPCs to a Validator's port | ✅ | Calls without a valid cluster HMAC are rejected `Unauthenticated` |
| Leader settles at a price consensus never saw | ✅ | Votes sign the price; Django also re-verifies it against the Oracle |
| Signed order fields altered in the database or via the API | ✅ | Every trade field is signed; nodes and Django re-verify; the API has no PUT/PATCH/DELETE |
| Tampering with committed chain history on disk | ✅ | Every field is hashed and the whole chain is verified at startup |
| Malformed gRPC request crashes a Validator | ✅ | Nil/short-field guards and a panic-recovery interceptor |
| Wallet balance edited directly in the database | ❌ | Balances are not part of the chain; Django is the source of truth |
| Attacker with database **and** server access re-signs orders | ❌ | Users' private keys are held by the server (custodial design) |
| Compromised Oracle service | ❌ | All nodes query the same source and would agree on a bad price |
| Two of three nodes colluding | ❌ | A 2-of-3 quorum assumes at most one faulty node |
| Option premiums | ⚠️ | Fetched by Django from Yahoo (not voted on), but floored at intrinsic value computed from the consensus-verified stock price |

In short: consensus protects the **integrity of the order as the user authorised it**
and the **price it executes at**, verified independently at every node and again at
Django, and Django will not settle without cryptographic proof of a quorum. It does
not make Django's database tamper-proof: Django remains the trusted source of truth
for balances and holdings, and it holds users' signing keys.

---

## Prerequisites

- Python 3.11+
- Go 1.26+ (the node binary is built from source by `setup.sh`)

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
2. Create `backend/.env` with a random `SECRET_KEY`, and `nodes/.env` with a random
   `CLUSTER_SECRET` and random node passwords (both files are mode 600 and gitignored)
3. Run Django migrations
4. Seed the S&P 500 stock catalog (54 tickers)
5. Create the three node users (`node1`, `node2`, `node3`) with the passwords from `nodes/.env`
6. Build the Go node binary

### Option B — manual steps

```bash
pip3 install -r requirements.txt

cp backend/.env.example backend/.env    # set SECRET_KEY to a long random string
cp nodes/.env.example nodes/.env        # set CLUSTER_SECRET and NODE1_PASS..NODE3_PASS

cd backend
python3 manage.py migrate
python3 manage.py seed_stocks
python3 manage.py create_node_users     # reads the passwords from ../nodes/.env

cd ../nodes && go build -o nodes_bin .
```

### Upgrading from an earlier version

The block hash format and the signed-message format changed. Stop every node, delete
`nodes/chain_*.jsonl`, run `python3 manage.py migrate` and `bash setup.sh` again, and
rebuild the node binary. Orders submitted before the upgrade must be submitted again.

---

## Running the Project

Open **five** terminal windows from the project root:

```bash
# Terminal 1 — Oracle service
cd oracle_service && python3 oracle_server.py

# Terminal 2 — Node 1: Leader
cd nodes && NODE_NAME=node1 IS_LEADER=true ./nodes_bin

# Terminal 3 — Node 2: Validator (listens on 9002)
cd nodes && NODE_NAME=node2 ./nodes_bin

# Terminal 4 — Node 3: Validator (listens on 9003)
cd nodes && NODE_NAME=node3 ./nodes_bin

# Terminal 5 — Django backend (also runs the SL/TP monitor)
cd backend && python3 manage.py runserver
```

Nodes read the shared settings (cluster secret, their Django password, URLs) from
`nodes/.env`; anything set on the command line overrides it. Nodes can be started
before Django — they keep retrying until they can register their signing key.

Open **http://127.0.0.1:8000** in your browser.

> **Rebuild the Go binary after source changes:** `cd nodes && go build -o nodes_bin .`,
> then **restart all three nodes**. Running a mix of old and new binaries is not supported.

---

## Demonstrating Consensus

**1. Limit orders rest.** Place a BUY with a limit below the market. The order stays
`SUBMITTED` (no block is proposed) and fills — at market — once the price reaches the limit.

**2. Node offline → 2/3, then automatic recovery.**  
Stop node3 and place an order: consensus succeeds with 2/3. Restart node3 and place
another: node3 reports `DESYNC`, the Leader replays the block it missed, and the tally
returns to 3/3 with identical chains on all nodes.

**3. Rogue Oracle — a Leader fed a manipulated price.**  
`oracle_service/rogue_oracle.py` speaks the same gRPC interface but reports a price of
your choosing.

```bash
# Terminal 6 — dishonest Oracle on port 8002
cd oracle_service && python3 rogue_oracle.py

# Terminal 2 — point ONLY the Leader at it
cd nodes && ORACLE_URL=127.0.0.1:8002 NODE_NAME=node1 IS_LEADER=true ./nodes_bin
```

The Validators still query the honest Oracle on 8001, see the divergence and reject:
`price divergence …% > 1%` → **1/3 approvals**, the order is rejected. Restart the
Leader without `ORACLE_URL` to restore normal operation.

**4. Database tampering.**  
Stop the Leader, place an order, then alter a signed field:

```python
# python3 manage.py shell
from trading.models import Order
o = Order.objects.filter(status='SUBMITTED').latest('id')
o.quantity = 1000          # attacker inflates the order
o.save()                   # signature is NOT regenerated
```

Restart the Leader: every node reports `ECDSA verification failed` → **0/3**, and the
order is rejected. (Django would also refuse to settle it on its own.)

**5. Malicious proposer.**  
`nodes/attacker/` stands in for a compromised node. It signs a made-up order with its own
key and tries to get a forged block (1000 shares at $1.00) approved with `Propose`, then to
append it directly with `Commit`.

```bash
cd nodes
go run ./attacker                                                       # an outsider
CLUSTER_SECRET=$(grep ^CLUSTER_SECRET .env | cut -d= -f2) go run ./attacker   # an insider
```

The outsider is refused `Unauthenticated`. The insider's `Propose` is refused because the
order is not in Django, and its `Commit` because Django never certified the block. The tool
doubles as a security regression test: if a future change reopens a hole, it reports it.

---

## Environment Variables

### Django (`backend/.env`)

| Variable | Default | Description |
|----------|---------|-------------|
| `SECRET_KEY` | *(required)* | Django secret key — generated by `setup.sh`. Outside `DEBUG`/tests Django refuses to start without it |
| `DEBUG` | `False` | `backend/.env` created by `setup.sh` sets `True` for local development |
| `ALLOWED_HOSTS` | `127.0.0.1,localhost` | Comma-separated allowed host names |
| `ORACLE_URL` | `127.0.0.1:8001` | Oracle gRPC address used for prices and settlement re-verification |
| `GEMINI_API_KEY` | *(empty)* | Enables AI news & chat — get a free key at [aistudio.google.com/app/apikey](https://aistudio.google.com/app/apikey) |
| `THROTTLE_REGISTER` | `20/hour` | Registration rate limit per client address |
| `THROTTLE_AI` | `120/hour` | AI news/chat rate limit per user |
| `SLTP_MONITOR` | `auto` | Set `external` when the monitor runs as `manage.py run_sltp_monitor` |

### Nodes (`nodes/.env`, see `nodes/.env.example`)

| Variable | Default | Description |
|----------|---------|-------------|
| `CLUSTER_SECRET` | *(required, ≥ 16 chars)* | Shared node-to-node credential — identical on every node |
| `NODE1_PASS`…`NODE3_PASS` | *(required)* | Each node's Django password (also read by `create_node_users`) |
| `NODE_NAME` | `node1` | Identifies the node (`node1`, `node2`, `node3`) |
| `NODE_PASS` | — | Overrides the `NODE<n>_PASS` entry for this node |
| `IS_LEADER` | `false` | Set `true` for the leader node only |
| `LISTEN_PORT` | auto (9002/9003) | gRPC port for validators |
| `DJANGO_URL` | `http://127.0.0.1:8000/api` | Backend API base URL |
| `ORACLE_URL` | `127.0.0.1:8001` | Oracle gRPC address |
| `VALIDATOR_ADDRESSES` | `localhost:9002,localhost:9003` | Comma-separated validator addresses (Leader only) |
| `LIMIT_ORDER_TTL` | `24h` | How long a limit order may wait for its price (Leader only) |

### Oracle (`oracle_service/`)

| Variable | Default | Description |
|----------|---------|-------------|
| `ORACLE_LISTEN` | `127.0.0.1:8001` | Address the Oracle binds to (`0.0.0.0:8001` only if nodes run on other hosts) |
| `ORACLE_CACHE_S` | `3` | Seconds a quote is reused, so all nodes see the same price |
| `ROGUE_PRICE` | `1.00` | *(rogue_oracle.py)* fabricated price to report |
| `ROGUE_PORT` | `8002` | *(rogue_oracle.py)* port to listen on |

---

## Management Commands

| Command | Description |
|---------|-------------|
| `python3 manage.py migrate` | Apply all database migrations |
| `python3 manage.py seed_stocks` | Populate the Stock table with 54 S&P 500 tickers |
| `python3 manage.py create_node_users` | Create/sync node1..node3 in the `consensus_nodes` group with the passwords from `nodes/.env` |
| `python3 manage.py run_sltp_monitor` | Run the SL/TP, CFD-margin and option-expiry monitor as its own process |
| `python3 manage.py create_test_users` | Create N test users for load testing (default 1000) |
| `python3 manage.py test trading` | Run the Django test suite |
| `python3 manage.py createsuperuser` | Create an admin user for `/admin` |

All seed/create commands are **idempotent** — safe to run multiple times.

---

## Running Tests

```bash
cd backend && python3 manage.py test trading          # Django — 114 tests
cd nodes && go test ./...                             # Go nodes
cd oracle_service && python3 -m unittest              # Oracle service
```

**Django** covers crypto utilities and the signed payload, registration and
validation, deposits, order creation and immutability, option/close validation,
execution (BUY/SELL, balances, stale prices, resting limit orders), consensus proof
(quorum, duplicate/forged/foreign votes, idempotent retries), node-key registration,
the node role, price re-verification, CFD closes and liquidation, option pricing and
expiry, SL/TP (routing, quantity capping, server-side levels), endpoint validation and
rate limits. A cross-language fixture checks that a vote signed by the Go code verifies
in Python.

**Go** covers chain persistence and tamper detection, commit certification and
conflicts, desync recovery, every Propose rejection path (key substitution, replay,
unknown order, price manipulation, database tampering), crash guards, cluster
authentication, the Django-produced ECDSA fixture, and end-to-end runs over real gRPC
(Leader + 2 Validators + fake Django): normal settlement, missing quorum, lost
settlement responses, a Leader recovering a lost chain, and limit orders.

> **Note on 403 vs 401:** unauthenticated requests receive `403`, not `401`.
> `trading/auth.py` defines `SilentBasicAuthentication`, which omits the
> `WWW-Authenticate` header so browsers never show their native credential
> dialog — the SPA handles login itself.

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
| `POST` | `/api/register/` | Create account — returns ECDSA public key and $10 000 wallet (rate-limited) |
| `GET` | `/api/portfolio/` | Balance and holdings |
| `POST` | `/api/deposit/` | Add USD to wallet ($0.01 – $1,000,000 per deposit) |

### Orders & consensus

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/api/orders/` | List orders (own only; nodes see all). `?status=` filters, `?limit=N` returns the N newest (max 500) |
| `POST` | `/api/orders/` | Create DRAFT order (optional `limit_price`, `stop_loss`/`take_profit` + quantities) |
| `POST` | `/api/orders/{id}/submit/` | Sign the order with ECDSA and submit it for consensus |
| `POST` | `/api/orders/{id}/execute_order/` | Settle after consensus — nodes only, requires `block_hash` and ≥ 2 signed `votes` |
| `POST` | `/api/orders/{id}/reject_order/` | Report failed consensus — nodes only |
| `POST` | `/api/node-key/` | A node registers its Ed25519 vote-signing key — nodes only |

Orders cannot be modified or deleted once created (`PUT`/`PATCH`/`DELETE` → 405).

### Market data (listed tickers only)

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/api/price/{ticker}/` | Live price via Oracle gRPC (with `market_time`) |
| `GET` | `/api/history/{ticker}/` | Historical OHLC data (supports `period` and `interval` params) |

### Positions

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/api/cfd/` | Open and closed CFD positions with P&L and liquidation price |
| `GET` | `/api/options/` | Option positions |
| `GET` | `/api/options/chain/{ticker}/` | Option chain (strikes, expiries, bid/ask) |
| `GET` | `/api/sltp/` | Stop-loss / take-profit levels (`?position=` or `?cfd=`) |
| `POST` | `/api/sltp/` | Create an SL/TP level on a position or CFD |
| `DELETE` | `/api/sltp/{id}/` | Delete an untriggered SL/TP level |

### AI (rate-limited, listed tickers only)

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/api/ai-news/{ticker}/` | Gemini-generated news summary for a ticker |
| `POST` | `/api/ai-chat/{ticker}/` | Ask a question about a ticker, grounded in live price data |
