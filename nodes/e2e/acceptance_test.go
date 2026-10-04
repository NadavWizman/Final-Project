package e2e

// The acceptance criteria: every attack scenario from the project review,
// played against a real 4-validator network running in this process.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cmtcfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/privval"

	"nodes/chainnode"
	"nodes/ledger"
	"nodes/quotes"
)

// registered returns a funded wallet on the cluster.
func registered(t *testing.T, c *cluster, name string) *wallet {
	w := newWallet()
	c.submit(0, w.register(name))
	return w
}

// watchAAPL rests a far-below-market limit order, so an automatic rule keeps
// watching AAPL and every block — whoever proposes it — carries AAPL quotes.
func watchAAPL(c *cluster, w *wallet) {
	c.submit(0, w.tx(ledger.Msg{Type: "order", Order: &ledger.OrderMsg{Kind: "STOCK", Side: "BUY",
		Ticker: "AAPL", Qty: "1", Limit: "1"}}))
}

func (c *cluster) totalRejected() (n int64) {
	for _, nd := range c.running() {
		n += nd.App.RejectedProposals()
	}
	return n
}

// proposerOf returns the index of the node that proposed block h.
func (c *cluster) proposerOf(h int64) int {
	addr := c.nodes[0].BlockStore().LoadBlockMeta(h).Header.ProposerAddress
	for i := range c.nodes {
		pv := privval.LoadFilePV(filepath.Join(c.net.NodeHome(i), "config", "priv_validator_key.json"),
			filepath.Join(c.net.NodeHome(i), "data", "priv_validator_state.json"))
		if pub, _ := pv.GetPubKey(); pub.Address().String() == addr.String() {
			return i
		}
	}
	return -1
}

// broadcastRaw sends a transaction and returns the mempool's verdict (a
// refusal by CometBFT itself, such as its duplicate cache, comes back as an
// error and is reported as code 1).
func (c *cluster) broadcastRaw(i int, tx []byte) (uint32, string) {
	res, err := c.client(i).BroadcastTxSync(context.Background(), tx)
	if err != nil {
		return 1, err.Error()
	}
	return res.Code, res.Log
}

// 1. A dishonest proposer shifts a price by 0.5 %: its block is rejected, the
// next proposer's block carries the true median.
func TestByzantineProposerShiftsPrice(t *testing.T) {
	c := newCluster(t, 4)
	c.setPrice("AAPL", 20000)
	c.nodes[2].App.ByzantinePrepare = func(txs [][]byte) [][]byte {
		if len(txs) == 0 || !quotes.IsBundle(txs[0]) {
			return txs
		}
		var b quotes.Bundle
		_ = json.Unmarshal(txs[0], &b)
		for k, p := range b.Prices {
			b.Prices[k] = p * 1005 / 1000
		}
		txs[0] = quotes.Encode(b)
		return txs
	}
	w := registered(t, c, "alice")
	watchAAPL(c, w)
	before := c.totalRejected()
	for i := 0; i < 4; i++ { // several heights, so node2 proposes at least once
		c.submit(i%4, w.buy("AAPL", "1"))
	}
	c.waitHeight(c.height()+6, 30*time.Second)
	if c.totalRejected() == before {
		t.Fatal("the shifted proposal was never rejected")
	}
	for _, r := range c.account(0, w.addr).History[1:] {
		if r.Status == "CONFIRMED" && r.Price != 20000 {
			t.Fatalf("an order executed at the shifted price: %+v", r)
		}
	}
	c.sameStateEverywhere()
}

// 2. Two different blocks at one height: each validator's signer refuses to
// sign a second, different proposal (or vote) for a height and round it has
// already signed — CometBFT's double-sign protection — so an equivocating
// proposer cannot gather a commit certificate for both.
func TestEquivocationIsRefusedBySigner(t *testing.T) {
	dir := t.TempDir()
	pv := privval.GenFilePV(filepath.Join(dir, "key.json"), filepath.Join(dir, "state.json"))
	if err := pv.SignProposal("tradedesk-e2e", proposal(10, "A")); err != nil {
		t.Fatal(err)
	}
	if err := pv.SignProposal("tradedesk-e2e", proposal(10, "B")); err == nil {
		t.Fatal("the signer signed two different proposals for the same height and round")
	}
	// the same block again is fine (a re-broadcast, not equivocation)
	if err := pv.SignProposal("tradedesk-e2e", proposal(10, "A")); err != nil {
		t.Fatalf("re-signing the same proposal: %v", err)
	}
}

