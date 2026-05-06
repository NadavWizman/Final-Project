package blockchain

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"google.golang.org/protobuf/proto"

	tradingpb "trading/proto"
)

// Chain is an append-only sequence of blocks persisted as JSON-Lines. Each
// line is a base64-encoded protobuf Block. Persistence is intentionally
// dumb — on startup we stream through the file and rebuild in-memory
// state. This makes crash recovery trivial.
type Chain struct {
	mu     sync.RWMutex
	blocks []*tradingpb.Block
	file   *os.File
}

// OpenChain opens (or creates) an append-only chain file at path. If the
// file is empty it seeds a genesis block with index 0.
func OpenChain(path string) (*Chain, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open chain file: %w", err)
	}
	c := &Chain{file: f}

	sc := bufio.NewScanner(f)
	// Allow very large lines — blocks with many txs can exceed the default 64KB.
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(string(line))
		if err != nil {
			return nil, fmt.Errorf("decode chain line: %w", err)
		}
		b := &tradingpb.Block{}
		if err := proto.Unmarshal(raw, b); err != nil {
			return nil, fmt.Errorf("unmarshal block: %w", err)
		}
		c.blocks = append(c.blocks, b)
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(c.blocks) == 0 {
		genesis, err := makeGenesisBlock()
		if err != nil {
			return nil, err
		}
		if err := c.appendNoLock(genesis); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func makeGenesisBlock() (*tradingpb.Block, error) {
	b := &tradingpb.Block{
		Index:     0,
		PrevHash:  make([]byte, 32),
		Timestamp: 0,
	}
	h, err := Hash(b)
	if err != nil {
		return nil, err
	}
	b.Hash = h
	return b, nil
}

func (c *Chain) Head() *tradingpb.Block {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.blocks[len(c.blocks)-1]
}

func (c *Chain) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.blocks)
}

// Append validates the link to the current head and writes the block to the
// log. It does NOT re-validate consensus signatures or state deltas — the
// caller (consensus package) does that before calling Append.
func (c *Chain) Append(b *tradingpb.Block) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	head := c.blocks[len(c.blocks)-1]
	if err := ValidateLink(head, b); err != nil {
		return err
	}
	if err := VerifyHash(b); err != nil {
		return err
	}
	return c.appendNoLock(b)
}

func (c *Chain) appendNoLock(b *tradingpb.Block) error {
	raw, err := proto.Marshal(b)
	if err != nil {
		return fmt.Errorf("marshal block: %w", err)
	}
	// JSON-Lines with base64 payload keeps the file grep-friendly.
	line := base64.StdEncoding.EncodeToString(raw) + "\n"
	if _, err := c.file.Write([]byte(line)); err != nil {
		return fmt.Errorf("write chain: %w", err)
	}
	if err := c.file.Sync(); err != nil {
		return fmt.Errorf("sync chain: %w", err)
	}
	c.blocks = append(c.blocks, b)
	return nil
}

// Iterate returns a shallow copy of the chain — useful for replay.
func (c *Chain) Iterate() []*tradingpb.Block {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*tradingpb.Block, len(c.blocks))
	copy(out, c.blocks)
	return out
}

// DumpJSON is a debug helper.
func (c *Chain) DumpJSON(w io.Writer) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(c.blocks)
}
