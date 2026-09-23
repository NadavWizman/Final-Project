package main

import (
	"os"
	"testing"
)

func TestLeaderSettlesOrderAndAllChainsAgree(t *testing.T) {
	c := newTestCluster(t, 2)
	c.submit(t, 1)

	c.leader.cycle()

	st, hash := c.status(1)
	if st != "CONFIRMED" {
		t.Fatalf("order status = %s, want CONFIRMED", st)
	}
	if got := c.leader.node.chain.Head().Hash; got != hash {
		t.Fatalf("leader head %s != settled block %s", short(got), short(hash))
	}
	for _, v := range c.validators {
		if v.chain.Head().Hash != hash {
			t.Fatalf("%s head %s != settled block %s", v.cfg.NodeName, short(v.chain.Head().Hash), short(hash))
		}
	}
}

func TestLeaderRejectsWhenQuorumMissing(t *testing.T) {
	c := newTestCluster(t, 2)
	for _, v := range c.validators {
		v.priceFn = fixedPrice("1.00") // both validators see a very different price
	}
	c.leader.node.priceFn = fixedPrice("200.00")
	c.submit(t, 1)

	c.leader.cycle()

	if st, _ := c.status(1); st != "REJECTED" {
		t.Fatalf("order status = %s, want REJECTED", st)
	}
	if c.leader.node.chain.Length() != 1 {
		t.Fatal("leader appended a block without consensus")
	}
}

// Django settled the order but the response was lost. The pending record makes
// the next cycle finish the commit instead of dropping the block.
func TestLeaderCompletesSettlementAfterLostResponse(t *testing.T) {
	c := newTestCluster(t, 2)
	c.dj.executeFails = true
	c.submit(t, 1)

	c.leader.cycle()
	if c.leader.node.chain.Length() != 1 {
		t.Fatal("block appended although the outcome was unknown")
	}
	if _, err := os.Stat(c.leader.pendingPath); err != nil {
		t.Fatalf("no pending settlement recorded: %v", err)
	}

	c.dj.executeFails = false
	c.leader.cycle()

	_, hash := c.status(1)
	if c.leader.node.chain.Head().Hash != hash {
		t.Fatal("leader did not complete the settled block")
	}
	for _, v := range c.validators {
		if v.chain.Head().Hash != hash {
			t.Fatalf("%s did not receive the settled block", v.cfg.NodeName)
		}
	}
	if _, err := os.Stat(c.leader.pendingPath); !os.IsNotExist(err) {
		t.Fatal("pending record not cleared")
	}
}

// A Leader restarted without its chain file recovers the committed blocks from
// a Validator (re-certified with Django) instead of being rejected forever.
func TestLeaderCatchesUpAfterLosingItsChain(t *testing.T) {
	c := newTestCluster(t, 2)
	c.submit(t, 1)
	c.leader.cycle()
	c.submit(t, 2)
	c.leader.cycle()
	want := c.validators[0].chain.Head().Hash

	// simulate the Leader losing its chain file
	c.leader.node.chain = newTestChain(t, "fresh-leader")
	c.submit(t, 3)
	c.leader.cycle() // validators answer AHEAD; the Leader catches up
	if c.leader.node.chain.HeadIndex() < 2 {
		t.Fatalf("leader head = %d after catch-up, want >= 2", c.leader.node.chain.HeadIndex())
	}
	if b, _ := c.leader.node.chain.BlockAt(2); b.Hash != want {
		t.Fatal("recovered block differs from the cluster's")
	}

	c.leader.cycle() // and consensus continues on top of it
	if st, _ := c.status(3); st != "CONFIRMED" {
		t.Fatalf("order 3 status = %s after catch-up, want CONFIRMED", st)
	}
}
