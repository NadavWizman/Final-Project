// Package consensus implements the PoA round:
//
//   Leader flow (SubmitOrder → Confirmed/Rejected):
//     1. Pre-validate the tx (sig, nonce, staleness, balance).
//     2. Build a candidate block extending the current head.
//     3. ProposeBlock to every authority (including self, for symmetry).
//     4. Count approvals. If >= quorum (2/3), sign-commit-broadcast.
//     5. Apply block to local state.
//
//   Validator flow (on ProposeBlock RPC):
//     1. Verify the block links to our current head.
//     2. For each tx: re-verify signature, re-check nonce, re-check
//        staleness against our oracle cache, re-check balance against
//        our state.
//     3. If all good, sign block.hash and return approve=true.
//
// The engine is deliberately synchronous. The leader blocks on validator
// votes, which keeps the happy-path reasoning simple.
package consensus

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/shopspring/decimal"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"

	"trading/internal/blockchain"
	"trading/internal/config"
	cryp "trading/internal/crypto"
	"trading/internal/oracleclient"
	"trading/internal/state"
	tradingpb "trading/proto"
)

type Engine struct {
	cfg     *config.Config
	priv    *ecdsa.PrivateKey
	pubDER  []byte
	chain   *blockchain.Chain
	state   *state.State
	oracle  *oracleclient.Cache
	peers   map[string]tradingpb.ConsensusServiceClient
	peersMu sync.RWMutex

	// The leader takes this to serialise proposals. Only one in-flight at a
	// time — enough for an MVP and avoids the hard fork-choice question.
	proposeLock sync.Mutex
}

func NewEngine(cfg *config.Config, priv *ecdsa.PrivateKey, chain *blockchain.Chain,
	st *state.State, oc *oracleclient.Cache) (*Engine, error) {

	pubDER, err := cryp.MarshalPublicKeyDER(&priv.PublicKey)
	if err != nil {
		return nil, err
	}
	e := &Engine{
		cfg:    cfg,
		priv:   priv,
		pubDER: pubDER,
		chain:  chain,
		state:  st,
		oracle: oc,
		peers:  make(map[string]tradingpb.ConsensusServiceClient),
	}
	if err := e.dialPeers(); err != nil {
		return nil, err
	}
	return e, nil
}

func (e *Engine) dialPeers() error {
	for _, auth := range e.cfg.Authorities {
		if auth.NodeID == e.cfg.NodeID {
			continue
		}
		conn, err := grpc.NewClient(
			auth.Address,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			return fmt.Errorf("dial peer %s@%s: %w", auth.NodeID, auth.Address, err)
		}
		e.peers[auth.NodeID] = tradingpb.NewConsensusServiceClient(conn)
	}
	return nil
}

// quorum returns the minimum number of approvals required (>= 2/3 of N).
func (e *Engine) quorum() int {
	n := len(e.cfg.Authorities)
	// ceil(2N/3)
	return (2*n + 2) / 3
}

// ============================================================================
// Shared validation: used by both leader (pre-propose) and validators.
// ============================================================================

