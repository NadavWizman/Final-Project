package ledger

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Result codes of a transaction in a block.
const (
	CodeOK      uint32 = 0 // applied; a business refusal is recorded as a REJECTED order
	CodeInvalid uint32 = 1 // bad signature, wrong nonce, malformed: nothing changed
)

// TxResult is the deterministic outcome of one transaction.
type TxResult struct {
	Code uint32 `json:"code"`
	Log  string `json:"log"`
}

// ApplyBlock applies one block to the state. prices holds the verified
// execution price (the median of the signed oracle quotes in the block) for
// every ticker quoted in it; trades in a ticker without a price wait or fail.
// The same inputs give the same state on every node.
func (s *State) ApplyBlock(height, blockTime int64, prices map[string]Cents, txs [][]byte) []TxResult {
	s.Height, s.Time = height, blockTime
	for _, t := range sortedTickers(prices) {
		s.LastPrices[t] = prices[t]
	}
	results := make([]TxResult, len(txs))
	for i, raw := range txs {
		results[i] = s.applyTx(raw, prices)
	}
	s.runRules(prices)
	return results
}

// Simulate applies one transaction to this state (meant for a scratch copy)
// at the given block height, time and prices, without running the automatic
// rules. Used by the proposer to leave out transactions that would be invalid.
func (s *State) Simulate(height, blockTime int64, prices map[string]Cents, tx []byte) TxResult {
	s.Height, s.Time = height, blockTime
	return s.applyTx(tx, prices)
}

// CheckTx validates a transaction for the mempool against this state, without
// changing it. pendingNonce is the next nonce the mempool expects from the
// sender (it may be ahead of the committed one while earlier transactions of
// the same sender wait in the mempool).
func (s *State) CheckTx(raw []byte, pendingNonce func(addr string) (uint64, bool)) (string, uint64, error) {
	d, err := s.decode(raw)
	if err != nil {
		return "", 0, err
	}
	want := uint64(0)
	if d.msg.Type == "register" {
		if err := s.checkRegister(d); err != nil {
			return "", 0, err
		}
	} else if acc := s.Accounts[d.msg.From]; acc != nil {
		want = acc.Nonce
	}
	if n, ok := pendingNonce(d.msg.From); ok && n > want {
		want = n
	}
	if d.nonce != want {
		return "", 0, fmt.Errorf("wrong nonce: expected %d, got %d", want, d.nonce)
	}
	return d.msg.From, d.nonce, nil
}

func (s *State) applyTx(raw []byte, prices map[string]Cents) TxResult {
	d, err := s.decode(raw)
	if err != nil {
		return TxResult{Code: CodeInvalid, Log: err.Error()}
	}
	m := d.msg

	if m.Type == "register" {
		return s.register(d)
	}
	acc := s.Accounts[m.From]
	if acc.Holdings == nil {
		acc.Holdings = map[string]Qty{}
	}
	if d.nonce != acc.Nonce {
		return TxResult{Code: CodeInvalid, Log: fmt.Sprintf("wrong nonce: expected %d, got %d", acc.Nonce, d.nonce)}
	}
	// A market order needs this block's verified price. Without one it is
	// not part of this block at all — invalid here, nonce untouched — so it
	// waits in the mempool for a block that has the price. (Recording it as
	// refused would let a proposer that leaves out a price burn users' orders.)
	if o := m.Order; m.Type == "order" && o != nil && o.Limit == "" && s.listed(o.Ticker) {
		if _, ok := prices[o.Ticker]; !ok {
			return TxResult{Code: CodeInvalid, Log: "no verified price for " + o.Ticker + " in this block"}
		}
	}
	// The nonce is used up even when the trade itself is refused, so a signed
	// transaction can never be replayed.
	acc.Nonce++

	switch m.Type {
	case "deposit":
		return s.deposit(acc, m.Amount)
	case "order":
		if m.Order == nil {
			return TxResult{Code: CodeOK, Log: "rejected: order missing"}
		}
		r := s.placeOrder(acc, *m.Order, prices)
		return TxResult{Code: CodeOK, Log: r.Status + " " + r.Reason}
	case "level_add":
		return s.addLevel(acc, m.Level)
	case "level_cancel":
		return s.cancelLevel(acc, m.LevelID)
	default:
		return TxResult{Code: CodeOK, Log: "rejected: unknown transaction type"}
	}
}

