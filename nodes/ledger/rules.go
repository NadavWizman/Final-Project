package ledger

import "sort"

// runRules applies the automatic events of the state machine after the
// block's transactions. They are not transactions: every node runs them on
// the same state with the same block prices, so they need no signature and
// no outside service. Each fires in the first block whose verified price (or
// time, for an option expiry) crosses its threshold.
//
// Order: accounts by address; within an account, resting limit orders,
// stop-loss / take-profit levels, CFD liquidation, option expiry — each by id.
func (s *State) runRules(prices map[string]Cents) {
	for _, addr := range s.sortedAddresses() {
		acc := s.Accounts[addr]
		s.fillResting(acc, prices)
		s.triggerLevels(acc, prices)
		s.liquidate(acc, prices)
		s.expireOptions(acc, prices)
	}
}

// fillResting executes resting limit orders whose price is reached and
// expires those past their time to live.
func (s *State) fillResting(acc *Account, prices map[string]Cents) {
	if len(acc.Resting) == 0 {
		return
	}
	sort.Slice(acc.Resting, func(i, j int) bool { return acc.Resting[i].ID < acc.Resting[j].ID })
	kept := acc.Resting[:0]
	for _, o := range acc.Resting {
		rec := acc.historyRecord(o.ID)
		limit, _ := ParseCents(o.Msg.Limit)
		px, priced := prices[o.Msg.Ticker]
		switch {
		case s.Time >= o.Expires:
			if rec != nil {
				rec.Status, rec.Reason = "EXPIRED", "limit order expired"
			}
		case priced && limitReached(o.Msg.Side, px, limit):
			if err := s.validateOrder(acc, &o.Msg); err != nil {
				if rec != nil {
					rec.Status, rec.Reason = "REJECTED", err.Error()
				}
				continue
			}
			r := rec
			if r == nil { // the OPEN record fell out of the history window
				q, _ := ParseQty(o.Msg.Qty)
				r = &Record{ID: o.ID, Kind: o.Msg.Kind, Side: o.Msg.Side, Ticker: o.Msg.Ticker, Qty: q}
				acc.record(s, r)
			}
			r.Reason = ""
			s.execute(acc, o.Msg, px, r)
		default:
			kept = append(kept, o)
		}
	}
	acc.Resting = kept
}

// triggerLevels fires stop-loss / take-profit levels crossed by this block's
// price. A level never closes more than is still held.
func (s *State) triggerLevels(acc *Account, prices map[string]Cents) {
	sort.Slice(acc.Levels, func(i, j int) bool { return acc.Levels[i].ID < acc.Levels[j].ID })
	for _, l := range append([]*Level(nil), acc.Levels...) {
		if l.Triggered {
			continue
		}
		px, priced := prices[l.Ticker]
		if !priced {
			continue
		}
		long := true
		var c *CFD
		if l.CFD != 0 {
			if c = acc.cfd(l.CFD); c == nil || !c.Open {
				continue
			}
			long = c.Long
		}
		// a stop-loss fires when the price moves against the position, a
		// take-profit when it moves in its favour
		against := (long && px <= l.Price) || (!long && px >= l.Price)
		favour := (long && px >= l.Price) || (!long && px <= l.Price)
		if (l.Kind == "SL" && !against) || (l.Kind == "TP" && !favour) {
			continue
		}
		l.Triggered, l.At = true, s.Time
		r := &Record{ID: s.nextID(), Kind: l.Kind, Side: "SELL", Ticker: l.Ticker, Price: px, Status: "CONFIRMED"}
		if c != nil {
			q := min(l.Qty, c.Qty)
			r.Qty, r.Amount = q, s.closeCFD(acc, c, q, px)
		} else {
			q := min(l.Qty, acc.Holdings[l.Ticker])
			if q <= 0 {
				continue
			}
			p, err := proceeds(q, px)
			if err != nil {
				continue
			}
			s.removeShares(acc, l.Ticker, q)
			acc.Cash += p
			r.Qty, r.Amount = q, p
		}
		acc.record(s, r)
	}
	s.dropLevels(acc, func(l *Level) bool { return l.Triggered && l.At < s.Time-30*24*3600 })
}

// liquidate closes a CFD whose loss has reached LiquidationBps of its margin,
// before the loss can exceed the margin posted.
func (s *State) liquidate(acc *Account, prices map[string]Cents) {
	for _, c := range append([]*CFD(nil), acc.CFDs...) {
		px, priced := prices[c.Ticker]
		if !c.Open || !priced {
			continue
		}
		pnl := cfdPnL(c, c.Qty, px)
		if pnl >= 0 {
			continue
		}
		limit, _ := mulDiv(int64(c.Margin), s.Params.LiquidationBps, 10_000)
		if int64(-pnl) < limit {
			continue
		}
		q := c.Qty
		got := s.closeCFD(acc, c, q, px)
		acc.record(s, &Record{ID: s.nextID(), Kind: "LIQUIDATION", Side: "SELL", Ticker: c.Ticker,
			Qty: q, Price: px, Status: "CONFIRMED", Amount: got,
			Reason: "loss reached the liquidation threshold"})
	}
}

// expireOptions settles options in the first block after their expiry time
// that carries a verified price for the underlying, at intrinsic value.
func (s *State) expireOptions(acc *Account, prices map[string]Cents) {
	for _, o := range append([]*Option(nil), acc.Options...) {
		if o.Status != "OPEN" {
			continue
		}
		cutoff, _ := s.expiryCutoff(o.Expiry)
		px, priced := prices[o.Ticker]
		if s.Time < cutoff || !priced {
			continue
		}
		v := intrinsic(o.Call, px, o.Strike)
		status := "EXPIRED"
		if v > 0 {
			status = "EXERCISED"
		}
		got := s.settleOption(acc, o, v, status)
		acc.record(s, &Record{ID: s.nextID(), Kind: "EXPIRY", Side: "SELL", Ticker: o.Ticker,
			Qty: Qty(o.Contracts * QtyScale), Price: v, Status: "CONFIRMED", Amount: got,
			Reason: "option expired: " + status})
	}
}

// LiquidationPrice is the price at which a CFD is force-closed.
func (s *State) LiquidationPrice(c *CFD) Cents {
	if c.Qty == 0 {
		return 0
	}
	limit, _ := mulDiv(int64(c.Margin), s.Params.LiquidationBps, 10_000)
	move, _ := mulDiv(limit, QtyScale, int64(c.Qty))
	if c.Long {
		return c.Entry - Cents(move)
	}
	return c.Entry + Cents(move)
}

func (a *Account) historyRecord(id uint64) *Record {
	for i := len(a.History) - 1; i >= 0; i-- {
		if a.History[i].ID == id {
			return a.History[i]
		}
	}
	return nil
}