// 3. A proposer that censors an order cannot stop it: the mempool is shared
// and the next proposer includes it.
func TestCensoringProposerCannotStopAnOrder(t *testing.T) {
	c := newCluster(t, 4)
	c.setPrice("MSFT", 41000)
	w := registered(t, c, "alice")
	c.nodes[0].App.ByzantinePrepare = func(txs [][]byte) [][]byte {
		var kept [][]byte
		for _, tx := range txs {
			if !strings.Contains(string(tx), w.addr) {
				kept = append(kept, tx)
			}
		}
		return kept
	}
	tx := w.buy("MSFT", "1")
	c.submit(0, tx) // sent to the censor itself
	r, _ := c.client(1).Tx(context.Background(), hashOf(tx), false)
	if p := c.proposerOf(r.Height); p == 0 {
		t.Fatal("the censoring node included the order")
	}
	if lastRecord(c.account(1, w.addr)).Status != "CONFIRMED" {
		t.Fatal("order not executed")
	}
}

// 4. The proposer goes down: after the propose timeout the turn passes to
// the next validator (a later round) and blocks keep coming.
func TestProposerDownTurnPasses(t *testing.T) {
	c := newCluster(t, 4)
	c.stop(3)
	start := c.height()
	c.waitHeight(start+10, 60*time.Second)
	roundChange := false
	for h := start + 1; h <= start+10; h++ {
		if cm := c.nodes[0].BlockStore().LoadBlockCommit(h); cm != nil && cm.Round > 0 {
			roundChange = true
		}
	}
	if !roundChange {
		t.Fatal("no round change while a proposer was down")
	}
}

// 5. One node down: the other three keep closing blocks and settling trades.
func TestOneNodeDownThreeContinue(t *testing.T) {
	c := newCluster(t, 4)
	c.setPrice("AAPL", 19000)
	c.stop(2)
	w := registered(t, c, "alice")
	c.submit(0, w.buy("AAPL", "1"))
	if lastRecord(c.account(1, w.addr)).Status != "CONFIRMED" {
		t.Fatal("trade not settled with 3 of 4 validators")
	}
}

// 6 and 7. A compromised gateway can neither sign for a user nor change an
// order in transit: a transaction not signed with the user's key, or changed
// after signing, is refused by every node.
func TestGatewayCannotForgeOrAlterTransactions(t *testing.T) {
	c := newCluster(t, 4)
	c.setPrice("AAPL", 19000)
	victim := registered(t, c, "victim")

	// 6: the gateway signs with its own key in the victim's name
	forger := newWallet()
	forger.addr, forger.nonce = victim.addr, victim.nonce
	if code, log := c.broadcastRaw(0, forger.buy("AAPL", "40")); code == 0 || !strings.Contains(log, "signature") {
		t.Fatalf("forged transaction accepted: %d %s", code, log)
	}

	// 7: the gateway changes the quantity of a genuinely signed order
	var env ledger.Envelope
	_ = json.Unmarshal(victim.buy("AAPL", "1"), &env)
	env.Msg = strings.Replace(env.Msg, `"qty":"1"`, `"qty":"40"`, 1)
	altered, _ := json.Marshal(env)
	if code, log := c.broadcastRaw(1, altered); code == 0 || !strings.Contains(log, "signature") {
		t.Fatalf("altered transaction accepted: %d %s", code, log)
	}
	if a := c.account(2, victim.addr); a.Cash != 1_000_000 || len(a.Holdings) != 0 {
		t.Fatal("the victim's account changed")
	}
}

// 8. A signed order sent again is refused — by the mempool, and, if a
// dishonest proposer forces it into a block anyway, by the validators: its
// nonce is used up.
func TestReplayedOrderIsRefused(t *testing.T) {
	c := newCluster(t, 4)
	c.setPrice("AAPL", 19000)
	w := registered(t, c, "alice")
	tx := w.buy("AAPL", "1")
	c.submit(0, tx)
	if code, _ := c.broadcastRaw(2, tx); code == 0 {
		t.Fatal("the mempool accepted a replay")
	}
	c.nodes[1].App.ByzantinePrepare = func(txs [][]byte) [][]byte { return append(txs, tx) }
	before := c.totalRejected()
	c.waitHeight(c.height()+6, 30*time.Second)
	if c.totalRejected() == before {
		t.Fatal("a block replaying a used nonce was not rejected")
	}
	if h := c.account(0, w.addr).Holdings["AAPL"]; h != ledger.QtyScale {
		t.Fatalf("holding %d after replay", h)
	}
	c.sameStateEverywhere()
}