// checkRegister is everything that makes a registration invalid. CheckTx
// runs it too, so a registration that could never be included does not sit
// in the mempool.
func (s *State) checkRegister(d *decoded) error {
	m := d.msg
	switch {
	case s.Accounts[m.From] != nil:
		return errors.New("account already registered")
	case d.nonce != 0:
		return errors.New("registration must use nonce 0")
	case !usernameRe.MatchString(m.Username):
		return errors.New("username must be 3-32 letters, digits, '.', '_' or '-'")
	}
	if _, taken := s.Usernames[usernameKey(m.Username)]; taken {
		return errors.New("username already taken")
	}
	return nil
}

// usernameKey makes names unique regardless of case ("Alice" = "alice"),
// so one user cannot pass for another by capitalisation.
func usernameKey(name string) string { return strings.ToLower(name) }

// AddressOf returns the address registered under a username.
func (s *State) AddressOf(name string) (string, bool) {
	a, ok := s.Usernames[usernameKey(name)]
	return a, ok
}

func (s *State) register(d *decoded) TxResult {
	m := d.msg
	if err := s.checkRegister(d); err != nil {
		return TxResult{Code: CodeInvalid, Log: err.Error()}
	}
	acc := &Account{
		Address: m.From, Username: m.Username, PubKey: d.pubKey, Nonce: 1,
		Cash: s.Params.Faucet, Holdings: map[string]Qty{},
	}
	s.Accounts[m.From] = acc
	s.Usernames[usernameKey(m.Username)] = m.From
	acc.record(s, &Record{ID: s.nextID(), Kind: "DEPOSIT", Status: "CONFIRMED",
		Amount: s.Params.Faucet, Reason: "registration grant (faucet)"})
	return TxResult{Code: CodeOK, Log: "registered"}
}

func (s *State) deposit(acc *Account, amount string) TxResult {
	v, err := ParseCents(amount)
	if err != nil || v <= 0 || v > s.Params.MaxDeposit {
		return s.reject(acc, &Record{Kind: "DEPOSIT"}, fmt.Sprintf("deposit must be between 0.01 and %s", s.Params.MaxDeposit))
	}
	cash, err := addCents(acc.Cash, v)
	if err != nil {
		return s.reject(acc, &Record{Kind: "DEPOSIT"}, err.Error())
	}
	acc.Cash = cash
	acc.record(s, &Record{ID: s.nextID(), Kind: "DEPOSIT", Status: "CONFIRMED", Amount: v, Reason: "demo faucet"})
	return TxResult{Code: CodeOK, Log: "CONFIRMED"}
}

func (s *State) reject(acc *Account, r *Record, reason string) TxResult {
	r.ID, r.Status, r.Reason = s.nextID(), "REJECTED", reason
	acc.record(s, r)
	return TxResult{Code: CodeOK, Log: "REJECTED " + reason}
}

// placeOrder validates an order and either executes it at this block's price
// or, for a limit order whose price is not reached, rests it.
func (s *State) placeOrder(acc *Account, o OrderMsg, prices map[string]Cents) *Record {
	r := &Record{ID: s.nextID(), Kind: o.Kind, Side: o.Side, Ticker: o.Ticker}
	fail := func(reason string) *Record {
		r.Status, r.Reason = "REJECTED", reason
		acc.record(s, r)
		return r
	}
	if err := s.validateOrder(acc, &o); err != nil {
		return fail(err.Error())
	}
	r.Qty, _ = ParseQty(o.Qty)

	px, priced := prices[o.Ticker]
	if o.Limit != "" {
		limit, _ := ParseCents(o.Limit)
		if !priced || !limitReached(o.Side, px, limit) {
			acc.Resting = append(acc.Resting, &Order{ID: r.ID, Msg: o, Expires: s.Time + s.Params.LimitTTLSeconds})
			r.Status, r.Reason = "OPEN", "resting until the price reaches "+limit.String()
			acc.record(s, r)
			return r
		}
	} else if !priced { // only reachable for an unlisted ticker, refused above
		return fail("no verified price for " + o.Ticker + " in this block")
	}
	s.execute(acc, o, px, r)
	acc.record(s, r)
	return r
}

