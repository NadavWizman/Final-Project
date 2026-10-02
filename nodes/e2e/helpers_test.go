package e2e

import (
	"crypto/sha256"
	"time"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
)

// proposal builds a block proposal at height h for a block identified by tag.
func proposal(h int64, tag string) *cmtproto.Proposal {
	sum := sha256.Sum256([]byte(tag))
	part := sha256.Sum256([]byte("parts-" + tag))
	return &cmtproto.Proposal{
		Type: cmtproto.ProposalType, Height: h, Round: 0, PolRound: -1,
		BlockID:   cmtproto.BlockID{Hash: sum[:], PartSetHeader: cmtproto.PartSetHeader{Total: 1, Hash: part[:]}},
		Timestamp: time.Now(),
	}
}

func hashOf(tx []byte) []byte { return types.Tx(tx).Hash() }
