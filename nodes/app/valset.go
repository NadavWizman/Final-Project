package app

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"

	abci "github.com/cometbft/cometbft/abci/types"
)

// The validator set is fixed in the genesis file. Changing it — adding,
// removing or re-weighting a validator — is itself a transaction that must
// carry signatures from validators holding more than 2/3 of the voting
// power (3 of 4 equal validators). There is no shared secret anywhere: each
// validator signs with its own consensus key, and node-to-node connections
// are authenticated and encrypted by CometBFT with each node's own key.

// ValsetChange is the content the validators sign.
type ValsetChange struct {
	Chain  string `json:"chain"`
	Seq    uint64 `json:"seq"`    // must equal the number of changes so far (no replay)
	PubKey []byte `json:"pubkey"` // Ed25519 consensus key of the validator to set
	Power  int64  `json:"power"`  // 0 removes it
}

// ValsetTx is a change plus the approving validators' signatures.
type ValsetTx struct {
	Valset ValsetChange     `json:"valset"`
	Sigs   []ValsetApproval `json:"sigs"`
}

// ValsetApproval is one validator's signature over the change.
type ValsetApproval struct {
	PubKey []byte `json:"pubkey"`
	Sig    []byte `json:"sig"`
}

const valsetMarker = `{"valset"`

// Message is the exact byte string each approving validator signs.
func (v ValsetChange) Message() []byte {
	return []byte("tradedesk-valset|" + v.Chain + "|" + strconv.FormatUint(v.Seq, 10) + "|" +
		base64.StdEncoding.EncodeToString(v.PubKey) + "|" + strconv.FormatInt(v.Power, 10))
}

func isValset(tx []byte) bool {
	return len(tx) >= len(valsetMarker) && string(tx[:len(valsetMarker)]) == valsetMarker
}

// checkValset verifies a change against the current set without applying it.
func (c *Chain) checkValset(raw []byte) (*Validator, error) {
	var tx ValsetTx
	if err := json.Unmarshal(raw, &tx); err != nil {
		return nil, fmt.Errorf("malformed validator-set change: %v", err)
	}
	ch := tx.Valset
	switch {
	case ch.Chain != c.Ledger.Params.ChainID:
		return nil, fmt.Errorf("validator-set change for another chain")
	case ch.Seq != c.ValsetSeq:
		return nil, fmt.Errorf("validator-set change out of sequence: expected %d", c.ValsetSeq)
	case len(ch.PubKey) != ed25519.PublicKeySize:
		return nil, fmt.Errorf("validator key must be a 32-byte Ed25519 key")
	case ch.Power < 0 || ch.Power > 1_000_000:
		return nil, fmt.Errorf("invalid voting power")
	}

	var total int64
	power := map[string]int64{}
	for _, v := range c.Validators {
		total += v.Power
		power[string(v.PubKey)] = v.Power
	}
	signed := map[string]bool{}
	var approving int64
	msg := ch.Message()
	for _, a := range tx.Sigs {
		p, isValidator := power[string(a.PubKey)]
		if !isValidator || signed[string(a.PubKey)] || !ed25519.Verify(a.PubKey, msg, a.Sig) {
			continue
		}
		signed[string(a.PubKey)] = true
		approving += p
	}
	if 3*approving <= 2*total {
		return nil, fmt.Errorf("validator-set change needs signatures from more than 2/3 of the voting power (has %d of %d)", approving, total)
	}

	// never leave the network without enough validators to tolerate one fault
	next := map[string]int64{}
	for k, p := range power {
		next[k] = p
	}
	if ch.Power == 0 {
		delete(next, string(ch.PubKey))
	} else {
		next[string(ch.PubKey)] = ch.Power
	}
	if len(next) < 4 {
		return nil, errors.New("the validator set cannot drop below 4 validators (3f+1 with f = 1)")
	}
	return &Validator{PubKey: ch.PubKey, Power: ch.Power}, nil
}

// applyValset checks and applies a change.
func (c *Chain) applyValset(raw []byte) error {
	v, err := c.checkValset(raw)
	if err != nil {
		return err
	}
	kept := c.Validators[:0]
	for _, x := range c.Validators {
		if !bytes.Equal(x.PubKey, v.PubKey) {
			kept = append(kept, x)
		}
	}
	if v.Power > 0 {
		kept = append(kept, *v)
	}
	c.Validators = kept
	sortValidators(c.Validators)
	c.ValsetSeq++
	return nil
}

// SignValset returns a validator's approval of a change.
func SignValset(key ed25519.PrivateKey, ch ValsetChange) ValsetApproval {
	return ValsetApproval{PubKey: key.Public().(ed25519.PublicKey), Sig: ed25519.Sign(key, ch.Message())}
}

func sortValidators(vs []Validator) {
	sort.Slice(vs, func(i, j int) bool { return bytes.Compare(vs[i].PubKey, vs[j].PubKey) < 0 })
}

// validatorDiff is what CometBFT must change to go from prev to next.
func validatorDiff(prev, next []Validator) []abci.ValidatorUpdate {
	var out []abci.ValidatorUpdate
	old := map[string]int64{}
	for _, v := range prev {
		old[string(v.PubKey)] = v.Power
	}
	seen := map[string]bool{}
	for _, v := range next {
		seen[string(v.PubKey)] = true
		if old[string(v.PubKey)] != v.Power {
			out = append(out, abci.Ed25519ValidatorUpdate(v.PubKey, v.Power))
		}
	}
	for _, v := range prev {
		if !seen[string(v.PubKey)] {
			out = append(out, abci.Ed25519ValidatorUpdate(v.PubKey, 0))
		}
	}
	return out
}