func limitReached(side string, px, limit Cents) bool {
	if side == "BUY" {
		return px <= limit
	}
	return px >= limit
}

// validateOrder checks everything about an order that does not depend on the
// price: fields, ownership, expiry. Balances are checked at execution.
func (s *State) validateOrder(acc *Account, o *OrderMsg) error {
	if !s.listed(o.Ticker) {
		return fmt.Errorf("%s is not a listed ticker", o.Ticker)
	}
	if o.Limit != "" && len(acc.Resting) >= MaxResting {
		return fmt.Errorf("at most %d open limit orders per account", MaxResting)
	}
	if (o.SL != "" || o.TP != "") && activeLevels(acc)+2 > MaxLevels {
		return fmt.Errorf("at most %d stop-loss/take-profit levels per account", MaxLevels)
	}
	if o.Side != "BUY" && o.Side != "SELL" {
		return fmt.Errorf("side must be BUY or SELL")
	}
	q, err := ParseQty(o.Qty)
	if err != nil || q <= 0 {
		return fmt.Errorf("quantity must be a positive number with at most 4 decimals")
	}
	if o.Limit != "" {
		if l, err := ParseCents(o.Limit); err != nil || l <= 0 {
			return fmt.Errorf("invalid limit price")
		}
	}
	for _, p := range []string{o.SL, o.TP} {
		if p != "" {
			if v, err := ParseCents(p); err != nil || v <= 0 {
				return fmt.Errorf("invalid stop-loss / take-profit price")
			}
		}
	}
	for _, p := range []string{o.SLQty, o.TPQty} {
		if p != "" {
			if v, err := ParseQty(p); err != nil || v <= 0 || v > q {
				return fmt.Errorf("stop-loss / take-profit quantity must be between 0 and the order quantity")
			}
		}
	}
	if (o.SL != "" || o.TP != "") && !(o.Kind == "CFD" || (o.Kind == "STOCK" && o.Side == "BUY")) {
		return fmt.Errorf("stop-loss / take-profit can only be attached to a stock BUY or a CFD")
	}

	switch o.Kind {
	case "STOCK":
	case "CFD":
		lev, err := strconv.ParseInt(o.Leverage, 10, 64)
		if err != nil || lev < s.Params.MinLeverage || lev > s.Params.MaxLeverage {
			return fmt.Errorf("leverage must be between %d and %d", s.Params.MinLeverage, s.Params.MaxLeverage)
		}
	case "CFD_CLOSE":
		c := acc.cfd(parseID(o.Position))
		if c == nil || !c.Open || c.Ticker != o.Ticker {
			return fmt.Errorf("no open CFD position %s on %s", o.Position, o.Ticker)
		}
	case "OPTION":
		if o.Side != "BUY" {
			return fmt.Errorf("only buying options is supported")
		}
		if q%QtyScale != 0 {
			return fmt.Errorf("options trade in whole contracts")
		}
		if o.OptionType != "CALL" && o.OptionType != "PUT" {
			return fmt.Errorf("option type must be CALL or PUT")
		}
		if k, err := ParseCents(o.Strike); err != nil || k <= 0 {
			return fmt.Errorf("invalid strike")
		}
		cutoff, err := s.expiryCutoff(o.Expiry)
		if err != nil {
			return err
		}
		if cutoff <= s.Time {
			return fmt.Errorf("this option has already expired")
		}
		if cutoff-s.Time > 3*365*24*3600 {
			return fmt.Errorf("expiry is more than 3 years away")
		}
	case "OPT_CLOSE", "OPT_EXER":
		opt := acc.option(parseID(o.Position))
		if opt == nil || opt.Status != "OPEN" || opt.Ticker != o.Ticker {
			return fmt.Errorf("no open option position %s on %s", o.Position, o.Ticker)
		}
		cutoff, _ := s.expiryCutoff(opt.Expiry)
		if cutoff <= s.Time {
			return fmt.Errorf("this option has expired")
		}
	default:
		return fmt.Errorf("unknown order kind %q", o.Kind)
	}
	if o.Kind != "STOCK" && o.Kind != "CFD" && o.Limit != "" {
		return fmt.Errorf("limit prices apply to stock and CFD orders only")
	}
	return nil
}

