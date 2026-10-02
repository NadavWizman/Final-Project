package app

import (
	"bytes"
	"sort"

	abci "github.com/cometbft/cometbft/abci/types"
)

// Validator-set changes (task 6) are added in a later commit; until then no
// transaction is a validator-set change.

func isValset(tx []byte) bool { return false }

func (c *Chain) checkValset(tx []byte) (*Validator, error) { return nil, nil }

func (c *Chain) applyValset(tx []byte) error { return nil }

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
