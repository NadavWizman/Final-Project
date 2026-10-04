package ledger

import (
	"strings"
	"testing"
)

// Edge cases carried over from the previous system's settlement tests
// (backend/trading/tests.py, removed with that system): each one that still
// describes a rule of the ledger is checked here against the state machine.

func rejected(t *testing.T, acc *Account, want string) {
	t.Helper()
	r := last(acc)
	if r.Status != "REJECTED" || !strings.Contains(r.Reason, want) {
		t.Fatalf("want REJECTED %q, got %s %q", want, r.Status, r.Reason)
	}
}

// Orders that must be refused before anything moves (old OrderCreationTests,
// OrderExecutionTests, OrderSLTPTests).
func TestInvalidOrdersAreRefused(t *testing.T) {
	s, u, acc := setup(t)
	for _, c := range []struct {
		o    OrderMsg
		want string
	}{
		{OrderMsg{Kind: "STOCK", Side: "BUY", Ticker: "TSLA", Qty: "1"}, "not a listed ticker"},
		{OrderMsg{Kind: "STOCK", Side: "BUY", Ticker: "AAPL", Qty: "0"}, "quantity"},
		{OrderMsg{Kind: "STOCK", Side: "BUY", Ticker: "AAPL", Qty: "-1"}, "quantity"},
		{OrderMsg{Kind: "STOCK", Side: "BUY", Ticker: "AAPL", Qty: "1.00001"}, "quantity"},
		{OrderMsg{Kind: "STOCK", Side: "BUY", Ticker: "AAPL", Qty: "1e3"}, "quantity"},
		{OrderMsg{Kind: "STOCK", Side: "BUY", Ticker: "AAPL", Qty: "1", Limit: "-5"}, "invalid limit"},
		{OrderMsg{Kind: "STOCK", Side: "BUY", Ticker: "AAPL", Qty: "1", SL: "0"}, "stop-loss"},
		{OrderMsg{Kind: "STOCK", Side: "BUY", Ticker: "AAPL", Qty: "1", SL: "90", SLQty: "2"}, "between 0 and the order quantity"},
		{OrderMsg{Kind: "STOCK", Side: "SELL", Ticker: "AAPL", Qty: "1", SL: "90"}, "only be attached"},
		{OrderMsg{Kind: "CFD", Side: "BUY", Ticker: "AAPL", Qty: "1", Leverage: "1"}, "leverage"},
		{OrderMsg{Kind: "CFD", Side: "BUY", Ticker: "AAPL", Qty: "1", Leverage: "101"}, "leverage"},
		{OrderMsg{Kind: "CFD_CLOSE", Side: "SELL", Ticker: "AAPL", Qty: "1", Position: "999"}, "no open CFD position"},
		{OrderMsg{Kind: "OPT_CLOSE", Side: "SELL", Ticker: "AAPL", Qty: "1", Position: "999"}, "no open option position"},
		{OrderMsg{Kind: "OPTION", Side: "BUY", Ticker: "AAPL", Qty: "1", OptionType: "CALL", Strike: "95", Expiry: "2030-01-18"}, "more than 3 years"},
		{OrderMsg{Kind: "OPTION", Side: "BUY", Ticker: "AAPL", Qty: "1", OptionType: "CALL", Strike: "0", Expiry: "2026-10-16"}, "invalid strike"},
		{OrderMsg{Kind: "OPTION", Side: "BUY", Ticker: "AAPL", Qty: "1", OptionType: "CALL", Strike: "95", Expiry: "16/10/2026"}, "YYYY-MM-DD"},
		{OrderMsg{Kind: "OPTION", Side: "BUY", Ticker: "AAPL", Qty: "1", OptionType: "CALL", Strike: "95", Expiry: "2026-10-16", Limit: "1"}, "limit prices apply"},
	} {
		before := acc.Cash
		mustOK(t, block(s, t0, p(10000), u.order(t, c.o)))
		rejected(t, acc, c.want)
		if acc.Cash != before {
			t.Fatalf("a refused order moved money: %+v", c.o)
		}
	}
}