// execute performs a validated order at price px and fills in r.
func (s *State) execute(acc *Account, o OrderMsg, px Cents, r *Record) {
	q, _ := ParseQty(o.Qty)
	r.Price = px
	done := func(amount Cents) { r.Status, r.Amount = "CONFIRMED", amount }
	fail := func(reason string) { r.Status, r.Reason = "REJECTED", reason }

	switch o.Kind {
	case "STOCK":
		if o.Side == "BUY" {
			c, err := cost(q, px)
			if err != nil || c > acc.Cash {
				fail(fmt.Sprintf("insufficient funds: need %s, have %s", c, acc.Cash))
				return
			}
			acc.Cash -= c
			acc.Holdings[o.Ticker] += q
			s.attachLevels(acc, o, o.Ticker, 0)
			done(-c)
			return
		}
		if acc.Holdings[o.Ticker] < q {
			fail(fmt.Sprintf("insufficient shares: have %s", acc.Holdings[o.Ticker]))
			return
		}
		p, err := proceeds(q, px)
		if err != nil {
			fail(err.Error())
			return
		}
		s.removeShares(acc, o.Ticker, q)
		acc.Cash += p
		done(p)

	case "CFD":
		lev, _ := strconv.ParseInt(o.Leverage, 10, 64)
		notional, err := cost(q, px)
		if err != nil {
			fail(err.Error())
			return
		}
		margin := Cents((int64(notional) + lev - 1) / lev)
		if margin > acc.Cash {
			fail(fmt.Sprintf("insufficient margin: need %s, have %s", margin, acc.Cash))
			return
		}
		acc.Cash -= margin
		c := &CFD{ID: s.nextID(), Ticker: o.Ticker, Long: o.Side == "BUY", Qty: q, Entry: px,
			Leverage: lev, Margin: margin, Open: true, OpenedAt: s.Time}
		acc.CFDs = append(acc.CFDs, c)
		s.attachLevels(acc, o, o.Ticker, c.ID)
		done(-margin)

	case "CFD_CLOSE":
		c := acc.cfd(parseID(o.Position))
		returned := s.closeCFD(acc, c, min(q, c.Qty), px)
		done(returned)

	case "OPTION":
		k, _ := ParseCents(o.Strike)
		cutoff, _ := s.expiryCutoff(o.Expiry)
		premium := OptionPremium(o.OptionType == "CALL", px, k, cutoff-s.Time, s.Params.OptionVolBps)
		contracts := int64(q / QtyScale)
		total, err := mulDiv(int64(premium), 100*contracts, 1)
		if err != nil || Cents(total) > acc.Cash {
			fail(fmt.Sprintf("insufficient funds: need %s, have %s", Cents(total), acc.Cash))
			return
		}
		acc.Cash -= Cents(total)
		acc.Options = append(acc.Options, &Option{ID: s.nextID(), Ticker: o.Ticker, Call: o.OptionType == "CALL",
			Strike: k, Expiry: o.Expiry, Contracts: contracts, Premium: premium, Status: "OPEN", OpenedAt: s.Time})
		r.Price = premium
		done(-Cents(total))

	case "OPT_CLOSE", "OPT_EXER":
		opt := acc.option(parseID(o.Position))
		cutoff, _ := s.expiryCutoff(opt.Expiry)
		perShare := OptionPremium(opt.Call, px, opt.Strike, cutoff-s.Time, s.Params.OptionVolBps)
		status := "CLOSED"
		if o.Kind == "OPT_EXER" {
			perShare = intrinsic(opt.Call, px, opt.Strike)
			if perShare <= 0 {
				fail("option is out of the money")
				return
			}
			status = "EXERCISED"
		}
		got := s.settleOption(acc, opt, perShare, status)
		r.Price = perShare
		done(got)
	}
}

