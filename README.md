# TradeDesk — Consensus-Backed Trading Platform

[![tests](https://github.com/NadavWizman/Final-Project/actions/workflows/tests.yml/badge.svg)](https://github.com/NadavWizman/Final-Project/actions/workflows/tests.yml)

A stock, CFD and options trading platform where **no single server can move money, set a
price or change who runs the network**.

- Every transaction is signed in the **TradeDesk Wallet**, a browser extension that keeps
  the key non-extractable and shows each transaction's details before you approve it.
  The web server only forwards what the wallet signed.
- Transactions are ordered and settled by **4 CometBFT validators**, which tolerate one
  faulty or malicious validator.
- Prices are the median of **at least 3 of 5 independently signed price sources**, so one
  lying source cannot move a price and one or two sources down do not stop trading.
- Money enters only through a **custodian's signature** registered in genesis.
- Stop-loss, take-profit, liquidation, limit orders and option expiry run **inside the
  replicated state machine**.

**Stack:** Go · CometBFT 0.38 (ABCI++) · Python gRPC price signers (Ed25519) · Django gateway · Chrome MV3 wallet extension (WebCrypto P-256)

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

**Prerequisites:** Python 3.11+, Go 1.26+ (macOS or Linux), and Chrome or Chromium for the wallet.

```bash
git clone https://github.com/NadavWizman/Final-Project.git
cd Final-Project
bash setup.sh          # once: Python deps, backend/.env, node binary, a 4-validator testnet
./run.sh start         # 5 price sources, 4 validators, the gateway
```

**Install the wallet once:** open `chrome://extensions`, turn on *Developer mode*, choose
*Load unpacked* and select the `wallet/` folder. Open the wallet from the toolbar →
**New account**, and write down the recovery code it shows.

Open **http://127.0.0.1:8000**, pick your wallet account and a username. The wallet asks
you to approve the registration, and you get $10,000. Stop everything with `./run.sh stop`.

| Command | What it does |
|---------|--------------|
| `./run.sh start` | Starts the price sources (yahoo, nasdaq, cnbc, tradingview, google), node0–node3 and the gateway |
| `./run.sh status` | Shows which services are running |
| `./run.sh logs [service]` | Follows a log (`yahoo` … `google`, `node0` … `node3`, `gateway`) |
| `./run.sh deposit <user> <amount>` | Credits an account as the custodian (signs with `testnet/custody.key`) |
| `./run.sh stop` | Stops everything |
| `DJANGO_PORT=8080 ./run.sh start` | Uses another web port |
| `ORACLE_MODE=fixed ./run.sh start` | **Offline demo:** every source signs a fixed price (`FIXED_PRICE`, default $190) |

`setup.sh` writes the network into `./testnet`:
- each validator's keys and config;
- one shared genesis file;
- a signing key for each price source;
- the custodian's key.

In the demo all of these keys are made on one machine. In a real deployment each one
would be held by a different operator. To start over from block 0, run `./run.sh stop`,
delete `testnet/` and run `bash setup.sh` again.

---

## Using the app

| Area | What you can do |
|------|-----------------|
| **Wallet** | Keys live in the TradeDesk Wallet extension, are non-extractable, and are backed up as a recovery code. Every transaction opens the wallet's approval window, which shows what will be signed |
| **Trade** | Market or limit BUY/SELL of S&P 500 stocks. A limit order rests on the chain until the verified price reaches it (up to 24 h) |
| **Stop loss / take profit** | Attach levels to an order or add them later. They fire inside the chain in the first block whose verified price crosses them |
| **CFD** | Long or short at 2x–20x leverage. Liquidated when the loss reaches 80 % of the margin. A loss past the margin (a price gap) is a debt, see [Trust model](#trust-model) |
| **Options** | CALL/PUT on the model premium. Sell back, exercise, or let them settle automatically at expiry |
| **Deposits** | Only the custodian can credit an account (`./run.sh deposit` in the demo) |
| **Activity** | Every transaction, including automatic events (SL, TP, liquidation, expiry, deposits), and the reason for any refusal |
| **AI** | News summary and Q&A per ticker (Gemini, `gemini-2.5-flash-lite`; needs `GEMINI_API_KEY`). Shown with a "not investment advice" notice |

---

## Architecture

```
 TradeDesk Wallet (extension: non-extractable P-256 key, approval window)
    ▲ "sign this text?"            │ signature, only after the user approves
    │                              ▼
 Browser page (served by the gateway; holds no key)
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
                 │ gRPC SignQuote (proposer, per block); a price needs 3 of 5
   yahoo :8001  nasdaq :8002  cnbc :8003  tradingview :8004  google :8005
```

| Component | Path | Role |
|-----------|------|------|
| Wallet | `wallet/` | Chrome MV3 extension: keys, recovery code, the approval window, signing |
| Ledger | `nodes/ledger` | Deterministic state machine: accounts, orders, CFDs, options, levels, custodian deposits. Integer arithmetic only, with overflow checks |
| Quotes | `nodes/quotes` | Signed quote format, verification, quorum and lower median |
| ABCI app | `nodes/app` | `PrepareProposal` / `ProcessProposal` / `FinalizeBlock`, queries, validator-set changes, state persistence |
| Node | `nodes/chainnode`, `nodes/cmd/tradedesk-node` | Embeds CometBFT. `init` creates the testnet; `start` runs a validator; `deposit` is the custodian's tool |
| Price signers | `oracle_service/oracle_server.py` | One process per source; signs `tradedesk-quote\|source\|ticker\|price_cents\|unix` |
| Gateway | `backend/gateway` | Read-only REST over the nodes. Also serves the page, charts, option chains and the AI endpoints |
| UI | `backend/templates/index.html` | Single page; asks the wallet to sign |

---

## Life of an order

1. **Approve.** The page builds `msg = {type, order, chain, from, nonce}` and asks the wallet
   to sign it. The wallet parses those exact bytes and shows ticker, side, quantity, price,
   leverage and the requesting site in its own window. It signs, with ECDSA P-256 in low-S
   form, only when you approve.
2. **Forward.** `POST /api/tx/` sends `{msg, sig}` to a node. `CheckTx` verifies the
   signature, the chain id, the next nonce and the field formats. The tx then enters the
   shared mempool.
3. **Propose.** The validator whose turn it is collects the txs, asks the five sources for
   signed quotes, and adds the quotes and their median. It stops waiting once each ticker
   has 3 quotes.
4. **Verify.** In `ProcessProposal` every validator:
   - checks each quote signature and its age (10 s at most);
   - requires 3 sources per priced ticker;
   - recomputes the median;
   - replays the block.

   Anything wrong means a vote against the block. A market order without a price in the
   block is not part of the block, so it waits in the mempool.
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
| Gateway / the page it serves | Show wrong information, or stop working (refuse to forward) | Sign anything: the key is in the wallet, which shows the real details from the signed bytes and needs your approval |
| One validator | Go offline, propose badly | Insert unsigned txs, shift a price, burn orders by omitting prices, add validators |
| One price source | Lie, or go offline (up to two may be offline) | Move the median outside the honest quotes |
| Custodian (money bridge) | Credit accounts: the one trusted point, stated explicitly | — |

**Assumptions:**
- at most 1 of 4 validators is dishonest;
- at most 1 of the 5 price sources lies, and at least 3 respond.

**Network membership:**
- The validator set, the price sources' keys and the custodian's key are fixed in genesis.
- Changing the validator set needs Ed25519 approvals from more than 2/3 of the voting
  power (3 of 4).

**Money:**
- The registration grant is capped in total by genesis.
- Every other dollar comes from a custodian-signed deposit. Each deposit carries a
  sequence number, so it can be applied only once.

**Losses past the margin:**
- Leverage is capped at 20× in genesis. Liquidation fires at a 4 % move, so a loss can pass the margin only on a gap of more than 5 % between priced blocks.
- Such a loss becomes a debt (a negative balance). Every later credit repays it first, and nothing new can be opened while the balance is negative.
- Nothing is forgiven. Debt left in an abandoned account is the house's loss and is reported in `/status` (`outstanding_debt`).

Full chapter: [docs/report/trust-model.md](docs/report/trust-model.md).

---

## Acceptance scenarios and tests

All fifteen scenarios run automatically in CI:
- 1–12, 14 and 15 against a real in-process 4-node network (`nodes/e2e`);
- 13 against the real wallet extension in headless Chromium (`wallet/test`).

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
| 13 | A compromised gateway page shows "1 share" and asks the wallet to sign 50 | The wallet shows 50; without approval there is no signature. An unconnected site sees no accounts and cannot ask |
| 14 | Price sources down | With 1 or 2 of 5 down, trading continues; with 3 down, a market order waits (nonce untouched) and executes when a source returns |
| 15 | The whole network stops for longer than a quote may be ahead of the block time | Blocks resume by themselves and a trade settles |

```bash
cd nodes && go test -race -cover ./ledger ./quotes ./app   # unit tests + coverage
cd nodes && go test -v -timeout 25m ./e2e                  # scenarios 1–12, 14, 15
cd wallet && npm install && npx playwright-core install chromium
cd wallet && node --test core.test.mjs && node test/scenario13.mjs
cd backend && python3 manage.py test gateway                # gateway
cd oracle_service && python3 -m unittest -v                 # price signers
```

Coverage: `nodes/ledger` 86 %, `nodes/quotes` 98 %, `nodes/app` 81 % (with the acceptance tests).

---

## Demonstrating the defences

With the network running, run `cd nodes && go run ./attacker`. The tool plays a
compromised gateway, and each of these attempts must be refused:
- signing in a victim's name;
- altering a signed order;
- replaying a transaction;
- injecting forged quotes;
- adding a validator with one signature;
- crediting oneself (a user-signed deposit);
- a deposit signed by a key other than the custodian's.

The tool prints each verdict and exits non-zero if any attempt is accepted.

- **Rogue price source:** run `python3 oracle_service/rogue_oracle.py` in place of the
  cnbc signer (stop that process first). The median stays on the honest sources.
- **Source down:** stop one or two `oracle_server.py` processes. Trading continues.
- **Node down:** `kill $(cat .run/node3.pid)`. Trading continues.

---

## Performance

Measured with `locustfile.py`: every simulated user is a wallet that signs its own
transactions. Each load level ran 60 s, with everything on one machine. Details and the
graphs are in [docs/report/performance.md](docs/report/performance.md).

| Concurrent users | Median to finality | P95 | Settled tx/s |
|---|---|---|---|
| 5 | 2.2 s | 2.8 s | 1.1 |
| 50 | 2.0 s | 3.0 s | 10.5 |
| 200 | 2.5 s | 3.7 s | 38.6 |
| 800 (highest throughput) | 2.3 s | 3.1 s | **141** |
| 1,600 (past saturation) | 2.7 s | 8.3 s | 63 |
| Previous architecture, 5 / 50 / 200 users | ≥ 23 / ≥ 31 / ≥ 29 s | | 0.50 / 0.12 / 0.017 |

Finality stays around 2 s up to 800 users. Past that, the limit is the Django development
server (one process) and the load generator on the same machine, not consensus: a node
stays under 50 % CPU.

With one node down, trading continued with no failures; the P95 rose to about 7 s,
because the dead node's proposer turns time out and the next validator takes over.

```bash
pip3 install locust
locust -f locustfile.py --host http://127.0.0.1:8000 --headless -u 50 -r 10 -t 60s --csv results
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
| `NUM_PROXIES` | `0` | Reverse proxies whose `X-Forwarded-For` is trusted |

Chain parameters live in `testnet/node*/config/genesis.json`:
- tickers;
- signup grant and its total cap;
- the custodian's key and the per-deposit cap;
- leverage range and liquidation level;
- limit-order lifetime;
- option volatility and expiry hour;
- the price quorum.

They are the same on every node and part of the consensus state.

---

## Gateway API

All endpoints are under `/api/`. Reads take `?address=<40 hex>`. The gateway has no
sessions.

| Method | Path | Purpose |
|--------|------|---------|
| GET | `status/` | Height, chain id, state root, validators, sources, custody sequence |
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
wallet/           the TradeDesk Wallet extension, its unit tests and scenario 13
nodes/            Go: ledger, quotes, app (ABCI), chainnode, cmd/tradedesk-node, e2e, attacker
oracle_service/   price signers (yahoo / nasdaq / cnbc / tradingview / google / fixed) and the rogue source
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
| "The TradeDesk Wallet extension is needed" | Load `wallet/` as an unpacked extension (see Quick start) and reload the page |
| `port … is already in use` | Another program holds it. Use `DJANGO_PORT=…`, or stop it |
| Orders stay pending | Fewer than 3 price sources answer for that ticker; check `./run.sh logs yahoo` etc., or use `ORACLE_MODE=fixed` |
| "nonce" refusal | The UI re-reads the counter from the chain and retries once. If it persists (two tabs signing at once), reload the page |
| A node logs `saved state rejected; rebuilding it from the blocks` | Its saved state was edited; it was set aside and is being rebuilt from the blocks, no action needed |
| AI says it is not configured | Set `GEMINI_API_KEY` in `backend/.env` and restart |
