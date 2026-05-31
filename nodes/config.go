package main

import (
	"os"
	"strings"
)

// Config holds all node configuration
type Config struct {
	NodeName           string
	NodePass           string
	DjangoURL          string
	OracleURL          string
	IsLeader           bool
	ValidatorAddresses []string // addresses of the other nodes (used by Leader)
	ListenPort         string   // HTTP server port (used by Validators)
}

func LoadConfig() Config {
	name := os.Getenv("NODE_NAME")
	if name == "" {
		name = "node1"
	}
	pass := os.Getenv("NODE_PASS")
	if pass == "" {
		pass = "node1pass"
	}

	isLeader := os.Getenv("IS_LEADER") == "true"

	// Validator addresses — the Leader sends proposals to these
	validatorsEnv := os.Getenv("VALIDATOR_ADDRESSES")
	var validators []string
	if validatorsEnv != "" {
		validators = strings.Split(validatorsEnv, ",")
	} else if isLeader {
		// default: node2 on 9002, node3 on 9003
		validators = []string{"localhost:9002", "localhost:9003"}
	}

	// listen port (Validators only)
	listenPort := os.Getenv("LISTEN_PORT")
	if listenPort == "" && !isLeader {
		switch name {
		case "node2":
			listenPort = "9002"
		case "node3":
			listenPort = "9003"
		}
	}

	return Config{
		NodeName:           name,
		NodePass:           pass,
		DjangoURL:          "http://127.0.0.1:8000/api",
		OracleURL:          "127.0.0.1:8001",
		IsLeader:           isLeader,
		ValidatorAddresses: validators,
		ListenPort:         listenPort,
	}
}
