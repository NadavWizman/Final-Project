package quotes

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"

	pb "nodes/oracle"
)

var five = []string{"yahoo", "nasdaq", "cnbc", "tradingview", "google"}

// Availability policy: with five sources and a quorum of three, one or two
// sources that are down do not stop trading; three down do.
func TestQuorumOfThreeOutOfFive(t *testing.T) {
	ss, src := newSigners(five...)
	for up := 5; up >= 2; up-- {
		var qs []Quote
		for i := 0; i < up; i++ {
			qs = append(qs, ss[i].quote("AAPL", 20000+int64(i), now))
		}
		prices, err := Verify(Encode(Build(qs, 3)), src, 3, listed, now)
		if err != nil {
			t.Fatalf("%d sources up: %v", up, err)
		}
		if _, ok := prices["AAPL"]; ok != (up >= 3) {
			t.Fatalf("%d sources up: priced=%v", up, ok)
		}
	}
}

// The price is the lower median: always a quote some source signed, and
// with at most one liar among the quotes it lies between honest quotes —
// whichever sources the proposer includes and whichever is down.
func TestOneLiarNeverLeavesTheHonestRange(t *testing.T) {
	ss, src := newSigners(five...)
	honest := []int64{20000, 20010, 19990, 20005}
	for _, lie := range []int64{1, 19995, 9_999_999} {
		// the liar is source 4; the proposer includes every subset of
		// 3, 4 or 5 sources that contains it
		for mask := 0; mask < 16; mask++ {
			qs := []Quote{ss[4].quote("AAPL", lie, now)}
			var included []int64
			for i := 0; i < 4; i++ {
				if mask&(1<<i) != 0 {
					qs = append(qs, ss[i].quote("AAPL", honest[i], now))
					included = append(included, honest[i])
				}
			}
			if len(qs) < 3 {
				continue
			}
			prices, err := Verify(Encode(Build(qs, 3)), src, 3, listed, now)
			if err != nil {
				t.Fatal(err)
			}
			lo, hi := included[0], included[0]
			for _, h := range included {
				lo, hi = min(lo, h), max(hi, h)
			}
			if p := prices["AAPL"]; p < lo || p > hi {
				t.Fatalf("liar %d with honest %v moved the price to %d", lie, included, p)
			}
		}
	}
	if m := Median(map[string]int64{"a": 4, "b": 1, "c": 3, "d": 2}); m != 2 {
		t.Fatalf("lower median of 1..4 = %d", m)
	}
}

// A quorum below three (or above the number of sources) is never accepted.
func TestQuorumBounds(t *testing.T) {
	ss, src := newSigners(five...)
	b := Encode(Build([]Quote{ss[0].quote("AAPL", 1, now), ss[1].quote("AAPL", 1, now)}, 2))
	for _, q := range []int{0, 1, 2, 6} {
		if _, err := Verify(b, src, q, listed, now); err == nil {
			t.Errorf("quorum %d accepted", q)
		}
	}
}

// Keys written by `tradedesk-node init` load; anything else does not.
func TestLoadKey(t *testing.T) {
	dir := t.TempDir()
	_, priv, _ := ed25519.GenerateKey(nil)
	good := filepath.Join(dir, "good.key")
	_ = os.WriteFile(good, []byte(hex.EncodeToString(priv.Seed())+"\n"), 0o600)
	if k, err := LoadKey(good); err != nil || !k.Equal(priv) {
		t.Fatalf("load: %v", err)
	}
	bad := filepath.Join(dir, "bad.key")
	_ = os.WriteFile(bad, []byte("not a key"), 0o600)
	for _, f := range []string{bad, filepath.Join(dir, "missing.key")} {
		if _, err := LoadKey(f); err == nil {
			t.Errorf("%s loaded", f)
		}
	}
}

// LocalFetcher signs with every source that has a price.
func TestLocalFetcher(t *testing.T) {
	ss, src := newSigners("a", "b", "c")
	var f LocalFetcher
	for i, s := range ss {
		i := i
		f = append(f, LocalSource{Name: s.name, Key: s.key, Price: func(tk string) (int64, bool) {
			return 10000 + int64(i), tk == "AAPL"
		}})
	}
	qs := f.Fetch(context.Background(), []string{"AAPL", "MSFT"})
	if len(qs) != 3 {
		t.Fatalf("%d quotes", len(qs))
	}
	if p, err := Verify(Encode(Build(qs, 3)), src, 3, listed, time.Now().Unix()); err != nil || p["AAPL"] != 10001 {
		t.Fatalf("%v %v", p, err)
	}
}

