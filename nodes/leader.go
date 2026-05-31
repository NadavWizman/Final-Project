package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// ProposeRequest — what the Leader sends to Validators
type ProposeRequest struct {
	Block           Block  `json:"block"`
	OraclePrice     string `json:"oracle_price"`
	OracleTimestamp string `json:"oracle_timestamp"`
	// ECDSA fields — for verifying the order creator's identity
	Signature     string `json:"signature"`      // user's Base64 signature
	PublicKey     string `json:"public_key"`     // PEM public key
	SignedMessage string `json:"signed_message"` // signed message: stock|order_type|quantity|nonce
}

// VoteResponse — what the Validator returns
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

		// step 2: build proposed block (including signature)
		block := chain.CreateNextBlock(
			order.ID, order.Stock, order.OrderType,
			order.Quantity, oracle.ExecutionPrice, cfg.NodeName,
			order.Signature, order.PublicKey,
		)
		fmt.Printf("[Leader] Block #%d | hash: %s...\n", block.Index, block.Hash[:16])

		// build the signed message (same format as Python)
		signedMsg := fmt.Sprintf(`{"nonce":"%s","order_type":"%s","quantity":"%s","stock":"%s"}`,
			order.Nonce, order.OrderType, order.Quantity, order.Stock)

		proposal := ProposeRequest{
			Block:           block,
			OraclePrice:     oracle.ExecutionPrice,
			OracleTimestamp: oracle.Timestamp,
			Signature:       order.Signature,
			PublicKey:       order.PublicKey,
			SignedMessage:   signedMsg,
		}

		// step 3: collect votes from Validators
		approvals := 1 // Leader approves its own proposal
		fmt.Printf("[Leader] Self-vote: approve\n")

		for _, addr := range cfg.ValidatorAddresses {
			vote := askValidator(addr, proposal)
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

			// send execute_order to Django
			if sendExecuteOrder(cfg, order.ID, oracle) {
				// commit block to local chain
				chain.Append(block)
				fmt.Printf("[Leader] Block #%d committed | chain length: %d\n",
					block.Index, chain.Length())

				// broadcast final block to Validators
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

// askValidator sends a ProposeBlock to a Validator and waits for a response
func askValidator(address string, proposal ProposeRequest) VoteResponse {
	body, _ := json.Marshal(proposal)
	url := fmt.Sprintf("http://%s/propose", address)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return VoteResponse{NodeID: address, Approve: false,
			Reason: fmt.Sprintf("connection error: %v", err)}
	}
	defer resp.Body.Close()

	var vote VoteResponse
	if err := json.NewDecoder(resp.Body).Decode(&vote); err != nil {
		return VoteResponse{NodeID: address, Approve: false, Reason: "invalid response"}
	}
	return vote
}

// broadcastCommit notifies a Validator that the block has been finalized
func broadcastCommit(address string, block Block) {
	body, _ := json.Marshal(block)
	url := fmt.Sprintf("http://%s/commit", address)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewBuffer(body))
	if err != nil {
		log.Printf("[Leader] Commit broadcast to %s failed: %v", address, err)
		return
	}
	defer resp.Body.Close()
	fmt.Printf("[Leader] Block broadcast to %s succeeded\n", address)
}

// sendExecuteOrder sends the POST request to Django
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
