// Package app is the TradeDesk state machine as a CometBFT (ABCI++)
// application. CometBFT orders transactions into blocks with Byzantine fault
// tolerant consensus (rotating proposer, commits signed by more than 2/3 of
// the validators); this package decides what a block means:
//
//	PrepareProposal  the proposer adds signed oracle quotes for the tickers the
//	                 block needs and drops transactions that would be invalid
//	ProcessProposal  every validator checks the quotes and re-executes the block;
//	                 a forged quote, a shifted price or an invalid transaction
//	                 makes it vote against the block
//	FinalizeBlock    the decided block is applied to the ledger (package ledger)
//	Commit           the new state is saved; its hash is the next AppHash
//
// The ledger — balances, positions, orders — exists only here, on every node.
package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/libs/log"

	"nodes/ledger"
	"nodes/quotes"
)

// GenesisState is the app_state section of the genesis file.
type GenesisState struct {
	Params  ledger.Params   `json:"params"`
	Oracles []quotes.Source `json:"oracles"`
}

// Chain is everything the application keeps, and everything AppHash covers.
type Chain struct {
	Ledger     *ledger.State   `json:"ledger"`
	Oracles    []quotes.Source `json:"oracles"`
	Validators []Validator     `json:"validators"`
	ValsetSeq  uint64          `json:"valset_seq"` // number of validator-set changes so far
}

// Validator is one member of the validator set (task 6, valset.go).
type Validator struct {
	PubKey []byte `json:"pubkey"` // Ed25519
	Power  int64  `json:"power"`
}

// AppHash commits to the whole application state: the ledger's Merkle root
// (the state root over every balance) plus the oracle and validator sets.
func (c *Chain) AppHash() []byte {
	root := c.Ledger.Root()
	o, _ := json.Marshal(c.Oracles)
	v, _ := json.Marshal(c.Validators)
	h := sha256.New()
	h.Write([]byte{2})
	h.Write(root[:])
	oh, vh := sha256.Sum256(o), sha256.Sum256(v)
	h.Write(oh[:])
	h.Write(vh[:])
	return h.Sum(nil)
}

func (c *Chain) clone() *Chain {
	b, _ := json.Marshal(c)
	var out Chain
	_ = json.Unmarshal(b, &out)
	return &out
}

// App implements abci.Application.
type App struct {
	abci.BaseApplication

	mu      sync.Mutex
	home    string // where app_state.json lives
	logger  log.Logger
	fetcher quotes.Fetcher

	chain   *Chain // committed state
	next    *Chain // state after FinalizeBlock, until Commit
	nextRes []*abci.ExecTxResult

	pendingNonce map[string]uint64    // mempool: next nonce per sender
	evaluated    map[string]*evaluate // proposal hash → its result

	// Byzantine hooks, used only by the acceptance tests to play a dishonest
	// proposer. Nil in a real node.
	ByzantinePrepare func(txs [][]byte) [][]byte

	rejected atomic.Int64 // proposals this node voted against
}

// RejectedProposals is how many proposals this node has refused.
func (a *App) RejectedProposals() int64 { return a.rejected.Load() }

type evaluate struct {
	chain   *Chain
	results []*abci.ExecTxResult
	err     error
}

// New creates the application. home holds its saved state.
func New(home string, fetcher quotes.Fetcher, logger log.Logger) (*App, error) {
	a := &App{home: home, logger: logger, fetcher: fetcher,
		pendingNonce: map[string]uint64{}, evaluated: map[string]*evaluate{}}
	if err := a.load(); err != nil {
		return nil, err
	}
	return a, nil
}

// ---------------------------------------------------------------- lifecycle

func (a *App) Info(_ context.Context, _ *abci.RequestInfo) (*abci.ResponseInfo, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.chain == nil {
		return &abci.ResponseInfo{Data: "tradedesk", Version: "1"}, nil
	}
	return &abci.ResponseInfo{Data: "tradedesk", Version: "1",
		LastBlockHeight: a.chain.Ledger.Height, LastBlockAppHash: a.chain.AppHash()}, nil
}

func (a *App) InitChain(_ context.Context, req *abci.RequestInitChain) (*abci.ResponseInitChain, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var g GenesisState
	if err := json.Unmarshal(req.AppStateBytes, &g); err != nil {
		return nil, fmt.Errorf("invalid app_state in genesis: %w", err)
	}
	if len(g.Oracles)%2 == 0 {
		return nil, fmt.Errorf("the number of oracle sources must be odd (median of one real quote), got %d", len(g.Oracles))
	}
	g.Params.ChainID = req.ChainId
	c := &Chain{Ledger: ledger.NewState(g.Params), Oracles: g.Oracles}
	for _, v := range req.Validators {
		c.Validators = append(c.Validators, Validator{PubKey: v.PubKey.GetEd25519(), Power: v.Power})
	}
	sortValidators(c.Validators)
	a.chain = c
	return &abci.ResponseInitChain{}, nil
}

// --------------------------------------------------------------- mempool

