package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	pb "nodes/consensus"
)

// ProposeRequest — what the Leader sends to Validators (kept for reference, proto handles the wire)
type ProposeRequest struct {
	Block           Block  `json:"block"`
	OraclePrice     string `json:"oracle_price"`
	OracleTimestamp string `json:"oracle_timestamp"`
	Signature       string `json:"signature"`
	PublicKey       string `json:"public_key"`
	SignedMessage   string `json:"signed_message"`
}

// VoteResponse — what the Validator returns (kept for reference)
type VoteResponse struct {
	NodeID  string `json:"node_id"`
	Approve bool   `json:"approve"`
	Reason  string `json:"reason"`
}

// runLeader — main loop of the Leader
func runLeader(cfg Config, chain *Chain) {
	fmt.Printf("[%s] Leader mode — listening for new orders...\n", cfg.NodeName)
	fmt.Printf("Validators: %v\n", cfg.ValidatorAddresses)

	for {
		processLeaderCycle(cfg, chain)
		time.Sleep(5 * time.Second)
	}
}

func processLeaderCycle(cfg Config, chain *Chain) {
	orders, err := fetchOrders(cfg)
	if err != nil {
		log.Printf("[Leader] Error fetching orders: %v", err)
		return
	}

	found := false
	for _, order := range orders {
		if order.Status != "SUBMITTED" {
			continue
		}
		found = true
		fmt.Printf("\n[Leader] Order #%d | %s %s %s\n",
			order.ID, order.OrderType, order.Quantity, order.Stock)

		// step 1: query Oracle
		oracle, err := FetchPrice(cfg.OracleURL, order.Stock)
		if err != nil {
			log.Printf("[Leader] Oracle error: %v", err)
			continue
		}
		fmt.Printf("[Leader] Oracle: $%s\n", oracle.ExecutionPrice)

		// step 2: build proposed block
		block := chain.CreateNextBlock(
			order.ID, order.Stock, order.OrderType,
			order.Quantity, oracle.ExecutionPrice, cfg.NodeName,
			order.Signature, order.PublicKey,
		)
		fmt.Printf("[Leader] Block #%d | hash: %s...\n", block.Index, block.Hash[:16])

		// build the signed message (same compact JSON format as Python)
		signedMsg := fmt.Sprintf(`{"nonce":"%s","order_type":"%s","quantity":"%s","stock":"%s"}`,
			order.Nonce, order.OrderType, order.Quantity, order.Stock)

		// step 3: collect votes from Validators via gRPC
		approvals := 1 // Leader counts itself as approved
		fmt.Printf("[Leader] Self-vote: approve\n")

		for _, addr := range cfg.ValidatorAddresses {
			vote := askValidator(addr, block, oracle.ExecutionPrice, oracle.Timestamp,
				order.Signature, order.PublicKey, signedMsg)
			if vote.Approve {
				approvals++
				fmt.Printf("[Leader] Approved by %s\n", vote.NodeID)
			} else {
				fmt.Printf("[Leader] Rejected by %s: %s\n", vote.NodeID, vote.Reason)
			}
		}

		fmt.Printf("[Leader] Tally: %d/3 approvals\n", approvals)

		// step 4: consensus — minimum 2/3
		if approvals >= 2 {
			fmt.Printf("[Leader] Consensus reached! Executing order...\n")

			if sendExecuteOrder(cfg, order.ID, oracle) {
				chain.Append(block)
				fmt.Printf("[Leader] Block #%d committed | chain length: %d\n",
					block.Index, chain.Length())

				for _, addr := range cfg.ValidatorAddresses {
					broadcastCommit(addr, block)
				}
			}
		} else {
			fmt.Printf("[Leader] Consensus failed for order #%d\n", order.ID)
		}

		time.Sleep(300 * time.Millisecond)
	}

	if !found {
		fmt.Printf("[Leader] No pending orders\n")
	}
}

// askValidator sends a Propose RPC to a Validator and returns its vote
func askValidator(address string, block Block, oraclePrice, oracleTimestamp,
	signature, publicKey, signedMsg string) VoteResponse {

	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
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

	return VoteResponse{NodeID: resp.NodeId, Approve: resp.Approve, Reason: resp.Reason}
}

// broadcastCommit sends a Commit RPC to a Validator to finalize the block
func broadcastCommit(address string, block Block) {
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Printf("[Leader] Commit connection to %s failed: %v", address, err)
		return
	}
	defer conn.Close()

	client := pb.NewConsensusServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = client.Commit(ctx, &pb.CommitRequest{Block: blockToProto(block)})
	if err != nil {
		log.Printf("[Leader] Commit RPC to %s failed: %v", address, err)
		return
	}
	fmt.Printf("[Leader] Block broadcast to %s succeeded\n", address)
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

// sendExecuteOrder sends the POST request to Django — stays as HTTP
func sendExecuteOrder(cfg Config, orderID int, oracle *OracleData) bool {
	payload := map[string]string{
		"execution_price": oracle.ExecutionPrice,
		"timestamp":       oracle.Timestamp,
	}
	body, _ := json.Marshal(payload)

	url := fmt.Sprintf("%s/orders/%d/execute_order/", cfg.DjangoURL, orderID)
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(body))
	req.SetBasicAuth(cfg.NodeName, cfg.NodePass)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[Leader] Django error: %v", err)
		return false
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	fmt.Printf("[Leader] Django response: %s\n", string(respBody))
	return resp.StatusCode == 200
}
