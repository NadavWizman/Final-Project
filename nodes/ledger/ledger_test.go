package ledger

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

const chain = "tradedesk-test"

// user signs transactions the way the browser does: ECDSA P-256 over SHA-256,
// signature as r‖s.
type user struct {
	key   *ecdsa.PrivateKey
	pub   []byte
	addr  string
	nonce uint64
}

func newUser(t *testing.T) *user {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	pub, _ := k.PublicKey.Bytes()
	return &user{key: k, pub: pub, addr: Address(pub)}
}

func (u *user) sign(t *testing.T, m Msg, withKey bool) []byte {
	t.Helper()
	m.Chain, m.From = chain, u.addr
	m.Nonce = itoa(u.nonce)
	u.nonce++
	msg, _ := json.Marshal(m)
	digest := sha256.Sum256(msg)
	r, s, _ := ecdsa.Sign(rand.Reader, u.key, digest[:])
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	env := Envelope{Msg: string(msg), Sig: base64.StdEncoding.EncodeToString(sig)}
	if withKey {
		env.PubKey = base64.StdEncoding.EncodeToString(u.pub)
	}
	raw, _ := json.Marshal(env)
	return raw
}

func itoa(n uint64) string { b, _ := json.Marshal(n); return string(b) }

func (u *user) register(t *testing.T, name string) []byte {
	return u.sign(t, Msg{Type: "register", Username: name}, true)
}
func (u *user) order(t *testing.T, o OrderMsg) []byte {
	return u.sign(t, Msg{Type: "order", Order: &o}, false)
}

func newLedger() *State { return NewState(DefaultParams(chain, []string{"AAPL", "MSFT", "NVDA"})) }

var height int64

// block applies txs at the given price list and time.
func block(s *State, at int64, prices map[string]Cents, txs ...[]byte) []TxResult {
	height++
	return s.ApplyBlock(height, at, prices, txs)
}

func p(aapl Cents) map[string]Cents { return map[string]Cents{"AAPL": aapl} }

func mustOK(t *testing.T, res []TxResult) {
	t.Helper()
	for _, r := range res {
		if r.Code != CodeOK {
			t.Fatalf("tx failed: %+v", r)
		}
	}
}

func last(a *Account) *Record { return a.History[len(a.History)-1] }

const t0 = int64(1_790_000_000)

func setup(t *testing.T) (*State, *user, *Account) {
	s, u := newLedger(), newUser(t)
	mustOK(t, block(s, t0, nil, u.register(t, "alice")))
	return s, u, s.Accounts[u.addr]
}