// 9. One lying price source (the "rogue oracle"): the median ignores it.
func TestRogueOracleIsOutvoted(t *testing.T) {
	c := newCluster(t, 4)
	c.setPrice("NVDA", 12000, 12010, 100) // the third source reports $1.00
	w := registered(t, c, "alice")
	c.submit(0, w.buy("NVDA", "1"))
	if r := lastRecord(c.account(0, w.addr)); r.Price != 12000 {
		t.Fatalf("executed at %s, want the honest median 120.00", r.Price)
	}
}

// 10. A quote changed on the network fails its signature: the block is rejected.
func TestAlteredQuoteRejectsTheBlock(t *testing.T) {
	c := newCluster(t, 4)
	c.setPrice("AAPL", 20000)
	c.nodes[1].App.ByzantinePrepare = func(txs [][]byte) [][]byte {
		if len(txs) == 0 || !quotes.IsBundle(txs[0]) {
			return txs
		}
		var b quotes.Bundle
		_ = json.Unmarshal(txs[0], &b)
		for i := range b.Quotes {
			b.Quotes[i].Price = 25000 // every quote altered, medians kept consistent
		}
		for k := range b.Prices {
			b.Prices[k] = 25000
		}
		txs[0] = quotes.Encode(b)
		return txs
	}
	w := registered(t, c, "alice")
	watchAAPL(c, w)
	before := c.totalRejected()
	for i := 0; i < 4; i++ {
		c.submit(i%4, w.buy("AAPL", "1"))
	}
	c.waitHeight(c.height()+6, 30*time.Second)
	if c.totalRejected() == before {
		t.Fatal("blocks with altered quotes were never rejected")
	}
	for _, r := range c.account(0, w.addr).History[1:] {
		if r.Status == "CONFIRMED" && r.Price != 20000 {
			t.Fatalf("executed at an altered price: %+v", r)
		}
	}
}

