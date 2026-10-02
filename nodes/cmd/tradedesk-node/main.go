// tradedesk-node runs a TradeDesk validator: CometBFT consensus with the
// TradeDesk ledger as its application.
//
//	tradedesk-node init  [-dir testnet] [-validators 4] [-oracles yahoo,nasdaq,cnbc]
//	tradedesk-node start -home testnet/node0
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	cmtlog "github.com/cometbft/cometbft/libs/log"

	"nodes/chainnode"
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
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  tradedesk-node init  [-dir testnet] [-validators 4] [-chain-id tradedesk-local]
                       [-oracles yahoo,nasdaq,cnbc] [-base-port 26656] [-oracle-port 8001]
  tradedesk-node start -home testnet/node0 [-oracles host:port,...] [-v]`)
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

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
