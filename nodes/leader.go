package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
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

// Vote is one node's signed approval, forwarded to Django as consensus proof.
type Vote struct {
	Node      string `json:"node"`
	Signature string `json:"signature"`
}

// quorum is the number of signed approvals (Leader included) needed to settle.
// Django enforces the same threshold (trading/consensus.py:QUORUM).
const quorum = 2

// oracleRetryWindow is how long the Leader keeps retrying an order while the
// Oracle is unreachable. A brief outage should not kill a valid order, but an
// Oracle that never returns must not leave the order queued forever.
const oracleRetryWindow = 60 * time.Second

// settleGiveUp is how long an order may keep failing settlement for a
// temporary reason (Django or its price sources unavailable) before the
// Leader rejects it instead of retrying forever.
const settleGiveUp = 5 * time.Minute

// Leader drives consensus: it proposes one block per SUBMITTED order, collects
// signed votes and asks Django to settle.
type Leader struct {
	node *ValidatorServer
	// when the Oracle first started failing, per order
	outages map[int]time.Time
	// when settlement first failed temporarily, per order
	settleFailures map[int]time.Time
	// write-ahead record of the settlement in flight (see pendingSettlement)
	pendingPath string
}

// pendingSettlement is written to disk before the Leader asks Django to
// settle, and removed once the outcome is known. If the Leader crashes or the
// request times out, the next cycle reads it back and finishes the job: Django
// may already have moved the money, and the block must then still be added to
// the chain rather than lost.
type pendingSettlement struct {
	Block   Block       `json:"block"`
	Oracle  *OracleData `json:"oracle"`
	Votes   []Vote      `json:"votes"`
	Created time.Time   `json:"created"`
}

// runLeader — main loop of the Leader
func runLeader(cfg Config, chain *Chain) {
	fmt.Printf("[%s] Leader mode — listening for new orders...\n", cfg.NodeName)
	fmt.Printf("Validators: %v\n", cfg.ValidatorAddresses)
	clusterSecret = cfg.ClusterSecret

	node, err := newNode(cfg, chain)
	if err != nil {
		log.Fatalf("[%s] %v", cfg.NodeName, err)
	}
	l := &Leader{
		node:           node,
		outages:        map[int]time.Time{},
		settleFailures: map[int]time.Time{},
		pendingPath:    fmt.Sprintf("pending_%s.json", cfg.NodeName),
	}

	// A Leader restarted with a missing or older chain file must first recover
	// the blocks the cluster already committed, or every proposal would be
	// built on a stale head.
	for _, addr := range cfg.ValidatorAddresses {
		l.catchUp(addr)
	}

	for {
		l.cycle()
		time.Sleep(5 * time.Second)
	}
}

// cycle processes every SUBMITTED order once.
func (l *Leader) cycle() {
	// Never build on a head whose next block may already be settled in Django.
	if !l.resolvePending() {
		return
	}

	orders, err := l.node.django.SubmittedOrders()
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
		if stop := l.process(order); stop {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}

	// drop bookkeeping for orders that are no longer awaiting consensus
	for _, m := range []map[int]time.Time{l.outages, l.settleFailures} {
		for id := range m {
			if !submitted[id] {
				delete(m, id)
			}
		}
	}

	if !found {
		fmt.Printf("[Leader] No pending orders\n")
	}
}

