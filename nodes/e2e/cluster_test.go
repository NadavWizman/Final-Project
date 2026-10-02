package e2e

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	mrand "math/rand"
	"sync"
	"testing"
	"time"

	rpclocal "github.com/cometbft/cometbft/rpc/client/local"

	"nodes/chainnode"
	"nodes/ledger"
	"nodes/quotes"
)

// cluster is a 4-validator TradeDesk network running in this process, with
// three in-process oracle signers.
type cluster struct {
	t       *testing.T
	net     chainnode.Testnet
	nodes   []*chainnode.Node
	sources []quotes.LocalSource
	mu      sync.Mutex
	prices  map[string]map[string]int64 // source → ticker → cents
}

func newCluster(t *testing.T, validators int) *cluster {
	t.Helper()
	base := 30000 + mrand.Intn(20000)
	c := &cluster{t: t, prices: map[string]map[string]int64{}}
	c.net = chainnode.Testnet{
		Dir: t.TempDir(), Validators: validators, ChainID: "tradedesk-e2e",
		Oracles: []string{"yahoo", "nasdaq", "cnbc"}, BasePort: base, OracleBase: base + 900,
		BlockTime: 200 * time.Millisecond, EmptyEvery: time.Second, TimeoutPropose: time.Second,
	}
	if _, err := chainnode.Init(c.net); err != nil {
		t.Fatal(err)
	}
	for _, name := range c.net.Oracles {
		key, err := quotes.LoadKey(c.net.OracleKeyFile(name))
		if err != nil {
			t.Fatal(err)
		}
		name := name
		c.prices[name] = map[string]int64{}
		c.sources = append(c.sources, quotes.LocalSource{Name: name, Key: key, Price: func(ticker string) (int64, bool) {
			c.mu.Lock()
			defer c.mu.Unlock()
			p, ok := c.prices[name][ticker]
			return p, ok
		}})
	}
	for i := 0; i < validators; i++ {
		c.start(i)
	}
	t.Cleanup(c.stopAll)
	c.waitHeight(2, 30*time.Second)
	return c
}

func (c *cluster) start(i int) {
	c.t.Helper()
	n, err := chainnode.Start(c.net.NodeHome(i), quotes.LocalFetcher(c.sources), nil)
	if err != nil {
		c.t.Fatalf("start node%d: %v", i, err)
	}
	for len(c.nodes) <= i {
		c.nodes = append(c.nodes, nil)
	}
	c.nodes[i] = n
}

// stop shuts nodes down safely. CometBFT v0.38's consensus reactor has a
// goroutine per peer that sleeps 2 s between queries and may then read the
// block store; stopping the node outright can close the store under it.
// Disconnecting first and waiting out that sleep lets those goroutines exit.
func (c *cluster) stop(idx ...int) {
	var live []int
	for _, i := range idx {
		if n := c.nodes[i]; n != nil && n.IsRunning() {
			_ = n.Switch().Stop()
			live = append(live, i)
		}
	}
	if len(live) > 0 {
		time.Sleep(2500 * time.Millisecond)
	}
	for _, i := range live {
		_ = c.nodes[i].Stop()
		c.nodes[i].Wait()
	}
	for _, i := range idx {
		c.nodes[i] = nil
	}
}

func (c *cluster) stopAll() {
	var all []int
	for i := range c.nodes {
		all = append(all, i)
	}
	c.stop(all...)
}

// setPrice sets what each source reports for a ticker.
func (c *cluster) setPrice(ticker string, perSource ...int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, name := range c.net.Oracles {
		p := perSource[0]
		if i < len(perSource) {
			p = perSource[i]
		}
		c.prices[name][ticker] = p
	}
}

func (c *cluster) running() []*chainnode.Node {
	var out []*chainnode.Node
	for _, n := range c.nodes {
		if n != nil {
			out = append(out, n)
		}
	}
	return out
}

func (c *cluster) height() int64 {
	var h int64
	for _, n := range c.running() {
		if b := n.BlockStore().Height(); b > h {
			h = b
		}
	}
	return h
}

