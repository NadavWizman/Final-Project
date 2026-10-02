package app

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"

	abci "github.com/cometbft/cometbft/abci/types"

	"nodes/ledger"
)

// Query serves read-only views of the committed state. The Django gateway
// shows these to users; it has no state of its own.
//
//	/status                 height, block time, state root, app hash, sets
//	/account   data=address the account, with liquidation prices
//	/username  data=name    the address registered under a username
//	/prices                 the last verified price of every ticker
//	/option_quote data=json the model premium of an option at the last price
func (a *App) Query(_ context.Context, req *abci.RequestQuery) (*abci.ResponseQuery, error) {
	a.mu.Lock()
	c := a.chain
	a.mu.Unlock()
	if c == nil {
		return &abci.ResponseQuery{Code: 1, Log: "chain not initialised"}, nil
	}
	s := c.Ledger
	reply := func(v any) (*abci.ResponseQuery, error) {
		b, _ := json.Marshal(v)
		return &abci.ResponseQuery{Code: 0, Value: b, Height: s.Height}, nil
	}
	notFound := func(what string) (*abci.ResponseQuery, error) {
		return &abci.ResponseQuery{Code: 2, Log: what + " not found", Height: s.Height}, nil
	}

	switch strings.TrimSuffix(req.Path, "/") {
	case "/status":
		root := s.Root()
		names := make([]string, len(c.Oracles))
		for i, o := range c.Oracles {
			names[i] = o.Name
		}
		return reply(map[string]any{
			"height": s.Height, "time": s.Time, "chain_id": s.Params.ChainID,
			"state_root": hex.EncodeToString(root[:]), "app_hash": hex.EncodeToString(c.AppHash()),
			"validators": len(c.Validators), "valset_seq": c.ValsetSeq, "oracles": names, "accounts": len(s.Accounts),
			"tickers": s.Params.Tickers,
		})

	case "/account":
		acc := s.Accounts[string(req.Data)]
		if acc == nil {
			return notFound("account")
		}
		liq := map[uint64]ledger.Cents{}
		for _, cfd := range acc.CFDs {
			if cfd.Open {
				liq[cfd.ID] = s.LiquidationPrice(cfd)
			}
		}
		return reply(map[string]any{"account": acc, "liquidation": liq, "last_prices": s.LastPrices, "time": s.Time})

	case "/username":
		addr, ok := s.AddressOf(string(req.Data))
		if !ok {
			return notFound("username")
		}
		return reply(map[string]string{"address": addr})

	case "/prices":
		return reply(map[string]any{"prices": s.LastPrices, "height": s.Height, "time": s.Time})

	case "/option_quote":
		var q struct{ Ticker, Type, Strike, Expiry string }
		if json.Unmarshal(req.Data, &q) != nil {
			return &abci.ResponseQuery{Code: 1, Log: "bad option query"}, nil
		}
		px, ok := s.LastPrices[q.Ticker]
		k, err := ledger.ParseCents(q.Strike)
		cutoff, err2 := s.ExpiryCutoff(q.Expiry)
		if !ok || err != nil || err2 != nil {
			return &abci.ResponseQuery{Code: 1, Log: "no price yet, or invalid strike/expiry"}, nil
		}
		premium := ledger.OptionPremium(q.Type == "CALL", px, k, cutoff-s.Time, s.Params.OptionVolBps)
		return reply(map[string]any{"premium": premium, "underlying": px})

	default:
		return &abci.ResponseQuery{Code: 1, Log: "unknown query path " + req.Path}, nil
	}
}
