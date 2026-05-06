// Binary node boots a single PoA execution node. It is identical in code
// for Leader and Validators — the only difference is the JSON config it
// loads (which sets is_leader).
//
// Usage:  ./node -config /etc/trading/node-1.json
package main

import (
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"

	"trading/internal/blockchain"
	"trading/internal/config"
	cryp "trading/internal/crypto"
	"trading/internal/consensus"
	"trading/internal/oracleclient"
	"trading/internal/server"
	"trading/internal/state"
	tradingpb "trading/proto"
)

func main() {
	cfgPath := flag.String("config", "config.json", "path to node config")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	log.Printf("[boot] node_id=%s leader=%v listen=%s", cfg.NodeID, cfg.IsLeader, cfg.GRPCListen)

	priv, err := cryp.LoadPrivateKeyPEM(cfg.PrivKeyPath)
	if err != nil {
		log.Fatalf("load key: %v", err)
	}

	st := state.New()
	if err := st.LoadGenesis(cfg.GenesisPath); err != nil {
		log.Fatalf("load genesis: %v", err)
	}

	chain, err := blockchain.OpenChain(cfg.ChainPath)
	if err != nil {
		log.Fatalf("open chain: %v", err)
	}
	log.Printf("[boot] chain head at index %d", chain.Head().Index)

	oracle := oracleclient.New(cfg.MaxPriceAge())

	engine, err := consensus.NewEngine(cfg, priv, chain, st, oracle)
	if err != nil {
		log.Fatalf("engine: %v", err)
	}

	srv := &server.Server{
		Engine: engine,
		State:  st,
		Oracle: oracle,
	}
	gs := grpc.NewServer()
	tradingpb.RegisterTradingServiceServer(gs, srv)
	tradingpb.RegisterConsensusServiceServer(gs, srv)
	tradingpb.RegisterOracleSinkServer(gs, srv)

	lis, err := net.Listen("tcp", cfg.GRPCListen)
	if err != nil {
		log.Fatalf("listen %s: %v", cfg.GRPCListen, err)
	}

	go func() {
		log.Printf("[boot] gRPC serving on %s", cfg.GRPCListen)
		if err := gs.Serve(lis); err != nil {
			log.Fatalf("grpc serve: %v", err)
		}
	}()

	// Graceful shutdown.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Printf("[boot] shutting down")
	gs.GracefulStop()
}
