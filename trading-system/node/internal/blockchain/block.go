// Package blockchain defines the block structure, hashing rules, and the
// append-only chain. It is deliberately narrow — consensus logic lives in
// package consensus, state transitions live in package state.
package blockchain

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"

	tradingpb "trading/proto"
)

// Hash derives a canonical block hash. Must be deterministic across nodes.
// We hash:  index || prev_hash || timestamp || merkle_root(txs)
func Hash(b *tradingpb.Block) ([]byte, error) {
	h := sha256.New()
	var buf [8]byte

	binary.BigEndian.PutUint64(buf[:], b.Index)
	h.Write(buf[:])
	h.Write(b.PrevHash)
	binary.BigEndian.PutUint64(buf[:], uint64(b.Timestamp))
	h.Write(buf[:])

	root, err := MerkleRoot(b.Txs)
	if err != nil {
		return nil, err
	}
	h.Write(root)
	return h.Sum(nil), nil
}

// MerkleRoot is a simple SHA-256 pair-hash merkle tree. For an empty tx
// list we return the zero hash.
func MerkleRoot(txs []*tradingpb.SignedOrderTx) ([]byte, error) {
	if len(txs) == 0 {
		zero := make([]byte, 32)
		return zero, nil
	}
	layer := make([][]byte, 0, len(txs))
	for _, t := range txs {
		raw, err := proto.Marshal(t)
		if err != nil {
			return nil, fmt.Errorf("marshal signed tx: %w", err)
		}
		sum := sha256.Sum256(raw)
		layer = append(layer, sum[:])
	}
	for len(layer) > 1 {
		if len(layer)%2 == 1 {
			// duplicate last (bitcoin-style)
			layer = append(layer, layer[len(layer)-1])
		}
		next := make([][]byte, 0, len(layer)/2)
		for i := 0; i < len(layer); i += 2 {
			h := sha256.New()
			h.Write(layer[i])
			h.Write(layer[i+1])
			next = append(next, h.Sum(nil))
		}
		layer = next
	}
	return layer[0], nil
}

// ValidateLink checks that `child` correctly extends `parent`.
func ValidateLink(parent, child *tradingpb.Block) error {
	if child.Index != parent.Index+1 {
		return fmt.Errorf("bad index: parent=%d child=%d", parent.Index, child.Index)
	}
	if !bytesEqual(child.PrevHash, parent.Hash) {
		return fmt.Errorf("prev_hash mismatch: expected %s got %s",
			hex.EncodeToString(parent.Hash), hex.EncodeToString(child.PrevHash))
	}
	return nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

var ErrHashMismatch = errors.New("block hash does not match recomputed hash")

// VerifyHash recomputes the hash and compares it to the one stored on the block.
func VerifyHash(b *tradingpb.Block) error {
	h, err := Hash(b)
	if err != nil {
		return err
	}
	if !bytesEqual(h, b.Hash) {
		return ErrHashMismatch
	}
	return nil
}