// process runs consensus for one order. It returns true when the rest of the
// cycle must be skipped (chain head uncertain or just changed by a catch-up).
func (l *Leader) process(order Order) (stop bool) {
	cfg, chain, django := l.node.cfg, l.node.chain, l.node.django

	if order.Signature == "" || order.SignedMessage == "" {
		fmt.Printf("[Leader] Order #%d has no signature — rejecting\n", order.ID)
		django.RejectOrder(order.ID, "order is not signed")
		return false
	}

	// step 1: query Oracle
	oracle, err := l.node.price(order.Stock)
	if err != nil {
		since, seen := l.outages[order.ID]
		if !seen {
			// report the outage once — repeating it every cycle adds nothing
			log.Printf("[Leader] Oracle unreachable for order #%d: %v", order.ID, err)
			fmt.Printf("[Leader] Retrying order #%d quietly for up to %s\n", order.ID, oracleRetryWindow)
			l.outages[order.ID] = time.Now()
			return false
		}
		if waited := time.Since(since); waited >= oracleRetryWindow {
			fmt.Printf("\n[Leader] Oracle still unreachable after %s — rejecting order #%d\n",
				waited.Round(time.Second), order.ID)
			if django.RejectOrder(order.ID, "price feed unavailable") {
				fmt.Printf("[Leader] Order #%d marked REJECTED\n", order.ID)
			}
			delete(l.outages, order.ID)
		}
		return false
	}
	if _, recovered := l.outages[order.ID]; recovered {
		fmt.Printf("[Leader] Oracle recovered for order #%d\n", order.ID)
		delete(l.outages, order.ID)
	}

	// A limit order rests until the market reaches its limit — or until it
	// expires. No block is proposed for a price the user would not accept.
	if order.LimitPrice != "" {
		if cfg.LimitOrderTTL > 0 && !order.CreatedAt.IsZero() && time.Since(order.CreatedAt) > cfg.LimitOrderTTL {
			fmt.Printf("[Leader] Limit order #%d expired after %s — rejecting\n", order.ID, cfg.LimitOrderTTL)
			django.RejectOrder(order.ID, "limit order expired")
			return false
		}
		if ok, err := limitSatisfied(order, oracle.ExecutionPrice); err != nil {
			fmt.Printf("[Leader] Order #%d has an unusable limit price: %v — rejecting\n", order.ID, err)
			django.RejectOrder(order.ID, "invalid limit price")
			return false
		} else if !ok {
			return false // keep waiting; re-checked next cycle
		}
	}

	fmt.Printf("\n[Leader] Order #%d | %s %s %s\n", order.ID, order.OrderType, order.Quantity, order.Stock)
	fmt.Printf("[Leader] Oracle: $%s\n", oracle.ExecutionPrice)

	// step 2: build proposed block
	block := chain.CreateNextBlock(
		order.ID, order.Stock, order.OrderType,
		order.Quantity, oracle.ExecutionPrice, cfg.NodeName,
		order.Signature, order.PublicKey,
	)
	fmt.Printf("[Leader] Block #%d | hash: %s...\n", block.Index, short(block.Hash))

	req := &pb.ProposeRequest{
		Block: blockToProto(block), OraclePrice: oracle.ExecutionPrice,
		OracleTimestamp: oracle.Timestamp, Signature: order.Signature,
		PublicKey: order.PublicKey, SignedMessage: order.SignedMessage,
	}

	// step 3: the Leader votes only after running the same checks as a
	// Validator — its approval is a signed statement, not a formality.
	var votes []Vote
	if reason := l.node.evaluate(block, req); reason != "" {
		fmt.Printf("[Leader] Self-check: reject — %s\n", reason)
	} else {
		votes = append(votes, Vote{Node: cfg.NodeName, Signature: signVote(l.node.key, block)})
		fmt.Printf("[Leader] Self-check: approve\n")
	}

	// step 4: collect votes from all Validators in parallel
	results := make([]VoteResponse, len(cfg.ValidatorAddresses))
	var wg sync.WaitGroup
	for i, addr := range cfg.ValidatorAddresses {
		wg.Add(1)
		go func(i int, addr string) {
			defer wg.Done()
			vote := askValidator(addr, req)
			// A node that was offline rejects because its chain is behind, not
			// because the block is bad. Replay what it missed, then ask again.
			if head, behind := parseHead(vote.Reason, desyncPrefix); !vote.Approve && behind {
				fmt.Printf("[Leader] %s is behind at #%d (we are at #%d) — replaying %d block(s)\n",
					vote.NodeID, head, chain.HeadIndex(), chain.HeadIndex()-head)
				if resyncValidator(addr, chain, head) {
					vote = askValidator(addr, req)
				}
			}
			results[i] = vote
		}(i, addr)
	}
	wg.Wait()

	for i, vote := range results {
		if head, ahead := parseHead(vote.Reason, aheadPrefix); !vote.Approve && ahead {
			// The Leader is the one behind: recover the committed blocks and
			// start over — this block was built on a stale head.
			fmt.Printf("[Leader] %s is ahead at #%d (we are at #%d) — catching up\n",
				vote.NodeID, head, chain.HeadIndex())
			l.catchUp(cfg.ValidatorAddresses[i])
			return true
		}
		if vote.Approve && vote.Signature != "" {
			votes = append(votes, Vote{Node: vote.NodeID, Signature: vote.Signature})
			fmt.Printf("[Leader] Approved by %s\n", vote.NodeID)
		} else {
			fmt.Printf("[Leader] Rejected by %s: %s\n", vote.NodeID, vote.Reason)
		}
	}

	fmt.Printf("[Leader] Tally: %d/%d approvals\n", len(votes), len(cfg.ValidatorAddresses)+1)

	// step 5: consensus — a quorum of signed approvals
	if len(votes) < quorum {
		// Mark the order REJECTED so it leaves the SUBMITTED queue — otherwise
		// the Leader would re-propose the same failing order every cycle forever.
		fmt.Printf("[Leader] Consensus failed for order #%d (%d approvals) — rejecting\n", order.ID, len(votes))
		if django.RejectOrder(order.ID, fmt.Sprintf("consensus not reached (%d approvals)", len(votes))) {
			fmt.Printf("[Leader] Order #%d marked REJECTED\n", order.ID)
		}
		return false
	}

	fmt.Printf("[Leader] Consensus reached! Executing order...\n")
	p := &pendingSettlement{Block: block, Oracle: oracle, Votes: votes, Created: time.Now()}
	if err := l.savePending(p); err != nil {
		log.Printf("[Leader] Cannot record settlement for order #%d, skipping: %v", order.ID, err)
		return true
	}
	return !l.settle(p)
}

