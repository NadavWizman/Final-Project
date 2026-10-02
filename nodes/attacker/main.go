// attacker plays a compromised gateway (or any outsider who can reach a
// node's RPC) against a running TradeDesk network, and reports what the
// network did with each attempt. Every attempt must be refused; the tool
// exits non-zero if one is accepted.
//
//	cd nodes && go run ./attacker                 # node RPC on :26657
//	go run ./attacker -rpc http://127.0.0.1:26667
//
// The deeper attacks a dishonest *validator* could try (shifting a price,
// altering a quote, censoring, equivocating, replaying inside a block) are
// played automatically by the acceptance tests in nodes/e2e.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	rpchttp "github.com/cometbft/cometbft/rpc/client/http"

	"nodes/app"
	"nodes/ledger"
	"nodes/quotes"
)

type wallet struct {
	key   *ecdsa.PrivateKey
	pub   []byte
	addr  string
	nonce uint64
	chain string
}

func newWallet(chain string) *wallet {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	pub, _ := k.PublicKey.Bytes()
	return &wallet{key: k, pub: pub, addr: ledger.Address(pub), chain: chain}
}

func (w *wallet) tx(m ledger.Msg) []byte {
	m.Chain, m.From, m.Nonce = w.chain, w.addr, fmt.Sprint(w.nonce)
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

func buy(qty string) ledger.Msg {
	return ledger.Msg{Type: "order", Order: &ledger.OrderMsg{Kind: "STOCK", Side: "BUY", Ticker: "AAPL", Qty: qty}}
}

func main() {
	rpc := flag.String("rpc", "http://127.0.0.1:26657", "a node's RPC address")
	flag.Parse()
	client, err := rpchttp.New(*rpc, "/websocket")
	if err != nil {
		fail(err)
	}
	ctx := context.Background()
	st, err := client.Status(ctx)
	if err != nil {
		fail(fmt.Errorf("cannot reach %s: %v", *rpc, err))
	}
	chain := st.NodeInfo.Network
	line := strings.Repeat("─", 66)
	fmt.Printf("\n%s\n  ATTACKER — a compromised gateway against chain %q\n%s\n", line, chain, line)

	// a victim with a genuine account
	victim := newWallet(chain)
	if res, err := client.BroadcastTxCommit(ctx, victim.tx(ledger.Msg{Type: "register",
		Username: fmt.Sprintf("victim_%d", time.Now().Unix()%100000)})); err != nil || res.TxResult.Code != 0 {
		fail(fmt.Errorf("could not register the victim: %v", err))
	}
	fmt.Printf("  victim registered: %s\n\n", victim.addr)

	accepted := 0
	try := func(name string, tx []byte) {
		res, err := client.BroadcastTxSync(ctx, tx)
		switch {
		case err != nil:
			fmt.Printf("  %-46s REFUSED  %s\n", name, short(err.Error()))
		case res.Code != 0:
			fmt.Printf("  %-46s REFUSED  %s\n", name, short(res.Log))
		default:
			accepted++
			fmt.Printf("  %-46s ACCEPTED — a defence has regressed!\n", name)
		}
	}

	// 1. sign an order in the victim's name with the gateway's own key
	forger := newWallet(chain)
	forger.addr, forger.nonce = victim.addr, victim.nonce
	try("order signed with a key that is not the victim's", forger.tx(buy("50")))

	// 2. change a genuinely signed order in transit
	var env ledger.Envelope
	genuine := victim.tx(buy("1"))
	_ = json.Unmarshal(genuine, &env)
	env.Msg = strings.Replace(env.Msg, `"qty":"1"`, `"qty":"50"`, 1)
	altered, _ := json.Marshal(env)
	try("victim's order with the quantity changed", altered)

	// 3. replay a used transaction (the registration)
	victim.nonce = 0
	try("replay of an already-executed transaction", victim.tx(ledger.Msg{Type: "register", Username: "again"}))

	// 4. inject prices: a quote bundle signed with an unregistered key
	_, fake, _ := ed25519.GenerateKey(nil)
	q := quotes.Quote{Source: "yahoo", Ticker: "AAPL", Price: 100, Time: time.Now().Unix()}
	q.Sig = ed25519.Sign(fake, q.Message())
	try("forged price quotes sent to the mempool", quotes.Encode(quotes.Bundle{Quotes: []quotes.Quote{q},
		Prices: map[string]int64{"AAPL": 100}}))

	// 5. take over the network: add a validator with a single signature
	pub, priv, _ := ed25519.GenerateKey(nil)
	ch := app.ValsetChange{Chain: chain, Seq: 0, PubKey: pub, Power: 1000}
	vtx, _ := json.Marshal(app.ValsetTx{Valset: ch, Sigs: []app.ValsetApproval{app.SignValset(priv, ch)}})
	try("add a validator without 3 of 4 approvals", vtx)

	fmt.Printf("\n%s\n", line)
	if accepted > 0 {
		fmt.Printf("  RESULT: %d attack(s) accepted — VULNERABILITY\n%s\n\n", accepted, line)
		os.Exit(1)
	}
	fmt.Printf("  RESULT: every attempt was refused. DEFENCE HOLDS.\n%s\n\n", line)
}

func short(s string) string {
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(2)
}
