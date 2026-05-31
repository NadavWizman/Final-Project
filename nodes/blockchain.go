package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
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

// NewChain loads an existing chain from disk, or creates a new one with a Genesis block
func NewChain(nodeName string) *Chain {
	filePath := fmt.Sprintf("chain_%s.jsonl", nodeName)
	c := &Chain{filePath: filePath}

	// try to load an existing chain
	if data, err := os.ReadFile(filePath); err == nil {
		for _, line := range splitLines(string(data)) {
			var b Block
			if json.Unmarshal([]byte(line), &b) == nil {
				c.blocks = append(c.blocks, b)
			}
		}
		fmt.Printf("[%s] Loaded existing chain (%d blocks)\n", nodeName, len(c.blocks))
	}

	// if no blocks — create Genesis block
	if len(c.blocks) == 0 {
		genesis := Block{
			Index:     0,
			PrevHash:  "0000000000000000000000000000000000000000000000000000000000000000",
			Timestamp: time.Now().Unix(),
			OrderID:   0,
			Stock:     "GENESIS",
			OrderType: "GENESIS",
			Quantity:  "0",
			Price:     "0",
			NodeName:  nodeName,
		}
		genesis.Hash = computeHash(genesis)
		c.blocks = append(c.blocks, genesis)
		c.appendToFile(genesis)
		fmt.Printf("[%s] Created new chain with Genesis block\n", nodeName)
	}

	return c
}

// computeHash calculates SHA-256 over the block's fields
func computeHash(b Block) string {
	raw := fmt.Sprintf("%d|%s|%d|%d|%s|%s|%s|%s",
		b.Index, b.PrevHash, b.Timestamp, b.OrderID,
		b.Stock, b.OrderType, b.Price, b.Quantity,
	)
	h := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", h)
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

// Append adds a block to the chain and saves it to disk
func (c *Chain) Append(b Block) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.blocks = append(c.blocks, b)
	c.appendToFile(b)
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
	head := c.Head()
	if b.Index != head.Index+1 {
		return fmt.Errorf("wrong index: expected %d, got %d", head.Index+1, b.Index)
	}
	if b.PrevHash != head.Hash {
		return fmt.Errorf("prev_hash mismatch: expected %s, got %s", head.Hash[:12], b.PrevHash[:12])
	}
	expected := computeHash(b)
	if b.Hash != expected {
		return fmt.Errorf("hash mismatch: expected %s, got %s", expected[:12], b.Hash[:12])
	}
	return nil
}

func (c *Chain) appendToFile(b Block) {
	data, err := json.Marshal(b)
	if err != nil {
		return
	}
	f, err := os.OpenFile(c.filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(data, '\n'))
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