// limitSatisfied reports whether price fills the order: a BUY at or below its
// limit, a SELL at or above it. Mirrors views._limit_satisfied in Django.
func limitSatisfied(order Order, price string) (bool, error) {
	limit, err := strconv.ParseFloat(order.LimitPrice, 64)
	if err != nil || limit <= 0 {
		return false, fmt.Errorf("limit %q", order.LimitPrice)
	}
	p, err := strconv.ParseFloat(price, 64)
	if err != nil {
		return false, fmt.Errorf("price %q", price)
	}
	if order.OrderType == "BUY" {
		return p <= limit, nil
	}
	return p >= limit, nil
}

// settle asks Django to execute a pending settlement and acts on the answer.
// It returns true when the outcome is final (the pending record is cleared).
func (l *Leader) settle(p *pendingSettlement) bool {
	id := p.Block.OrderID
	code, err := l.node.django.ExecuteOrder(id, p.Oracle, p.Block.Hash, p.Votes)
	switch {
	case err == nil && code == http.StatusOK:
		delete(l.settleFailures, id)
		return l.finalize(p)

	case err != nil || code >= 500:
		// Unknown or temporary outcome — keep the record and retry next cycle.
		// Give up on the order if it keeps failing for too long.
		first, seen := l.settleFailures[id]
		if !seen {
			l.settleFailures[id] = time.Now()
		} else if time.Since(first) > settleGiveUp {
			fmt.Printf("[Leader] Settlement of order #%d failing for %s — rejecting\n",
				id, time.Since(first).Round(time.Second))
			if l.node.django.RejectOrder(id, "settlement unavailable") {
				delete(l.settleFailures, id)
				l.clearPending()
				return true
			}
		}
		log.Printf("[Leader] Settlement of order #%d pending (status %d, err %v) — will retry", id, code, err)
		return false

	default:
		// Django refused definitively (rejected the order, or the quote went
		// stale). Nothing was settled; forget the block.
		fmt.Printf("[Leader] Django did not settle order #%d (status %d)\n", id, code)
		l.clearPending()
		return true
	}
}

// resolvePending finishes a settlement left over from a timeout or crash.
// It returns false while the outcome is still unknown.
func (l *Leader) resolvePending() bool {
	p, err := l.loadPending()
	if err != nil {
		log.Printf("[Leader] Unreadable pending settlement %s: %v — discarding", l.pendingPath, err)
		l.clearPending()
		return true
	}
	if p == nil {
		return true
	}
	order, err := l.node.django.Order(p.Block.OrderID)
	if err != nil {
		log.Printf("[Leader] Cannot check pending settlement of order #%d: %v", p.Block.OrderID, err)
		return false
	}
	switch {
	case order.Status == "CONFIRMED" && order.BlockHash == p.Block.Hash:
		fmt.Printf("[Leader] Order #%d was settled under block #%d — completing the commit\n",
			order.ID, p.Block.Index)
		return l.finalize(p)
	case order.Status == "SUBMITTED":
		return l.settle(p)
	default:
		fmt.Printf("[Leader] Pending block for order #%d is obsolete (order %s) — discarding\n",
			order.ID, order.Status)
		l.clearPending()
		return true
	}
}

