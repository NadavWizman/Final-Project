package main

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"nodes/clusterauth"
	pb "nodes/consensus"
)

// clusterSecret is the shared credential the Leader proves on every node-to-node
// call (see package clusterauth). Set once in runLeader from config.
var clusterSecret string

// dialValidator opens a client connection that authenticates every call.
func dialValidator(address string) (*grpc.ClientConn, error) {
	return grpc.NewClient(address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(clusterauth.ClientInterceptor(clusterSecret)),
	)
}

// VoteResponse — a Validator's answer to a proposal
type VoteResponse struct {
	NodeID    string `json:"node_id"`
	Approve   bool   `json:"approve"`
	Reason    string `json:"reason"`
	Signature string `json:"vote_signature"`
}

// quorum is the number of signed approvals (Leader included) needed to settle.
// Django enforces the same threshold (trading/consensus.py:QUORUM).
const quorum = 2

// oracleRetryWindow is how long the Leader keeps retrying an order while the
// Oracle is unreachable. A brief outage should not kill a valid order, but an
// Oracle that never returns must not leave the order queued forever.
const oracleRetryWindow = 60 * time.Second

// runLeader — main loop of the Leader
func runLeader(cfg Config, chain *Chain) {
	fmt.Printf("[%s] Leader mode — listening for new orders...\n", cfg.NodeName)
	fmt.Printf("Validators: %v\n", cfg.ValidatorAddresses)
	clusterSecret = cfg.ClusterSecret

	node, err := newNode(cfg, chain)
	if err != nil {
		log.Fatalf("[%s] %v", cfg.NodeName, err)
	}

	// when the Oracle first started failing, per order
	outages := map[int]time.Time{}

	for {
		processLeaderCycle(node, outages)
		time.Sleep(5 * time.Second)
	}
}

// Vote is one node's signed approval, forwarded to Django as consensus proof.
type Vote struct {
	Node      string `json:"node"`
	Signature string `json:"signature"`
}

