package app

import (
	"crypto/ed25519"
	"testing"

	"nodes/quotes"
)

// A genesis source list that would weaken the price quorum is refused.
func TestCheckOracles(t *testing.T) {
	key := func() ed25519.PublicKey { p, _, _ := ed25519.GenerateKey(nil); return p }
	a, b, c := key(), key(), key()
	good := []quotes.Source{{Name: "a", PubKey: a}, {Name: "b", PubKey: b}, {Name: "c", PubKey: c}}
	if err := checkOracles(good, 3); err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string][]quotes.Source{
		"duplicate name": {{Name: "a", PubKey: a}, {Name: "a", PubKey: b}, {Name: "c", PubKey: c}},
		"shared key":     {{Name: "a", PubKey: a}, {Name: "b", PubKey: a}, {Name: "c", PubKey: c}},
		"short key":      {{Name: "a", PubKey: a[:31]}, {Name: "b", PubKey: b}, {Name: "c", PubKey: c}},
		"empty name":     {{Name: "", PubKey: a}, {Name: "b", PubKey: b}, {Name: "c", PubKey: c}},
	} {
		if checkOracles(bad, 3) == nil {
			t.Errorf("%s accepted", name)
		}
	}
	for _, q := range []int{2, 4} {
		if checkOracles(good, q) == nil {
			t.Errorf("quorum %d of 3 accepted", q)
		}
	}
}
