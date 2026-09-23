package main

import (
	"net"
	"path/filepath"

	"google.golang.org/grpc"
	"nodes/clusterauth"

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
	// executeFails makes execute_order settle the order but answer 500, as
	// when the response to a successful settlement is lost.
	executeFails bool
	executeCalls int
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
	if r.Method == http.MethodPost {
		f.servePost(w, r, path)
		return
	}
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

// certify records every non-genesis block of chain as a settled order in
// Django, the way execute_order does after verifying the quorum.
func (f *fakeDjango) certify(chain *Chain) {
	for _, b := range chain.BlocksFrom(1) {
		f.put(Order{ID: b.OrderID, Stock: b.Stock, OrderType: b.OrderType, Quantity: b.Quantity,
			Status: "CONFIRMED", BlockHash: b.Hash})
	}
}

// servePost mimics execute_order (quorum check, idempotent retry),
// reject_order and node-key registration.
func (f *fakeDjango) servePost(w http.ResponseWriter, r *http.Request, path string) {
	if path == "node-key" {
		w.WriteHeader(http.StatusCreated)
		return
	}
	var id int
	var action string
	if _, err := fmt.Sscanf(strings.ReplaceAll(path, "/", " "), "orders %d %s", &id, &action); err != nil {
		http.NotFound(w, r)
		return
	}
	o, ok := f.orders[id]
	if !ok {
		http.NotFound(w, r)
		return
	}
	var body struct {
		BlockHash string `json:"block_hash"`
		Votes     []Vote `json:"votes"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	switch action {
	case "reject_order":
		o.Status = "REJECTED"
	case "execute_order":
		f.executeCalls++
		if o.Status == "CONFIRMED" && o.BlockHash == body.BlockHash {
			break // idempotent retry
		}
		if o.Status != "SUBMITTED" || len(body.Votes) < quorum {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		o.Status, o.BlockHash = "CONFIRMED", body.BlockHash
		if f.executeFails {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	}
	w.Write([]byte(`{"status":"ok"}`))
}

// testCluster is a Leader plus Validators served over real gRPC on localhost.
type testCluster struct {
	dj         *fakeDjango
	leader     *Leader
	validators []*ValidatorServer
}

func newTestCluster(t *testing.T, nValidators int) *testCluster {
	t.Helper()
	clusterSecret = "test-cluster-secret-0123456789"
	dj := newFakeDjango(t)
	c := &testCluster{dj: dj}
	var addrs []string
	for i := 0; i < nValidators; i++ {
		v := newTestValidatorWith(t, fmt.Sprintf("node%d", i+2), dj)
		lis, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		srv := grpc.NewServer(grpc.ChainUnaryInterceptor(
			recoveryInterceptor, clusterauth.ServerInterceptor(clusterSecret)))
		pb.RegisterConsensusServiceServer(srv, v)
		go srv.Serve(lis)
		t.Cleanup(srv.Stop)
		c.validators = append(c.validators, v)
		addrs = append(addrs, lis.Addr().String())
	}
	node := newTestValidatorWith(t, "node1", dj)
	node.cfg.ValidatorAddresses = addrs
	c.leader = &Leader{node: node, outages: map[int]time.Time{}, settleFailures: map[int]time.Time{},
		pendingPath: filepath.Join(t.TempDir(), "pending_node1.json")}
	return c
}

// submit adds a signed SUBMITTED order to Django.
func (c *testCluster) submit(t *testing.T, id int) {
	o, _ := signedOrder(t, id, "AAPL", "BUY", "1.0000")
	c.dj.put(o)
}

func (c *testCluster) status(id int) (string, string) {
	c.dj.mu.Lock()
	defer c.dj.mu.Unlock()
	o := c.dj.orders[id]
	return o.Status, o.BlockHash
}
