# TradeDesk — Consensus-Backed Trading Platform

[![tests](https://github.com/NadavWizman/Final-Project/actions/workflows/tests.yml/badge.svg)](https://github.com/NadavWizman/Final-Project/actions/workflows/tests.yml)

A stock, CFD and options trading platform where **no single server can move money, set a
price or change who runs the network**.

- Every order is signed **in the user's browser** with a key that never leaves it.
- Orders are ordered and settled by **4 CometBFT validators**. The network tolerates one
  faulty or malicious validator.
- Prices are the **exact median of three independently signed price sources**.
- Stop-loss, take-profit, liquidation, limit orders and option expiry all run **inside
  the replicated state machine**.
- The web server is a read-only gateway with no database.

**Stack:** Go · CometBFT 0.38 (ABCI++) · Python gRPC price signers (Ed25519) · Django gateway · single-page UI with WebCrypto (P-256)

---

## Contents

1. [Quick start](#quick-start)
2. [Using the app](#using-the-app)
3. [Architecture](#architecture)
4. [Life of an order](#life-of-an-order)
5. [Trust model](#trust-model)
6. [Acceptance scenarios and tests](#acceptance-scenarios-and-tests)
7. [Demonstrating the defences](#demonstrating-the-defences)
8. [Performance](#performance)
9. [Configuration](#configuration)
10. [Gateway API](#gateway-api)
11. [Project layout](#project-layout)
12. [Troubleshooting](#troubleshooting)

Report chapters: [trust model](docs/report/trust-model.md) ·
[performance](docs/report/performance.md) · [related work](docs/report/related-work.md)

---

## Quick start

**Prerequisites:** Python 3.11+ and Go 1.26+ (macOS or Linux).

```bash
git clone https://github.com/NadavWizman/Final-Project.git
cd Final-Project
bash setup.sh          # once: Python deps, backend/.env, node binary, a 4-validator testnet
./run.sh start         # 3 price signers, 4 validators, the gateway
```

Open **http://127.0.0.1:8000**, choose a username (your key is created in the browser,
and you get $10,000) and trade. Stop everything with `./run.sh stop`.

| Command | What it does |
|---------|--------------|
| `./run.sh start` | Starts the price signers (yahoo, nasdaq, cnbc), node0–node3 and the gateway, waiting until each is up |
| `./run.sh status` | Shows which services are running |
| `./run.sh logs [service]` | Follows a log (`yahoo`, `nasdaq`, `cnbc`, `node0`…`node3`, `gateway`) |
| `./run.sh stop` | Stops everything |
| `DJANGO_PORT=8080 ./run.sh start` | Uses another web port |
| `ORACLE_MODE=fixed ./run.sh start` | **Offline demo:** all three sources sign a fixed price (`FIXED_PRICE`, default $190) |

`setup.sh` writes the network into `./testnet`: each validator's keys and config, one
shared genesis file, and a signing key for each of the three price sources. To start
over from block 0, run `./run.sh stop`, delete `testnet/` and run `bash setup.sh` again.

---

## Using the app

| Area | What you can do |
|------|-----------------|
| **Keys** | Your P-256 key is created and kept in this browser (IndexedDB). The 🔑 button exports a backup; "restore from key backup" imports it on another browser |
| **Trade** | Market or limit BUY/SELL of S&P 500 stocks. A limit order rests on the chain until the verified price reaches it (up to 24 h) |
| **Stop loss / take profit** | Attach levels to an order or add them later. They fire inside the chain in the first block whose verified price crosses them |
| **CFD** | Long or short at 2x–100x leverage. Liquidated automatically before the loss exceeds the margin |
| **Options** | CALL/PUT on the model premium. Sell back, exercise, or let them settle automatically at expiry |
| **Activity** | Every transaction, including automatic events (SL, TP, liquidation, expiry), and the reason for any refusal |
| **AI** | News summary and Q&A per ticker (Gemini, `gemini-2.5-flash-lite`; needs `GEMINI_API_KEY`). Shown with a "not investment advice" notice |

A transaction is final in about 2 seconds, when its block is committed.

---

## Architecture

```
 Browser (WebCrypto P-256 key)
    │  signed tx {msg, sig}
    ▼
 Django gateway  ── no DB, no users; forwards txs, reads state ──┐
    │ JSON-RPC (failover across nodes)                           │
    ▼                                                            │
 ┌──────────── CometBFT, 4 validators, f = 1 ──────────────┐      │
 │ node0   node1   node2   node3                           │◄─────┘ ABCI Query
 │  shared mempool · rotating proposer · commit = >2/3     │
 │  each node runs the same deterministic state machine:   │
 │  ledger (integer cents) · rules (SL/TP, liquidation,    │
 │  limits, expiry) · Merkle state root → app_hash         │
 └───────────────▲─────────────────────────────────────────┘
                 │ gRPC SignQuote (proposer, per block)
   yahoo :8001   nasdaq :8002   cnbc :8003   ← each signs with its own Ed25519 key
```

| Component | Path | Role |
|-----------|------|------|
| Ledger | `nodes/ledger` | Deterministic state machine: accounts, orders, CFDs, options, levels. Integer arithmetic only, with overflow checks |
| Quotes | `nodes/quotes` | Signed quote format, verification, exact median |
| ABCI app | `nodes/app` | `PrepareProposal` / `ProcessProposal` / `FinalizeBlock`, queries, validator-set changes, state persistence |
| Node | `nodes/chainnode`, `nodes/cmd/tradedesk-node` | Embeds CometBFT. `init` creates the testnet; `start` runs a validator |
| Price signers | `oracle_service/oracle_server.py` | One process per source; signs `tradedesk-quote\|source\|ticker\|price_cents\|unix` |
| Gateway | `backend/gateway` | Read-only REST over the nodes. Also serves charts, option chains and the AI endpoints |
| UI | `backend/templates/index.html` | Single page; signs every transaction itself |

---

## Life of an order

1. **Sign.** The browser builds `msg = {type, order, chain, from, nonce}` and signs the
   exact bytes with ECDSA P-256. The address is `sha256(pubkey)[:40]`.
2. **Forward.** `POST /api/tx/` sends `{msg, sig}` to a node. `CheckTx` verifies the
   signature, the chain id and the next nonce, then the tx enters the shared mempool.
3. **Propose.** The validator whose turn it is collects the txs, asks the three sources
   for signed quotes of the tickers involved, and adds the quotes and their median.
4. **Verify.** In `ProcessProposal` every validator checks each quote signature and its
   age, recomputes the median, and replays the block. Anything wrong means a vote against
   the block.
5. **Commit.** With >2/3 precommits the block is final. `FinalizeBlock` applies the txs,
   then the rules (fills resting limits, fires SL/TP, liquidates, expires options), and
   commits the new state root into `app_hash`.
6. **Show.** The UI polls `/api/tx/<hash>/` until it is committed, then reads the
   account through the gateway.

---

## Trust model

| Who | Can | Cannot |
|-----|-----|--------|
| User | Act on their own account | Act on another account or replay a tx (strict per-account nonce) |
| Gateway | Refuse to forward (censor); a user then uses another node. It also serves the page that holds the keys: a compromised gateway serving malicious JavaScript could steal them (mitigated by a strict CSP; see the trust model) | Forge, alter or invent a transaction that passes through it; it holds no keys and no state |
| One validator | Go offline, propose badly | Insert unsigned txs, shift a price, add validators; honest validators reject its block |
| One price source | Lie or go offline | Move the median; while it is offline, price-dependent txs wait |
| Money bridge | In the demo: a $10,000 signup grant and a self-service `deposit` faucet (up to $1M per tx) | — the one trusted point, stated explicitly; a real bridge would require a custodian's signature |

**Assumptions:** at most 1 of 4 validators and at most 1 of 3 price sources is dishonest.
The validator set is fixed in genesis; changing it needs Ed25519 approvals from more than
2/3 of the voting power (3 of 4), with a sequence number, and never drops below 4. There is
no shared secret (HMAC) and no trust-on-first-use. Full chapter:
[docs/report/trust-model.md](docs/report/trust-model.md).

---

## Acceptance scenarios and tests

All twelve scenarios run automatically in CI against a real in-process 4-node network
(`nodes/e2e`, about 2–3 minutes):

| # | Scenario | Expected |
|---|----------|----------|
| 1 | A proposer shifts a price by 0.5 % | Block rejected; the next proposer settles at the true median |
| 2 | Two different blocks at one height | Each validator's signer refuses to double-sign |
| 3 | A proposer censors an order | The next proposer includes it from the shared mempool |
| 4 | The proposer goes down | After the timeout the turn passes; blocks continue |
| 5 | One node down | The other three keep closing blocks and settling trades |
| 6 | The gateway signs for a user | Refused (signature is not the account's key) |
| 7 | The gateway alters a signed order | Refused (signature over the exact bytes) |
| 8 | A signed order is replayed | Refused by the mempool, and by validators if a proposer forces it into a block |
| 9 | One price source lies ($1.00) | The median ignores it |
| 10 | A quote is altered in transit | Signature fails; block rejected |
| 11 | A node's saved state file is edited | Detected on restart, set aside and rebuilt from the blocks |
| 12 | A stop-loss level is crossed | Fires inside the chain in the first block with that verified price |

```bash
cd nodes && go test ./ledger ./quotes ./app       # unit tests
cd nodes && go test -v -timeout 20m ./e2e         # the 12 scenarios
cd backend && python3 manage.py test gateway       # gateway
cd oracle_service && python3 -m unittest -v        # price signers
```

---

## Demonstrating the defences

With the network running:

```bash
cd nodes && go run ./attacker
```

The tool plays a compromised gateway. It tries to:

- sign in a victim's name;
- alter a signed order;
- replay a transaction;
- inject forged quotes;
- add a validator with a single signature.

Each attempt is printed with the network's answer. It exits non-zero if any attempt is
accepted.

**Rogue price source:** run `python3 oracle_service/rogue_oracle.py` in place of the cnbc
signer, and the median stays on the two honest sources.

**Node down:** kill one node (`kill $(cat .run/node3.pid)`). Trading continues. Restart
it with `./run.sh stop && ./run.sh start`, and it catches up to the same `app_hash`.

---

## Performance

Measured with `locustfile.py` (50 concurrent signing wallets, 90 s, everything on one
machine). Details: [docs/report/performance.md](docs/report/performance.md).

| | Median to finality | P95 | Settled tx/s |
|---|---|---|---|
| 4 nodes up | 2.0 s | ~4 s | ~10.5 |
| 1 node down (11 proposer turns timed out → leader replaced) | 2.5 s | ~7 s | ~8.4 |
| Previous architecture (single leader, Django DB), same load | 104 s | 167 s | ~0.38 |

```bash
pip3 install locust
locust -f locustfile.py --host http://127.0.0.1:8000 --headless -u 50 -r 10 -t 90s --csv results
```

The gateway limits transactions per IP (`THROTTLE_TX`, default 600/minute). Locust runs
every user from one IP, so raise the limit for load tests.

---

## Configuration

`backend/.env` (created by `setup.sh` from `backend/.env.example`):

| Variable | Default | Meaning |
|----------|---------|---------|
| `SECRET_KEY` | random | Django secret |
| `NODE_RPC` | the 4 local nodes | Node RPC URLs, tried in order |
| `ORACLE_DISPLAY_URL` | `127.0.0.1:8001` | Source used only for display prices before the chain has one |
| `GEMINI_API_KEY` | empty | Enables the AI features |
| `GEMINI_MODEL` | `gemini-2.5-flash-lite` | Pinned Gemini model |
| `THROTTLE_TX` / `THROTTLE_AI` | `600/minute` / `120/hour` | Per-IP limits |

Chain parameters (tickers, signup grant, leverage range, liquidation level, limit-order
lifetime, option volatility and expiry hour) live in `testnet/node*/config/genesis.json`.
They are the same on every node and part of the consensus state.

---

## Gateway API

All endpoints are under `/api/`. Reads take `?address=<40 hex>`. The gateway has no
sessions.

| Method | Path | Purpose |
|--------|------|---------|
| GET | `status/` | Height, chain id, state root, validators, sources |
| POST | `tx/` | Forward a signed `{msg, sig[, pubkey]}`; returns the tx hash |
| GET | `tx/<hash>/` | `pending` / `committed`, with the result |
| GET | `username/<name>/` | Address registered under a username |
| GET | `portfolio/`, `orders/`, `cfd/`, `options/`, `sltp/` | Account views from the chain state |
| GET | `price/<ticker>/` | Last verified price |
| GET | `option-quote/` | Model premium for a ticker/type/strike/expiry |
| GET | `history/<ticker>/`, `options/chain/<ticker>/` | Market data for display |
| POST | `ai-news/<ticker>/`, `ai-chat/<ticker>/` | Gemini analysis (not investment advice) |

---

## Project layout

```
nodes/            Go: ledger, quotes, app (ABCI), chainnode, cmd/tradedesk-node, e2e, attacker
oracle_service/   price signers (yahoo / nasdaq / cnbc / fixed) and the rogue source
backend/          Django gateway (backend/gateway) and the single-page UI
blockchain/proto/ oracle.proto (SignQuote)
docs/report/      trust model, performance, related work
setup.sh run.sh   one-time setup and service manager
locustfile.py     load test
```

---

## Troubleshooting

| Symptom | Fix |
|---------|-----|
| `port … is already in use` | Another program holds it. Use `DJANGO_PORT=…`, or stop it |
| Orders stay pending | A price source is down or has no data; check `./run.sh logs yahoo`, or use `ORACLE_MODE=fixed` |
| "nonce" refusal | The UI re-reads the counter from the chain and retries once. If it persists (the same key used in two tabs at once), reload the page |
| A node logs `saved state rejected; rebuilding it from the blocks` | Its saved state was edited; it was set aside and is being rebuilt from the blocks, no action needed |
| AI says it is not configured | Set `GEMINI_API_KEY` in `backend/.env` and restart |