// attachLevels creates the stop-loss / take-profit given with an order.
func (s *State) attachLevels(acc *Account, o OrderMsg, ticker string, cfdID uint64) {
	q, _ := ParseQty(o.Qty)
	for _, l := range []struct{ kind, price, qty string }{{"SL", o.SL, o.SLQty}, {"TP", o.TP, o.TPQty}} {
		if l.price == "" {
			continue
		}
		p, _ := ParseCents(l.price)
		lq := q
		if l.qty != "" {
			lq, _ = ParseQty(l.qty)
		}
		acc.Levels = append(acc.Levels, &Level{ID: s.nextID(), Kind: l.kind, Ticker: ticker, CFD: cfdID, Price: p, Qty: lq})
	}
}

// closeCFD closes qty of a CFD at px and returns the cash credited.
func (s *State) closeCFD(acc *Account, c *CFD, qty Qty, px Cents) Cents {
	share := c.Margin
	if qty < c.Qty {
		m, _ := mulDiv(int64(c.Margin), int64(qty), int64(c.Qty))
		share = Cents(m)
	}
	pnl := cfdPnL(c, qty, px)
	back := share + pnl
	if back < 0 {
		// The price gapped past the liquidation level (e.g. at the market
		// open): the loss beyond the margin is charged to the account's cash,
		// as far as it goes. Forgiving it let a hedged long+short pair turn
		// a gap into free money.
		acc.Cash -= min(-back, acc.Cash)
		back = 0
	}
	acc.Cash += back
	c.Qty -= qty
	c.Margin -= share
	c.PnL += pnl
	if c.Qty == 0 {
		c.Open, c.ClosePx, c.ClosedAt = false, px, s.Time
		s.dropLevels(acc, func(l *Level) bool { return l.CFD == c.ID })
		s.pruneClosed(acc)
	}
	return back
}

// cfdPnL is the profit (or loss, negative) of qty of c at px.
func cfdPnL(c *CFD, qty Qty, px Cents) Cents {
	diff := int64(px - c.Entry)
	if !c.Long {
		diff = -diff
	}
	abs := diff
	if abs < 0 {
		abs = -abs
	}
	// gains round down and losses round up, so splitting a close into many
	// small pieces cannot collect rounding cents
	if diff < 0 {
		v, _ := mulDivUp(abs, int64(qty), QtyScale)
		return -Cents(v)
	}
	v, _ := mulDiv(abs, int64(qty), QtyScale)
	return Cents(v)
}

// settleOption pays perShare × 100 × contracts and closes the option.
func (s *State) settleOption(acc *Account, opt *Option, perShare Cents, status string) Cents {
	got, _ := mulDiv(int64(perShare), 100*opt.Contracts, 1)
	paid, _ := mulDiv(int64(opt.Premium), 100*opt.Contracts, 1)
	acc.Cash += Cents(got)
	opt.Status, opt.ClosePx, opt.PnL, opt.ClosedAt = status, perShare, Cents(got-paid), s.Time
	s.pruneClosed(acc)
	return Cents(got)
}

// removeShares sells qty of a holding; when it reaches zero, its open
// stop-loss / take-profit levels go too.
func (s *State) removeShares(acc *Account, ticker string, q Qty) {
	acc.Holdings[ticker] -= q
	if acc.Holdings[ticker] == 0 {
		delete(acc.Holdings, ticker)
		s.dropLevels(acc, func(l *Level) bool { return l.CFD == 0 && l.Ticker == ticker && !l.Triggered })
	}
}

func (s *State) dropLevels(acc *Account, match func(*Level) bool) {
	kept := acc.Levels[:0]
	for _, l := range acc.Levels {
		if !match(l) {
			kept = append(kept, l)
		}
	}
	acc.Levels = kept
}