func TestAmounts(t *testing.T) {
	for in, want := range map[string]Cents{"190.25": 19025, "1": 100, "0.01": 1, "10.5": 1050} {
		if got, err := ParseCents(in); err != nil || got != want {
			t.Errorf("ParseCents(%q) = %d, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "-1", "1e3", " 1", "1.234", "1.", ".5", "+1", "NaN", "99999999999999999999"} {
		if _, err := ParseCents(bad); err == nil {
			t.Errorf("ParseCents(%q) accepted", bad)
		}
	}
	if c, _ := cost(Qty(1), 19025); c != 2 { // 0.0001 × 190.25 = 0.019025 → rounds up
		t.Errorf("cost rounds up: %d", c)
	}
	if v, _ := proceeds(Qty(1), 19025); v != 1 {
		t.Errorf("proceeds round down: %d", v)
	}
	if _, err := mulDiv(1<<62, 1<<62, 1); err != ErrOverflow {
		t.Error("overflow not detected")
	}
}

func TestRegistration(t *testing.T) {
	s, u, acc := setup(t)
	if acc.Cash != s.Params.Faucet || acc.Username != "alice" || acc.Nonce != 1 {
		t.Fatalf("account = %+v", acc)
	}
	// the same username, another key
	v := newUser(t)
	if r := block(s, t0, nil, v.register(t, "alice")); r[0].Code != CodeInvalid {
		t.Fatal("duplicate username accepted")
	}
	// registering again with the same key
	u.nonce = 0
	if r := block(s, t0, nil, u.register(t, "bob")); r[0].Code != CodeInvalid {
		t.Fatal("re-registration accepted")
	}
}

func TestSignatureNonceAndChainAreEnforced(t *testing.T) {
	s, u, acc := setup(t)
	tx := u.order(t, OrderMsg{Kind: "STOCK", Side: "BUY", Ticker: "AAPL", Qty: "1"})
	mustOK(t, block(s, t0, p(19000), tx))
	cash := acc.Cash

	if r := block(s, t0, p(19000), tx); r[0].Code != CodeInvalid { // replay
		t.Fatal("replayed transaction accepted")
	}
	// tampering with the signed message (e.g. a gateway changing the quantity)
	var env Envelope
	_ = json.Unmarshal(u.order(t, OrderMsg{Kind: "STOCK", Side: "BUY", Ticker: "AAPL", Qty: "1"}), &env)
	env.Msg = strings.Replace(env.Msg, `"qty":"1"`, `"qty":"40"`, 1)
	forged, _ := json.Marshal(env)
	if r := block(s, t0, p(19000), forged); r[0].Code != CodeInvalid || !strings.Contains(r[0].Log, "signature") {
		t.Fatalf("tampered message: %+v", r[0])
	}
	// another user's key signing for alice
	m := newUser(t)
	m.addr = u.addr
	m.nonce = acc.Nonce
	if r := block(s, t0, p(19000), m.order(t, OrderMsg{Kind: "STOCK", Side: "SELL", Ticker: "AAPL", Qty: "1"})); r[0].Code != CodeInvalid {
		t.Fatal("foreign signature accepted")
	}
	if acc.Cash != cash || acc.Holdings["AAPL"] != QtyScale {
		t.Fatal("state changed by invalid transactions")
	}
	// a message for another chain
	s2 := NewState(DefaultParams("other-chain", []string{"AAPL"}))
	if _, err := s2.decode(u.register(t, "x")); err == nil || !strings.Contains(err.Error(), "wrong chain") {
		t.Fatalf("cross-chain replay: %v", err)
	}
}

func TestStockTrading(t *testing.T) {
	s, u, acc := setup(t)
	mustOK(t, block(s, t0, p(19025), u.order(t, OrderMsg{Kind: "STOCK", Side: "BUY", Ticker: "AAPL", Qty: "2.5"})))
	if acc.Cash != 1_000_000-47_563 || acc.Holdings["AAPL"] != 25_000 { // 2.5 × 190.25 = 475.625 → 475.63
		t.Fatalf("after buy: cash %s, shares %s", acc.Cash, acc.Holdings["AAPL"])
	}
	mustOK(t, block(s, t0, p(20000), u.order(t, OrderMsg{Kind: "STOCK", Side: "SELL", Ticker: "AAPL", Qty: "2.5"})))
	if acc.Cash != 1_000_000-47_563+50_000 || acc.Holdings["AAPL"] != 0 {
		t.Fatalf("after sell: cash %s", acc.Cash)
	}
	mustOK(t, block(s, t0, p(20000), u.order(t, OrderMsg{Kind: "STOCK", Side: "SELL", Ticker: "AAPL", Qty: "1"})))
	if r := last(acc); r.Status != "REJECTED" || !strings.Contains(r.Reason, "insufficient shares") {
		t.Fatalf("oversell: %+v", r)
	}
	mustOK(t, block(s, t0, p(20000), u.order(t, OrderMsg{Kind: "STOCK", Side: "BUY", Ticker: "AAPL", Qty: "1000"})))
	if r := last(acc); r.Status != "REJECTED" || !strings.Contains(r.Reason, "insufficient funds") {
		t.Fatalf("overspend: %+v", r)
	}
	mustOK(t, block(s, t0, nil, u.order(t, OrderMsg{Kind: "STOCK", Side: "BUY", Ticker: "AAPL", Qty: "1"})))
	if r := last(acc); r.Status != "REJECTED" || !strings.Contains(r.Reason, "no verified price") {
		t.Fatalf("no price: %+v", r)
	}
	mustOK(t, block(s, t0, p(1), u.order(t, OrderMsg{Kind: "STOCK", Side: "BUY", Ticker: "TSLA", Qty: "1"})))
	if r := last(acc); r.Status != "REJECTED" {
		t.Fatal("unlisted ticker accepted")
	}
}

func TestLimitOrderRestsFillsAndExpires(t *testing.T) {
	s, u, acc := setup(t)
	mustOK(t, block(s, t0, p(20000), u.order(t, OrderMsg{Kind: "STOCK", Side: "BUY", Ticker: "AAPL", Qty: "1", Limit: "190"})))
	if r := last(acc); r.Status != "OPEN" || len(acc.Resting) != 1 {
		t.Fatalf("limit not resting: %+v", r)
	}
	block(s, t0+10, p(19500))
	if len(acc.Resting) != 1 {
		t.Fatal("filled above the limit")
	}
	block(s, t0+20, p(18900))
	if len(acc.Resting) != 0 || acc.Holdings["AAPL"] != QtyScale || acc.History[1].Status != "CONFIRMED" || acc.History[1].Price != 18900 {
		t.Fatalf("limit did not fill at market: %+v", acc.History[1])
	}
	mustOK(t, block(s, t0+30, p(20000), u.order(t, OrderMsg{Kind: "STOCK", Side: "BUY", Ticker: "AAPL", Qty: "1", Limit: "100"})))
	block(s, t0+30+s.Params.LimitTTLSeconds, p(20000))
	if len(acc.Resting) != 0 || last(acc).Status != "EXPIRED" {
		t.Fatalf("limit did not expire: %+v", last(acc))
	}
}

func TestCFDOpenPartialCloseAndLiquidation(t *testing.T) {
	s, u, acc := setup(t)
	mustOK(t, block(s, t0, p(10000), u.order(t, OrderMsg{Kind: "CFD", Side: "BUY", Ticker: "AAPL", Qty: "10", Leverage: "10"})))
	c := acc.CFDs[0]
	if c.Margin != 10_000 || acc.Cash != 990_000 { // $1000 notional / 10
		t.Fatalf("margin %s cash %s", c.Margin, acc.Cash)
	}
	mustOK(t, block(s, t0, p(11000), u.order(t, OrderMsg{Kind: "CFD_CLOSE", Side: "SELL", Ticker: "AAPL", Qty: "4", Position: itoa(c.ID)})))
	if c.Qty != 60_000 || c.Margin != 6_000 || acc.Cash != 990_000+4_000+4_000 {
		t.Fatalf("partial close: %+v cash %s", c, acc.Cash)
	}
	liq := s.LiquidationPrice(c) // 80 % of $60 margin over 6 shares = $8 below entry
	if liq != 9_200 {
		t.Fatalf("liquidation price %s", liq)
	}
	block(s, t0, p(9_201))
	if !c.Open {
		t.Fatal("liquidated above the threshold")
	}
	block(s, t0, p(9_200))
	if c.Open || last(acc).Kind != "LIQUIDATION" {
		t.Fatalf("not liquidated: %+v", last(acc))
	}
}

func TestStopLossAndTakeProfit(t *testing.T) {
	s, u, acc := setup(t)
	mustOK(t, block(s, t0, p(10000), u.order(t, OrderMsg{Kind: "STOCK", Side: "BUY", Ticker: "AAPL", Qty: "10",
		SL: "90", TP: "120", TPQty: "4"})))
	if len(acc.Levels) != 2 {
		t.Fatalf("levels %d", len(acc.Levels))
	}
	block(s, t0, p(12000)) // take-profit sells 4
	if acc.Holdings["AAPL"] != 60_000 || last(acc).Kind != "TP" {
		t.Fatalf("TP: %+v", last(acc))
	}
	block(s, t0, p(8000)) // stop-loss: never sells more than is left (6, not 10)
	if acc.Holdings["AAPL"] != 0 || last(acc).Qty != 60_000 || last(acc).Price != 8000 {
		t.Fatalf("SL: %+v", last(acc))
	}

	// a short CFD's stop-loss fires when the price rises
	mustOK(t, block(s, t0, p(10000), u.order(t, OrderMsg{Kind: "CFD", Side: "SELL", Ticker: "AAPL", Qty: "1", Leverage: "5", SL: "105"})))
	block(s, t0, p(10400))
	if !acc.CFDs[len(acc.CFDs)-1].Open {
		t.Fatal("short SL fired early")
	}
	block(s, t0, p(10500))
	if acc.CFDs[len(acc.CFDs)-1].Open {
		t.Fatal("short SL did not fire")
	}
}

func TestOptions(t *testing.T) {
	s, u, acc := setup(t)
	expiry := "2026-10-16" // t0 is 2026-09-21
	mustOK(t, block(s, t0, p(10000), u.order(t, OrderMsg{Kind: "OPTION", Side: "BUY", Ticker: "AAPL", Qty: "2",
		OptionType: "CALL", Strike: "95", Expiry: expiry})))
	o := acc.Options[0]
	if o.Premium <= 500 || acc.Cash != 1_000_000-o.Premium*200 { // above intrinsic $5
		t.Fatalf("option premium %s, cash %s", o.Premium, acc.Cash)
	}
	for _, bad := range []OrderMsg{
		{Kind: "OPTION", Side: "SELL", Ticker: "AAPL", Qty: "1", OptionType: "CALL", Strike: "95", Expiry: expiry},
		{Kind: "OPTION", Side: "BUY", Ticker: "AAPL", Qty: "0.5", OptionType: "CALL", Strike: "95", Expiry: expiry},
		{Kind: "OPTION", Side: "BUY", Ticker: "AAPL", Qty: "1", OptionType: "CALL", Strike: "95", Expiry: "2020-01-17"},
	} {
		mustOK(t, block(s, t0, p(10000), u.order(t, bad)))
		if last(acc).Status != "REJECTED" {
			t.Fatalf("accepted %+v", bad)
		}
	}
	// expiry settles at intrinsic, in the first block after the cutoff that has a price
	cutoff, _ := s.expiryCutoff(expiry)
	block(s, cutoff+5, nil)
	if o.Status != "OPEN" {
		t.Fatal("expired without a price")
	}
	block(s, cutoff+10, p(11000))
	if o.Status != "EXERCISED" || o.ClosePx != 1500 {
		t.Fatalf("expiry: %+v", o)
	}
}

func TestOptionPremiumModel(t *testing.T) {
	month := int64(30 * 24 * 3600)
	atm := OptionPremium(true, 10000, 10000, month, 3000)
	if atm < 300 || atm > 400 { // ≈ 0.4 × 100 × 0.3 × √(30/365) ≈ $3.44
		t.Fatalf("ATM premium %s", atm)
	}
	if OptionPremium(true, 10000, 9000, month, 3000) < 1000 {
		t.Fatal("ITM premium below intrinsic")
	}
	if OptionPremium(false, 10000, 5000, month, 3000) != 1 {
		t.Fatal("far OTM put should be worth the 1 cent floor")
	}
	if OptionPremium(true, 10000, 9000, 0, 3000) != 1000 {
		t.Fatal("at expiry the premium is intrinsic")
	}
}

// Every node that applies the same blocks gets the same state root.
func TestStateRootIsDeterministic(t *testing.T) {
	a, b := newLedger(), newLedger()
	u, v := newUser(t), newUser(t)
	blocks := [][][]byte{
		{u.register(t, "alice"), v.register(t, "bob")},
		{u.order(t, OrderMsg{Kind: "STOCK", Side: "BUY", Ticker: "AAPL", Qty: "3"}),
			v.order(t, OrderMsg{Kind: "CFD", Side: "SELL", Ticker: "MSFT", Qty: "2", Leverage: "20"})},
	}
	prices := map[string]Cents{"AAPL": 19025, "MSFT": 41000, "NVDA": 12000}
	for i, txs := range blocks {
		a.ApplyBlock(int64(i+1), t0+int64(i), prices, txs)
		b.ApplyBlock(int64(i+1), t0+int64(i), prices, txs)
	}
	if a.Root() != b.Root() {
		t.Fatal("same blocks, different roots")
	}
	c := a.Clone()
	if c.Root() != a.Root() {
		t.Fatal("clone changed the root")
	}
	c.Accounts[u.addr].Cash++
	if c.Root() == a.Root() {
		t.Fatal("a one-cent change did not change the root")
	}
}

func TestLevelLimits(t *testing.T) {
	s, u, acc := setup(t)
	mustOK(t, block(s, t0, p(10000), u.order(t, OrderMsg{Kind: "STOCK", Side: "BUY", Ticker: "AAPL", Qty: "5"})))
	add := func(kind, qty string) TxResult {
		return block(s, t0, nil, u.sign(t, Msg{Type: "level_add", Level: &LevelMsg{Kind: kind, Ticker: "AAPL", Price: "90", Qty: qty}}, false))[0]
	}
	if r := add("SL", "3"); !strings.HasPrefix(r.Log, "CONFIRMED") {
		t.Fatal(r.Log)
	}
	if r := add("SL", "3"); !strings.HasPrefix(r.Log, "REJECTED") {
		t.Fatal("SL total above the holding accepted")
	}
	id := acc.Levels[0].ID
	if r := block(s, t0, nil, u.sign(t, Msg{Type: "level_cancel", LevelID: itoa(id)}, false))[0]; r.Log != "CONFIRMED" {
		t.Fatal(r.Log)
	}
	if len(acc.Levels) != 0 {
		t.Fatal("level not cancelled")
	}
}
