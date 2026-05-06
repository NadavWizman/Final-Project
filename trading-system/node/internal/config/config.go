package config

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// AuthorityKey identifies a PoA authority node. PubKeyPEM is a PEM-encoded
// SubjectPublicKeyInfo (so we can trivially paste it into Python or Go).
type AuthorityKey struct {
	NodeID    string `json:"node_id"`
	PubKeyPEM string `json:"pub_key_pem"`
	Address   string `json:"address"` // host:port for gRPC
}

type Config struct {
	NodeID           string         `json:"node_id"`
	IsLeader         bool           `json:"is_leader"`
	GRPCListen       string         `json:"grpc_listen"`         // e.g. ":9001"
	PrivKeyPath      string         `json:"priv_key_path"`       // PEM PKCS8
	GenesisPath      string         `json:"genesis_path"`        // JSON
	ChainPath        string         `json:"chain_path"`          // file to append to
	Authorities      []AuthorityKey `json:"authorities"`         // 3 entries
	MaxPriceAgeSecs  int            `json:"max_price_age_secs"`  // default 30
	ConsensusTimeout int            `json:"consensus_timeout_ms"`// default 2000
}

func (c *Config) MaxPriceAge() time.Duration {
	if c.MaxPriceAgeSecs == 0 {
		return 30 * time.Second
	}
	return time.Duration(c.MaxPriceAgeSecs) * time.Second
}

func (c *Config) ConsensusTimeoutDuration() time.Duration {
	if c.ConsensusTimeout == 0 {
		return 2 * time.Second
	}
	return time.Duration(c.ConsensusTimeout) * time.Millisecond
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if len(c.Authorities) == 0 {
		return nil, fmt.Errorf("config must include authorities")
	}
	return &c, nil
}
