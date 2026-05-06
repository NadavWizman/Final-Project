# Decentralized Trading System — Runnable MVP

A three-node PoA blockchain (Go) sitting behind a Django API gateway, with a
Python oracle pushing S&P 500 prices. Orders flow:

```
Django  ──gRPC──▶  Leader (node-1)  ──ProposeBlock──▶  node-2, node-3
                         ▲                                    │
                         └──────── votes (2/3 quorum) ────────┘
```

See `ARCHITECTURE.md` for a full explanation.

## First-run checklist

```bash
# 1. Generate keys (authorities + demo users)
pip install cryptography
python3 scripts/gen_keys.py

# 2. Auto-fill the REPLACE_WITH_* placeholders in node configs + genesis
python3 scripts/wire_configs.py

# 3. Boot the stack
docker compose up --build

# 4. Bootstrap Django users (in another shell)
docker compose exec django python manage.py migrate
docker compose exec django python manage.py createsuperuser
docker compose exec django python manage.py shell -c \
  "from users.models import User; \
   alice = User.objects.create(username='alice', external_id='alice', \
     priv_key_pem=open('/app/keys/user-alice.key.pem').read(), \
     pub_key_pem=open('/app/keys/user-alice.pub.pem').read()); \
   alice.set_password('demo'); alice.save()"
```

## End-to-end smoke test

```bash
# Draft a market order
curl -u alice:demo -X POST http://localhost:8000/api/orders/ \
  -H 'Content-Type: application/json' \
  -d '{"symbol":"AAPL","side":"BUY","quantity":"1"}'

# Draft a limit order (don't pay more than $190.00 per share)
curl -u alice:demo -X POST http://localhost:8000/api/orders/ \
  -H 'Content-Type: application/json' \
  -d '{"symbol":"AAPL","side":"BUY","quantity":"1","limit_price":"190.00"}'

# Submit it → consensus round runs
curl -u alice:demo -X POST http://localhost:8000/api/orders/<ORDER_ID>/submit/

# Check portfolio (pulled live from node-1)
curl -u alice:demo http://localhost:8000/api/portfolio/
```

On success, Alice's cash decreases and her AAPL position goes up — and the
same state is visible if you query nodes 2 and 3 directly over gRPC.

## What to look at

- `ARCHITECTURE.md` — system design, data flow, layered components.
- `THREAT_MODEL.md` — STRIDE-style threat analysis (Section 7 + 9 deliverable).
- `proto/trading.proto` — wire contract, single source of truth.
- `node/internal/consensus/poa.go` — PoA round, ~300 LOC.
- `node/internal/state/state.go` — deterministic state machine (incl. limit_price check).
- `django_app/orders/views.py` — order lifecycle.
- `django_app/orders/signer.py` — ECDSA signing (must match Go verifier byte-for-byte).

## Limitations of this MVP

- **Static leader.** Leader rotation/failover is not implemented. If node-1
  dies, submissions fail. Production needs a view-change protocol.
- **One-tx blocks.** The data structures support batching; the leader just
  doesn't do it yet.
- **In-process mempool.** Nothing durable between ProposeBlock and
  CommitBlock. Restart during a round may lose the in-flight tx.
- **Private keys in Django DB.** Fine for a demo, not fine for production.
  Move to HSM / let clients sign.
- **Limited whitelist.** Genesis ships with 20 symbols from the S&P 500 —
  extend `node/configs/genesis.json` and `settings.SP500_WHITELIST` to match
  your needs.