// Not enough money or shares: nothing moves (old OrderExecutionTests).
func TestInsufficientFundsAndShares(t *testing.T) {
	s, u, acc := setup(t)
	mustOK(t, block(s, t0, p(10000), u.order(t, OrderMsg{Kind: "CFD", Side: "BUY", Ticker: "AAPL", Qty: "1001", Leverage: "10"})))
	rejected(t, acc, "insufficient margin")
	mustOK(t, block(s, t0, p(10000), u.order(t, OrderMsg{Kind: "OPTION", Side: "BUY", Ticker: "AAPL", Qty: "1000",
		OptionType: "CALL", Strike: "50", Expiry: "2026-10-16"})))
	rejected(t, acc, "insufficient funds")
	// selling more than is held, also through a limit order that fills later
	mustOK(t, block(s, t0, p(10000), u.order(t, OrderMsg{Kind: "STOCK", Side: "BUY", Ticker: "AAPL", Qty: "2"})))
	mustOK(t, block(s, t0, p(10000), u.order(t, OrderMsg{Kind: "STOCK", Side: "SELL", Ticker: "AAPL", Qty: "3", Limit: "150"})))
	block(s, t0+1, p(15000))
	rejected(t, acc, "insufficient shares")
	if acc.Holdings["AAPL"] != 20_000 || len(acc.Resting) != 0 {
		t.Fatalf("holding %s, resting %d", acc.Holdings["AAPL"], len(acc.Resting))
	}
	if acc.Cash != 1_000_000-20_000 {
		t.Fatalf("cash %s", acc.Cash)
	}
}

// Closing a CFD (old CFDPartialCloseTests): proportional margin, the
// remainder closes the position, a close larger than the position closes it
// exactly, and a short profits when the price falls.
func TestCFDCloses(t *testing.T) {
	s, u, acc := setup(t)
	mustOK(t, block(s, t0, p(10000), u.order(t, OrderMsg{Kind: "CFD", Side: "SELL", Ticker: "AAPL", Qty: "10", Leverage: "10"})))
	c := acc.CFDs[0]
	mustOK(t, block(s, t0, p(9000), u.order(t, OrderMsg{Kind: "CFD_CLOSE", Side: "BUY", Ticker: "AAPL", Qty: "4", Position: itoa(c.ID)})))
	if !c.Open || c.Qty != 60_000 || c.Margin != 6_000 || last(acc).Amount != 4_000+4_000 { // margin $40 + profit $40
		t.Fatalf("partial close of a short: %+v, %+v", c, last(acc))
	}
	mustOK(t, block(s, t0, p(9000), u.order(t, OrderMsg{Kind: "CFD_CLOSE", Side: "BUY", Ticker: "AAPL", Qty: "100", Position: itoa(c.ID)})))
	if c.Open || last(acc).Amount != 6_000+6_000 || acc.Cash != 1_000_000+10_000 {
		t.Fatalf("close beyond the size: %+v, cash %s", c, acc.Cash)
	}
	mustOK(t, block(s, t0, p(9000), u.order(t, OrderMsg{Kind: "CFD_CLOSE", Side: "BUY", Ticker: "AAPL", Qty: "1", Position: itoa(c.ID)})))
	rejected(t, acc, "no open CFD position")
	// a CFD's levels attach to that position and go away with it
	mustOK(t, block(s, t0, p(10000), u.order(t, OrderMsg{Kind: "CFD", Side: "BUY", Ticker: "AAPL", Qty: "1", Leverage: "5", SL: "90", TP: "130"})))
	n := acc.CFDs[len(acc.CFDs)-1]
	for _, l := range acc.Levels {
		if l.CFD != n.ID {
			t.Fatalf("level %+v not on position %d", l, n.ID)
		}
	}
	mustOK(t, block(s, t0, p(10000), u.order(t, OrderMsg{Kind: "CFD_CLOSE", Side: "SELL", Ticker: "AAPL", Qty: "1", Position: itoa(n.ID)})))
	if len(acc.Levels) != 0 {
		t.Fatalf("levels outlived their position: %d", len(acc.Levels))
	}
}

