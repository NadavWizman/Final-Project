// Package chainnode creates and runs TradeDesk validator nodes: a CometBFT
// node with the TradeDesk application (package app) in the same process.
package chainnode

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	cfg "github.com/cometbft/cometbft/config"
	cmtlog "github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/node"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/privval"
	"github.com/cometbft/cometbft/proxy"
	"github.com/cometbft/cometbft/types"
	"github.com/spf13/viper"

	"nodes/app"
	"nodes/ledger"
	"nodes/quotes"
)

// Tickers are the tradable S&P 500 symbols, fixed in the genesis file.
var Tickers = []string{
	"AAPL", "MSFT", "GOOGL", "AMZN", "NVDA", "META", "TSLA", "BRK.B",
	"UNH", "LLY", "JPM", "XOM", "V", "AVGO", "PG", "MA", "HD", "COST",
	"MRK", "CVX", "ABBV", "ORCL", "WMT", "BAC", "KO", "PFE", "NFLX",
	"CRM", "AMD", "TMO", "ACN", "MCD", "LIN", "CSCO", "TXN", "ADBE",
	"DHR", "NEE", "NKE", "INTC", "PM", "UPS", "AMGN", "HON", "LOW",
	"IBM", "SBUX", "QCOM", "GE", "CAT", "GS", "MS", "BLK", "SPGI",
}

// Testnet describes a local network to generate.
type Testnet struct {
	Dir        string   // output directory
	Validators int      // number of validator nodes (4 tolerates 1 faulty node)
	ChainID    string   //
	Oracles    []string // oracle source names, one signer each
	BasePort   int      // node i uses BasePort+10*i (p2p) and +1 (RPC)
	OracleBase int      // oracle signer i listens on OracleBase+i
	BlockTime  time.Duration
	EmptyEvery time.Duration // an empty block at least this often, so rules keep running
	// TimeoutPropose is how long validators wait for a proposer before moving
	// to the next one (0 = CometBFT's default of 3 s).
	TimeoutPropose time.Duration
}

// Ports of node i.
func (t Testnet) P2PPort(i int) int { return t.BasePort + 10*i }
func (t Testnet) RPCPort(i int) int { return t.BasePort + 10*i + 1 }

// NodeHome is the home directory of node i.
func (t Testnet) NodeHome(i int) string { return filepath.Join(t.Dir, fmt.Sprintf("node%d", i)) }

// OracleKeyFile is where the signing key of an oracle source is written.
func (t Testnet) OracleKeyFile(name string) string {
	return filepath.Join(t.Dir, "oracles", name+".key")
}

// NodeSettings are TradeDesk settings of one node, next to CometBFT's config.
type NodeSettings struct {
	Oracles []string `json:"oracles"` // oracle signer addresses (used when proposing)
}

