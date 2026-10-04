// tradedesk-node runs a TradeDesk validator: CometBFT consensus with the
// TradeDesk ledger as its application.
//
//	tradedesk-node init  [-dir testnet] [-validators 4] [-oracles yahoo,nasdaq,cnbc]
//	tradedesk-node start -home testnet/node0
//	tradedesk-node valset-sign   -home testnet/node0 -pubkey <base64> -power 10 -seq 0
//	tradedesk-node valset-submit -rpc http://127.0.0.1:26657 -pubkey <base64> -power 10 -seq 0 approval.json...
//	tradedesk-node deposit -key testnet/custody.key -to <username|address> -amount 500.00
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	cmtlog "github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/privval"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	"github.com/cometbft/cometbft/types"

	"nodes/app"
	"nodes/chainnode"
	"nodes/ledger"
	"nodes/quotes"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "init":
		initCmd(os.Args[2:])
	case "start":
		startCmd(os.Args[2:])
	case "valset-sign":
		valsetSignCmd(os.Args[2:])
	case "valset-submit":
		valsetSubmitCmd(os.Args[2:])
	case "deposit":
		depositCmd(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  tradedesk-node init  [-dir testnet] [-validators 4] [-chain-id tradedesk-local]
                       [-oracles yahoo,nasdaq,cnbc] [-base-port 26656] [-oracle-port 8001]
  tradedesk-node start -home testnet/node0 [-oracles host:port,...] [-v]
  tradedesk-node valset-sign   -home testnet/node0 -pubkey <base64> -power <n> -seq <n>
  tradedesk-node valset-submit -rpc <url> -pubkey <base64> -power <n> -seq <n> approval.json...
    A validator-set change needs approvals from more than 2/3 of the voting power.
  tradedesk-node deposit -key testnet/custody.key -to <username|address> -amount <dollars> [-rpc <url>]
    Credits an account. Only the custodian's key (registered in genesis) can sign deposits.`)
	os.Exit(2)
}

func initCmd(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	dir := fs.String("dir", "testnet", "output directory")
	n := fs.Int("validators", 4, "number of validators (n >= 3f+1; 4 tolerates one faulty node)")
	chainID := fs.String("chain-id", "tradedesk-local", "chain id, signed into every transaction")
	oracles := fs.String("oracles", "yahoo,nasdaq,cnbc", "price sources (odd number), one signing key each")
	basePort := fs.Int("base-port", 26656, "node i listens on base+10i (p2p) and base+10i+1 (RPC)")
	oraclePort := fs.Int("oracle-port", 8001, "oracle signer i listens on oracle-port+i")
	_ = fs.Parse(args)

	if _, err := os.Stat(*dir); err == nil {
		fail(fmt.Errorf("%s already exists; remove it to create a new network", *dir))
	}
	t := chainnode.Testnet{
		Dir: *dir, Validators: *n, ChainID: *chainID, Oracles: strings.Split(*oracles, ","),
		BasePort: *basePort, OracleBase: *oraclePort,
		BlockTime: time.Second, EmptyEvery: 5 * time.Second,
	}
	gen, err := chainnode.Init(t)
	if err != nil {
		fail(err)
	}
	fmt.Printf("Created %d validators for chain %s in %s\n", len(gen.Validators), gen.ChainID, *dir)
	for i := 0; i < t.Validators; i++ {
		fmt.Printf("  node%d  p2p :%d  rpc :%d\n", i, t.P2PPort(i), t.RPCPort(i))
	}
	for i, o := range t.Oracles {
		fmt.Printf("  oracle %-7s :%d  key %s\n", o, t.OracleBase+i, t.OracleKeyFile(o))
	}
}

func startCmd(args []string) {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	home := fs.String("home", "", "node home directory (created by init)")
	oracles := fs.String("oracles", "", "oracle signer addresses (default: from config/tradedesk.json)")
	verbose := fs.Bool("v", false, "log every consensus step")
	_ = fs.Parse(args)
	if *home == "" {
		usage()
	}
	addrs := strings.Split(*oracles, ",")
	if *oracles == "" {
		s, err := chainnode.LoadSettings(*home)
		if err != nil {
			fail(err)
		}
		addrs = s.Oracles
	}

	logger := cmtlog.NewTMLogger(cmtlog.NewSyncWriter(os.Stdout))
	if !*verbose {
		logger = cmtlog.NewFilter(logger, cmtlog.AllowError(),
			cmtlog.AllowInfoWith("module", "tradedesk"), cmtlog.AllowInfoWith("module", "state"))
	}
	n, err := chainnode.Start(*home, &quotes.GRPCFetcher{Addrs: addrs}, logger)
	if err != nil {
		fail(err)
	}
	fmt.Printf("TradeDesk node running (home %s, oracles %v). Ctrl+C to stop.\n", *home, addrs)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	_ = n.Stop()
	n.Wait()
}

