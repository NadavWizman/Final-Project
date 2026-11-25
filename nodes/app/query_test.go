package app

import (
	"context"
	"encoding/json"
	"testing"

	abci "github.com/cometbft/cometbft/abci/types"

	"nodes/ledger"
)

// The option quote follows the price the gateway passes (the live one), falls
// back to the last verified price without one, and refuses a bad price.
func TestOptionQuoteAtTheGivenPrice(t *testing.T) {
	c, _ := testChain(4)
	c.Ledger.Time = 1792750000 // 2026-10-22
	c.Ledger.LastPrices["AAPL"] = 34463
	a := &App{chain: c}
	quote := func(ticker, price string) (uint32, map[string]ledger.Cents) {
		data, _ := json.Marshal(map[string]string{"Ticker": ticker, "Type": "CALL", "Strike": "345", "Expiry": "2026-12-18", "Price": price})
		res, err := a.Query(context.Background(), &abci.RequestQuery{Path: "/option_quote", Data: data})
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]ledger.Cents
		_ = json.Unmarshal(res.Value, &out)
		return res.Code, out
	}
	cutoff, _ := c.Ledger.ExpiryCutoff("2026-12-18")
	premium := func(px ledger.Cents) ledger.Cents {
		return ledger.OptionPremium(true, px, 34500, cutoff-c.Ledger.Time, c.Ledger.Params.OptionVolBps)
	}

	if code, q := quote("AAPL", ""); code != 0 || q["underlying"] != 34463 || q["premium"] != premium(34463) {
		t.Fatalf("without a price: code %d, %v; want the last verified price", code, q)
	}
	if code, q := quote("AAPL", "349.35"); code != 0 || q["underlying"] != 34935 || q["premium"] != premium(34935) {
		t.Fatalf("at a live price: code %d, %v; want the model at 349.35", code, q)
	}
	for _, bad := range []string{"0", "-1", "abc", "349.355"} {
		if code, _ := quote("AAPL", bad); code == 0 {
			t.Errorf("price %q accepted", bad)
		}
	}
	if code, _ := quote("SPY", "500"); code == 0 {
		t.Error("an unlisted ticker was quoted")
	}
}