func (a *App) CheckTx(_ context.Context, req *abci.RequestCheckTx) (*abci.ResponseCheckTx, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if quotes.IsBundle(req.Tx) {
		return &abci.ResponseCheckTx{Code: 1, Log: "quote bundles come only from the block proposer"}, nil
	}
	if isValset(req.Tx) {
		if _, err := a.chain.checkValset(req.Tx); err != nil {
			return &abci.ResponseCheckTx{Code: 1, Log: err.Error()}, nil
		}
		return &abci.ResponseCheckTx{Code: 0}, nil
	}
	addr, nonce, err := a.chain.Ledger.CheckTx(req.Tx, func(addr string) (uint64, bool) {
		n, ok := a.pendingNonce[addr]
		return n, ok
	})
	if err != nil {
		return &abci.ResponseCheckTx{Code: 1, Log: err.Error()}, nil
	}
	a.pendingNonce[addr] = nonce + 1
	return &abci.ResponseCheckTx{Code: 0}, nil
}

// --------------------------------------------------------------- consensus

func (a *App) PrepareProposal(ctx context.Context, req *abci.RequestPrepareProposal) (*abci.ResponsePrepareProposal, error) {
	a.mu.Lock()
	base := a.chain
	a.mu.Unlock()

	var user [][]byte
	for _, tx := range req.Txs {
		if !quotes.IsBundle(tx) {
			user = append(user, tx)
		}
	}
	at := req.Time.Unix()

	var bundle []byte
	if tickers := neededTickers(base.Ledger, user, at); len(tickers) > 0 && a.fetcher != nil {
		b := quotes.Build(a.fetcher.Fetch(ctx, tickers), len(base.Oracles))
		if len(b.Quotes) > 0 {
			bundle = quotes.Encode(b)
		}
	}

	// Leave out transactions that would be invalid in this block (a nonce gap,
	// a sender not yet registered): validators reject a block that has any.
	// An invalid transaction never changes the state, so one simulated pass
	// over a copy tells which ones to keep.
	var txs [][]byte
	var prices map[string]ledger.Cents
	if bundle != nil {
		txs = append(txs, bundle)
		if medians, err := quotes.Verify(bundle, base.Oracles, base.Ledger.Listed, at); err == nil {
			prices = toCents(medians)
		}
	}
	sim := base.clone()
	for _, tx := range user {
		if isValset(tx) {
			if sim.applyValset(tx) == nil {
				txs = append(txs, tx)
			}
			continue
		}
		if sim.Ledger.Simulate(req.Height, at, prices, tx).Code == ledger.CodeOK {
			txs = append(txs, tx)
		}
	}
	txs = capBytes(txs, req.MaxTxBytes)
	if a.ByzantinePrepare != nil {
		txs = a.ByzantinePrepare(txs)
	}
	return &abci.ResponsePrepareProposal{Txs: txs}, nil
}

func (a *App) ProcessProposal(_ context.Context, req *abci.RequestProcessProposal) (*abci.ResponseProcessProposal, error) {
	a.mu.Lock()
	base := a.chain
	a.mu.Unlock()
	ev := base.run(req.Height, req.Time.Unix(), req.Txs, false)
	if ev.err != nil {
		a.rejected.Add(1)
		a.logger.Info("rejecting proposal", "height", req.Height, "reason", ev.err)
		return &abci.ResponseProcessProposal{Status: abci.ResponseProcessProposal_REJECT}, nil
	}
	a.mu.Lock()
	a.evaluated[hex.EncodeToString(req.Hash)] = ev
	a.mu.Unlock()
	return &abci.ResponseProcessProposal{Status: abci.ResponseProcessProposal_ACCEPT}, nil
}

func (a *App) FinalizeBlock(_ context.Context, req *abci.RequestFinalizeBlock) (*abci.ResponseFinalizeBlock, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ev, ok := a.evaluated[hex.EncodeToString(req.Hash)]
	if !ok || ev.err != nil {
		// The block was decided by more than 2/3 of the validators, so apply it
		// whatever this node thought of it (lenient: an invalid quote bundle
		// gives no prices, an invalid transaction changes nothing).
		ev = a.chain.run(req.Height, req.Time.Unix(), req.Txs, true)
	}
	prevValidators := a.chain.Validators
	a.next, a.nextRes = ev.chain, ev.results
	return &abci.ResponseFinalizeBlock{
		TxResults:        ev.results,
		AppHash:          ev.chain.AppHash(),
		ValidatorUpdates: validatorDiff(prevValidators, ev.chain.Validators),
	}, nil
}

func (a *App) Commit(_ context.Context, _ *abci.RequestCommit) (*abci.ResponseCommit, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.next != nil {
		a.chain, a.next = a.next, nil
		if err := a.save(); err != nil {
			return nil, err
		}
	}
	a.evaluated = map[string]*evaluate{}
	a.pendingNonce = map[string]uint64{} // rebuilt by the mempool's recheck
	return &abci.ResponseCommit{}, nil
}