func processLeaderCycle(node *ValidatorServer, outages map[int]time.Time) {
	cfg, chain := node.cfg, node.chain
	orders, err := node.django.Orders()
	if err != nil {
		log.Printf("[Leader] Error fetching orders: %v", err)
		return
	}

	found := false
	submitted := map[int]bool{}
	for _, order := range orders {
		if order.Status != "SUBMITTED" {
			continue
		}
		found = true
		submitted[order.ID] = true

		if order.Signature == "" || order.SignedMessage == "" {
			fmt.Printf("[Leader] Order #%d has no signature — rejecting\n", order.ID)
			node.django.RejectOrder(order.ID, "order is not signed")
			continue
		}

		// step 1: query Oracle
		oracle, err := FetchPrice(cfg.OracleURL, order.Stock)
		if err != nil {
			since, seen := outages[order.ID]
			if !seen {
				// report the outage once — repeating it every cycle adds nothing
				log.Printf("[Leader] Oracle unreachable for order #%d: %v", order.ID, err)
				fmt.Printf("[Leader] Retrying order #%d quietly for up to %s\n",
					order.ID, oracleRetryWindow)
				outages[order.ID] = time.Now()
				continue
			}
			if waited := time.Since(since); waited >= oracleRetryWindow {
				fmt.Printf("\n[Leader] Oracle still unreachable after %s — rejecting order #%d\n",
					waited.Round(time.Second), order.ID)
				if node.django.RejectOrder(order.ID, "price feed unavailable") {
					fmt.Printf("[Leader] Order #%d marked REJECTED\n", order.ID)
				}
				delete(outages, order.ID)
			}
			continue
		}
		if _, recovered := outages[order.ID]; recovered {
			fmt.Printf("[Leader] Oracle recovered for order #%d\n", order.ID)
			delete(outages, order.ID)
		}

		fmt.Printf("\n[Leader] Order #%d | %s %s %s\n",
			order.ID, order.OrderType, order.Quantity, order.Stock)
		fmt.Printf("[Leader] Oracle: $%s\n", oracle.ExecutionPrice)

		// step 2: build proposed block
		block := chain.CreateNextBlock(
			order.ID, order.Stock, order.OrderType,
			order.Quantity, oracle.ExecutionPrice, cfg.NodeName,
			order.Signature, order.PublicKey,
		)
		fmt.Printf("[Leader] Block #%d | hash: %s...\n", block.Index, short(block.Hash))

		// the exact bytes the user's signature covers, straight from Django
		signedMsg := order.SignedMessage

		// step 3: the Leader votes only after running the same checks as a
		// Validator — its approval is a signed statement, not a formality.
		req := &pb.ProposeRequest{
			Block: blockToProto(block), OraclePrice: oracle.ExecutionPrice,
			OracleTimestamp: oracle.Timestamp, Signature: order.Signature,
			PublicKey: order.PublicKey, SignedMessage: signedMsg,
		}
		var votes []Vote
		if reason := node.evaluate(block, req); reason != "" {
			fmt.Printf("[Leader] Self-check: reject — %s\n", reason)
		} else {
			votes = append(votes, Vote{Node: cfg.NodeName, Signature: signVote(node.key, block)})
			fmt.Printf("[Leader] Self-check: approve\n")
		}

		// step 4: collect votes from Validators via gRPC
		for _, addr := range cfg.ValidatorAddresses {
			vote := askValidator(addr, block, oracle.ExecutionPrice, oracle.Timestamp,
				order.Signature, order.PublicKey, signedMsg)

			// A node that was offline rejects because its chain is behind, not because
			// the block is bad. Replay what it missed, then let it vote again.
			if !vote.Approve {
				if head, behind := parseDesync(vote.Reason); behind {
					fmt.Printf("[Leader] %s is behind at #%d (we are at #%d) — replaying %d block(s)\n",
						vote.NodeID, head, chain.HeadIndex(), chain.HeadIndex()-head)
					if resyncValidator(addr, chain, head) {
						vote = askValidator(addr, block, oracle.ExecutionPrice, oracle.Timestamp,
							order.Signature, order.PublicKey, signedMsg)
					}
				}
			}

			if vote.Approve && vote.Signature != "" {
				votes = append(votes, Vote{Node: vote.NodeID, Signature: vote.Signature})
				fmt.Printf("[Leader] Approved by %s\n", vote.NodeID)
			} else {
				fmt.Printf("[Leader] Rejected by %s: %s\n", vote.NodeID, vote.Reason)
			}
		}

		approvals := len(votes)
		fmt.Printf("[Leader] Tally: %d/%d approvals\n", approvals, len(cfg.ValidatorAddresses)+1)

		// step 5: consensus — a quorum of signed approvals
		if approvals >= quorum {
			fmt.Printf("[Leader] Consensus reached! Executing order...\n")

			if node.django.ExecuteOrder(order.ID, oracle, block.Hash, votes) {
				if err := chain.Append(block); err != nil {
					log.Printf("[Leader] CRITICAL: order #%d executed but block #%d could not be stored: %v",
						order.ID, block.Index, err)
					continue
				}
				fmt.Printf("[Leader] Block #%d committed | chain length: %d\n",
					block.Index, chain.Length())

				for _, addr := range cfg.ValidatorAddresses {
					broadcastCommit(addr, block)
				}
			}
		} else {
			// Mark the order REJECTED so it leaves the SUBMITTED queue — otherwise
			// the Leader would re-propose the same failing order every cycle forever.
			fmt.Printf("[Leader] Consensus failed for order #%d (%d approvals) — rejecting\n",
				order.ID, approvals)
			if node.django.RejectOrder(order.ID, fmt.Sprintf("consensus not reached (%d approvals)", approvals)) {
				fmt.Printf("[Leader] Order #%d marked REJECTED\n", order.ID)
			}
		}

		time.Sleep(300 * time.Millisecond)
	}

	// drop outage records for orders that are no longer awaiting consensus
	for id := range outages {
		if !submitted[id] {
			delete(outages, id)
		}
	}

	if !found {
		fmt.Printf("[Leader] No pending orders\n")
	}
}

