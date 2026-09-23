package main

import (
	"bufio"
	"log"
	"os"
	"strings"
	"time"
)

// Config holds all node configuration
type Config struct {
	NodeName           string
	NodePass           string
	DjangoURL          string
	OracleURL          string
	IsLeader           bool
	ValidatorAddresses []string      // addresses of the other nodes (used by Leader)
	ListenPort         string        // HTTP server port (used by Validators)
	ClusterSecret      string        // shared credential for node-to-node gRPC auth
	LimitOrderTTL      time.Duration // how long a limit order may wait for its price
}

// minSecretLen is the shortest CLUSTER_SECRET accepted.
const minSecretLen = 16

// LoadConfig reads the node configuration from the environment, after loading
// defaults from a .env file in the working directory (see .env.example;
// setup.sh generates one with random secrets). Real environment variables
// always win over the file. Secrets have no built-in defaults: a node refuses
// to start without them rather than fall back to a value published in the repo.
func LoadConfig() Config {
	loadDotEnv(".env")

	name := os.Getenv("NODE_NAME")
	if name == "" {
		name = "node1"
	}

	// NODE_PASS, or a per-node NODE1_PASS / NODE2_PASS / NODE3_PASS from .env
	pass := os.Getenv("NODE_PASS")
	if pass == "" {
		pass = os.Getenv(strings.ToUpper(name) + "_PASS")
	}
	if pass == "" {
		log.Fatalf("No password for %s: set NODE_PASS or %s_PASS (run setup.sh to generate nodes/.env)",
			name, strings.ToUpper(name))
	}

	isLeader := os.Getenv("IS_LEADER") == "true"

	// Validator addresses — the Leader sends proposals to these
	validatorsEnv := os.Getenv("VALIDATOR_ADDRESSES")
	var validators []string
	if validatorsEnv != "" {
		for _, a := range strings.Split(validatorsEnv, ",") {
			if a = strings.TrimSpace(a); a != "" {
				validators = append(validators, a)
			}
		}
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

	djangoURL := os.Getenv("DJANGO_URL")
	if djangoURL == "" {
		djangoURL = "http://127.0.0.1:8000/api"
	}
	oracleURL := os.Getenv("ORACLE_URL")
	if oracleURL == "" {
		oracleURL = "127.0.0.1:8001"
	}

	// Shared credential for node-to-node gRPC. All nodes must agree on it; a
	// caller that cannot prove knowledge of it is not part of the cluster.
	clusterSecret := os.Getenv("CLUSTER_SECRET")
	if len(clusterSecret) < minSecretLen {
		log.Fatalf("CLUSTER_SECRET must be set to at least %d characters "+
			"(run setup.sh to generate nodes/.env)", minSecretLen)
	}

	limitTTL := 24 * time.Hour
	if v := os.Getenv("LIMIT_ORDER_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			log.Fatalf("LIMIT_ORDER_TTL must be a positive duration such as 24h or 90m, got %q", v)
		}
		limitTTL = d
	}

	return Config{
		NodeName:           name,
		NodePass:           pass,
		DjangoURL:          strings.TrimRight(djangoURL, "/"),
		OracleURL:          oracleURL,
		IsLeader:           isLeader,
		ValidatorAddresses: validators,
		ListenPort:         listenPort,
		ClusterSecret:      clusterSecret,
		LimitOrderTTL:      limitTTL,
	}
}

// loadDotEnv sets KEY=VALUE pairs from path for keys not already in the
// environment. A missing file is not an error.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(strings.TrimPrefix(key, "export "))
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		if _, set := os.LookupEnv(key); !set {
			os.Setenv(key, val)
		}
	}
}
