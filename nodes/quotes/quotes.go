// Package quotes verifies signed price quotes and turns them into the
// execution price of each ticker: the exact median of one quote per source.
//
// Each price source (Yahoo, Nasdaq, CNBC, …) runs behind an oracle signer
// that signs (source, ticker, price, time) with its own Ed25519 key; the
// public keys are fixed in the genesis file. The block proposer collects the
// quotes and puts them in the block, and every validator recomputes the
// medians from the signatures — there is no tolerance band. A proposer can
// neither shift a price (any changed quote fails its signature, and a
// declared median must match exactly) nor rely on one lying source (the
// median of three ignores it).
package quotes

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
)

// Quote is one source's signed price for one ticker.
type Quote struct {
	Source string `json:"source"`
	Ticker string `json:"ticker"`
	Price  int64  `json:"price"` // cents
	Time   int64  `json:"time"`  // unix seconds when the source was read
	Sig    []byte `json:"sig"`   // Ed25519 over Message()
}

// Message is the exact byte string a source signs.
func (q Quote) Message() []byte {
	return []byte("tradedesk-quote|" + q.Source + "|" + q.Ticker + "|" +
		strconv.FormatInt(q.Price, 10) + "|" + strconv.FormatInt(q.Time, 10))
}

// Source is a price source registered in the genesis file.
type Source struct {
	Name   string            `json:"name"`
	PubKey ed25519.PublicKey `json:"pubkey"`
}

// Bundle is the first transaction of every block that carries prices: the
// quotes, and the medians the proposer declares from them.
type Bundle struct {
	Quotes []Quote          `json:"quotes"`
	Prices map[string]int64 `json:"prices"`
}

// Marker distinguishes a quote bundle from a user transaction (whose JSON
// starts with {"msg").
const Marker = `{"quotes"`

// Rules for accepting quotes, relative to the block time.
const (
	MaxAge    = 10 // a quote older than this (seconds) is stale; honest quotes are signed while the block is built
	MaxFuture = 30 // clocks may run a little ahead of the block time
)

// Encode serialises a bundle.
func Encode(b Bundle) []byte {
	raw, _ := json.Marshal(b)
	return raw
}

// IsBundle reports whether a transaction is a quote bundle.
func IsBundle(tx []byte) bool {
	return len(tx) >= len(Marker) && string(tx[:len(Marker)]) == Marker
}

// Verify checks every quote in a bundle and returns the median price per
// ticker. A ticker gets a price only when every registered source quoted it
// (so the median of an odd number of sources is always one real quote). The
// declared medians must match the recomputed ones exactly.
func Verify(raw []byte, sources []Source, listed func(string) bool, blockTime int64) (map[string]int64, error) {
	var b Bundle
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, fmt.Errorf("malformed quote bundle: %v", err)
	}
	byName := map[string]ed25519.PublicKey{}
	for _, s := range sources {
		byName[s.Name] = s.PubKey
	}
	perTicker := map[string]map[string]int64{} // ticker → source → price
	for _, q := range b.Quotes {
		key, ok := byName[q.Source]
		switch {
		case !ok:
			return nil, fmt.Errorf("quote from unknown source %q", q.Source)
		case !listed(q.Ticker):
			return nil, fmt.Errorf("quote for unlisted ticker %q", q.Ticker)
		case q.Price <= 0:
			return nil, fmt.Errorf("non-positive price from %s for %s", q.Source, q.Ticker)
		case q.Time < blockTime-MaxAge:
			return nil, fmt.Errorf("stale quote from %s for %s", q.Source, q.Ticker)
		case q.Time > blockTime+MaxFuture:
			return nil, fmt.Errorf("quote from %s for %s is in the future", q.Source, q.Ticker)
		case !ed25519.Verify(key, q.Message(), q.Sig):
			return nil, fmt.Errorf("signature of %s's quote for %s does not verify", q.Source, q.Ticker)
		}
		if perTicker[q.Ticker] == nil {
			perTicker[q.Ticker] = map[string]int64{}
		}
		if _, dup := perTicker[q.Ticker][q.Source]; dup {
			return nil, fmt.Errorf("two quotes from %s for %s", q.Source, q.Ticker)
		}
		perTicker[q.Ticker][q.Source] = q.Price
	}

	medians := map[string]int64{}
	for ticker, bySource := range perTicker {
		if len(bySource) != len(sources) {
			continue // not every source quoted it: no price this block
		}
		medians[ticker] = Median(bySource)
	}
	if len(medians) != len(b.Prices) {
		return nil, errors.New("declared prices do not match the quotes")
	}
	for t, m := range medians {
		if b.Prices[t] != m {
			return nil, fmt.Errorf("declared price for %s is %d, the median of the signed quotes is %d", t, b.Prices[t], m)
		}
	}
	return medians, nil
}

// Median of an odd number of prices is the middle one.
func Median(bySource map[string]int64) int64 {
	vals := make([]int64, 0, len(bySource))
	for _, v := range bySource {
		vals = append(vals, v)
	}
	sort.Slice(vals, func(i, j int) bool { return vals[i] < vals[j] })
	return vals[len(vals)/2]
}

// Build assembles a bundle from fetched quotes: keeps one quote per source
// and ticker and declares the medians the validators will recompute.
func Build(qs []Quote, nSources int) Bundle {
	seen := map[string]bool{}
	perTicker := map[string]map[string]int64{}
	var kept []Quote
	sort.Slice(qs, func(i, j int) bool {
		if qs[i].Ticker != qs[j].Ticker {
			return qs[i].Ticker < qs[j].Ticker
		}
		return qs[i].Source < qs[j].Source
	})
	for _, q := range qs {
		k := q.Source + "|" + q.Ticker
		if seen[k] {
			continue
		}
		seen[k] = true
		kept = append(kept, q)
		if perTicker[q.Ticker] == nil {
			perTicker[q.Ticker] = map[string]int64{}
		}
		perTicker[q.Ticker][q.Source] = q.Price
	}
	prices := map[string]int64{}
	for t, m := range perTicker {
		if len(m) == nSources {
			prices[t] = Median(m)
		}
	}
	return Bundle{Quotes: kept, Prices: prices}
}
