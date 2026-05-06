# Threat Model

A structured analysis of the threats facing the decentralized trading system,
the mitigations implemented, and the residual risks that remain. This document
is the deliverable required by sections 7 and 9 of the project proposal.

## 1. System summary (assets, actors, trust boundaries)

**Assets we are protecting:**

| Asset                | Where it lives                       | What we lose if compromised                      |
|----------------------|--------------------------------------|--------------------------------------------------|
| User cash balances   | Node state (`accounts/<user>.cash`)  | Direct financial loss                            |
| User stock positions | Node state (`accounts/<user>.pos`)   | Direct financial loss                            |
| Order integrity      | Block chain (`chain.log`)            | Disputed trades, audit failure                   |
| User signing keys    | Django DB (encrypted at rest in prod)| Attacker can forge orders                        |
| Authority keys       | Each node's local PEM file           | Attacker can forge consensus votes               |
| Oracle price feed    | Oracle service + each node's cache   | Attacker can drain accounts via mispricing       |

**Actors:**

- **User** — submits orders, may be honest, careless, or malicious.
- **Django gateway** — semi-trusted; signs on the user's behalf.
- **Authority nodes** (3) — semi-trusted; one may be compromised at a time.
- **Oracle** — semi-trusted; single point of price truth in the MVP.
- **External attacker** — passive (eavesdrop) or active (MITM, replay, DoS).

**Trust boundaries:**

```
[User] ──HTTPS──▶ [Django] ──gRPC──▶ [Leader] ──gRPC──▶ [Validators]
                                         ▲
                                         │ gRPC
                                     [Oracle]
```

We assume the Django→Leader and Leader↔Validator channels are inside a private
network. In production this would be enforced with mTLS — for the MVP gRPC is
plaintext between containers on the docker-compose network.

## 2. Threats and mitigations

We use STRIDE-lite categorization (Spoofing, Tampering, Repudiation,
Information disclosure, Denial of Service, Elevation of privilege).

### T1 — Replay attack (S, T)

