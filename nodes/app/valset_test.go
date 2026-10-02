package app

import (
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"

	"nodes/ledger"
)

func testChain(n int) (*Chain, []ed25519.PrivateKey) {
	c := &Chain{Ledger: ledger.NewState(ledger.DefaultParams("vs-test", []string{"AAPL"}))}
	var keys []ed25519.PrivateKey
	for i := 0; i < n; i++ {
		pub, priv, _ := ed25519.GenerateKey(nil)
		c.Validators = append(c.Validators, Validator{PubKey: pub, Power: 10})
		keys = append(keys, priv)
	}
	sortValidators(c.Validators)
	return c, keys
}

func change(c *Chain, signers []ed25519.PrivateKey, pub []byte, power int64) []byte {
	ch := ValsetChange{Chain: "vs-test", Seq: c.ValsetSeq, PubKey: pub, Power: power}
	tx := ValsetTx{Valset: ch}
	for _, k := range signers {
		tx.Sigs = append(tx.Sigs, SignValset(k, ch))
	}
	raw, _ := json.Marshal(tx)
	return raw
}

func TestValsetChangeNeedsThreeOfFour(t *testing.T) {
	c, keys := testChain(4)
	newPub, _, _ := ed25519.GenerateKey(nil)

	if err := c.applyValset(change(c, keys[:2], newPub, 10)); err == nil || !strings.Contains(err.Error(), "2/3") {
		t.Fatalf("2 of 4 signatures accepted: %v", err)
	}
	dup := change(c, []ed25519.PrivateKey{keys[0], keys[0], keys[0]}, newPub, 10)
	if err := c.applyValset(dup); err == nil {
		t.Fatal("one validator signing three times accepted")
	}
	_, outsider, _ := ed25519.GenerateKey(nil)
	if err := c.applyValset(change(c, []ed25519.PrivateKey{keys[0], keys[1], outsider}, newPub, 10)); err == nil {
		t.Fatal("an outsider's signature counted")
	}

	good := change(c, keys[:3], newPub, 10)
	if err := c.applyValset(good); err != nil {
		t.Fatalf("3 of 4: %v", err)
	}
	if len(c.Validators) != 5 || c.ValsetSeq != 1 {
		t.Fatalf("set not updated: %d validators, seq %d", len(c.Validators), c.ValsetSeq)
	}
	if err := c.applyValset(good); err == nil || !strings.Contains(err.Error(), "sequence") {
		t.Fatalf("replayed change accepted: %v", err)
	}
}

func TestValsetCannotDropBelowFour(t *testing.T) {
	c, keys := testChain(4)
	if err := c.applyValset(change(c, keys, c.Validators[0].PubKey, 0)); err == nil || !strings.Contains(err.Error(), "below 4") {
		t.Fatalf("removal to 3 validators accepted: %v", err)
	}
}

func TestValidatorDiff(t *testing.T) {
	c, _ := testChain(4)
	prev := append([]Validator(nil), c.Validators...)
	next := append([]Validator(nil), prev[1:]...)
	next[0].Power = 20
	if d := validatorDiff(prev, next); len(d) != 2 {
		t.Fatalf("diff = %v", d)
	}
}