func (c *cluster) waitHeight(h int64, timeout time.Duration) {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ok := true
		for _, n := range c.running() {
			if n.BlockStore().Height() < h {
				ok = false
			}
		}
		if ok {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	c.t.Fatalf("cluster did not reach height %d (at %d)", h, c.height())
}

func (c *cluster) client(i int) *rpclocal.Local { return rpclocal.New(c.nodes[i].Node) }

// submit broadcasts a transaction through node i and waits until it is in a block.
func (c *cluster) submit(i int, tx []byte) {
	c.t.Helper()
	res, err := c.client(i).BroadcastTxSync(context.Background(), tx)
	if err != nil || res.Code != 0 {
		c.t.Fatalf("broadcast: %v %+v", err, res)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if r, err := c.client(i).Tx(context.Background(), res.Hash, false); err == nil {
			_ = r
			c.waitHeight(r.Height+1, 10*time.Second) // committed and applied everywhere
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	c.t.Fatal("transaction not committed")
}

func (c *cluster) account(i int, addr string) *ledger.Account {
	c.t.Helper()
	r, err := c.client(i).ABCIQuery(context.Background(), "/account", []byte(addr))
	if err != nil || r.Response.Code != 0 {
		c.t.Fatalf("query account: %v %+v", err, r)
	}
	var out struct{ Account *ledger.Account }
	_ = json.Unmarshal(r.Response.Value, &out)
	return out.Account
}

func (c *cluster) status(i int) map[string]any {
	r, err := c.client(i).ABCIQuery(context.Background(), "/status", nil)
	if err != nil {
		c.t.Fatal(err)
	}
	var out map[string]any
	_ = json.Unmarshal(r.Response.Value, &out)
	return out
}

// sameStateEverywhere waits until every running node reports the same app
// hash at the same height.
func (c *cluster) sameStateEverywhere() string {
	c.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var hashes []string
		var heights []float64
		for i, n := range c.nodes {
			if n == nil {
				continue
			}
			s := c.status(i)
			hashes = append(hashes, s["app_hash"].(string))
			heights = append(heights, s["height"].(float64))
		}
		same := true
		for k := range hashes {
			if hashes[k] != hashes[0] || heights[k] != heights[0] {
				same = false
			}
		}
		if same {
			return hashes[0]
		}
		time.Sleep(100 * time.Millisecond)
	}
	c.t.Fatal("nodes did not converge on one state")
	return ""
}

// wallet signs transactions the way the browser does.
type wallet struct {
	key   *ecdsa.PrivateKey
	pub   []byte
	addr  string
	nonce uint64
}

func newWallet() *wallet {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	pub, _ := k.PublicKey.Bytes()
	return &wallet{key: k, pub: pub, addr: ledger.Address(pub)}
}

func (w *wallet) tx(m ledger.Msg) []byte {
	m.Chain, m.From, m.Nonce = "tradedesk-e2e", w.addr, fmt.Sprint(w.nonce)
	w.nonce++
	msg, _ := json.Marshal(m)
	d := sha256.Sum256(msg)
	r, s, _ := ecdsa.Sign(rand.Reader, w.key, d[:])
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	env := ledger.Envelope{Msg: string(msg), Sig: base64.StdEncoding.EncodeToString(sig)}
	if m.Type == "register" {
		env.PubKey = base64.StdEncoding.EncodeToString(w.pub)
	}
	raw, _ := json.Marshal(env)
	return raw
}

func (w *wallet) register(name string) []byte {
	return w.tx(ledger.Msg{Type: "register", Username: name})
}

func (w *wallet) buy(ticker, qty string) []byte {
	return w.tx(ledger.Msg{Type: "order", Order: &ledger.OrderMsg{Kind: "STOCK", Side: "BUY", Ticker: ticker, Qty: qty}})
}

func lastRecord(a *ledger.Account) *ledger.Record { return a.History[len(a.History)-1] }

var _ = hex.EncodeToString

func TestFourNodesSettleATradeIdentically(t *testing.T) {
	c := newCluster(t, 4)
	c.setPrice("AAPL", 19025, 19030, 19020)
	w := newWallet()
	c.submit(0, w.register("alice"))
	c.submit(1, w.buy("AAPL", "2"))

	for i := range c.nodes {
		a := c.account(i, w.addr)
		if r := lastRecord(a); r.Status != "CONFIRMED" || r.Price != 19025 {
			t.Fatalf("node%d: %+v", i, r)
		}
		if a.Cash != 1_000_000-38_050 || a.Holdings["AAPL"] != 2*ledger.QtyScale {
			t.Fatalf("node%d balance %s", i, a.Cash)
		}
	}
	c.sameStateEverywhere()
}