**Scenario:** An attacker captures a signed `OrderTx` on the wire and replays
it later, hoping the trade re-executes (e.g., to drain the user's balance).

**Mitigation:**
- Every `OrderTx` carries a `nonce` (uint64). Each user has a strictly
  increasing nonce counter maintained both in Django (`User.nonce`, locked
  with `SELECT FOR UPDATE` during submit) and in node state
  (`Account.LastNonce`).
- `state.Validate` rejects any tx where `tx.nonce <= account.LastNonce`.
- Code: `state/state.go::Validate`, `orders/views.py::submit_order`.

**Residual risk:** None for replay of an already-applied tx. A
not-yet-applied tx could be replayed by a network attacker, but the result
is the same as the user submitting once — no duplicate effect.

### T2 — Stale-price exploitation (T)

**Scenario:** An attacker (possibly the user) submits an order quoting a
much older oracle price to buy/sell at a price that no longer represents
fair market value.

**Mitigation:**
- Each `OrderTx` includes `price_timestamp` (unix seconds, sourced from the
  oracle).
- Validators reject any tx where `now - price_timestamp > MAX_PRICE_AGE`
  (default 30s, configurable per node).
- Validators additionally re-check the embedded price against their own
  oracle cache; divergence > 1% triggers rejection
  (`consensus/poa.go::ValidateSignedTx`).
- Oracle pushes are accepted only if their timestamp is newer than the
  cached one (`oracleclient/cache.go::Put`).

**Residual risk:** A coordinated attack between Django and a malicious
oracle could backdate prices. Mitigated by the divergence check, which
would catch a mismatch between Django's quote and the validator's
independently-fed cache. Real fix is multi-oracle (optional Section 8 item).

### T3 — Insufficient-funds bypass (E, T)

**Scenario:** A user crafts an order to spend money they don't have, hoping
a race condition or buggy validator lets it through.

**Mitigation:**
- Both `state.Validate` (read path) and `state.Apply` (write path) re-check
  cash/share balances under a write lock.
- All three nodes run identical balance checks independently — a buggy
  Leader cannot silently approve under-funded trades because the Validators
  would reject and quorum would fail.
- Optimistic check in Django (`orders/views.py::_optimistic_balance_check`)
  pre-empts most cases at API time.

**Residual risk:** If two of three nodes are compromised, balance checks
can be bypassed. This is the explicit limit of any 2-of-3 quorum and is
documented as part of the security model.

### T4 — Oracle Manipulation (T, S)

**Scenario:** Attacker compromises the oracle (or impersonates it) and
pushes a bogus price (e.g., AAPL at $1) to drain accounts.

**Mitigation:**
- Architecturally separate oracle from execution (no oracle code runs on
  nodes; nodes only consume signed-timestamped pushes).
- Oracle pushes are versioned by timestamp — only newer timestamps update
  the cache, preventing replay of an older "good" price as a sandwich.
- The `limit_price` field gives users a hard ceiling/floor — a bogus oracle
  price outside the user's limit causes rejection at the Validator
  (`state/state.go::Validate`).
- Stale-price check (T2) ensures a stuck oracle cannot be exploited.

**Residual risk:** A live, fully-compromised oracle remains a single point
of failure for users who don't set a limit price. This is the project
proposal's optional "Multi-Oracle Verification" mitigation. We've left the
data path ready for it: adding a second oracle is a question of validators
requiring quorum across N feeds in `oracleclient/cache.go`.

### T5 — Leader failure or malice (S, D, T)

**Scenario:** The Leader (`node-1`) crashes, hangs, or actively proposes
fraudulent blocks (e.g., a tx that drains another user).

**Mitigation:**
- PoA quorum requires ≥ 2/3 approvals. A malicious leader cannot commit
  alone — both validators independently re-run every check
  (`consensus/poa.go::HandleProposal`) and refuse to sign anything that
  fails. A fraudulent block gets at most one vote (the leader's) and dies.
- Validators verify block hash, prev_hash linkage, signatures, nonces,
  staleness, and balance — same code path as the leader's pre-validate.

**Residual risk:**
- **Liveness, not safety:** if the leader dies, the cluster cannot make
  progress. Submissions to Django will time out and the order is marked
  REJECTED. This is documented in `README.md` under "Limitations" — the
  fix is leader rotation/view-change, deferred to a future iteration.
- A compromised leader plus one compromised validator (2 of 3) breaks
  safety. This is the formal limit of the consensus mechanism.

### T6 — Authority key compromise (E)

**Scenario:** Attacker steals one node's signing key.

**Mitigation:**
- Authority keys are held only on the node hosts (PEM files mounted
  read-only via Docker volumes).
- A single compromised authority cannot reach quorum alone (2 of 3 still
  required).
- Authority key rotation is supported by editing each node's config —
  for the MVP this requires a coordinated restart.

**Residual risk:** Two simultaneous authority compromises break the
chain's safety guarantees. Mitigation in production: HSM-backed signing
keys, separated host/security domains for the three authorities, and
periodic rotation.

### T7 — User key compromise (S)

**Scenario:** Attacker obtains a user's ECDSA private key and submits
trades on their behalf.

**Mitigation:**
- For the MVP private keys live in the Django DB. In production they
  should move to an HSM/KMS or be held client-side (a wallet).
- Even with the key, the attacker is bounded by the user's existing
  balance — there is no way to overdraw.
- Nonce monotonicity prevents replay of past trades to amplify damage.

**Residual risk:** Direct theft of funds up to the user's balance. This
is the same exposure as a stolen banking password and is unavoidable in
any system where the user's identity is a key.

### T8 — Symbol-list bypass (T)

**Scenario:** User tries to trade an asset outside the S&P 500 whitelist
(e.g., a meme coin) by signing an `OrderTx` with an arbitrary symbol.

**Mitigation:**
- Whitelist enforced at three layers:
  1. Django serializer (`orders/serializers.py::validate_symbol`).
  2. Genesis state (`node/configs/genesis.json::symbols`).
  3. Validator state check (`state/state.go::Validate` — `SymbolAllowed`).
- All three nodes reject unknown symbols.

**Residual risk:** None. Bypassing requires editing all three nodes'
genesis files, which is equivalent to compromising the cluster.

### T9 — Denial of service via order flood (D)

**Scenario:** A user (or compromised account) submits thousands of orders
to overwhelm the Leader and starve other users.

**Mitigation (current):**
- Per-user nonce serialization in Django prevents one user from forking
  multiple in-flight rounds.
- Leader serializes proposals (`proposeLock`) — bad actors slow down
  but cannot crash the cluster.

**Residual risk:** A determined attacker can still slow the system. Mitigations
not implemented in the MVP: rate-limiting per user in Django, mempool
batching to amortize consensus cost, fee-based prioritization. Documented
under known limitations.

### T10 — Network partition / split-brain (T)

**Scenario:** Network split isolates one validator from the leader. Some
clients keep submitting; some validators see different proposed blocks.

**Mitigation:**
- The leader cannot commit without 2-of-3 signatures, so a partitioned
  validator cannot create a fork. If the leader is on the minority side,
  no progress is made (safety preserved, liveness lost).
- Each block contains the prev_hash; a partitioned validator that
  reconnects rejects any block that doesn't link to its current head and
  re-syncs from disk.

**Residual risk:** Liveness loss during partitions. Same as T5.

### T11 — Confidentiality on the wire (I)

**Scenario:** Network observer reads order details (positions, sizes,
strategies) in transit.

**Mitigation (current MVP):** None — gRPC is plaintext on the docker
network. Acceptable for the local demo; not for production.

**Residual risk:** Full information disclosure of orders, balances, and
authority votes. Production fix: enable mTLS on every gRPC channel and
HTTPS on the Django front. The protobuf contract doesn't change.

### T12 — Repudiation (R)

**Scenario:** A user denies having submitted a trade ("I never bought that").

**Mitigation:**
- Every order is signed with the user's ECDSA key.
- The signed `OrderTx` is committed to the chain inside the block, with
  the user's pubkey embedded in `SignedOrderTx.pub_key`.
- Replaying the chain log reconstructs every approved trade with proof of
  authorship.

**Residual risk:** None within the cryptographic model. A user denying
authorship would have to demonstrate their key was compromised (which is
T7), and the chain remains the source of truth.

## 3. Mitigation matrix

| #   | Threat                       | Implemented? | Where to look                              |
|-----|------------------------------|--------------|--------------------------------------------|
| T1  | Replay                       | ✅ full      | nonce checks in state + Django             |
| T2  | Stale price                  | ✅ full      | timestamp + divergence check               |
| T3  | Insufficient-funds bypass    | ✅ full      | per-node balance check, 2-of-3 quorum      |
| T4  | Oracle manipulation          | ⚠️ partial   | limit_price + divergence; multi-oracle TBD |
| T5  | Leader failure (safety)      | ✅ full      | PoA quorum                                 |
| T5  | Leader failure (liveness)    | ❌ deferred  | leader rotation TBD                        |
| T6  | Authority key compromise     | ✅ partial   | 2-of-3 quorum; HSM TBD                     |
| T7  | User key compromise          | ⚠️ partial   | bounded by balance; client-side wallet TBD |
| T8  | Symbol whitelist bypass      | ✅ full      | three-layer enforcement                    |
| T9  | DoS / order flood            | ⚠️ partial   | per-user serialization; rate limit TBD     |
| T10 | Network partition            | ✅ safety    | quorum; liveness loss accepted             |
| T11 | On-the-wire confidentiality  | ❌ deferred  | mTLS TBD                                   |
| T12 | Repudiation                  | ✅ full      | ECDSA + chain log                          |

## 4. Conclusions

The system as built protects integrity and authenticity for the typical
threat model of a small-scale trading prototype: a user with stolen
credentials cannot cause damage beyond their own balance, a malicious
leader cannot commit fraud alone, and a stale or manipulated oracle is
caught by the limit-price + divergence guards.

The deliberate, documented gaps are:
1. **Leader rotation** — fix in a future iteration.
2. **Multi-oracle quorum** — listed as Section 8 optional; the data path
   is already structured to accept multiple sources.
3. **Wire-level encryption** — turn on mTLS in `node/cmd/node/main.go` by
   swapping `insecure.NewCredentials()` for a real TLS config.
4. **Client-side signing** — move private keys out of the Django DB.

These are scope choices for an MVP, not design flaws. Each has a clear
extension point in the codebase; none would require redesigning the
consensus or state-machine layers.