func (e *Engine) ValidateSignedTx(st *tradingpb.SignedOrderTx) error {
	if st == nil || st.Tx == nil {
		return errors.New("nil tx")
	}
	// 1. Signature check.
	pub, err := cryp.ParsePublicKeyDER(st.PubKey)
	if err != nil {
		return fmt.Errorf("bad pubkey: %w", err)
	}
	txBytes, err := proto.Marshal(st.Tx)
	if err != nil {
		return fmt.Errorf("marshal tx: %w", err)
	}
	if !cryp.Verify(pub, txBytes, st.Signature) {
		return errors.New("signature verification failed")
	}
	// 2. Oracle freshness (based on the tx's declared price_timestamp).
	if err := e.oracle.CheckFreshness(st.Tx.Symbol, st.Tx.PriceTimestamp); err != nil {
		return fmt.Errorf("stale oracle: %w", err)
	}
	// 3. Build a Trade and defer to state validator for nonce/balance/whitelist.
	qty, err := decimal.NewFromString(st.Tx.Quantity)
	if err != nil {
		return fmt.Errorf("bad quantity: %w", err)
	}
	txPrice, err := decimal.NewFromString(st.Tx.Price)
	if err != nil {
		return fmt.Errorf("bad price: %w", err)
	}

	// Cross-check the tx's price against our own oracle cache. This blocks
	// a malicious Django (or compromised key) from signing with a fabricated
	// price. We allow up to MaxPriceDivergencePct between the two — anything
	// further apart and we reject. The threshold is loose enough to tolerate
	// normal mid-second jitter but tight enough to catch tampering.
	const MaxPriceDivergencePct = 1 // 1%
	if quote, err := e.oracle.Lookup(st.Tx.Symbol); err == nil {
		diff := txPrice.Sub(quote.Price).Abs()
		threshold := quote.Price.Mul(decimal.NewFromInt(MaxPriceDivergencePct)).Div(decimal.NewFromInt(100))
		if diff.GreaterThan(threshold) {
			return fmt.Errorf("tx price %s diverges from oracle %s by more than %d%%",
				txPrice, quote.Price, MaxPriceDivergencePct)
		}
	}
	// (If the validator has no quote at all, CheckFreshness above already
	// gave the tx the benefit of the doubt based on its own timestamp.)

	// limit_price is optional ("" = no limit / market order).
	limit := decimal.Zero
	if st.Tx.LimitPrice != "" {
		limit, err = decimal.NewFromString(st.Tx.LimitPrice)
		if err != nil {
			return fmt.Errorf("bad limit_price: %w", err)
		}
	}
	tr := state.Trade{
		UserID:     st.Tx.UserId,
		Symbol:     st.Tx.Symbol,
		IsBuy:      st.Tx.Side == tradingpb.Side_BUY,
		Quantity:   qty,
		Price:      txPrice,
		LimitPrice: limit,
		Nonce:      st.Tx.Nonce,
	}
	// 4. Also cross-check the submitted pubkey matches the account on file.
	acc, ok := e.state.Snapshot(st.Tx.UserId)
	if !ok {
		return fmt.Errorf("unknown user %s", st.Tx.UserId)
	}
	if hex.EncodeToString(acc.PubKeyDER) != hex.EncodeToString(st.PubKey) {
		return errors.New("pub key does not match registered account")
	}
	return e.state.Validate(tr)
}

// ============================================================================
// Leader path
// ============================================================================

// RunLeaderRound takes a single signed tx and runs a full PoA round.
// Returns (committed block, nil) on success, or ("", err) on rejection.
func (e *Engine) RunLeaderRound(ctx context.Context, stx *tradingpb.SignedOrderTx) (*tradingpb.Block, error) {
	if !e.cfg.IsLeader {
		return nil, errors.New("this node is not the leader")
	}

	// Serialise proposals. The MVP processes one block per tx — batching is
	// left as a TODO (the data structures already support multi-tx blocks).
	e.proposeLock.Lock()
	defer e.proposeLock.Unlock()

	// Leader-side pre-validation. Fail fast so we don't waste a consensus round.
	if err := e.ValidateSignedTx(stx); err != nil {
		return nil, fmt.Errorf("pre-validate: %w", err)
	}

	// Build candidate block.
	head := e.chain.Head()
	block := &tradingpb.Block{
		Index:        head.Index + 1,
		PrevHash:     head.Hash,
		Timestamp:    time.Now().Unix(),
		Txs:          []*tradingpb.SignedOrderTx{stx},
		LeaderPubkey: e.pubDER,
	}
	h, err := blockchain.Hash(block)
	if err != nil {
		return nil, err
	}
	block.Hash = h

	// Leader's own vote — count it first.
	leaderSig, err := cryp.Sign(e.priv, block.Hash)
	if err != nil {
		return nil, fmt.Errorf("leader self-sign: %w", err)
	}
	approvals := 1
	sigs := [][]byte{leaderSig}

	// Ask each validator in parallel.
	roundCtx, cancel := context.WithTimeout(ctx, e.cfg.ConsensusTimeoutDuration())
	defer cancel()

	type vote struct {
		nodeID  string
		approve bool
		reason  string
		sig     []byte
	}
	votes := make(chan vote, len(e.peers))
	var wg sync.WaitGroup
	e.peersMu.RLock()
	for nodeID, client := range e.peers {
		wg.Add(1)
		go func(nodeID string, c tradingpb.ConsensusServiceClient) {
			defer wg.Done()
			resp, err := c.ProposeBlock(roundCtx, &tradingpb.ProposeBlockRequest{Block: block})
			if err != nil {
				votes <- vote{nodeID: nodeID, approve: false, reason: err.Error()}
				return
			}
			votes <- vote{nodeID: nodeID, approve: resp.Approve, reason: resp.Reason, sig: resp.Signature}
		}(nodeID, client)
	}
	e.peersMu.RUnlock()
	wg.Wait()
	close(votes)

	for v := range votes {
		if v.approve {
			approvals++
			sigs = append(sigs, v.sig)
			log.Printf("[consensus] approval from %s", v.nodeID)
		} else {
			log.Printf("[consensus] rejection from %s: %s", v.nodeID, v.reason)
		}
	}

	if approvals < e.quorum() {
		return nil, fmt.Errorf("consensus failed: %d/%d approvals (need %d)",
			approvals, len(e.cfg.Authorities), e.quorum())
	}
	block.Signatures = sigs

	// Commit locally first — if this fails we haven't told anyone yet.
	if err := e.commitLocal(block); err != nil {
		return nil, fmt.Errorf("leader commit: %w", err)
	}

	// Broadcast CommitBlock. Errors here are logged but don't fail the order;
	// validators that missed it will re-sync from disk on restart, and in
	// practice the ProposeBlock approval already contained the state delta
	// they need.
	e.broadcastCommit(ctx, block)

	return block, nil
}