// Init writes keys, configuration and one shared genesis file for every node,
// and a signing key for every oracle source. The genesis file fixes the
// validator set, the oracle public keys and the ledger parameters — nothing
// about the network's membership lives anywhere else.
func Init(t Testnet) (*types.GenesisDoc, error) {
	if t.Validators < 1 {
		return nil, fmt.Errorf("need at least one validator")
	}
	if len(t.Oracles)%2 == 0 {
		return nil, fmt.Errorf("use an odd number of oracle sources (got %d)", len(t.Oracles))
	}
	var sources []quotes.Source
	var oracleAddrs []string
	for i, name := range t.Oracles {
		pub, priv, err := ed25519.GenerateKey(nil)
		if err != nil {
			return nil, err
		}
		f := t.OracleKeyFile(name)
		if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(f, []byte(hex.EncodeToString(priv.Seed())+"\n"), 0o600); err != nil {
			return nil, err
		}
		sources = append(sources, quotes.Source{Name: name, PubKey: pub})
		oracleAddrs = append(oracleAddrs, fmt.Sprintf("127.0.0.1:%d", t.OracleBase+i))
	}

	appState, _ := json.Marshal(app.GenesisState{
		Params:  ledger.DefaultParams(t.ChainID, Tickers),
		Oracles: sources,
	})
	gen := &types.GenesisDoc{
		ChainID:         t.ChainID,
		GenesisTime:     time.Now().UTC(),
		ConsensusParams: types.DefaultConsensusParams(),
		AppState:        appState,
	}

	var peers []string
	configs := make([]*cfg.Config, t.Validators)
	for i := 0; i < t.Validators; i++ {
		home := t.NodeHome(i)
		c := cfg.DefaultConfig()
		c.SetRoot(home)
		c.Moniker = fmt.Sprintf("node%d", i)
		for _, d := range []string{filepath.Join(home, "config"), filepath.Join(home, "data")} {
			if err := os.MkdirAll(d, 0o700); err != nil {
				return nil, err
			}
		}
		pv := privval.GenFilePV(c.PrivValidatorKeyFile(), c.PrivValidatorStateFile())
		pv.Save()
		nk, err := p2p.LoadOrGenNodeKey(c.NodeKeyFile())
		if err != nil {
			return nil, err
		}
		pub, _ := pv.GetPubKey()
		gen.Validators = append(gen.Validators, types.GenesisValidator{
			Address: pub.Address(), PubKey: pub, Power: 10, Name: c.Moniker,
		})
		peers = append(peers, fmt.Sprintf("%s@127.0.0.1:%d", nk.ID(), t.P2PPort(i)))
		configs[i] = c
	}

	for i, c := range configs {
		c.P2P.ListenAddress = fmt.Sprintf("tcp://127.0.0.1:%d", t.P2PPort(i))
		c.RPC.ListenAddress = fmt.Sprintf("tcp://127.0.0.1:%d", t.RPCPort(i))
		var others []string
		for j, p := range peers {
			if j != i {
				others = append(others, p)
			}
		}
		c.P2P.PersistentPeers = strings.Join(others, ",")
		c.P2P.AddrBookStrict = false
		c.P2P.AllowDuplicateIP = true
		c.Consensus.TimeoutCommit = t.BlockTime
		if t.TimeoutPropose > 0 {
			c.Consensus.TimeoutPropose = t.TimeoutPropose
		}
		c.Consensus.CreateEmptyBlocks = true
		c.Consensus.CreateEmptyBlocksInterval = t.EmptyEvery
		c.TxIndex.Indexer = "kv"
		c.Instrumentation.Prometheus = false
		cfg.WriteConfigFile(filepath.Join(c.RootDir, "config", "config.toml"), c)
		if err := gen.SaveAs(c.GenesisFile()); err != nil {
			return nil, err
		}
		s, _ := json.MarshalIndent(NodeSettings{Oracles: oracleAddrs}, "", "  ")
		if err := os.WriteFile(filepath.Join(c.RootDir, "config", "tradedesk.json"), s, 0o644); err != nil {
			return nil, err
		}
	}
	return gen, gen.ValidateAndComplete()
}

// LoadConfig reads a node's CometBFT configuration from its home directory.
func LoadConfig(home string) (*cfg.Config, error) {
	c := cfg.DefaultConfig()
	v := viper.New()
	v.SetConfigFile(filepath.Join(home, "config", "config.toml"))
	if err := v.ReadInConfig(); err != nil {
		return nil, err
	}
	if err := v.Unmarshal(c); err != nil {
		return nil, err
	}
	c.SetRoot(home)
	return c, c.ValidateBasic()
}

// LoadSettings reads a node's TradeDesk settings.
func LoadSettings(home string) (NodeSettings, error) {
	var s NodeSettings
	b, err := os.ReadFile(filepath.Join(home, "config", "tradedesk.json"))
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(b, &s)
}

// Node is a running validator: CometBFT plus the application.
type Node struct {
	*node.Node
	App *app.App
}

// Start runs the node in home. fetcher supplies signed quotes when this node
// proposes; logger may be nil for quiet output.
func Start(home string, fetcher quotes.Fetcher, logger cmtlog.Logger) (*Node, error) {
	c, err := LoadConfig(home)
	if err != nil {
		return nil, err
	}
	// CometBFT's optional gRPC broadcast server is never started: nothing
	// uses it, and gRPC servers are affected by GO-2026-6443 (a request
	// without :authority/Host panics the server), which has no released fix
	// yet. The node's only gRPC use is as a client of the price sources.
	c.RPC.GRPCListenAddress = ""
	if logger == nil {
		logger = cmtlog.NewNopLogger()
	}
	a, err := app.New(home, fetcher, logger.With("module", "tradedesk"))
	if err != nil {
		return nil, err
	}
	nk, err := p2p.LoadNodeKey(c.NodeKeyFile())
	if err != nil {
		return nil, err
	}
	n, err := node.NewNode(c,
		privval.LoadFilePV(c.PrivValidatorKeyFile(), c.PrivValidatorStateFile()),
		nk,
		proxy.NewLocalClientCreator(a),
		node.DefaultGenesisDocProviderFunc(c),
		cfg.DefaultDBProvider,
		node.DefaultMetricsProvider(c.Instrumentation),
		logger,
	)
	if err != nil {
		return nil, err
	}
	if err := n.Start(); err != nil {
		return nil, err
	}
	return &Node{Node: n, App: a}, nil
}
