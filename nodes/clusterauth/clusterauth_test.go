package clusterauth

import (
	"strings"
	"testing"
	"time"
)

const method = "/consensus.ConsensusService/Propose"

func TestTokenRoundTrip(t *testing.T) {
	now := time.Now()
	if err := Verify("s3cret", method, Token("s3cret", method, now), now); err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
}

func TestTokenNeverContainsSecret(t *testing.T) {
	if tok := Token("s3cret-value", method, time.Now()); strings.Contains(tok, "s3cret-value") {
		t.Fatal("token leaks the secret")
	}
}

func TestRejections(t *testing.T) {
	now := time.Now()
	good := Token("s3cret", method, now)
	cases := map[string]error{
		"wrong secret": Verify("other", method, good, now),
		"other method": Verify("s3cret", "/consensus.ConsensusService/Commit", good, now),
		"expired":      Verify("s3cret", method, good, now.Add(2*MaxSkew)),
		"future":       Verify("s3cret", method, good, now.Add(-2*MaxSkew)),
		"malformed":    Verify("s3cret", method, "garbage", now),
		"raw secret":   Verify("s3cret", method, "s3cret", now),
	}
	for name, err := range cases {
		if err == nil {
			t.Errorf("%s: token accepted", name)
		}
	}
}
