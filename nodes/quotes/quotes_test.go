package quotes

import (
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
)

type signer struct {
	name string
	key  ed25519.PrivateKey
}

func newSigners(names ...string) ([]signer, []Source) {
	var ss []signer
	var src []Source
	for _, n := range names {
		pub, priv, _ := ed25519.GenerateKey(nil)
		ss = append(ss, signer{n, priv})
		src = append(src, Source{Name: n, PubKey: pub})
	}
	return ss, src
}

func (s signer) quote(ticker string, price, at int64) Quote {
	q := Quote{Source: s.name, Ticker: ticker, Price: price, Time: at}
	q.Sig = ed25519.Sign(s.key, q.Message())
	return q
}

func listed(t string) bool { return t == "AAPL" || t == "MSFT" }

const now = int64(1_790_000_000)

func TestMedianOfThreeIgnoresOneLiar(t *testing.T) {
	ss, src := newSigners("yahoo", "nasdaq", "rogue")
	b := Build([]Quote{ss[0].quote("AAPL", 19025, now), ss[1].quote("AAPL", 19030, now), ss[2].quote("AAPL", 100, now)}, 3)
	prices, err := Verify(Encode(b), src, 3, listed, now)
	if err != nil || prices["AAPL"] != 19025 {
		t.Fatalf("median = %v, %v", prices, err)
	}
}

func TestShiftedDeclaredPriceIsRejected(t *testing.T) {
	ss, src := newSigners("a", "b", "c")
	b := Build([]Quote{ss[0].quote("AAPL", 20000, now), ss[1].quote("AAPL", 20010, now), ss[2].quote("AAPL", 19990, now)}, 3)
	b.Prices["AAPL"] = 20100 // the proposer shifts the price by 0.5 %
	if _, err := Verify(Encode(b), src, 3, listed, now); err == nil || !strings.Contains(err.Error(), "median") {
		t.Fatalf("shifted price accepted: %v", err)
	}
}

func TestAlteredQuoteFailsItsSignature(t *testing.T) {
	ss, src := newSigners("a", "b", "c")
	b := Build([]Quote{ss[0].quote("AAPL", 20000, now), ss[1].quote("AAPL", 20010, now), ss[2].quote("AAPL", 19990, now)}, 3)
	b.Quotes[1].Price = 25000 // changed on the network
	b.Prices["AAPL"] = 20010
	if _, err := Verify(Encode(b), src, 3, listed, now); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("altered quote accepted: %v", err)
	}
}

func TestOtherRejections(t *testing.T) {
	ss, src := newSigners("a", "b", "c")
	_, stranger := newSigners("a")
	cases := map[string]Bundle{
		"stale":    Build([]Quote{ss[0].quote("AAPL", 1, now-MaxAge-1), ss[1].quote("AAPL", 1, now), ss[2].quote("AAPL", 1, now)}, 3),
		"future":   Build([]Quote{ss[0].quote("AAPL", 1, now+MaxFuture+1), ss[1].quote("AAPL", 1, now), ss[2].quote("AAPL", 1, now)}, 3),
		"unlisted": Build([]Quote{ss[0].quote("TSLA", 1, now), ss[1].quote("TSLA", 1, now), ss[2].quote("TSLA", 1, now)}, 3),
	}
	for name, b := range cases {
		if _, err := Verify(Encode(b), src, 3, listed, now); err == nil {
			t.Errorf("%s bundle accepted", name)
		}
	}
	good := Build([]Quote{ss[0].quote("AAPL", 1, now), ss[1].quote("AAPL", 1, now), ss[2].quote("AAPL", 1, now)}, 3)
	srcWrongKey := append([]Source{stranger[0]}, src[1:]...)
	if _, err := Verify(Encode(good), srcWrongKey, 3, listed, now); err == nil {
		t.Error("quote signed by an unregistered key accepted")
	}
	dup := good
	dup.Quotes = append(dup.Quotes, ss[0].quote("AAPL", 1, now))
	if _, err := Verify(Encode(dup), src, 3, listed, now); err == nil {
		t.Error("two quotes from one source accepted")
	}
}

func TestTickerBelowQuorumHasNoPrice(t *testing.T) {
	ss, src := newSigners("a", "b", "c")
	b := Build([]Quote{ss[0].quote("AAPL", 100, now), ss[1].quote("AAPL", 100, now)}, 3)
	prices, err := Verify(Encode(b), src, 3, listed, now)
	if err != nil || len(prices) != 0 {
		t.Fatalf("partial quotes priced: %v %v", prices, err)
	}
	var check Bundle
	_ = json.Unmarshal(Encode(b), &check)
	if !IsBundle(Encode(b)) || IsBundle([]byte(`{"msg":"x"}`)) {
		t.Fatal("IsBundle")
	}
}
