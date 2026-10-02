package ledger

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
)

func noPending(string) (uint64, bool) { return 0, false }

// A signed transaction has exactly one valid encoding: the high-S twin of a
// signature, non-canonical base64, case-variant or duplicate keys and
// trailing bytes are all refused before the mempool.
func TestTransactionHasOneEncoding(t *testing.T) {
	s := newLedger()
	u := newUser(t)
	mustOK(t, block(s, 1000, nil, u.register(t, "single")))

	good := u.order(t, OrderMsg{Kind: "STOCK", Side: "BUY", Ticker: "AAPL", Qty: "1"})
	if _, _, err := s.CheckTx(good, noPending); err != nil {
		t.Fatalf("canonical tx refused: %v", err)
	}
	var env Envelope
	_ = json.Unmarshal(good, &env)
	reenc := func(e Envelope) []byte { b, _ := json.Marshal(e); return b }

	sig, _ := base64.StdEncoding.DecodeString(env.Sig)
	n := elliptic.P256().Params().N
	high := new(big.Int).Sub(n, new(big.Int).SetBytes(sig[32:]))
	twin := append([]byte(nil), sig...)
	high.FillBytes(twin[32:])
	e := env
	e.Sig = base64.StdEncoding.EncodeToString(twin)

	b64 := []byte(env.Sig)
	b64[len(b64)-3] ^= 1 // flips padding bits only: same bytes under lax decoding
	e2 := env
	e2.Sig = string(b64)
	e3 := env
	e3.Sig = env.Sig[:40] + "\n" + env.Sig[40:]

	cases := map[string][]byte{
		"high-S signature":     reenc(e),
		"non-canonical base64": reenc(e2),
		"line break in base64": reenc(e3),
		"trailing bracket":     append(append([]byte(nil), good...), ']'),
		"upper-case key":       []byte(strings.Replace(string(good), `"sig"`, `"SIG"`, 1)),
		"duplicate key":        []byte(strings.Replace(string(good), `{"msg"`, `{"sig":"x","msg"`, 1)),
	}
	for name, raw := range cases {
		if _, _, err := s.CheckTx(raw, noPending); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// Enumerated fields outside their sets make a transaction malformed: it is
// refused by CheckTx and nothing of it is stored (no HTML in anyone's history).
func TestMalformedFieldsAreNotStored(t *testing.T) {
	s := newLedger()
	u := newUser(t)
	mustOK(t, block(s, 1000, nil, u.register(t, "shape")))
	acc := s.Accounts[u.addr]
	for _, o := range []OrderMsg{
		{Kind: "STOCK", Side: `<img src=x onerror=alert(1)>`, Ticker: "AAPL", Qty: "1"},
		{Kind: `<b>`, Side: "BUY", Ticker: "AAPL", Qty: "1"},
		{Kind: "STOCK", Side: "BUY", Ticker: `AAPL"><script>`, Qty: "1"},
		{Kind: "OPTION", Side: "BUY", Ticker: "AAPL", Qty: "1", OptionType: "<i>"},
	} {
		raw := u.order(t, o)
		u.nonce--
		if _, _, err := s.CheckTx(raw, noPending); err == nil {
			t.Errorf("malformed order accepted: %+v", o)
		}
		before := len(acc.History)
		if res := block(s, 1001, p(10000), raw); res[0].Code != CodeInvalid || len(acc.History) != before {
			t.Errorf("malformed order stored: %+v", res[0])
		}
	}
}

// SignP1363 always produces the low-S form.
func TestSignP1363IsLowS(t *testing.T) {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	pub, _ := k.PublicKey.Bytes()
	for i := 0; i < 200; i++ {
		msg := []byte{byte(i)}
		if err := VerifyP1363(pub, msg, SignP1363(k, msg)); err != nil {
			t.Fatal(err)
		}
	}
}

// A registration that could never be included is refused by CheckTx, so it
// cannot fill the mempool; names are unique regardless of case.
func TestRegistrationCheckedBeforeMempool(t *testing.T) {
	s := newLedger()
	alice := newUser(t)
	mustOK(t, block(s, 1000, nil, alice.register(t, "Alice")))
	for name, n := range map[string]string{"taken": "Alice", "case variant": "alice", "invalid": "<b>x</b>"} {
		u := newUser(t)
		if _, _, err := s.CheckTx(u.register(t, n), noPending); err == nil {
			t.Errorf("%s username passed CheckTx", name)
		}
	}
	if a, ok := s.AddressOf("ALICE"); !ok || a != alice.addr {
		t.Fatal("lookup is not case-insensitive")
	}
}

// Open limit orders per account are bounded.
func TestRestingOrdersAreCapped(t *testing.T) {
	s := newLedger()
	u := newUser(t)
	mustOK(t, block(s, 1000, nil, u.register(t, "spammer")))
	acc := s.Accounts[u.addr]
	var txs [][]byte
	for i := 0; i < MaxResting+5; i++ {
		txs = append(txs, u.order(t, OrderMsg{Kind: "STOCK", Side: "BUY", Ticker: "AAPL", Qty: "1", Limit: "1.00"}))
	}
	mustOK(t, block(s, 1001, nil, txs...))
	if len(acc.Resting) != MaxResting || !strings.Contains(last(acc).Reason, "at most") {
		t.Fatalf("resting %d, last %+v", len(acc.Resting), last(acc))
	}
}