// 11. One node's saved state is edited: on restart it detects the mismatch
// and rebuilds its state from the blocks, matching the others again.
func TestEditedStateFileIsDetectedAndRebuilt(t *testing.T) {
	c := newCluster(t, 4)
	c.setPrice("AAPL", 19000)
	w := registered(t, c, "alice")
	c.submit(0, w.buy("AAPL", "1"))
	c.stop(3)

	path := filepath.Join(c.net.NodeHome(3), "data", "app_state.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	_ = json.Unmarshal(raw, &saved)
	acc := saved["chain"].(map[string]any)["ledger"].(map[string]any)["accounts"].(map[string]any)[w.addr].(map[string]any)
	acc["cash"] = acc["cash"].(float64) + 100_000_000 // an extra $1,000,000
	edited, _ := json.Marshal(saved)
	if err := os.WriteFile(path, edited, 0o644); err != nil {
		t.Fatal(err)
	}

	// CometBFT v0.38 keeps its transaction-index database open after an
	// in-process Stop; a real node exits and releases it. Restart this one
	// without the index so it can reopen its home directory.
	conf, err := chainnode.LoadConfig(c.net.NodeHome(3))
	if err != nil {
		t.Fatal(err)
	}
	conf.TxIndex.Indexer = "null"
	cmtcfg.WriteConfigFile(filepath.Join(c.net.NodeHome(3), "config", "config.toml"), conf)

	c.start(3)
	c.waitHeight(c.height()+2, 30*time.Second)
	c.sameStateEverywhere()
	if a := c.account(3, w.addr); a.Cash != c.account(0, w.addr).Cash {
		t.Fatal("the edited balance survived")
	}
	if m, _ := filepath.Glob(path + ".corrupt-*"); len(m) == 0 {
		t.Fatal("the edited file was not set aside")
	}
}

// 12. A stop-loss fires inside the chain, in the first block whose verified
// price crosses it — no outside monitor and no Django involved.
func TestStopLossFiresInsideTheChain(t *testing.T) {
	c := newCluster(t, 4)
	c.setPrice("AAPL", 20000)
	w := registered(t, c, "alice")
	c.submit(0, w.tx(ledger.Msg{Type: "order", Order: &ledger.OrderMsg{Kind: "STOCK", Side: "BUY",
		Ticker: "AAPL", Qty: "2", SL: "190"}}))
	c.setPrice("AAPL", 18500)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		a := c.account(0, w.addr)
		if r := lastRecord(a); r.Kind == "SL" {
			if a.Holdings["AAPL"] != 0 || r.Price != 18500 {
				t.Fatalf("stop-loss record %+v, holding %d", r, a.Holdings["AAPL"])
			}
			c.sameStateEverywhere()
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("the stop-loss never fired")
}

// 14. Price sources down. The availability policy: a price needs signed
// quotes from 3 of the 5 sources. With one or two sources down trading
// continues, at the lower median of the rest; with three down there is no
// price, so a market order waits in the mempool (nonce untouched) and
// executes as soon as a source comes back.
func TestPriceSourcesDown(t *testing.T) {
	c := newCluster(t, 4)
	c.setPrice("AAPL", 20000, 20010, 19990, 20020, 20005)
	w := registered(t, c, "alice")

	c.setDown(true, "google")
	c.submit(0, w.buy("AAPL", "1"))
	if r := lastRecord(c.account(0, w.addr)); r.Status != "CONFIRMED" || r.Price != 20000 {
		t.Fatalf("one source down: %+v", r) // lower median of 19990, 20000, 20010, 20020
	}
	c.setDown(true, "tradingview")
	c.submit(1, w.buy("AAPL", "1"))
	if r := lastRecord(c.account(0, w.addr)); r.Status != "CONFIRMED" || r.Price != 20000 {
		t.Fatalf("two sources down: %+v", r) // median of 19990, 20000, 20010
	}

	c.setDown(true, "cnbc") // three down: no price
	nonce := c.account(0, w.addr).Nonce
	tx := w.buy("AAPL", "1")
	if code, log := c.broadcastRaw(2, tx); code != 0 {
		t.Fatalf("refused by the mempool: %s", log)
	}
	c.waitHeight(c.height()+4, 20*time.Second)
	if a := c.account(0, w.addr); a.Nonce != nonce || a.Holdings["AAPL"] != 2*ledger.QtyScale {
		t.Fatalf("executed or burned without a price: nonce %d→%d, holding %s", nonce, a.Nonce, a.Holdings["AAPL"])
	}

	c.setDown(false, "cnbc") // back to three sources: the waiting order executes
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if a := c.account(0, w.addr); a.Holdings["AAPL"] == 3*ledger.QtyScale {
			if r := lastRecord(a); r.Status != "CONFIRMED" || r.Price != 20000 {
				t.Fatalf("after recovery: %+v", r)
			}
			c.sameStateEverywhere()
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("the waiting order did not execute after a source came back")
}

// The whole network stops for longer than a quote may be ahead of the block
// time (MaxFuture). The next block's time is the last commit's — before the
// stop — so every fresh quote looks like it comes from the future. The
// proposer must drop those quotes rather than propose a bundle that every
// validator rejects: the block closes without prices, the one after it has
// a current time, and trading resumes by itself.
func TestNetworkRecoversAfterDowntime(t *testing.T) {
	c := newCluster(t, 4)
	c.setPrice("AAPL", 20000)
	w := registered(t, c, "alice")
	watchAAPL(c, w) // every block now asks for AAPL quotes
	before := c.height()

	c.stopAll()
	time.Sleep(time.Duration(quotes.MaxFuture+10) * time.Second)
	for i := range c.nodes {
		// see TestEditedStateFileIsDetectedAndRebuilt: an in-process restart
		// cannot reopen the transaction index, so run without it
		conf, err := chainnode.LoadConfig(c.net.NodeHome(i))
		if err != nil {
			t.Fatal(err)
		}
		conf.TxIndex.Indexer = "null"
		cmtcfg.WriteConfigFile(filepath.Join(c.net.NodeHome(i), "config", "config.toml"), conf)
		c.start(i)
	}
	c.waitHeight(before+3, 60*time.Second)
	if code, log := c.broadcastRaw(0, w.buy("AAPL", "1")); code != 0 {
		t.Fatalf("refused after the downtime: %s", log)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if a := c.account(0, w.addr); a.Holdings["AAPL"] == ledger.QtyScale {
			if r := lastRecord(a); r.Status != "CONFIRMED" || r.Price != 20000 {
				t.Fatalf("after the downtime: %+v", r)
			}
			c.waitHeight(c.height()+1, 10*time.Second)
			c.sameStateEverywhere()
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("no trade settled after the downtime (height %d)", c.height())
}
