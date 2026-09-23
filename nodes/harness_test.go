package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	pb "nodes/consensus"
)

// fakeDjango serves /orders/ and /orders/<id>/ from an in-memory map, the way
// the real API does for a node account.
type fakeDjango struct {
	mu     sync.Mutex
	orders map[int]*Order
	srv    *httptest.Server
}

func newFakeDjango(t *testing.T) *fakeDjango {
	t.Helper()
	f := &fakeDjango{orders: map[int]*Order{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeDjango) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.Trim(r.URL.Path, "/")
	if path == "orders" {
		var list []Order
		for _, o := range f.orders {
			list = append(list, *o)
		}
		json.NewEncoder(w).Encode(list)
		return
	}
	var id int
	if _, err := fmt.Sscanf(path, "orders/%d", &id); err == nil {
		if o, ok := f.orders[id]; ok {
			json.NewEncoder(w).Encode(o)
			return
		}
	}
	http.NotFound(w, r)
}

func (f *fakeDjango) put(o Order) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.orders[o.ID] = &o
}

func (f *fakeDjango) client() *DjangoClient {
	return &DjangoClient{baseURL: f.srv.URL, user: "node", password: "x", http: f.srv.Client()}
}

// signedOrder builds a SUBMITTED order signed the same way Django signs.
func signedOrder(t *testing.T, id int, stock, side, qty string) (Order, *ecdsa.PrivateKey) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	return signOrderWith(t, key, id, stock, side, qty), key
}

func signOrderWith(t *testing.T, key *ecdsa.PrivateKey, id int, stock, side, qty string) Order {
	t.Helper()
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	pub := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	msg := fmt.Sprintf(`{"nonce":"n%d","order_type":"%s","quantity":"%s","stock":"%s","trade_type":"STOCK"}`,
		id, side, qty, stock)
	digest := sha256.Sum256([]byte(msg))
	sig, _ := ecdsa.SignASN1(rand.Reader, key, digest[:])
	return Order{
		ID: id, Stock: stock, Status: "SUBMITTED", OrderType: side, TradeType: "STOCK",
		Quantity: qty, Nonce: fmt.Sprintf("n%d", id),
		Signature: base64.StdEncoding.EncodeToString(sig), PublicKey: pub, SignedMessage: msg,
	}
}

// fixedPrice is a price source that always reports the given price, fresh.
func fixedPrice(price string) func(string) (*OracleData, error) {
	return func(ticker string) (*OracleData, error) {
		return &OracleData{Ticker: ticker, ExecutionPrice: price,
			Timestamp: time.Now().UTC().Format(time.RFC3339Nano)}, nil
	}
}

// proposalFor builds the block and envelope an honest Leader would send.
func proposalFor(chain *Chain, o Order, price string) (Block, *pb.ProposeRequest) {
	b := chain.CreateNextBlock(o.ID, o.Stock, o.OrderType, o.Quantity, price, "node1", o.Signature, o.PublicKey)
	return b, &pb.ProposeRequest{
		Block: blockToProto(b), OraclePrice: price,
		OracleTimestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Signature:       o.Signature, PublicKey: o.PublicKey, SignedMessage: o.SignedMessage,
	}
}
