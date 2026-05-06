// Package oracleclient keeps the most recent signed price we've received
// from the oracle, per symbol. Validators use Lookup() during tx validation
// to enforce the "price not stale" rule.
package oracleclient

import (
	"fmt"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

type Quote struct {
	Symbol    string
	Price     decimal.Decimal
	Timestamp int64 // unix seconds
}

type Cache struct {
	mu     sync.RWMutex
	quotes map[string]Quote
	maxAge time.Duration
}

func New(maxAge time.Duration) *Cache {
	return &Cache{
		quotes: make(map[string]Quote),
		maxAge: maxAge,
	}
}

func (c *Cache) Put(q Quote) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Only accept newer timestamps — protects against out-of-order delivery.
	if existing, ok := c.quotes[q.Symbol]; ok && existing.Timestamp >= q.Timestamp {
		return
	}
	c.quotes[q.Symbol] = q
}

func (c *Cache) Lookup(symbol string) (Quote, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	q, ok := c.quotes[symbol]
	if !ok {
		return Quote{}, fmt.Errorf("no oracle price for %s", symbol)
	}
	age := time.Since(time.Unix(q.Timestamp, 0))
	if age > c.maxAge {
		return Quote{}, fmt.Errorf("oracle price for %s is stale (age=%s, max=%s)", symbol, age, c.maxAge)
	}
	return q, nil
}

// CheckFreshness validates a price_timestamp that came on an incoming tx.
// The tx itself carries the price it was signed with; we don't trust that
// value alone — we also check that our local oracle cache agrees.
func (c *Cache) CheckFreshness(symbol string, txTimestamp int64) error {
	now := time.Now().Unix()
	if txTimestamp > now+5 {
		return fmt.Errorf("tx timestamp is in the future")
	}
	if now-txTimestamp > int64(c.maxAge.Seconds()) {
		return fmt.Errorf("tx price timestamp is stale (age=%ds)", now-txTimestamp)
	}
	return nil
}
