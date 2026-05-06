// Package state holds the deterministic, in-memory state machine that every
// node runs. All three nodes start from the same genesis and apply blocks in
// the same order, so their state converges.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/shopspring/decimal"
)

type Account struct {
	UserID    string                     `json:"user_id"`
	PubKeyDER []byte                     `json:"pub_key_der"` // DER-encoded P-256 pub key
	Cash      decimal.Decimal            `json:"cash"`
	Positions map[string]decimal.Decimal `json:"positions"` // symbol -> shares
	LastNonce uint64                     `json:"last_nonce"`
}

// Genesis is the JSON shape we seed the chain from. All three nodes must
// load the same genesis file.
type Genesis struct {
	Accounts []Account `json:"accounts"`
	Symbols  []string  `json:"symbols"` // S&P 500 whitelist (subset fine for MVP)
}

type State struct {
	mu       sync.RWMutex
	accounts map[string]*Account
	symbols  map[string]struct{}
}

func New() *State {
	return &State{
		accounts: make(map[string]*Account),
		symbols:  make(map[string]struct{}),
	}
}

// LoadGenesis populates the state from a genesis.json file on disk.
func (s *State) LoadGenesis(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read genesis: %w", err)
	}
	var g Genesis
	if err := json.Unmarshal(raw, &g); err != nil {
		return fmt.Errorf("parse genesis: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range g.Accounts {
		a := g.Accounts[i]
		if a.Positions == nil {
			a.Positions = map[string]decimal.Decimal{}
		}
		s.accounts[a.UserID] = &a
	}
	for _, sym := range g.Symbols {
		s.symbols[sym] = struct{}{}
	}
	return nil
}

func (s *State) SymbolAllowed(symbol string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.symbols[symbol]
	return ok
}

func (s *State) Snapshot(userID string) (*Account, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.accounts[userID]
	if !ok {
		return nil, false
	}
	// return a copy — callers must not mutate the live account
	cp := *a
	cp.Positions = make(map[string]decimal.Decimal, len(a.Positions))
	for k, v := range a.Positions {
		cp.Positions[k] = v
	}
	return &cp, true
}

// Trade describes the net effect of a single order tx on the state.
type Trade struct {
	UserID   string
	Symbol   string
	IsBuy    bool
	Quantity decimal.Decimal
	Price    decimal.Decimal
	// LimitPrice is the user's worst acceptable price. Zero means "no limit".
	// Validators enforce: BUY → Price <= LimitPrice, SELL → Price >= LimitPrice.
	LimitPrice decimal.Decimal
	Nonce      uint64
}

// Validate checks a trade WITHOUT mutating state. This is what the PoA
// validators use to decide whether to approve a proposed block.
func (s *State) Validate(t Trade) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.symbols[t.Symbol]; !ok {
		return fmt.Errorf("symbol %q not in whitelist", t.Symbol)
	}
	a, ok := s.accounts[t.UserID]
	if !ok {
		return fmt.Errorf("unknown user %q", t.UserID)
	}
	if t.Nonce <= a.LastNonce {
		return fmt.Errorf("stale nonce: got %d, last applied %d", t.Nonce, a.LastNonce)
	}
	if t.Quantity.Sign() <= 0 {
		return errors.New("quantity must be positive")
	}
	if t.Price.Sign() <= 0 {
		return errors.New("price must be positive")
	}
	// Limit-price guard. Zero LimitPrice = "no limit" (market order).
	if t.LimitPrice.Sign() > 0 {
		if t.IsBuy && t.Price.GreaterThan(t.LimitPrice) {
			return fmt.Errorf("buy limit violated: oracle price %s > limit %s",
				t.Price, t.LimitPrice)
		}
		if !t.IsBuy && t.Price.LessThan(t.LimitPrice) {
			return fmt.Errorf("sell limit violated: oracle price %s < limit %s",
				t.Price, t.LimitPrice)
		}
	}
	notional := t.Quantity.Mul(t.Price)
	if t.IsBuy {
		if a.Cash.LessThan(notional) {
			return fmt.Errorf("insufficient cash: have %s need %s", a.Cash, notional)
		}
	} else {
		have := a.Positions[t.Symbol]
		if have.LessThan(t.Quantity) {
			return fmt.Errorf("insufficient shares of %s: have %s need %s", t.Symbol, have, t.Quantity)
		}
	}
	return nil
}

// Apply mutates the state. Call this ONLY after a block has been committed
// by consensus. Returns an error if validation fails (which should not
// happen if validators did their job).
func (s *State) Apply(t Trade) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.accounts[t.UserID]
	if !ok {
		return fmt.Errorf("unknown user %q", t.UserID)
	}
	if t.Nonce <= a.LastNonce {
		return fmt.Errorf("stale nonce: got %d, last applied %d", t.Nonce, a.LastNonce)
	}
	if _, ok := s.symbols[t.Symbol]; !ok {
		return fmt.Errorf("symbol %q not in whitelist", t.Symbol)
	}
	notional := t.Quantity.Mul(t.Price)
	if t.IsBuy {
		if a.Cash.LessThan(notional) {
			return fmt.Errorf("insufficient cash")
		}
		a.Cash = a.Cash.Sub(notional)
		a.Positions[t.Symbol] = a.Positions[t.Symbol].Add(t.Quantity)
	} else {
		have := a.Positions[t.Symbol]
		if have.LessThan(t.Quantity) {
			return fmt.Errorf("insufficient shares")
		}
		a.Positions[t.Symbol] = have.Sub(t.Quantity)
		a.Cash = a.Cash.Add(notional)
	}
	a.LastNonce = t.Nonce
	return nil
}
