package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// Each node signs its approvals with its own Ed25519 key. Django verifies a
// quorum of these signatures before settling, so no single node — not even the
// Leader — can move money on its own.

// voteMessage is the exact text a node signs to approve a block. It must match
// trading/consensus.py:vote_message byte for byte.
func voteMessage(orderID int, blockHash, price string) []byte {
	return []byte(fmt.Sprintf("tradedesk-vote|%d|%s|%s", orderID, blockHash, price))
}

// signVote returns the base64 signature of a node's approval of a block.
func signVote(key ed25519.PrivateKey, b Block) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(key, voteMessage(b.OrderID, b.Hash, b.Price)))
}

// loadOrCreateNodeKey reads the node's key seed from nodekey_<name>.hex,
// generating and saving a new one (mode 0600) on first start.
func loadOrCreateNodeKey(nodeName string) (ed25519.PrivateKey, error) {
	path := fmt.Sprintf("nodekey_%s.hex", nodeName)
	if data, err := os.ReadFile(path); err == nil {
		seed, err := hex.DecodeString(strings.TrimSpace(string(data)))
		if err != nil || len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("%s is not a valid key seed", path)
		}
		return ed25519.NewKeyFromSeed(seed), nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(seed)+"\n"), 0600); err != nil {
		return nil, fmt.Errorf("save %s: %w", path, err)
	}
	fmt.Printf("[%s] Generated new node signing key (%s)\n", nodeName, path)
	return ed25519.NewKeyFromSeed(seed), nil
}

// RegisterNodeKey tells Django which public key this node signs with. Django
// keeps the first key it sees for a node (trust on first use).
func (d *DjangoClient) RegisterNodeKey(pub ed25519.PublicKey) error {
	body, _ := json.Marshal(map[string]string{"public_key": hex.EncodeToString(pub)})
	req, err := http.NewRequest("POST", d.baseURL+"/node-key/", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.SetBasicAuth(d.user, d.password)
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		return nil
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("django refused node key (%d): %s", resp.StatusCode, msg)
}

// registerKeyUntilDone retries key registration until Django accepts it, so a
// node can be started before the backend.
func registerKeyUntilDone(d *DjangoClient, nodeName string, pub ed25519.PublicKey) {
	for attempt := 0; ; attempt++ {
		err := d.RegisterNodeKey(pub)
		if err == nil {
			fmt.Printf("[%s] Node signing key registered with Django\n", nodeName)
			return
		}
		if strings.Contains(err.Error(), "(409)") {
			log.Fatalf("[%s] %v\n  This node's key file does not match the key Django has on record. "+
				"Restore the original nodekey_%s.hex, or have an admin delete the node key in /admin.",
				nodeName, err, nodeName)
		}
		if attempt == 0 {
			log.Printf("[%s] Cannot register node key yet (%v) — retrying every 5s", nodeName, err)
		}
		time.Sleep(5 * time.Second)
	}
}