// oracleServer is an in-process price source.
type oracleServer struct {
	pb.UnimplementedOracleServiceServer
	s     signer
	price int64
	fail  bool
	delay time.Duration
}

func (o *oracleServer) SignQuote(_ context.Context, r *pb.PriceRequest) (*pb.SignedQuote, error) {
	if o.fail {
		return nil, errors.New("source down")
	}
	time.Sleep(o.delay)
	q := o.s.quote(r.Ticker, o.price, time.Now().Unix())
	return &pb.SignedQuote{Source: q.Source, Ticker: q.Ticker, PriceCents: q.Price, Timestamp: q.Time, Signature: q.Sig}, nil
}

// GRPCFetcher collects from every source over gRPC; a failing source and an
// unreachable address just contribute nothing.
func TestGRPCFetcher(t *testing.T) {
	ss, src := newSigners("a", "b", "c", "d")
	var addrs []string
	for i, s := range ss {
		lis, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		g := grpc.NewServer()
		pb.RegisterOracleServiceServer(g, &oracleServer{s: s, price: 20000 + int64(i), fail: i == 3})
		go func() { _ = g.Serve(lis) }()
		t.Cleanup(g.Stop)
		addrs = append(addrs, lis.Addr().String())
	}
	addrs = append(addrs, "127.0.0.1:1") // nothing listens there
	f := &GRPCFetcher{Addrs: addrs, Timeout: 2 * time.Second}
	qs := f.Fetch(context.Background(), []string{"AAPL"})
	if len(qs) != 3 {
		t.Fatalf("%d quotes from 3 working sources", len(qs))
	}
	p, err := Verify(Encode(Build(qs, 3)), src, 3, listed, time.Now().Unix())
	if err != nil || p["AAPL"] != 20001 {
		t.Fatalf("%v %v", p, err)
	}
}

// With a quorum, a slow source does not set the block time: the fetch ends
// shortly after every ticker has enough quotes.
func TestGRPCFetcherDoesNotWaitForTheSlowest(t *testing.T) {
	ss, src := newSigners(five...)
	var addrs []string
	for i, s := range ss {
		lis, _ := net.Listen("tcp", "127.0.0.1:0")
		g := grpc.NewServer()
		var d time.Duration
		if i == 4 {
			d = 2 * time.Second // the slow one
		}
		pb.RegisterOracleServiceServer(g, &oracleServer{s: s, price: 20000 + int64(i), delay: d})
		go func() { _ = g.Serve(lis) }()
		t.Cleanup(g.Stop)
		addrs = append(addrs, lis.Addr().String())
	}
	f := &GRPCFetcher{Addrs: addrs, MinQuotes: 3}
	f.Fetch(context.Background(), []string{"AAPL"}) // connect
	start := time.Now()
	qs := f.Fetch(context.Background(), []string{"AAPL", "MSFT"})
	if took := time.Since(start); took > time.Second {
		t.Fatalf("waited %v for the slow source", took)
	}
	if p, err := Verify(Encode(Build(qs, 3)), src, 3, listed, time.Now().Unix()); err != nil || len(p) != 2 {
		t.Fatalf("%v %v (%d quotes)", p, err, len(qs))
	}
}

// One faulty source (clock far ahead, unknown name, bad signature, zero
// price) does not spoil the bundle: Valid drops its quotes and the others
// still give a price.
func TestFaultySourceIsDroppedNotFatal(t *testing.T) {
	ss, src := newSigners(five...)
	good := []Quote{ss[0].quote("AAPL", 20000, now), ss[1].quote("AAPL", 20010, now), ss[2].quote("AAPL", 19990, now)}
	bad := []Quote{
		ss[3].quote("AAPL", 20000, now+MaxFuture+30), // skewed clock
		ss[4].quote("AAPL", 0, now),                  // sub-cent price rounded to 0
		{Source: "nobody", Ticker: "AAPL", Price: 1, Time: now},
		{Source: "google", Ticker: "AAPL", Price: 5, Time: now, Sig: []byte{1, 2, 3}},
	}
	if _, err := Verify(Encode(Build(append(good, bad...), 3)), src, 3, listed, now); err == nil {
		t.Fatal("a bundle with a faulty quote should not verify")
	}
	kept := Valid(append(good, bad...), src, listed, now)
	p, err := Verify(Encode(Build(kept, 3)), src, 3, listed, now)
	if err != nil || len(kept) != 3 || p["AAPL"] != 20000 {
		t.Fatalf("kept %d: %v %v", len(kept), p, err)
	}
}