// run applies a block to a copy of the chain. Strict mode (proposals) fails
// on any invalid content; lenient mode (decided blocks) never fails.
func (c *Chain) run(height, at int64, txs [][]byte, lenient bool) *evaluate {
	next := c.clone()
	results := make([]*abci.ExecTxResult, len(txs))
	var prices map[string]ledger.Cents

	var user [][]byte
	userIdx := []int{}
	for i, tx := range txs {
		switch {
		case quotes.IsBundle(tx):
			if i != 0 {
				if lenient {
					results[i] = &abci.ExecTxResult{Code: 1, Log: "misplaced quote bundle"}
					continue
				}
				return &evaluate{err: fmt.Errorf("quote bundle must be the first transaction")}
			}
			medians, err := quotes.Verify(tx, c.Oracles, next.Ledger.Listed, at)
			if err != nil {
				if !lenient {
					return &evaluate{err: err}
				}
				results[i] = &abci.ExecTxResult{Code: 1, Log: err.Error()}
				continue
			}
			prices = toCents(medians)
			results[i] = &abci.ExecTxResult{Code: 0, Log: fmt.Sprintf("quotes: %d prices", len(prices))}
		case isValset(tx):
			// validator-set changes are applied in order with the user transactions below
			user = append(user, tx)
			userIdx = append(userIdx, i)
		default:
			user = append(user, tx)
			userIdx = append(userIdx, i)
		}
	}

	// Validator-set changes are app-level; everything else is the ledger's.
	var ledgerTxs [][]byte
	var ledgerIdx []int
	for k, tx := range user {
		if isValset(tx) {
			if err := next.applyValset(tx); err != nil {
				if !lenient {
					return &evaluate{err: err}
				}
				results[userIdx[k]] = &abci.ExecTxResult{Code: 1, Log: err.Error()}
				continue
			}
			results[userIdx[k]] = &abci.ExecTxResult{Code: 0, Log: "validator set updated"}
			continue
		}
		ledgerTxs = append(ledgerTxs, tx)
		ledgerIdx = append(ledgerIdx, userIdx[k])
	}
	for k, r := range next.Ledger.ApplyBlock(height, at, prices, ledgerTxs) {
		if r.Code != ledger.CodeOK && !lenient {
			return &evaluate{err: fmt.Errorf("invalid transaction %d: %s", ledgerIdx[k], r.Log)}
		}
		results[ledgerIdx[k]] = &abci.ExecTxResult{Code: r.Code, Log: r.Log}
	}
	return &evaluate{chain: next, results: results}
}

// neededTickers are the tickers a block should carry prices for: those of
// its orders, plus every ticker an automatic rule is watching.
func neededTickers(s *ledger.State, txs [][]byte, at int64) []string {
	set := map[string]bool{}
	for _, tx := range txs {
		if t := ledger.TickerOf(tx); t != "" {
			set[t] = true
		}
	}
	for _, t := range s.WatchedTickers(at) {
		set[t] = true
	}
	out := make([]string, 0, len(set))
	for t := range set {
		if s.Listed(t) {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

func toCents(m map[string]int64) map[string]ledger.Cents {
	out := make(map[string]ledger.Cents, len(m))
	for t, p := range m {
		out[t] = ledger.Cents(p)
	}
	return out
}

func capBytes(txs [][]byte, max int64) [][]byte {
	var total int64
	for i, tx := range txs {
		total += int64(len(tx))
		if total > max {
			return txs[:i]
		}
	}
	return txs
}

// ------------------------------------------------------------------ storage

// The state is saved as JSON with its AppHash after every commit. On start
// the hash is recomputed: a file that was edited no longer matches, and the
// node rebuilds its state by replaying the blocks it already holds (or
// fetches from its peers) instead of trusting it.
type saved struct {
	Chain   *Chain `json:"chain"`
	AppHash string `json:"app_hash"`
}

func (a *App) path() string { return filepath.Join(a.home, "data", "app_state.json") }

func (a *App) save() error {
	b, err := json.Marshal(saved{Chain: a.chain, AppHash: hex.EncodeToString(a.chain.AppHash())})
	if err != nil {
		return err
	}
	tmp := a.path() + ".tmp"
	if err := os.MkdirAll(filepath.Dir(tmp), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, a.path())
}

func (a *App) load() error {
	b, err := os.ReadFile(a.path())
	if os.IsNotExist(err) {
		return nil // fresh node: InitChain, then blocks
	}
	if err != nil {
		return err
	}
	var s saved
	if err := json.Unmarshal(b, &s); err != nil || s.Chain == nil || s.Chain.Ledger == nil {
		a.discard("unreadable")
		return nil
	}
	if got := hex.EncodeToString(s.Chain.AppHash()); got != s.AppHash {
		a.discard("its contents do not match its hash")
		return nil
	}
	a.chain = s.Chain
	return nil
}

// discard sets a corrupt state file aside; the node then reports height 0 and
// CometBFT replays every block into a fresh state.
func (a *App) discard(why string) {
	bad := fmt.Sprintf("%s.corrupt-%d", a.path(), time.Now().Unix())
	_ = os.Rename(a.path(), bad)
	a.logger.Error("saved state rejected; rebuilding it from the blocks", "reason", why, "moved_to", bad)
}