func valsetSignCmd(args []string) {
	fs := flag.NewFlagSet("valset-sign", flag.ExitOnError)
	home := fs.String("home", "", "this validator's home directory")
	pub := fs.String("pubkey", "", "Ed25519 consensus key of the validator to set (base64)")
	power := fs.Int64("power", 10, "voting power; 0 removes the validator")
	seq := fs.Uint64("seq", 0, "number of validator-set changes so far")
	_ = fs.Parse(args)
	key, err := base64.StdEncoding.DecodeString(*pub)
	if err != nil || len(key) != 32 {
		fail(fmt.Errorf("-pubkey must be a base64 Ed25519 key"))
	}
	c, err := chainnode.LoadConfig(*home)
	if err != nil {
		fail(err)
	}
	gen, err := types.GenesisDocFromFile(c.GenesisFile())
	if err != nil {
		fail(err)
	}
	pv := privval.LoadFilePV(c.PrivValidatorKeyFile(), c.PrivValidatorStateFile())
	approval := app.SignValset(ed25519.PrivateKey(pv.Key.PrivKey.Bytes()),
		app.ValsetChange{Chain: gen.ChainID, Seq: *seq, PubKey: key, Power: *power})
	out, _ := json.Marshal(approval)
	fmt.Println(string(out))
}

func valsetSubmitCmd(args []string) {
	fs := flag.NewFlagSet("valset-submit", flag.ExitOnError)
	rpc := fs.String("rpc", "http://127.0.0.1:26657", "a node's RPC address")
	chainID := fs.String("chain-id", "tradedesk-local", "chain id")
	pub := fs.String("pubkey", "", "")
	power := fs.Int64("power", 10, "")
	seq := fs.Uint64("seq", 0, "")
	_ = fs.Parse(args)
	key, err := base64.StdEncoding.DecodeString(*pub)
	if err != nil || len(key) != 32 {
		fail(fmt.Errorf("-pubkey must be a base64 Ed25519 key"))
	}
	tx := app.ValsetTx{Valset: app.ValsetChange{Chain: *chainID, Seq: *seq, PubKey: key, Power: *power}}
	for _, f := range fs.Args() {
		var a app.ValsetApproval
		b, err := os.ReadFile(f)
		if err != nil || json.Unmarshal(b, &a) != nil {
			fail(fmt.Errorf("cannot read approval %s", f))
		}
		tx.Sigs = append(tx.Sigs, a)
	}
	raw, _ := json.Marshal(tx)
	client, err := rpchttp.New(*rpc, "/websocket")
	if err != nil {
		fail(err)
	}
	res, err := client.BroadcastTxCommit(context.Background(), raw)
	if err != nil {
		fail(err)
	}
	if res.CheckTx.Code != 0 {
		fail(fmt.Errorf("refused: %s", res.CheckTx.Log))
	}
	fmt.Printf("validator set updated at height %d: %s\n", res.Height, res.TxResult.Log)
}

func depositCmd(args []string) {
	fs := flag.NewFlagSet("deposit", flag.ExitOnError)
	keyFile := fs.String("key", "testnet/custody.key", "the custodian's signing key")
	rpc := fs.String("rpc", "http://127.0.0.1:26657", "a node's RPC address")
	to := fs.String("to", "", "username or address to credit")
	amount := fs.String("amount", "", "amount in dollars, e.g. 500.00")
	_ = fs.Parse(args)
	key, err := quotes.LoadKey(*keyFile)
	if err != nil {
		fail(fmt.Errorf("cannot read the custodian key: %v", err))
	}
	client, err := rpchttp.New(*rpc, "/websocket")
	if err != nil {
		fail(err)
	}
	ctx := context.Background()
	query := func(path, data string, v any) error {
		res, err := client.ABCIQuery(ctx, path, []byte(data))
		if err != nil {
			return err
		}
		if res.Response.Code != 0 {
			return fmt.Errorf("%s", res.Response.Log)
		}
		return json.Unmarshal(res.Response.Value, v)
	}
	var st struct {
		ChainID    string `json:"chain_id"`
		CustodySeq uint64 `json:"custody_seq"`
	}
	if err := query("/status", "", &st); err != nil {
		fail(err)
	}
	addr := *to
	if len(addr) != 40 {
		var u struct{ Address string }
		if err := query("/username", addr, &u); err != nil {
			fail(fmt.Errorf("no account %q", addr))
		}
		addr = u.Address
	}
	raw, err := ledger.SignDeposit(key, ledger.DepositMsg{Chain: st.ChainID, Seq: fmt.Sprint(st.CustodySeq), To: addr, Amount: *amount})
	if err != nil {
		fail(err)
	}
	res, err := client.BroadcastTxCommit(ctx, raw)
	if err != nil {
		fail(err)
	}
	if res.CheckTx.Code != 0 {
		fail(fmt.Errorf("refused: %s", res.CheckTx.Log))
	}
	fmt.Printf("deposit #%d of %s to %s committed at height %d\n", st.CustodySeq, *amount, addr, res.Height)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
