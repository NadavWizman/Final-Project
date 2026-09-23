package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
)

// Order — order struct as received from Django
type Order struct {
	ID        int    `json:"id"`
	Stock     string `json:"stock"`
	Status    string `json:"status"`
	OrderType string `json:"order_type"`
	Quantity  string `json:"quantity"`
	Nonce     string `json:"nonce"`      // prevents replay attacks
	Signature string `json:"signature"`  // user's ECDSA signature
	PublicKey string `json:"public_key"` // PEM public key for verification
	// SignedMessage is the exact canonical message the signature covers, as
	// produced by Django. Nodes verify these bytes; they never rebuild them.
	SignedMessage string `json:"signed_message"`
}

// fetchOrders — fetches orders from the Django API
func fetchOrders(cfg Config) ([]Order, error) {
	req, err := http.NewRequest("GET", cfg.DjangoURL+"/orders/", nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(cfg.NodeName, cfg.NodePass)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var orders []Order
	if err := json.NewDecoder(resp.Body).Decode(&orders); err != nil {
		return nil, err
	}
	return orders, nil
}

func main() {
	cfg := LoadConfig()
	chain := NewChain(cfg.NodeName)

	fmt.Printf("\n========================================\n")
	fmt.Printf("  Execution Node: %s\n", cfg.NodeName)
	fmt.Printf("  Role:           %s\n", role(cfg))
	fmt.Printf("  Chain:          %d blocks\n", chain.Length())
	fmt.Printf("  Head:           #%d %s...\n", chain.Head().Index, chain.Head().Hash[:16])
	fmt.Printf("========================================\n\n")

	if cfg.IsLeader {
		runLeader(cfg, chain)
	} else {
		if cfg.ListenPort == "" {
			log.Fatal("LISTEN_PORT not set for Validator. Set NODE_NAME=node2 or node3.")
		}
		runValidator(cfg, chain)
	}
}

func role(cfg Config) string {
	if cfg.IsLeader {
		return "Leader (consensus coordinator)"
	}
	return fmt.Sprintf("Validator (listening on :%s)", cfg.ListenPort)
}
