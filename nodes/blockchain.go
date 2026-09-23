package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

// Block represents a single block in the chain — contains one transaction
type Block struct {
	Index     int    `json:"index"`
	PrevHash  string `json:"prev_hash"`
	Timestamp int64  `json:"timestamp"`
	OrderID   int    `json:"order_id"`
	Stock     string `json:"stock"`
	OrderType string `json:"order_type"`
	Quantity  string `json:"quantity"`
	Price     string `json:"price"`
	NodeName  string `json:"node_name"`  // node that proposed the block
	Signature string `json:"signature"`  // ECDSA signature of the order creator
	PublicKey string `json:"public_key"` // PEM public key for verification
	Hash      string `json:"hash"`
}

// Chain manages an append-only blockchain
type Chain struct {
	mu       sync.RWMutex
	blocks   []Block
	filePath string // JSONL file for disk persistence
}

// NewChain loads an existing chain from disk, or creates a new one with a Genesis block.
// A chain file that fails verification is fatal: silently skipping bad lines
// would let a tampered or truncated history pass as valid.
func NewChain(nodeName string) *Chain {
	c, err := LoadChain(fmt.Sprintf("chain_%s.jsonl", nodeName))
	if err != nil {
		log.Fatalf("[%s] %v\n  The chain file is corrupt or was written by an older, incompatible "+
			"version. Stop all nodes and delete nodes/chain_*.jsonl to start a fresh chain.", nodeName, err)
	}
	fmt.Printf("[%s] Chain ready (%d blocks, verified)\n", nodeName, len(c.blocks))
	return c
}

// LoadChain reads and fully verifies a chain file, creating it with a Genesis
// block when it does not exist yet.
func LoadChain(filePath string) (*Chain, error) {
	c := &Chain{filePath: filePath}

	data, err := os.ReadFile(filePath)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("cannot read %s: %w", filePath, err)
	}
	for n, line := range splitLines(string(data)) {
		var b Block
		if err := json.Unmarshal([]byte(line), &b); err != nil {
			return nil, fmt.Errorf("%s line %d is not a valid block: %w", filePath, n+1, err)
		}
		c.blocks = append(c.blocks, b)
	}

	if len(c.blocks) == 0 {
		genesis := genesisBlock()
		if err := c.appendToFile(genesis); err != nil {
			return nil, err
		}
		c.blocks = append(c.blocks, genesis)
		return c, nil
	}
	if err := verifyChain(c.blocks); err != nil {
		return nil, fmt.Errorf("%s failed verification: %w", filePath, err)
	}
	return c, nil
}

// genesisBlock is identical on every node, so all chains share block #0.
func genesisBlock() Block {
	g := Block{
		Index:     0,
		PrevHash:  "0000000000000000000000000000000000000000000000000000000000000000",
		Timestamp: 0,
		OrderID:   0,
		Stock:     "GENESIS",
		OrderType: "GENESIS",
		Quantity:  "0",
		Price:     "0",
		NodeName:  "genesis",
	}
	g.Hash = computeHash(g)
	return g
}

// verifyChain checks genesis, index continuity, hash links and every block's hash.
func verifyChain(blocks []Block) error {
	if len(blocks) == 0 || blocks[0] != genesisBlock() {
		return fmt.Errorf("block #0 is not the expected genesis block")
	}
	for i := 1; i < len(blocks); i++ {
		prev, b := blocks[i-1], blocks[i]
		switch {
		case b.Index != prev.Index+1:
			return fmt.Errorf("block at position %d has index %d, want %d", i, b.Index, prev.Index+1)
		case b.PrevHash != prev.Hash:
			return fmt.Errorf("block #%d prev_hash does not match block #%d", b.Index, prev.Index)
		case b.Hash != computeHash(b):
			return fmt.Errorf("block #%d hash does not match its contents", b.Index)
		}
	}
	return nil
}

// computeHash calculates SHA-256 over every field of the block except the hash
// itself. JSON encoding keeps fields unambiguous (no delimiter collisions) and
// covers the signature, public key and proposer too, so none of them can be
// altered without breaking the chain.
func computeHash(b Block) string {
	b.Hash = ""
	raw, _ := json.Marshal(b)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

// Head returns the last block in the chain
func (c *Chain) Head() Block {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.blocks[len(c.blocks)-1]
}

// Length returns the number of blocks in the chain
func (c *Chain) Length() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.blocks)
}

// HeadIndex returns the index of the last block in the chain
func (c *Chain) HeadIndex() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.blocks[len(c.blocks)-1].Index
}

// BlocksFrom returns every block with Index >= from, in order.
// The Leader uses it to replay the blocks a lagging Validator missed.
func (c *Chain) BlocksFrom(from int) []Block {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []Block
	for _, b := range c.blocks {
		if b.Index >= from {
			out = append(out, b)
		}
	}
	return out
}

// Append verifies that the block extends the current head and persists it.
// Validation and append happen under one lock, so two concurrent appends can
// never both pass validation against the same head. The block is added in
// memory only once it is safely on disk.
func (c *Chain) Append(b Block) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := validateAgainst(c.blocks[len(c.blocks)-1], b); err != nil {
		return err
	}
	if err := c.appendToFile(b); err != nil {
		return err
	}
	c.blocks = append(c.blocks, b)
	return nil
}

// CreateNextBlock builds a new block chained from the current Head
func (c *Chain) CreateNextBlock(orderID int, stock, orderType, quantity, price, nodeName, signature, publicKey string) Block {
	head := c.Head()
	b := Block{
		Index:     head.Index + 1,
		PrevHash:  head.Hash,
		Timestamp: time.Now().Unix(),
		OrderID:   orderID,
		Stock:     stock,
		OrderType: orderType,
		Quantity:  quantity,
		Price:     price,
		NodeName:  nodeName,
		Signature: signature,
		PublicKey: publicKey,
	}
	b.Hash = computeHash(b)
	return b
}

// ValidateBlock checks that the block connects to the local chain and its hash is valid
func (c *Chain) ValidateBlock(b Block) error {
	return validateAgainst(c.Head(), b)
}

func validateAgainst(head, b Block) error {
	if b.Index != head.Index+1 {
		return fmt.Errorf("wrong index: expected %d, got %d", head.Index+1, b.Index)
	}
	if b.PrevHash != head.Hash {
		return fmt.Errorf("prev_hash mismatch: expected %s, got %s", short(head.Hash), short(b.PrevHash))
	}
	expected := computeHash(b)
	if b.Hash != expected {
		return fmt.Errorf("hash mismatch: expected %s, got %s", short(expected), short(b.Hash))
	}
	return nil
}

// appendToFile writes one block and fsyncs it, reporting any failure.
func (c *Chain) appendToFile(b Block) error {
	data, err := json.Marshal(b)
	if err != nil {
		return fmt.Errorf("encode block #%d: %w", b.Index, err)
	}
	f, err := os.OpenFile(c.filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open %s: %w", c.filePath, err)
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write block #%d: %w", b.Index, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync block #%d: %w", b.Index, err)
	}
	return nil
}

func splitLines(s string) []string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// short abbreviates a hash for log output. Hashes arriving over the network
// can be any length, so this never slices past the end of the string.
func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
