package quotes

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"
)

// LocalSource signs quotes in-process: used by the tests and by the
// offline demo, with the same keys and message format as the real signers.
type LocalSource struct {
	Name  string
	Key   ed25519.PrivateKey
	Price func(ticker string) (cents int64, ok bool)
}

// Sign returns a signed quote for ticker at the current time.
func (s LocalSource) Sign(ticker string) (Quote, bool) {
	p, ok := s.Price(ticker)
	if !ok {
		return Quote{}, false
	}
	q := Quote{Source: s.Name, Ticker: ticker, Price: p, Time: time.Now().Unix()}
	q.Sig = ed25519.Sign(s.Key, q.Message())
	return q, true
}

// LocalFetcher asks every LocalSource.
type LocalFetcher []LocalSource

func (f LocalFetcher) Fetch(_ context.Context, tickers []string) []Quote {
	var out []Quote
	for _, s := range f {
		for _, t := range tickers {
			if q, ok := s.Sign(t); ok {
				out = append(out, q)
			}
		}
	}
	return out
}

// LoadKey reads an oracle signing key written by `tradedesk-node init`.
func LoadKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	seed, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("%s is not an Ed25519 seed", path)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
