package main

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"path/filepath"
	"testing"

	pb "nodes/consensus"
)

// newTestChain builds an isolated chain backed by a temp file, seeded with Genesis.
func newTestChain(t *testing.T, name string) *Chain {
	t.Helper()
	c, err := LoadChain(filepath.Join(t.TempDir(), "chain_"+name+".jsonl"))
	if err != nil {
		t.Fatalf("LoadChain: %v", err)
	}
	return c
}

// grow appends n synthetic order blocks to the chain.
func grow(c *Chain, n int) {
	for i := 0; i < n; i++ {
		b := c.CreateNextBlock(c.HeadIndex()+1, "AAPL", "BUY", "1", "200.00", "node1", "", "")
		if err := c.Append(b); err != nil {
			panic(err)
		}
	}
}

func newTestValidator(t *testing.T, name string) *ValidatorServer {
	t.Helper()
	return newTestValidatorWith(t, name, newFakeDjango(t))
}

func newTestValidatorWith(t *testing.T, name string, dj *fakeDjango) *ValidatorServer {
	t.Helper()
	_, key, _ := ed25519.GenerateKey(nil)
	return &ValidatorServer{cfg: Config{NodeName: name}, chain: newTestChain(t, name),
		django: dj.client(), key: key, priceFn: fixedPrice("200.00")}
}

func TestParseDesync(t *testing.T) {
	cases := []struct {
		name     string
		reason   string
		wantHead int
		wantOK   bool
	}{
		{"behind", "DESYNC head=72 | chain validation failed: wrong index: expected 73, got 74", 72, true},
		{"genesis only", "DESYNC head=0 | chain validation failed", 0, true},
		{"prev hash mismatch is not a desync", "chain validation failed: prev_hash mismatch: expected abc, got def", 0, false},
		{"oracle failure is not a desync", "oracle unreachable: connection refused", 0, false},
		{"malformed index", "DESYNC head=xx | broken", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			head, ok := parseDesync(tc.reason)
			if ok != tc.wantOK || head != tc.wantHead {
				t.Fatalf("parseDesync(%q) = (%d, %v), want (%d, %v)",
					tc.reason, head, ok, tc.wantHead, tc.wantOK)
			}
		})
	}
}

func TestBlocksFrom(t *testing.T) {
	c := newTestChain(t, "leader")
	grow(c, 5) // indices 1..5

	got := c.BlocksFrom(3)
	if len(got) != 3 {
		t.Fatalf("BlocksFrom(3) returned %d blocks, want 3", len(got))
	}
	for i, b := range got {
		if want := 3 + i; b.Index != want {
			t.Fatalf("block %d has index %d, want %d", i, b.Index, want)
		}
	}
	if n := len(c.BlocksFrom(99)); n != 0 {
		t.Fatalf("BlocksFrom(99) returned %d blocks, want 0", n)
	}
}

// A Validator must not append a block that leaves a gap in its chain.
func TestCommitRejectsGap(t *testing.T) {
	leader := newTestChain(t, "leader")
	grow(leader, 5)

	v := newTestValidator(t, "node2") // still at Genesis (#0)
	future := leader.BlocksFrom(5)[0] // block #5

	resp, err := v.Commit(context.Background(), &pb.CommitRequest{Block: blockToProto(future)})
	if err != nil {
		t.Fatalf("Commit returned error: %v", err)
	}
	if resp.Status == "committed" {
		t.Fatal("Commit accepted a block that leaves a gap; it must be rejected")
	}
	if v.chain.Length() != 1 {
		t.Fatalf("chain grew to %d blocks after a rejected commit, want 1", v.chain.Length())
	}
}

// Replayed commits are expected during resync, so they must not duplicate blocks.
func TestCommitIsIdempotent(t *testing.T) {
	leader := newTestChain(t, "leader")
	grow(leader, 1)
	block := leader.BlocksFrom(1)[0]

	v := newTestValidator(t, "node2")
	proto := blockToProto(block)

	first, _ := v.Commit(context.Background(), &pb.CommitRequest{Block: proto})
	if first.Status != "committed" {
		t.Fatalf("first commit status = %q, want committed", first.Status)
	}

	second, _ := v.Commit(context.Background(), &pb.CommitRequest{Block: proto})
	if second.Status != "already_present" {
		t.Fatalf("second commit status = %q, want already_present", second.Status)
	}
	if v.chain.Length() != 2 {
		t.Fatalf("chain has %d blocks after duplicate commit, want 2", v.chain.Length())
	}
}

// The full recovery path: a Validator that missed commits while offline reports
// how far behind it is, and replaying those blocks brings it back in sync.
func TestValidatorRecoversFromDesync(t *testing.T) {
	leader := newTestChain(t, "leader")
	v := newTestValidator(t, "node3")

	// both start in sync at block #1
	grow(leader, 1)
	if _, err := v.Commit(context.Background(), &pb.CommitRequest{Block: blockToProto(leader.BlocksFrom(1)[0])}); err != nil {
		t.Fatalf("initial commit failed: %v", err)
	}

	// node3 goes offline; the other nodes commit blocks #2..#4 without it
	grow(leader, 3)
	if v.HeadIndexForTest() != 1 {
		t.Fatalf("validator head = %d before resync, want 1", v.HeadIndexForTest())
	}

	// node3 returns and is asked to vote on block #5 — it must report a desync
	reason := desyncReasonFor(v, leader)
	head, ok := parseDesync(reason)
	if !ok {
		t.Fatalf("expected a DESYNC rejection, got %q", reason)
	}
	if head != 1 {
		t.Fatalf("reported head = %d, want 1", head)
	}

	// the Leader replays what it missed
	for _, b := range leader.BlocksFrom(head + 1) {
		resp, err := v.Commit(context.Background(), &pb.CommitRequest{Block: blockToProto(b)})
		if err != nil {
			t.Fatalf("resync commit of block #%d failed: %v", b.Index, err)
		}
		if resp.Status != "committed" {
			t.Fatalf("resync commit of block #%d returned %q", b.Index, resp.Status)
		}
	}

	if v.HeadIndexForTest() != leader.HeadIndex() {
		t.Fatalf("validator head = %d after resync, want %d",
			v.HeadIndexForTest(), leader.HeadIndex())
	}

	// and the next proposed block now connects cleanly
	next := leader.CreateNextBlock(99, "AAPL", "BUY", "1", "200.00", "node1", "", "")
	if err := v.chain.ValidateBlock(next); err != nil {
		t.Fatalf("validator still rejects the next block after resync: %v", err)
	}
}

// HeadIndexForTest exposes the validator's chain head for assertions.
func (s *ValidatorServer) HeadIndexForTest() int { return s.chain.HeadIndex() }

// desyncReasonFor reproduces the chain-connectivity rejection Propose would emit,
// without requiring a live Oracle for the remaining checks.
func desyncReasonFor(v *ValidatorServer, leader *Chain) string {
	block := leader.CreateNextBlock(100, "AAPL", "BUY", "1", "200.00", "node1", "", "")
	err := v.chain.ValidateBlock(block)
	if err == nil {
		return ""
	}
	reason := fmt.Sprintf("chain validation failed: %v", err)
	if head := v.chain.HeadIndex(); block.Index > head+1 {
		reason = fmt.Sprintf("%s%d | %s", desyncPrefix, head, reason)
	}
	return reason
}