// askValidator sends a Propose RPC to a Validator and returns its vote
func askValidator(address string, block Block, oraclePrice, oracleTimestamp,
	signature, publicKey, signedMsg string) VoteResponse {

	conn, err := dialValidator(address)
	if err != nil {
		return VoteResponse{NodeID: address, Approve: false,
			Reason: fmt.Sprintf("connection error: %v", err)}
	}
	defer conn.Close()

	client := pb.NewConsensusServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := client.Propose(ctx, &pb.ProposeRequest{
		Block:           blockToProto(block),
		OraclePrice:     oraclePrice,
		OracleTimestamp: oracleTimestamp,
		Signature:       signature,
		PublicKey:       publicKey,
		SignedMessage:   signedMsg,
	})
	if err != nil {
		return VoteResponse{NodeID: address, Approve: false,
			Reason: fmt.Sprintf("rpc error: %v", err)}
	}

	return VoteResponse{NodeID: resp.NodeId, Approve: resp.Approve, Reason: resp.Reason,
		Signature: resp.VoteSignature}
}

// parseDesync reports whether a rejection was caused only by the Validator
// lagging behind, and if so returns the index its chain currently ends at.
func parseDesync(reason string) (int, bool) {
	i := strings.Index(reason, desyncPrefix)
	if i < 0 {
		return 0, false
	}
	rest := reason[i+len(desyncPrefix):]
	if end := strings.IndexAny(rest, " |"); end >= 0 {
		rest = rest[:end]
	}
	head, err := strconv.Atoi(rest)
	if err != nil {
		return 0, false
	}
	return head, true
}

// resyncValidator replays every committed block the Validator missed, in order,
// so it can rejoin consensus instead of rejecting every future proposal.
func resyncValidator(address string, chain *Chain, validatorHead int) bool {
	missing := chain.BlocksFrom(validatorHead + 1)
	if len(missing) == 0 {
		return false
	}
	for _, b := range missing {
		if !sendCommit(address, b) {
			fmt.Printf("[Leader] Resync of %s failed at block #%d\n", address, b.Index)
			return false
		}
	}
	fmt.Printf("[Leader] Resync complete — %s caught up to #%d\n", address, chain.HeadIndex())
	return true
}

// broadcastCommit sends a Commit RPC to a Validator to finalize the block
func broadcastCommit(address string, block Block) {
	if sendCommit(address, block) {
		fmt.Printf("[Leader] Block broadcast to %s succeeded\n", address)
	}
}

// sendCommit delivers one block to a Validator and reports whether it now holds it
func sendCommit(address string, block Block) bool {
	conn, err := dialValidator(address)
	if err != nil {
		log.Printf("[Leader] Commit connection to %s failed: %v", address, err)
		return false
	}
	defer conn.Close()

	client := pb.NewConsensusServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := client.Commit(ctx, &pb.CommitRequest{Block: blockToProto(block)})
	if err != nil {
		log.Printf("[Leader] Commit RPC to %s failed: %v", address, err)
		return false
	}
	if resp.Status != "committed" && resp.Status != "already_present" {
		log.Printf("[Leader] %s refused block #%d: %s", address, block.Index, resp.Status)
		return false
	}
	return true
}

// blockToProto converts the local Block struct to a proto Block message
func blockToProto(b Block) *pb.Block {
	return &pb.Block{
		Index:     int32(b.Index),
		PrevHash:  b.PrevHash,
		Timestamp: b.Timestamp,
		OrderId:   int32(b.OrderID),
		Stock:     b.Stock,
		OrderType: b.OrderType,
		Quantity:  b.Quantity,
		Price:     b.Price,
		NodeName:  b.NodeName,
		Signature: b.Signature,
		PublicKey: b.PublicKey,
		Hash:      b.Hash,
	}
}