// Liquidation closes the whole position once (old CFDLiquidationTests).
func TestLiquidationHappensOnce(t *testing.T) {
	s, u, acc := setup(t)
	mustOK(t, block(s, t0, p(10000), u.order(t, OrderMsg{Kind: "CFD", Side: "BUY", Ticker: "AAPL", Qty: "10", Leverage: "20"})))
	block(s, t0+1, p(9500))
	block(s, t0+2, p(9000))
	n := 0
	for _, r := range acc.History {
		if r.Kind == "LIQUIDATION" {
			n++
			if r.Qty != 100_000 {
				t.Fatalf("partial liquidation %+v", r)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d liquidations", n)
	}
}

// Levels (old StockStopLossTests, CFDStopLossRoutingTests, SmallFixesTests):
// a level fires once, not before its price, never sells more than is left,
// and a triggered level cannot be cancelled.
func TestLevelEdgeCases(t *testing.T) {
	s, u, acc := setup(t)
	mustOK(t, block(s, t0, p(10000), u.order(t, OrderMsg{Kind: "STOCK", Side: "BUY", Ticker: "AAPL", Qty: "5", SL: "90"})))
	lvl := acc.Levels[0]
	block(s, t0+1, p(9001))
	if lvl.Triggered {
		t.Fatal("stop-loss fired above its price")
	}
	// the shares were sold by hand: their level goes with them, and a later
	// crossing records nothing
	mustOK(t, block(s, t0+2, p(9001), u.order(t, OrderMsg{Kind: "STOCK", Side: "SELL", Ticker: "AAPL", Qty: "5"})))
	before := len(acc.History)
	block(s, t0+3, p(8000))
	if len(acc.Levels) != 0 || len(acc.History) != before {
		t.Fatalf("level outlived its holding: levels %d, records %d→%d", len(acc.Levels), before, len(acc.History))
	}
	// a triggered level cannot be cancelled
	mustOK(t, block(s, t0+4, p(10000), u.order(t, OrderMsg{Kind: "STOCK", Side: "BUY", Ticker: "AAPL", Qty: "5", SL: "90", SLQty: "1"})))
	lvl = acc.Levels[0]
	block(s, t0+4, p(8900))
	if !lvl.Triggered || acc.Holdings["AAPL"] != 40_000 {
		t.Fatalf("partial stop-loss: %+v holding %s", lvl, acc.Holdings["AAPL"])
	}
	mustOK(t, block(s, t0+4, nil, u.sign(t, Msg{Type: "level_cancel", LevelID: itoa(lvl.ID)}, false)))
	rejected(t, acc, "already triggered")
	mustOK(t, block(s, t0+4, nil, u.sign(t, Msg{Type: "level_cancel", LevelID: "424242"}, false)))
	rejected(t, acc, "no such level")

	// a level cannot exceed the holding, and levels of one kind add up
	mustOK(t, block(s, t0+5, p(10000), u.order(t, OrderMsg{Kind: "STOCK", Side: "SELL", Ticker: "AAPL", Qty: "1"}))) // 3 left
	addLevel := func(qty string) []byte {
		return u.sign(t, Msg{Type: "level_add", Level: &LevelMsg{Kind: "TP", Ticker: "AAPL", Price: "120", Qty: qty}}, false)
	}
	mustOK(t, block(s, t0+5, nil, addLevel("2")))
	mustOK(t, block(s, t0+5, nil, addLevel("2")))
	rejected(t, acc, "exceeds the position")
	mustOK(t, block(s, t0+5, nil, u.sign(t, Msg{Type: "level_add", Level: &LevelMsg{Kind: "SL", Ticker: "MSFT", Price: "1", Qty: "1"}}, false)))
	rejected(t, acc, "no holding")

	// a CFD stop-loss fires once
	mustOK(t, block(s, t0+6, p(10000), u.order(t, OrderMsg{Kind: "CFD", Side: "BUY", Ticker: "AAPL", Qty: "2", Leverage: "2", SL: "95", SLQty: "1"})))
	c := acc.CFDs[len(acc.CFDs)-1]
	block(s, t0+7, p(9400))
	block(s, t0+8, p(9300))
	if c.Qty != 10_000 || !c.Open {
		t.Fatalf("CFD stop-loss fired more than once: %+v", c)
	}
}

// Options (old OptionOrderValidationTests, OptionSettlementTests): the
// premium never falls below intrinsic value, an out-of-the-money option
// cannot be exercised, an expired one cannot be closed, and closing pays the
// model premium.
func TestOptionEdgeCases(t *testing.T) {
	if p := OptionPremium(true, 20000, 10000, 3600, 3000); p != 10000 {
		t.Fatalf("deep in-the-money call an hour before expiry: %s, want intrinsic 100.00", p)
	}
	if p := OptionPremium(false, 20000, 10000, 3600, 3000); p != 1 {
		t.Fatalf("worthless put: %s, want the 1 cent minimum", p)
	}
	s, u, acc := setup(t)
	expiry := "2026-10-16"
	mustOK(t, block(s, t0, p(10000), u.order(t, OrderMsg{Kind: "OPTION", Side: "BUY", Ticker: "AAPL", Qty: "1", OptionType: "PUT", Strike: "90", Expiry: expiry})))
	put := acc.Options[0]
	mustOK(t, block(s, t0, p(10000), u.order(t, OrderMsg{Kind: "OPT_EXER", Side: "SELL", Ticker: "AAPL", Qty: "1", Position: itoa(put.ID)})))
	rejected(t, acc, "out of the money")
	mustOK(t, block(s, t0, map[string]Cents{"AAPL": 10000, "MSFT": 30000}, u.order(t, OrderMsg{Kind: "OPT_CLOSE", Side: "SELL", Ticker: "MSFT", Qty: "1", Position: itoa(put.ID)})))
	rejected(t, acc, "no open option position")
	before := acc.Cash
	mustOK(t, block(s, t0, p(10000), u.order(t, OrderMsg{Kind: "OPT_CLOSE", Side: "SELL", Ticker: "AAPL", Qty: "1", Position: itoa(put.ID)})))
	if put.Status != "CLOSED" || acc.Cash != before+put.ClosePx*100 {
		t.Fatalf("close: %+v, cash %s→%s", put, before, acc.Cash)
	}
	mustOK(t, block(s, t0, p(10000), u.order(t, OrderMsg{Kind: "OPTION", Side: "BUY", Ticker: "AAPL", Qty: "1", OptionType: "CALL", Strike: "90", Expiry: expiry})))
	call := acc.Options[len(acc.Options)-1]
	cutoff, _ := s.expiryCutoff(expiry)
	// after the cutoff the order is refused; the expiry rule then settles the
	// option itself, in the same block, at intrinsic value
	mustOK(t, block(s, cutoff+1, p(12000), u.order(t, OrderMsg{Kind: "OPT_EXER", Side: "SELL", Ticker: "AAPL", Qty: "1", Position: itoa(call.ID)})))
	exer, exp := acc.History[len(acc.History)-2], last(acc)
	if exer.Status != "REJECTED" || !strings.Contains(exer.Reason, "expired") || exp.Kind != "EXPIRY" || call.ClosePx != 3000 {
		t.Fatalf("exercise after expiry: %+v, then %+v", exer, exp)
	}
}