func (e *Engine) broadcastCommit(ctx context.Context, block *tradingpb.Block) {
	bctx, cancel := context.WithTimeout(ctx, e.cfg.ConsensusTimeoutDuration())
	defer cancel()
	e.peersMu.RLock()
	defer e.peersMu.RUnlock()
	for nodeID, client := range e.peers {
		go func(nodeID string, c tradingpb.ConsensusServiceClient) {
			if _, err := c.CommitBlock(bctx, &tradingpb.CommitBlockRequest{Block: block}); err != nil {
				log.Printf("[consensus] commit broadcast to %s failed: %v", nodeID, err)
			}
		}(nodeID, client)
	}
}

// ============================================================================
// Validator path (called from the gRPC server)
// ============================================================================

// HandleProposal runs validator-side checks and returns a signed approval
// if everything passes.
func (e *Engine) HandleProposal(block *tradingpb.Block) (*tradingpb.ProposeBlockResponse, error) {
	// Link check.
	head := e.chain.Head()
	if err := blockchain.ValidateLink(head, block); err != nil {
		return e.reject("link: " + err.Error())
	}
	if err := blockchain.VerifyHash(block); err != nil {
		return e.reject("hash: " + err.Error())
	}
	// Re-run every tx through our own validation.
	for _, stx := range block.Txs {
		if err := e.ValidateSignedTx(stx); err != nil {
			return e.reject("tx: " + err.Error())
		}
	}
	sig, err := cryp.Sign(e.priv, block.Hash)
	if err != nil {
		return e.reject("sign: " + err.Error())
	}
	return &tradingpb.ProposeBlockResponse{
		Approve:   true,
		Signature: sig,
		Pubkey:    e.pubDER,
	}, nil
}

func (e *Engine) reject(reason string) (*tradingpb.ProposeBlockResponse, error) {
	return &tradingpb.ProposeBlockResponse{Approve: false, Reason: reason}, nil
}

// HandleCommit applies a leader-committed block to our local state.
func (e *Engine) HandleCommit(block *tradingpb.Block) error {
	return e.commitLocal(block)
}

// commitLocal is the shared commit path used by both the leader and the
// validators. It appends to the chain and applies each tx to state.
func (e *Engine) commitLocal(block *tradingpb.Block) error {
	// Idempotency: if we already have this block by index, just no-op.
	head := e.chain.Head()
	if block.Index <= head.Index {
		log.Printf("[consensus] already at or past index %d, skipping commit", block.Index)
		return nil
	}
	if err := e.chain.Append(block); err != nil {
		return fmt.Errorf("append: %w", err)
	}
	for _, stx := range block.Txs {
		qty, _ := decimal.NewFromString(stx.Tx.Quantity)
		price, _ := decimal.NewFromString(stx.Tx.Price)
		limit := decimal.Zero
		if stx.Tx.LimitPrice != "" {
			limit, _ = decimal.NewFromString(stx.Tx.LimitPrice)
		}
		err := e.state.Apply(state.Trade{
			UserID:     stx.Tx.UserId,
			Symbol:     stx.Tx.Symbol,
			IsBuy:      stx.Tx.Side == tradingpb.Side_BUY,
			Quantity:   qty,
			Price:      price,
			LimitPrice: limit,
			Nonce:      stx.Tx.Nonce,
		})
		if err != nil {
			// This should never happen if validators did their job. Log loudly.
			log.Printf("[consensus] CRITICAL: state apply failed for committed block %d: %v",
				block.Index, err)
			return err
		}
	}
	log.Printf("[consensus] committed block %d (%d tx) hash=%s",
		block.Index, len(block.Txs), hex.EncodeToString(block.Hash)[:12])
	return nil
}