// finalize appends a settled block and broadcasts it to the Validators.
func (l *Leader) finalize(p *pendingSettlement) bool {
	chain := l.node.chain
	if existing, ok := chain.BlockAt(p.Block.Index); !ok || existing.Hash != p.Block.Hash {
		if err := chain.Append(p.Block); err != nil {
			log.Printf("[Leader] CRITICAL: order #%d settled but block #%d cannot be stored: %v",
				p.Block.OrderID, p.Block.Index, err)
			return false
		}
	}
	fmt.Printf("[Leader] Block #%d committed | chain length: %d\n", p.Block.Index, chain.Length())
	for _, addr := range l.node.cfg.ValidatorAddresses {
		broadcastCommit(addr, p.Block)
	}
	l.clearPending()
	return true
}

func (l *Leader) savePending(p *pendingSettlement) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	tmp := l.pendingPath + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, l.pendingPath)
}

func (l *Leader) loadPending() (*pendingSettlement, error) {
	data, err := os.ReadFile(l.pendingPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p pendingSettlement
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (l *Leader) clearPending() {
	if err := os.Remove(l.pendingPath); err != nil && !os.IsNotExist(err) {
		log.Printf("[Leader] Cannot remove %s: %v", l.pendingPath, err)
	}
}

// catchUp copies committed blocks the Leader is missing from a Validator.
// Every block must extend the Leader's chain and be certified by Django, so a
// Validator cannot feed the Leader a forged history.
func (l *Leader) catchUp(address string) {
	chain := l.node.chain
	for {
		blocks, err := fetchBlocks(address, chain.HeadIndex()+1)
		if err != nil {
			log.Printf("[Leader] Catch-up from %s failed: %v", address, err)
			return
		}
		if len(blocks) == 0 {
			return
		}
		for _, b := range blocks {
			if err := l.node.certified(b); err != nil {
				log.Printf("[Leader] Catch-up from %s stopped at block #%d: %v", address, b.Index, err)
				return
			}
			if err := chain.Append(b); err != nil {
				log.Printf("[Leader] Catch-up from %s stopped at block #%d: %v", address, b.Index, err)
				return
			}
		}
		fmt.Printf("[Leader] Caught up from %s to block #%d\n", address, chain.HeadIndex())
		if len(blocks) < maxBlocksPerFetch {
			return
		}
	}
}

// fetchBlocks asks a Validator for its committed blocks from an index on.
func fetchBlocks(address string, from int) ([]Block, error) {
	conn, err := dialValidator(address)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := pb.NewConsensusServiceClient(conn).GetBlocks(ctx, &pb.GetBlocksRequest{FromIndex: int32(from)})
	if err != nil {
		return nil, err
	}
	var blocks []Block
	for _, b := range resp.GetBlocks() {
		blocks = append(blocks, protoToBlock(b))
	}
	return blocks, nil
}

// askValidator sends a Propose RPC to a Validator and returns its vote
func askValidator(address string, req *pb.ProposeRequest) VoteResponse {
	conn, err := dialValidator(address)
	if err != nil {
		return VoteResponse{NodeID: address, Approve: false,
			Reason: fmt.Sprintf("connection error: %v", err)}
	}
	defer conn.Close()

	client := pb.NewConsensusServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.Propose(ctx, req)
	if err != nil {
		return VoteResponse{NodeID: address, Approve: false,
			Reason: fmt.Sprintf("rpc error: %v", err)}
	}

	return VoteResponse{NodeID: resp.NodeId, Approve: resp.Approve, Reason: resp.Reason,
		Signature: resp.VoteSignature}
}

// parseHead extracts the chain head a Validator reported after prefix
// (DESYNC head=<n> when it is behind, AHEAD head=<n> when it is ahead).
func parseHead(reason, prefix string) (int, bool) {
	i := strings.Index(reason, prefix)
	if i < 0 {
		return 0, false
	}
	rest := reason[i+len(prefix):]
	if end := strings.IndexAny(rest, " |"); end >= 0 {
		rest = rest[:end]
	}
	head, err := strconv.Atoi(rest)
	if err != nil {
		return 0, false
	}
	return head, true
}

// parseDesync reports whether a rejection was caused only by the Validator
// lagging behind, and if so returns the index its chain currently ends at.
func parseDesync(reason string) (int, bool) { return parseHead(reason, desyncPrefix) }

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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
