# TradeDesk — Consensus-Backed Trading Platform

[![tests](https://github.com/NadavWizman/Final-Project/actions/workflows/tests.yml/badge.svg)](https://github.com/NadavWizman/Final-Project/actions/workflows/tests.yml)

A stock, CFD and options trading platform in which **no trade settles on the word of a
single server**. Every order is signed with the user's ECDSA key, independently checked by
a 3-node blockchain cluster, and settled by Django only when it receives cryptographic
proof that at least two of the three nodes approved exactly that order at exactly that price.

**Stack:** Django + DRF · Go + gRPC · Python gRPC Oracle (Yahoo Finance) · Protobuf · single-page web UI

---

## Contents

1. [Quick start](#quick-start)
2. [Using the app](#using-the-app)
3. [Architecture](#architecture)
4. [Order lifecycle](#order-lifecycle)
5. [Consensus and security](#consensus-and-security)
6. [Demonstrating consensus](#demonstrating-consensus)
7. [Testing](#testing)
8. [Configuration](#configuration)
9. [Management commands](#management-commands)
10. [API reference](#api-reference)
11. [Load testing](#load-testing)
12. [Troubleshooting](#troubleshooting)
13. [Design decisions and limitations](#design-decisions-and-limitations)

---

## Quick start

**Prerequisites:** Python 3.11+ and Go 1.26+ (macOS or Linux).

```bash
git clone https://github.com/NadavWizman/Final-Project.git
cd Final-Project
bash setup.sh          # once: dependencies, secrets, database, node binary
./run.sh start         # starts all five services
```

Open **http://127.0.0.1:8000**, create an account (you get a $10,000 wallet) and trade.
Stop everything with `./run.sh stop`.

| Command | What it does |
|---------|--------------|
| `./run.sh start` | Starts the Oracle, Django, both Validators and the Leader, waiting until each is up |
| `./run.sh status` | Shows which services are running |
| `./run.sh logs [service]` | Follows the logs (`oracle`, `django`, `node1`, `node2`, `node3`) |
| `./run.sh stop` | Stops everything |
| `DJANGO_PORT=8080 ./run.sh start` | Uses another web port (the nodes follow automatically) |
| `ORACLE_MODE=fixed ./run.sh start` | **Offline demo:** every ticker is priced at `FIXED_PRICE` (default $190) — use it without internet or when Yahoo has no data |

`setup.sh` installs the Python requirements, generates `backend/.env` (random
`SECRET_KEY`) and `nodes/.env` (random cluster secret and node passwords), runs the
migrations, seeds 54 S&P 500 stocks, creates the three node accounts and builds the Go
binary. Secrets are never committed.

<details>
<summary>Running each service in its own terminal instead</summary>

```bash
cd oracle_service && python3 oracle_server.py            # 1. Oracle (port 8001)
cd backend && python3 manage.py runserver                 # 2. Django (port 8000)
cd nodes && NODE_NAME=node2 ./nodes_bin                   # 3. Validator (port 9002)
cd nodes && NODE_NAME=node3 ./nodes_bin                   # 4. Validator (port 9003)
cd nodes && NODE_NAME=node1 IS_LEADER=true ./nodes_bin    # 5. Leader
```

Nodes read the shared settings from `nodes/.env`; anything set on the command line
overrides it. After changing Go code, rebuild with `cd nodes && go build -o nodes_bin .`
and restart all three nodes.
</details>

---

## Using the app

| Area | What you can do |
|------|-----------------|
| **Trade** | Market or limit BUY/SELL of 54 S&P 500 stocks. A limit order waits until the market reaches its price (up to 24 h) |
| **Stop loss / take profit** | Set them with the order (they are signed with it) or add levels later; several levels per position, each with its own quantity |
| **CFD** | Leveraged long or short positions (2x–100x) with live P&L; a position is liquidated automatically before its loss exceeds the margin |
| **Options** | Browse the real option chain, buy CALL/PUT contracts, sell them back or exercise them; expired options settle automatically |
| **Activity** | Every order with its status, and the reason whenever one is rejected |
| **AI** | Per-ticker news summary and Q&A (needs a free `GEMINI_API_KEY` in `backend/.env`) |

Orders go through consensus, which takes a few seconds. The status moves from
`SUBMITTED` to `CONFIRMED` or `REJECTED` on its own.

---

## Architecture

```
            Browser (single-page app)
                     │ HTTP/JSON
                     ▼
   ┌──────────── Django backend ◀──────────────┐
   │   REST API · settlement · SL/TP monitor    │ nodes re-read orders from
   │                                            │ Django with their own login
   │ gRPC                    execute_order +    │
   ▼                         ≥ 2 signed votes   │
 Oracle ◀───── gRPC ───── Node 1 (Leader) ──────┤
 (Yahoo)  ◀──┐               │  Propose/Commit  │
             │               ▼   (gRPC, HMAC)   │
             ├─────── Node 2 (Validator) ───────┤
             └─────── Node 3 (Validator) ───────┘
```

| Component | Technology | Responsibility |
|-----------|------------|----------------|
| `backend/` | Django + DRF | REST API, accounts, order lifecycle, settlement, SL/TP / margin / expiry monitor, web UI |
| `nodes/` | Go + gRPC | Consensus: the Leader proposes blocks, the Validators vote, every node keeps its own chain |
| `oracle_service/` | Python + gRPC | Live prices from Yahoo Finance (plus a *rogue* Oracle for demos) |
| `blockchain/proto/` | Protobuf | Wire contracts; `generate.sh` regenerates the Go and Python code |

```
.
├── setup.sh · run.sh                 one-time setup · start/stop everything
├── backend/
│   ├── myproject/settings.py         configuration (read from backend/.env)
│   ├── templates/index.html          the single-page web UI
│   └── trading/
│       ├── views.py                  REST API and settlement (execute_order)
│       ├── consensus.py              verification of the nodes' signed votes
│       ├── crypto_utils.py           ECDSA keys and the canonical signed order
│       ├── sltp.py                   SL/TP, CFD margin and option-expiry monitor
│       ├── models.py · serializers.py · roles.py · oracle_client.py
│       ├── management/commands/      setup and maintenance commands
│       └── tests.py                  Django test suite
├── nodes/
│   ├── main.go · config.go           entry point and configuration
│   ├── leader.go                     proposing, vote collection, crash-safe settlement
│   ├── validator.go                  proposal checks, certified commits, block sync
│   ├── blockchain.go                 the append-only, fully verified chain
│   ├── django.go · nodekey.go        Django client · Ed25519 vote signing
│   ├── clusterauth/                  HMAC authentication between nodes
│   ├── attacker/                     malicious-node demo / security regression test
│   └── *_test.go                     unit and end-to-end cluster tests
├── oracle_service/                   oracle_server.py · rogue_oracle.py · tests
├── blockchain/proto/                 consensus.proto · oracle.proto · generate.sh
└── locustfile.py                     load test
```

Each node keeps its **own** append-only chain (`nodes/chain_node1.jsonl`, …). It is fully
verified (genesis, links, every block hash) whenever a node starts, so a restarted node
resumes where it left off and a tampered chain file is refused.

---

## Order lifecycle

```
DRAFT → SUBMITTED → CONFIRMED
                 └→ REJECTED (with a reason)
```

1. **DRAFT** — the user creates an order: ticker, quantity, and optionally a limit price,
   leverage, option contract, or stop loss / take profit.
2. **SUBMITTED** — Django signs **every trade-defining field** with the user's ECDSA key.
3. **Proposal** — the Leader picks up SUBMITTED orders, gets a price from the Oracle and
   builds a block. Every node — the Leader included — then checks independently:
   - the block extends **its own** chain;
   - the order, as re-read **from Django with the node's own credentials**, is still
     `SUBMITTED`, and block, proposal and Django agree on the trade, the signature and
     the user's public key;
   - the ECDSA signature is valid for the key **stored in Django**;
   - the price quote is under 60 seconds old and the block price is within **1 %** of the
     node's own Oracle lookup.

   A node approves by signing `tradedesk-vote|<order>|<block hash>|<price>` with its
   Ed25519 key.
4. **Settlement** — with ≥ 2 signed approvals the Leader calls `execute_order`. Django
   verifies the votes of two distinct registered nodes, re-verifies the user's signature
   and the price (≤ 2 % from its own Oracle lookup), then settles under database row locks
   and records the certified block hash on the order.
5. **Commit** — the Leader appends the block and broadcasts it. A Validator appends a block
   **only if Django shows its order CONFIRMED under exactly that hash**.
6. **REJECTED** — insufficient funds, signature mismatch, price rejected, consensus not
   reached, price feed unavailable, or limit order expired. The reason is shown to the user.

No order waits forever: an unreachable Oracle is retried for 60 s, a settlement that keeps
failing for a temporary reason is rejected after 5 min, and limit orders expire after 24 h.

A background monitor runs every 15 s. It triggers SL/TP levels (never selling more than
is held), liquidates CFDs whose loss reaches 80 % of their margin, and settles expired
options at the expiry-day close. SL/TP and liquidation closes are **signed orders that go
through consensus** like any manual trade.

---

## Consensus and security

**Proof of consensus.** Every node has its own Ed25519 key (`nodes/nodekey_<name>.hex`,
created on first start, never committed) and registers the public key with Django on
first use. Django settles nothing without valid signatures from **two distinct nodes** over
the exact order, block hash and price, so one compromised node, even the Leader, cannot
move money.

**Crash-safe settlement.** Before asking Django to settle, the Leader writes a write-ahead
record. If it crashes or loses the response, the next cycle completes the commit:
`execute_order` is idempotent for the same certified block, so nothing is lost and nothing
is charged twice.

**Self-healing cluster.** A Validator that was offline answers `DESYNC`; the Leader replays
the blocks it missed. A Leader that fell behind (for example after losing its chain file)
sees `AHEAD` and copies the committed blocks from a Validator, re-certifying each with Django.

**Authenticated channel.** Node-to-node calls carry a 30-second HMAC token bound to the RPC
method; the shared secret itself never travels over the network.

| Attack | Stopped? | How |
|--------|:--------:|-----|
| A user settles their own order directly | ✅ | `execute_order` is limited to the `consensus_nodes` role |
| One node (e.g. the Leader) settles alone | ✅ | Django requires Ed25519 votes from ≥ 2 registered nodes |
| The Leader reports a manipulated price | ✅ | Every node asks the Oracle itself; > 1 % divergence is rejected |
| The Leader forges a block or signs orders with its own key | ✅ | Order, signature and public key are taken from Django, not from the Leader |
| An old signed order is replayed | ✅ | The order must still be `SUBMITTED`; nonces are unique |
| An insider injects a block with `Commit` | ✅ | Only blocks Django certified are appended |
| A stranger calls a Validator's port | ✅ | Calls without a valid HMAC token are rejected |
| A signed order is altered (database or API) | ✅ | Every field is signed and re-verified by the nodes and by Django; the API has no PUT/PATCH/DELETE |
| Committed chain history is edited on disk | ✅ | Every field is hashed; the whole chain is verified at startup |
| A malformed request crashes a Validator | ✅ | Input guards and a panic-recovery interceptor |
| Script injection via AI news or names | ✅ | Untrusted content is escaped; only http(s) links are rendered |
| A wallet balance is edited in the database | ❌ | Balances are not on the chain — Django is the source of truth |
| Someone with database **and** server access re-signs orders | ❌ | Users' keys are held by the server (custodial design) |
| The Oracle itself is compromised | ❌ | All nodes use the same price source |
| Two of the three nodes collude | ❌ | A 2-of-3 quorum tolerates one faulty node |

---

## Demonstrating consensus

Start everything with `./run.sh start`. A process can be stopped with
`kill $(cat .run/<service>.pid)`. Processes you restart by hand in these demos run in
your terminal — stop them with Ctrl+C, since `./run.sh stop` only stops what it started.

**1. Limit order.** Buy with a limit below the market price. The order stays `SUBMITTED`
(no block is proposed) and fills at market once the price reaches the limit.

**2. A node goes offline and recovers.**
```bash
kill $(cat .run/node3.pid)                       # place an order → consensus 2/3
cd nodes && NODE_NAME=node3 ./nodes_bin          # place another → node3 catches up, 3/3
```

**3. The Leader is fed a fake price.**
```bash
kill $(cat .run/node1.pid)
cd oracle_service && python3 rogue_oracle.py &                       # fake Oracle on 8002
cd nodes && ORACLE_URL=127.0.0.1:8002 NODE_NAME=node1 IS_LEADER=true ./nodes_bin
```
The Validators still use the honest Oracle, see `price divergence … > 1%` and reject: 1/3.

**4. Database tampering.** Stop the Leader, place an order, then change it:
```python
# cd backend && python3 manage.py shell
from trading.models import Order
o = Order.objects.filter(status='SUBMITTED').latest('id')
o.quantity = 1000
o.save()
```
Restart the Leader: every node reports `ECDSA verification failed`, 0/3, and the order is rejected.

**5. A malicious node.** `nodes/attacker` signs a made-up order with its own key and tries
to get a forged block (1000 shares at $1) approved with `Propose` and appended with `Commit`:
```bash
cd nodes
go run ./attacker                                                            # outsider
CLUSTER_SECRET=$(grep ^CLUSTER_SECRET .env | cut -d= -f2) go run ./attacker  # insider
```
Every attempt is refused. The tool reports a regression if one ever succeeds.

---

## Testing

```bash
cd backend && python3 manage.py test trading      # Django — 114 tests
cd nodes && go test ./...                         # Go — unit + end-to-end cluster tests
cd oracle_service && python3 -m unittest          # Oracle
```

All suites run automatically on every push (GitHub Actions, badge at the top).

* **Django** — the signed payload, registration, deposits, order validation and
  immutability, execution and balances, resting limit orders, the consensus proof
  (quorum, forged, duplicate and foreign votes, idempotent retries), node roles and keys,
  price re-verification, CFDs and liquidation, option pricing and expiry, SL/TP, input
  validation and rate limits. A shared fixture checks that a vote signed in Go verifies in Python.
* **Go** — chain persistence and tamper detection, certified commits, desync recovery, every
  rejection path of a proposal, crash guards, cluster authentication, and end-to-end runs of a
  Leader with two Validators over real gRPC: normal settlement, missing quorum, lost
  responses, a Leader recovering a lost chain, and limit orders.
* **Oracle** — symbol mapping, market time, caching, incomplete data.

> Unauthenticated API requests get `403`, not `401`: `SilentBasicAuthentication` omits the
> `WWW-Authenticate` header so the browser never shows its own login dialog.

---

## Configuration

### Django — `backend/.env`

| Variable | Default | Description |
|----------|---------|-------------|
| `SECRET_KEY` | *(generated by setup.sh)* | Required outside debug/test runs |
| `DEBUG` | `False` (`True` in the generated file) | Debug mode for local development |
| `ALLOWED_HOSTS` | `127.0.0.1,localhost` | Allowed host names |
| `ORACLE_URL` | `127.0.0.1:8001` | Oracle address |
| `GEMINI_API_KEY` | *(empty)* | Enables AI news and chat — free key at [aistudio.google.com/app/apikey](https://aistudio.google.com/app/apikey) |
| `THROTTLE_REGISTER` · `THROTTLE_AI` | `20/hour` · `120/hour` | Rate limits for sign-up (per address) and AI (per user) |
| `SLTP_MONITOR` | `auto` | `external` when the monitor runs as `manage.py run_sltp_monitor` |

### Nodes — `nodes/.env`

| Variable | Default | Description |
|----------|---------|-------------|
| `CLUSTER_SECRET` | *(generated, ≥ 16 chars)* | Shared node-to-node secret, identical on all nodes |
| `NODE1_PASS` … `NODE3_PASS` | *(generated)* | Each node's Django password (also used by `create_node_users`) |
| `NODE_NAME` · `IS_LEADER` | `node1` · `false` | Set per node on the command line |
| `LISTEN_PORT` | 9002 / 9003 | Validator port (from the node name) |
| `DJANGO_URL` · `ORACLE_URL` | `http://127.0.0.1:8000/api` · `127.0.0.1:8001` | Service addresses |
| `VALIDATOR_ADDRESSES` | `localhost:9002,localhost:9003` | Validators the Leader contacts |
| `LIMIT_ORDER_TTL` | `24h` | How long a limit order waits for its price |

### Oracle

| Variable | Default | Description |
|----------|---------|-------------|
| `ORACLE_LISTEN` | `127.0.0.1:8001` | Bind address |
| `ORACLE_CACHE_S` | `3` | Seconds a quote is reused, so all nodes see the same price |
| `ROGUE_PRICE` · `ROGUE_PORT` | `1.00` · `8002` | Settings of the demo `rogue_oracle.py` |

---

## Management commands

Run from `backend/`:

| Command | Description |
|---------|-------------|
| `python3 manage.py migrate` | Apply database migrations |
| `python3 manage.py seed_stocks` | Load the 54 S&P 500 tickers |
| `python3 manage.py create_node_users` | Create/sync the three node accounts from `nodes/.env` |
| `python3 manage.py run_sltp_monitor` | Run the SL/TP, margin and expiry monitor as its own process |
| `python3 manage.py create_test_users --count 1000` | Create test users for load testing |
| `python3 manage.py createsuperuser` | Create an admin for `/admin` |

All of them are safe to run more than once.

---

## API reference

All endpoints use HTTP Basic Auth except `POST /api/register/`.

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/api/register/` | Create an account ($10,000 wallet, ECDSA keys) — rate-limited |
| `GET` | `/api/portfolio/` | Balance and holdings |
| `POST` | `/api/deposit/` | Add funds ($0.01 – $1,000,000 per deposit) |
| `GET` | `/api/orders/` | Your orders (nodes see all). `?status=` filter, `?limit=N` newest N (max 500) |
| `POST` | `/api/orders/` | Create a DRAFT order (optional `limit_price`, `leverage`, option fields, `stop_loss`/`take_profit` and quantities) |
| `POST` | `/api/orders/{id}/submit/` | Sign and submit for consensus |
| `POST` | `/api/orders/{id}/execute_order/` | Settle — nodes only, needs `block_hash` and ≥ 2 signed `votes` |
| `POST` | `/api/orders/{id}/reject_order/` | Report failed consensus — nodes only |
| `POST` | `/api/node-key/` | Register a node's vote-signing key — nodes only |
| `GET` | `/api/price/{ticker}/` | Live price (with `market_time`) |
| `GET` | `/api/history/{ticker}/` | Price history (`period`, `interval`) |
| `GET` | `/api/cfd/` | CFD positions with P&L and liquidation price |
| `GET` | `/api/options/` · `/api/options/chain/{ticker}/` | Option positions · option chain |
| `GET` `POST` | `/api/sltp/` | List (`?position=` / `?cfd=`) or create SL/TP levels |
| `DELETE` | `/api/sltp/{id}/` | Delete an untriggered level |
| `GET` | `/api/ai-news/{ticker}/` | AI news summary — rate-limited |
| `POST` | `/api/ai-chat/{ticker}/` | Ask about a ticker — rate-limited |

Orders cannot be edited or deleted (`PUT`/`PATCH`/`DELETE` → 405). Market-data and AI
endpoints accept only listed tickers.

---

## Load testing

```bash
pip3 install locust
cd backend && python3 manage.py create_test_users --count 1000
cd .. && ./run.sh start
locust -f locustfile.py --host http://127.0.0.1:8000     # then open http://localhost:8089
```

Use 1000 users with a spawn rate of 50. The scenario mixes portfolio reads, stock buys and
sells, and CFD opens and closes.

---

## Troubleshooting

| Symptom | Fix |
|---------|-----|
| `port … is already in use` | Another program uses the port — stop it, or `DJANGO_PORT=8080 ./run.sh start` |
| Orders are rejected with `price feed unavailable` or `unusable price` | No market data (offline, or Yahoo has none right now) — use `ORACLE_MODE=fixed ./run.sh start` |
| A node exits with `chain file is corrupt or … incompatible` | Stop all nodes and delete `nodes/chain_*.jsonl` (e.g. after upgrading from an older version) |
| A node exits with `(409) … different key is already registered` | Its `nodes/nodekey_<name>.hex` was lost — restore it, or delete the node key in `/admin` |
| `SECRET_KEY is not set` | Run `bash setup.sh`, or add a `SECRET_KEY` to `backend/.env` |
| AI news says the key is missing | Add `GEMINI_API_KEY` to `backend/.env` and restart |
| Something else | `./run.sh logs` shows every service's output |

---

## Design decisions and limitations

* **Django is the source of truth for money.** The chain records every trade and
  consensus controls what may settle, but balances and holdings live in Django's database.
* **Custodial keys.** Users' ECDSA keys are generated and stored by the server, which keeps
  the web app simple; client-side keys (WebCrypto) would make signatures prove intent even
  against a fully compromised server.
* **One price source.** All nodes query the same Oracle. Independent feeds per node would
  protect against a compromised Oracle.
* **Option premiums** come from Yahoo and are not voted on, but they are floored at the
  intrinsic value derived from the consensus-verified stock price.
* **Development defaults.** SQLite (in immediate-transaction mode), Django's development
  server and plaintext gRPC on localhost. A production deployment would use PostgreSQL,
  a WSGI server with `run_sltp_monitor`, and TLS between nodes.
