package main

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"time"
)

// runValidator starts the HTTP server that receives proposals from the Leader
func runValidator(cfg Config, chain *Chain) {
	fmt.Printf("[%s] Validator mode — listening on port :%s\n", cfg.NodeName, cfg.ListenPort)

	mux := http.NewServeMux()
	mux.HandleFunc("/propose", handlePropose(cfg, chain))
	mux.HandleFunc("/commit",  handleCommit(cfg, chain))
	mux.HandleFunc("/health",  handleHealth(cfg, chain))

	log.Fatal(http.ListenAndServe(":"+cfg.ListenPort, mux))
}

// handlePropose — receives a block proposal from the Leader, validates it, and returns a vote
func handlePropose(cfg Config, chain *Chain) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		var proposal ProposeRequest
		if err := json.NewDecoder(r.Body).Decode(&proposal); err != nil {
			json.NewEncoder(w).Encode(VoteResponse{
				NodeID: cfg.NodeName, Approve: false, Reason: "invalid JSON",
			})
			return
		}

		block := proposal.Block
		fmt.Printf("\n[%s] Received proposal: block #%d | order #%d | %s $%s\n",
			cfg.NodeName, block.Index, block.OrderID, block.Stock, proposal.OraclePrice)

		// check 1: chain connectivity
		if err := chain.ValidateBlock(block); err != nil {
			reason := fmt.Sprintf("chain validation failed: %v", err)
			fmt.Printf("[%s] REJECT: %s\n", cfg.NodeName, reason)
			json.NewEncoder(w).Encode(VoteResponse{NodeID: cfg.NodeName, Approve: false, Reason: reason})
			return
		}

		// check 2: query independent Oracle
		myOracle, err := FetchPrice(cfg.OracleURL, block.Stock)
		if err != nil {
			reason := fmt.Sprintf("oracle unreachable: %v", err)
			fmt.Printf("[%s] REJECT: %s\n", cfg.NodeName, reason)
			json.NewEncoder(w).Encode(VoteResponse{NodeID: cfg.NodeName, Approve: false, Reason: reason})
			return
		}

		// check 3: price freshness (< 60 seconds)
		oracleTime, err := time.Parse(time.RFC3339Nano, proposal.OracleTimestamp)
		if err != nil {
			// try alternative format
			oracleTime, err = time.Parse(time.RFC3339, proposal.OracleTimestamp)
		}
		if err != nil || time.Since(oracleTime) > 60*time.Second {
			age := time.Since(oracleTime)
			reason := fmt.Sprintf("stale price: age=%v", age.Round(time.Second))
			fmt.Printf("[%s] REJECT: %s\n", cfg.NodeName, reason)
			json.NewEncoder(w).Encode(VoteResponse{NodeID: cfg.NodeName, Approve: false, Reason: reason})
			return
		}

		// check 4: price divergence < 1%
		proposedPrice, err1 := strconv.ParseFloat(proposal.OraclePrice, 64)
		myPrice, err2 := strconv.ParseFloat(myOracle.ExecutionPrice, 64)
		if err1 == nil && err2 == nil && myPrice > 0 {
			divergence := math.Abs(proposedPrice-myPrice) / myPrice * 100
			if divergence > 1.0 {
				reason := fmt.Sprintf("price divergence %.2f%% > 1%% (proposed=%.2f, mine=%.2f)",
					divergence, proposedPrice, myPrice)
				fmt.Printf("[%s] REJECT: %s\n", cfg.NodeName, reason)
				json.NewEncoder(w).Encode(VoteResponse{NodeID: cfg.NodeName, Approve: false, Reason: reason})
				return
			}
		}

		// check 5: ECDSA signature verification
		if proposal.Signature != "" && proposal.PublicKey != "" && proposal.SignedMessage != "" {
			if err := verifyECDSA(proposal.PublicKey, proposal.SignedMessage, proposal.Signature); err != nil {
				reason := fmt.Sprintf("ECDSA verification failed: %v", err)
				fmt.Printf("[%s] REJECT: %s\n", cfg.NodeName, reason)
				json.NewEncoder(w).Encode(VoteResponse{NodeID: cfg.NodeName, Approve: false, Reason: reason})
				return
			}
			fmt.Printf("[%s] ECDSA signature valid\n", cfg.NodeName)
		}

		// all checks passed — approve
		fmt.Printf("[%s] APPROVE block #%d (oracle=$%s, mine=$%s)\n",
			cfg.NodeName, block.Index, proposal.OraclePrice, myOracle.ExecutionPrice)
		json.NewEncoder(w).Encode(VoteResponse{NodeID: cfg.NodeName, Approve: true})
	}
}

// handleCommit — receives an approved block and appends it to the local chain
func handleCommit(cfg Config, chain *Chain) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		var block Block
		if err := json.NewDecoder(r.Body).Decode(&block); err != nil {
			http.Error(w, `{"error":"invalid block"}`, http.StatusBadRequest)
			return
		}

		chain.Append(block)
		fmt.Printf("[%s] Block #%d committed | chain length: %d\n",
			cfg.NodeName, block.Index, chain.Length())

		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "committed",
			"index":  block.Index,
			"chain_length": chain.Length(),
		})
	}
}

// verifyECDSA — verifies a P-256 ECDSA signature as produced by Python
// publicKeyPEM : PEM public key (SubjectPublicKeyInfo)
// message      : the original signed message (sorted JSON)
// signatureB64 : DER signature encoded in Base64
func verifyECDSA(publicKeyPEM, message, signatureB64 string) error {
	// decode PEM
	block, _ := pem.Decode([]byte(publicKeyPEM))
	if block == nil {
		return fmt.Errorf("failed to decode PEM block")
	}

	// load public key
	pubInterface, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return fmt.Errorf("failed to parse public key: %v", err)
	}
	pubKey, ok := pubInterface.(*ecdsa.PublicKey)
	if !ok {
		return fmt.Errorf("not an ECDSA public key")
	}

	// decode signature
	sigBytes, err := base64.StdEncoding.DecodeString(signatureB64)
	if err != nil {
		return fmt.Errorf("failed to decode signature: %v", err)
	}

	// compute SHA-256 of the message
	digest := sha256.Sum256([]byte(message))

	// verify (DER signature decoded internally by ecdsa.VerifyASN1)
	if !ecdsa.VerifyASN1(pubKey, digest[:], sigBytes) {
		return fmt.Errorf("signature verification failed")
	}
	return nil
}

// handleHealth — node liveness check
func handleHealth(cfg Config, chain *Chain) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		head := chain.Head()
		json.NewEncoder(w).Encode(map[string]interface{}{
			"node":         cfg.NodeName,
			"status":       "ok",
			"chain_length": chain.Length(),
			"head_index":   head.Index,
			"head_hash":    head.Hash[:16] + "...",
		})
	}
}
