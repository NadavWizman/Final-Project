package ledger

import (
	"crypto/sha256"
	"encoding/json"
)

// Root is the state root: a Merkle root over the whole ledger. Leaf 0 is the
// global state (height, time, parameters, prices, id counter); then one leaf
// per account, in address order. Two nodes have the same state exactly when
// they have the same root, and the root goes into every block, so any node
// or client can check a copy of the state against the chain.
func (s *State) Root() [32]byte {
	global := struct {
		Height     int64            `json:"height"`
		Time       int64            `json:"time"`
		Params     Params           `json:"params"`
		LastPrices map[string]Cents `json:"last_prices"`
		NextID     uint64           `json:"next_id"`
	}{s.Height, s.Time, s.Params, s.LastPrices, s.NextID}

	leaves := [][32]byte{leafHash(global)}
	for _, addr := range s.sortedAddresses() {
		leaves = append(leaves, leafHash(s.Accounts[addr]))
	}
	return merkleRoot(leaves)
}

// leafHash hashes a value's JSON (encoding/json sorts map keys, so the bytes
// are the same on every node), domain-separated from inner nodes.
func leafHash(v any) [32]byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // only plain data types are hashed
	}
	return sha256.Sum256(append([]byte{0}, b...))
}

// merkleRoot pairs hashes level by level; an odd one out is carried up.
func merkleRoot(level [][32]byte) [32]byte {
	if len(level) == 0 {
		return sha256.Sum256(nil)
	}
	for len(level) > 1 {
		var next [][32]byte
		for i := 0; i < len(level); i += 2 {
			if i+1 == len(level) {
				next = append(next, level[i])
				continue
			}
			buf := append([]byte{1}, level[i][:]...)
			next = append(next, sha256.Sum256(append(buf, level[i+1][:]...)))
		}
		level = next
	}
	return level[0]
}

// Clone returns a deep copy of the state (used to evaluate a proposed block
// without touching the committed state).
func (s *State) Clone() *State {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	var c State
	if err := json.Unmarshal(b, &c); err != nil {
		panic(err)
	}
	return &c
}
