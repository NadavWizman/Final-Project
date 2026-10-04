package ledger

import (
	"sort"
)

// State is the whole ledger: every account, balance and position. Every node
// holds its own copy and applies the same blocks to it, so all copies stay
// identical — which the state root (root.go) lets anyone check.
//
// Maps are fine for storage (encoding/json writes their keys sorted, so the
// state root is deterministic), but every loop that changes state iterates in
// sorted order (sortedKeys) — Go randomises map iteration.
type State struct {
	Height int64 `json:"height"` // last applied block
	Time   int64 `json:"time"`   // that block's time, unix seconds

	Params Params `json:"params"`

	Accounts  map[string]*Account `json:"accounts"`  // by address
	Usernames map[string]string   `json:"usernames"` // username → address

	// LastPrices is the last execution (median) price seen per ticker.
	LastPrices map[string]Cents `json:"last_prices"`

	NextID uint64 `json:"next_id"` // ids for orders, positions and levels

	CustodySeq uint64 `json:"custody_seq"` // deposits applied so far (each carries the next number)
	FaucetPaid Cents  `json:"faucet_paid"` // registration grants paid so far, capped by Params.FaucetTotal
}

// Params are fixed in the genesis file and identical on every node.
type Params struct {
	ChainID             string   `json:"chain_id"`               // signed into every transaction
	Tickers             []string `json:"tickers"`                // tradable symbols, sorted
	Faucet              Cents    `json:"faucet"`                 // granted at registration
	FaucetTotal         Cents    `json:"faucet_total"`           // all registration grants together never exceed this
	CustodyKey          []byte   `json:"custody_key"`            // Ed25519 key of the custodian; the only signer of deposits
	MaxDeposit          Cents    `json:"max_deposit"`            // per deposit
	MinLeverage         int64    `json:"min_leverage"`           // CFD
	MaxLeverage         int64    `json:"max_leverage"`           // CFD
	LiquidationBps      int64    `json:"liquidation_bps"`        // CFD loss / margin that forces a close
	LimitTTLSeconds     int64    `json:"limit_ttl_seconds"`      // resting limit orders expire
	OptionVolBps        int64    `json:"option_vol_bps"`         // annual volatility for the option model
	OptionExpiryUTCHour int64    `json:"option_expiry_utc_hour"` // options expire at this hour on their date
	MaxHistory          int      `json:"max_history"`            // order records kept per account
}

// DefaultParams are used by tests and by the generated genesis file.
func DefaultParams(chainID string, tickers []string) Params {
	t := append([]string(nil), tickers...)
	sort.Strings(t)
	return Params{
		ChainID:             chainID,
		Tickers:             t,
		Faucet:              1_000_000,      // $10,000.00
		FaucetTotal:         10_000_000_000, // $100,000,000.00: 10,000 grants
		MaxDeposit:          100_000_000,    // $1,000,000.00
		MinLeverage:         2,
		MaxLeverage:         100,
		LiquidationBps:      8_000, // 80 %
		LimitTTLSeconds:     24 * 3600,
		OptionVolBps:        3_000, // 30 % a year
		OptionExpiryUTCHour: 20,    // 16:00 New York
		MaxHistory:          200,
	}
}

// Account is one user, identified by the hash of their public key.
type Account struct {
	Address  string `json:"address"`
	Username string `json:"username"`
	PubKey   []byte `json:"pubkey"` // P-256, 65-byte uncompressed point
	Nonce    uint64 `json:"nonce"`  // next expected transaction number
	Cash     Cents  `json:"cash"`

	Holdings map[string]Qty `json:"holdings"` // ticker → shares
	CFDs     []*CFD         `json:"cfds"`
	Options  []*Option      `json:"options"`
	Levels   []*Level       `json:"levels"`  // stop-loss / take-profit
	Resting  []*Order       `json:"resting"` // limit orders waiting for their price
	History  []*Record      `json:"history"` // most recent last
}

