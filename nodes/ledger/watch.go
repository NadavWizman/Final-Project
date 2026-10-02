package ledger

import (
	"encoding/json"
	"sort"
)

// TickerOf returns the ticker a user transaction trades, or "" (the proposer
// uses it to know which prices a block needs). It does not verify anything.
func TickerOf(raw []byte) string {
	var env Envelope
	if json.Unmarshal(raw, &env) != nil {
		return ""
	}
	var m Msg
	if json.Unmarshal([]byte(env.Msg), &m) != nil {
		return ""
	}
	if m.Order != nil {
		return m.Order.Ticker
	}
	return ""
}

// WatchedTickers are the tickers an automatic rule depends on: resting limit
// orders, untriggered stop-loss / take-profit levels, open CFDs (liquidation)
// and open options (expiry). A block carries prices for them so the rules
// can fire in it.
func (s *State) WatchedTickers(at int64) []string {
	set := map[string]bool{}
	for _, acc := range s.Accounts {
		for _, o := range acc.Resting {
			set[o.Msg.Ticker] = true
		}
		for _, l := range acc.Levels {
			if !l.Triggered {
				set[l.Ticker] = true
			}
		}
		for _, c := range acc.CFDs {
			if c.Open {
				set[c.Ticker] = true
			}
		}
		for _, o := range acc.Options {
			if o.Status == "OPEN" {
				set[o.Ticker] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}
