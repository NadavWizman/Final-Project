# Decentralized Trading System — Architecture

## 1. High-level picture

```
                  ┌──────────────────────┐
                  │  User (HTTP client)  │
                  └──────────┬───────────┘
                             │ REST/JSON
                             ▼
             ┌────────────────────────────────┐
             │   Django API Gateway           │
             │   - Auth & user mgmt           │
             │   - Wallets / portfolio view   │
             │   - Order lifecycle (Draft →   │
             │     Submitted → Confirmed/     │
             │     Rejected)                  │
             │   - ECDSA signer               │
             └──────────────┬─────────────────┘
                            │ gRPC (SubmitOrder)
                            ▼
         ┌──────────────────────────────────────┐
         │  Leader Node (Go)  node-1 :9001      │
         │  - Validates tx                      │
         │  - Proposes block                    │
         │  - Collects votes (PoA 2/3)          │
         └───┬─────────────┬─────────────┬──────┘
             │ gRPC Vote   │ gRPC Vote   │ gRPC Commit broadcast
             ▼             ▼             ▼
      ┌────────────┐  ┌────────────┐
      │ Validator  │  │ Validator  │
      │ node-2     │  │ node-3     │
      │ :9002      │  │ :9003      │
      └─────┬──────┘  └──────┬─────┘
            │                │
            └────────┬───────┘
                     │  pull prices
                     ▼
           ┌──────────────────────┐
           │ Oracle (Python)      │
           │ Yahoo Finance → gRPC │
           │ push to all nodes    │
           └──────────────────────┘
```

## 2. Services

| Service       | Language        | Port   | Responsibility                                                      |
|---------------|-----------------|--------|---------------------------------------------------------------------|
| django-api    | Python / Django | 8000   | REST gateway, user/wallet/order persistence, ECDSA order signing    |
| node-1 (Leader)    | Go         | 9001   | Validates txs, proposes blocks, orchestrates PoA round              |
| node-2 (Validator) | Go         | 9002   | Verifies proposals, votes, applies committed blocks                 |
| node-3 (Validator) | Go         | 9003   | Verifies proposals, votes, applies committed blocks                 |
| oracle        | Python          | 7000   | Fetches S&P 500 prices on a schedule and pushes them (signed + ts)  |

## 3. Order lifecycle

1. **Draft** — user calls `POST /api/orders/`. Django validates the symbol is in the S&P 500 whitelist and that the user has sufficient funds/shares. Status = `DRAFT`.
2. **Submitted** — user calls `POST /api/orders/{id}/submit/`. Django:
   - Increments the user's nonce counter.
   - Builds a canonical `OrderTx` message.
   - Signs it with the user's ECDSA private key (stored encrypted server-side for the MVP).
   - Sends `SubmitOrder` over gRPC to the Leader.
   - Status = `SUBMITTED`.
3. **Consensus** — Leader runs the PoA round:
   - Verifies signature + nonce + oracle freshness + balances.
   - Packs the tx into a candidate block, broadcasts `ProposeBlock` to validators.
   - Each validator independently re-runs the same validation and returns `Vote{approve: bool}`.
   - If ≥ 2/3 approvals, Leader commits the block to its chain and broadcasts `CommitBlock`.
4. **Confirmed / Rejected** — Leader returns the outcome in the `SubmitOrder` response. Django updates the Order row to `CONFIRMED` or `REJECTED` and adjusts wallet balances accordingly.

## 4. Consensus (PoA, 2-of-3)

- Authority set is fixed at genesis (three public keys hard-coded in config).
- Leader is static for the MVP (`node-1`). A production system would rotate leaders each epoch.
- A block is committed iff `approvals >= ceil(2*N/3)` where `N = 3`, i.e. **≥ 2**. The leader's own approval counts.
- Validators reject proposals that fail ANY of:
  - Bad signature on the order tx.
  - Nonce ≤ last-applied nonce for that account.
  - Oracle price for the symbol is stale (`now - price_ts > MAX_STALE_SECONDS`, default 30s).
  - Tx-embedded price diverges from the validator's own oracle cache by > 1%.
  - **Limit-price violation:** BUY with `oracle_price > limit_price`, or SELL with `oracle_price < limit_price` (when `limit_price` is set; empty = market order).
  - Insufficient cash (buy) or shares (sell) in the validator's own state view.
  - Symbol not in the S&P 500 whitelist.

## 5. Block & chain

```go
type Block struct {
    Index        uint64
    PrevHash     []byte
    Timestamp    int64
    Txs          []OrderTx
    LeaderPubKey []byte
    Signatures   [][]byte   // authority signatures collected during PoA
    Hash         []byte     // sha256(Index||PrevHash||Timestamp||Merkle(Txs))
}
```

Each node keeps an append-only on-disk log (`chain.jsonl`) plus an in-memory state machine (`AccountState`). Replaying the log from genesis reproduces state — this is how a node recovers on restart.

## 6. State

```go
type Account struct {
    UserID      string
    PubKey      []byte
    Cash        decimal  // USD
    Positions   map[string]decimal // symbol -> shares
    LastNonce   uint64
}
```

All nodes start from an identical genesis state (seeded via `genesis.json`). Balance deltas are applied only when a block is committed, ensuring all three nodes converge.

## 7. Security properties

- **Replay protection:** `(user_id, nonce)` must be strictly increasing. Validators reject any tx whose nonce is ≤ the last applied nonce for that account.
- **Stale-price rejection:** Each tx carries `price_timestamp` from the oracle. Validators reject if `now - price_timestamp > 30s`.
- **Price-divergence guard:** The price embedded in the tx is cross-checked against each validator's own oracle cache; mismatch > 1% = reject.
- **Limit-price guard:** Users may attach a `limit_price` per order; trades execute only inside the user's tolerance window.
- **Signature verification:** Every tx is signed with the user's ECDSA (P-256) key. Nodes verify against the registered `PubKey` in state.
- **Whitelist:** Only symbols listed in `genesis.json::symbols` are accepted; enforced in Django, in genesis, and in node state.
- **Balance safety:** Both Django (optimistic) and Nodes (authoritative) check funds — but the Nodes' check is the one that matters.

For a full threat-by-threat analysis see [`THREAT_MODEL.md`](THREAT_MODEL.md).

## 8. Project layout

```
trading-system/
├── ARCHITECTURE.md                  (this file)
├── docker-compose.yml
├── proto/
│   └── trading.proto                gRPC contract (single source of truth)
├── node/                            Go execution node
│   ├── go.mod
│   ├── cmd/node/main.go             entrypoint
│   └── internal/
│       ├── blockchain/              Block, Chain, Merkle
│       ├── consensus/               PoA round orchestration
│       ├── state/                   Account + nonce + balance
│       ├── crypto/                  ECDSA helpers
│       ├── oracleclient/            Price cache + staleness check
│       ├── config/                  Genesis + authority keys
│       └── server/                  gRPC server handlers
├── django_app/                      Django API gateway
│   ├── manage.py
│   ├── requirements.txt
│   ├── trading_gateway/             settings / urls
│   ├── users/                       auth + ECDSA keys
│   ├── accounts/                    wallets & portfolio
│   └── orders/                      order model + views + gRPC client
└── oracle/                          Python price oracle
    ├── oracle.py
    └── requirements.txt
```