// CFD is a leveraged position on the price of a stock.
type CFD struct {
	ID       uint64 `json:"id"`
	Ticker   string `json:"ticker"`
	Long     bool   `json:"long"`
	Qty      Qty    `json:"qty"`
	Entry    Cents  `json:"entry"`
	Leverage int64  `json:"leverage"`
	Margin   Cents  `json:"margin"`
	Open     bool   `json:"open"`
	PnL      Cents  `json:"pnl"`
	ClosePx  Cents  `json:"close_px,omitempty"`
	OpenedAt int64  `json:"opened_at"`
	ClosedAt int64  `json:"closed_at,omitempty"`
}

// Option is a bought CALL or PUT contract (100 shares each).
type Option struct {
	ID        uint64 `json:"id"`
	Ticker    string `json:"ticker"`
	Call      bool   `json:"call"`
	Strike    Cents  `json:"strike"`
	Expiry    string `json:"expiry"` // YYYY-MM-DD
	Contracts int64  `json:"contracts"`
	Premium   Cents  `json:"premium"` // paid per share
	Status    string `json:"status"`  // OPEN, CLOSED, EXERCISED, EXPIRED
	ClosePx   Cents  `json:"close_px,omitempty"`
	PnL       Cents  `json:"pnl"`
	OpenedAt  int64  `json:"opened_at"`
	ClosedAt  int64  `json:"closed_at,omitempty"`
}

// Level is a stop-loss or take-profit on a stock holding or a CFD.
type Level struct {
	ID        uint64 `json:"id"`
	Kind      string `json:"kind"`   // SL or TP
	Ticker    string `json:"ticker"` // the stock, for a holding level
	CFD       uint64 `json:"cfd,omitempty"`
	Price     Cents  `json:"price"`
	Qty       Qty    `json:"qty"`
	Triggered bool   `json:"triggered"`
	At        int64  `json:"at,omitempty"`
}

// Order is a limit order resting until its price is reached.
type Order struct {
	ID      uint64   `json:"id"`
	Msg     OrderMsg `json:"msg"`
	Expires int64    `json:"expires"`
}

// Record is one entry in an account's order history.
type Record struct {
	ID     uint64 `json:"id"`
	Height int64  `json:"height"`
	Time   int64  `json:"time"`
	Kind   string `json:"kind"` // STOCK, CFD, CFD_CLOSE, OPTION, OPT_CLOSE, OPT_EXER, DEPOSIT, SL, TP, LIQUIDATION, EXPIRY
	Side   string `json:"side"` // BUY or SELL
	Ticker string `json:"ticker"`
	Qty    Qty    `json:"qty"`
	Status string `json:"status"` // OPEN (resting), CONFIRMED, REJECTED, EXPIRED
	Price  Cents  `json:"price,omitempty"`
	Amount Cents  `json:"amount,omitempty"` // cash moved, signed: + credit, − debit
	Reason string `json:"reason,omitempty"`
}

// NewState returns an empty ledger with the given parameters.
func NewState(p Params) *State {
	return &State{
		Params:     p,
		Accounts:   map[string]*Account{},
		Usernames:  map[string]string{},
		LastPrices: map[string]Cents{},
		NextID:     1,
	}
}

func (s *State) nextID() uint64 {
	id := s.NextID
	s.NextID++
	return id
}

// Listed reports whether a ticker can be traded.
func (s *State) Listed(ticker string) bool { return s.listed(ticker) }

func (s *State) listed(ticker string) bool {
	i := sort.SearchStrings(s.Params.Tickers, ticker)
	return i < len(s.Params.Tickers) && s.Params.Tickers[i] == ticker
}

// sortedAddresses returns account addresses in a fixed order.
func (s *State) sortedAddresses() []string {
	out := make([]string, 0, len(s.Accounts))
	for a := range s.Accounts {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

func (a *Account) record(s *State, r *Record) {
	r.Height, r.Time = s.Height, s.Time
	a.History = append(a.History, r)
	if over := len(a.History) - s.Params.MaxHistory; over > 0 {
		a.History = append([]*Record(nil), a.History[over:]...)
	}
}

func (a *Account) cfd(id uint64) *CFD {
	for _, c := range a.CFDs {
		if c.ID == id {
			return c
		}
	}
	return nil
}

func (a *Account) option(id uint64) *Option {
	for _, o := range a.Options {
		if o.ID == id {
			return o
		}
	}
	return nil
}
