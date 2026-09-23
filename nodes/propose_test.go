package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

func TestProposeApprovesHonestBlock(t *testing.T) {
	dj := newFakeDjango(t)
	v := newTestValidatorWith(t, "node2", dj)
	o, _ := signedOrder(t, 7, "AAPL", "BUY", "1.0000")
	dj.put(o)
	block, req := proposalFor(v.chain, o, "200.50")

	vote, err := v.Propose(context.Background(), req)
	if err != nil || !vote.Approve {
		t.Fatalf("honest block rejected: %v %q", err, vote.GetReason())
	}
	// the approval is a signature Django can verify with the node's public key
	sig, _ := base64.StdEncoding.DecodeString(vote.VoteSignature)
	pub := v.key.Public().(ed25519.PublicKey)
	if !ed25519.Verify(pub, voteMessage(block.OrderID, block.Hash, block.Price), sig) {
		t.Fatal("vote signature does not verify")
	}
}

func TestRejectionCarriesNoVoteSignature(t *testing.T) {
	v := newTestValidator(t, "node2")
	o, _ := signedOrder(t, 8, "AAPL", "BUY", "1.0000") // not in Django
	_, req := proposalFor(v.chain, o, "200.00")
	vote, _ := v.Propose(context.Background(), req)
	if vote.Approve || vote.VoteSignature != "" {
		t.Fatalf("rejection carried a vote signature: %+v", vote)
	}
}

func TestProposeRejections(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, dj *fakeDjango, v *ValidatorServer, o *Order) (priceInBlock string)
		want   string
	}{
		{"attacker's own key pair (the review's public-key substitution)", func(t *testing.T, dj *fakeDjango, v *ValidatorServer, o *Order) string {
			// Django holds the victim's order; the Leader presents an order it
			// signed itself for 1000 shares with a fresh key.
			dj.put(*o)
			key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			*o = signOrderWith(t, key, o.ID, "AAPL", "BUY", "1000.0000")
			return "200.00"
		}, "does not match order"},
		{"swapped key for the same trade", func(t *testing.T, dj *fakeDjango, v *ValidatorServer, o *Order) string {
			dj.put(*o)
			key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			*o = signOrderWith(t, key, o.ID, o.Stock, o.OrderType, o.Quantity)
			return "200.00"
		}, "differs from the order in Django"},
		{"order already confirmed (replay)", func(t *testing.T, dj *fakeDjango, v *ValidatorServer, o *Order) string {
			done := *o
			done.Status = "CONFIRMED"
			dj.put(done)
			return "200.00"
		}, "not SUBMITTED"},
		{"order unknown to Django", func(t *testing.T, dj *fakeDjango, v *ValidatorServer, o *Order) string {
			return "200.00"
		}, "cannot load order"},
		{"manipulated price", func(t *testing.T, dj *fakeDjango, v *ValidatorServer, o *Order) string {
			dj.put(*o)
			return "1.00"
		}, "price divergence"},
		{"order altered in the database after signing", func(t *testing.T, dj *fakeDjango, v *ValidatorServer, o *Order) string {
			tampered := *o
			tampered.Quantity = "1000.0000"
			tampered.SignedMessage = strings.Replace(o.SignedMessage, `"1.0000"`, `"1000.0000"`, 1)
			dj.put(tampered)
			*o = tampered
			return "200.00"
		}, "ECDSA verification failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dj := newFakeDjango(t)
			v := newTestValidatorWith(t, "node2", dj)
			o, _ := signedOrder(t, 7, "AAPL", "BUY", "1.0000")
			price := tc.mutate(t, dj, v, &o)
			_, req := proposalFor(v.chain, o, price)

			vote, err := v.Propose(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if vote.Approve || !strings.Contains(vote.Reason, tc.want) {
				t.Fatalf("approve=%v reason=%q, want rejection containing %q", vote.Approve, vote.Reason, tc.want)
			}
		})
	}
}
