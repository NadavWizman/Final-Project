package main

import (
	"fmt"
	"log"
)

func main() {
	cfg := LoadConfig()
	chain := NewChain(cfg.NodeName)

	fmt.Printf("\n========================================\n")
	fmt.Printf("  Execution Node: %s\n", cfg.NodeName)
	fmt.Printf("  Role:           %s\n", role(cfg))
	fmt.Printf("  Chain:          %d blocks\n", chain.Length())
	fmt.Printf("  Head:           #%d %s...\n", chain.Head().Index, short(chain.Head().Hash))
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
