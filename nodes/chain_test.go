package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChainPersistsAndReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chain_x.jsonl")
	c, err := LoadChain(path)
	if err != nil {
		t.Fatal(err)
	}
	grow(c, 3)
	reloaded, err := LoadChain(path)
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if reloaded.Length() != 4 || reloaded.Head().Hash != c.Head().Hash {
		t.Fatalf("reloaded chain differs: %d blocks, head %s", reloaded.Length(), short(reloaded.Head().Hash))
	}
}

// Every field is covered by the hash, including the signature and public key.
func TestTamperedChainFileIsRejected(t *testing.T) {
	for _, tc := range []struct{ name, from, to string }{
		{"price", `"price":"200.00"`, `"price":"1.00"`},
		{"signature", `"signature":"sig"`, `"signature":"forged"`},
		{"public key", `"public_key":"pk"`, `"public_key":"attacker"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "chain_x.jsonl")
			c, _ := LoadChain(path)
			b := c.CreateNextBlock(1, "AAPL", "BUY", "1", "200.00", "node1", "sig", "pk")
			if err := c.Append(b); err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(path)
			tampered := strings.Replace(string(data), tc.from, tc.to, 1)
			if tampered == string(data) {
				t.Fatalf("fixture did not contain %s", tc.from)
			}
			os.WriteFile(path, []byte(tampered), 0644)
			if _, err := LoadChain(path); err == nil {
				t.Fatal("tampered chain loaded without error")
			}
		})
	}
}

func TestCorruptLineIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chain_x.jsonl")
	c, _ := LoadChain(path)
	grow(c, 1)
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	f.WriteString("{not json\n")
	f.Close()
	if _, err := LoadChain(path); err == nil {
		t.Fatal("corrupt line was silently skipped")
	}
}

func TestAppendRejectsBlockThatDoesNotExtendHead(t *testing.T) {
	c := newTestChain(t, "x")
	stale := c.CreateNextBlock(1, "AAPL", "BUY", "1", "200.00", "node1", "", "")
	grow(c, 1)
	if err := c.Append(stale); err == nil {
		t.Fatal("Append accepted a block built on an old head")
	}
}