// pruneClosed keeps at most 50 closed CFDs and 50 settled options per account.
func (s *State) pruneClosed(acc *Account) {
	const keep = 50
	var openC, closedC []*CFD
	for _, c := range acc.CFDs {
		if c.Open {
			openC = append(openC, c)
		} else {
			closedC = append(closedC, c)
		}
	}
	if len(closedC) > keep {
		closedC = closedC[len(closedC)-keep:]
	}
	acc.CFDs = append(openC, closedC...)
	sort.Slice(acc.CFDs, func(i, j int) bool { return acc.CFDs[i].ID < acc.CFDs[j].ID })

	var openO, doneO []*Option
	for _, o := range acc.Options {
		if o.Status == "OPEN" {
			openO = append(openO, o)
		} else {
			doneO = append(doneO, o)
		}
	}
	if len(doneO) > keep {
		doneO = doneO[len(doneO)-keep:]
	}
	acc.Options = append(openO, doneO...)
	sort.Slice(acc.Options, func(i, j int) bool { return acc.Options[i].ID < acc.Options[j].ID })
}

func (s *State) addLevel(acc *Account, lm *LevelMsg) TxResult {
	r := &Record{Kind: "LEVEL"}
	if lm == nil || (lm.Kind != "SL" && lm.Kind != "TP") {
		return s.reject(acc, r, "level kind must be SL or TP")
	}
	p, err := ParseCents(lm.Price)
	if err != nil || p <= 0 {
		return s.reject(acc, r, "invalid level price")
	}
	q, err := ParseQty(lm.Qty)
	if err != nil || q <= 0 {
		return s.reject(acc, r, "invalid level quantity")
	}
	if activeLevels(acc) >= MaxLevels {
		return s.reject(acc, r, fmt.Sprintf("at most %d stop-loss/take-profit levels per account", MaxLevels))
	}
	lvl := &Level{Kind: lm.Kind, Price: p, Qty: q}
	var size Qty
	if lm.CFD != "" {
		c := acc.cfd(parseID(lm.CFD))
		if c == nil || !c.Open {
			return s.reject(acc, r, "no open CFD position "+lm.CFD)
		}
		lvl.CFD, lvl.Ticker, size = c.ID, c.Ticker, c.Qty
	} else {
		if acc.Holdings[lm.Ticker] <= 0 {
			return s.reject(acc, r, "no holding in "+lm.Ticker)
		}
		lvl.Ticker, size = lm.Ticker, acc.Holdings[lm.Ticker]
	}
	used := Qty(0)
	for _, l := range acc.Levels {
		if !l.Triggered && l.Kind == lvl.Kind && l.CFD == lvl.CFD && (lvl.CFD != 0 || l.Ticker == lvl.Ticker) {
			used += l.Qty
		}
	}
	if used+q > size {
		return s.reject(acc, r, fmt.Sprintf("total %s quantity %s exceeds the position %s", lvl.Kind, used+q, size))
	}
	lvl.ID = s.nextID()
	acc.Levels = append(acc.Levels, lvl)
	return TxResult{Code: CodeOK, Log: "CONFIRMED level " + strconv.FormatUint(lvl.ID, 10)}
}

func (s *State) cancelLevel(acc *Account, id string) TxResult {
	want := parseID(id)
	for _, l := range acc.Levels {
		if l.ID == want {
			if l.Triggered {
				return s.reject(acc, &Record{Kind: "LEVEL"}, "level already triggered")
			}
			s.dropLevels(acc, func(x *Level) bool { return x.ID == want })
			return TxResult{Code: CodeOK, Log: "CONFIRMED"}
		}
	}
	return s.reject(acc, &Record{Kind: "LEVEL"}, "no such level")
}

// ExpiryCutoff is the unix time at which an option dated YYYY-MM-DD expires.
func (s *State) ExpiryCutoff(date string) (int64, error) { return s.expiryCutoff(date) }

func (s *State) expiryCutoff(date string) (int64, error) {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return 0, fmt.Errorf("expiry must be a date YYYY-MM-DD")
	}
	return t.Unix() + s.Params.OptionExpiryUTCHour*3600, nil
}

func parseID(s string) uint64 {
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

func sortedTickers(m map[string]Cents) []string {
	out := make([]string, 0, len(m))
	for t := range m {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Every account's open limit orders and levels are checked in every block,
// so their number is bounded (there are no fees to discourage spam).
const (
	MaxResting = 50
	MaxLevels  = 50
)

func activeLevels(acc *Account) int {
	n := 0
	for _, l := range acc.Levels {
		if !l.Triggered {
			n++
		}
	}
	return n
}
