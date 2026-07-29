package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"log"
	"math"
	"net"
	"strconv"
	"time"

	"google.golang.org/grpc"
	pb "nodes/consensus"
)

// desyncPrefix marks a rejection caused purely by the Validator lagging behind.
// The Validator emits "DESYNC head=<n> | ..." and the Leader parses <n> to know
// which committed blocks to replay. Both sides must agree on this format.
const desyncPrefix = "DESYNC head="

// ValidatorServer implements the gRPC ConsensusServiceServer interface
type ValidatorServer struct {
	pb.UnimplementedConsensusServiceServer
	cfg   Config
	chain *Chain
}

// runValidator starts the gRPC server that receives proposals from the Leader
func runValidator(cfg Config, chain *Chain) {
	fmt.Printf("[%s] Validator mode — listening on port :%s\n", cfg.NodeName, cfg.ListenPort)

	lis, err := net.Listen("tcp", ":"+cfg.ListenPort)
	if err != nil {
		log.Fatalf("[%s] Failed to listen: %v", cfg.NodeName, err)
	}

	srv := grpc.NewServer()
	pb.RegisterConsensusServiceServer(srv, &ValidatorServer{cfg: cfg, chain: chain})

	fmt.Printf("[%s] gRPC server ready\n", cfg.NodeName)
	log.Fatal(srv.Serve(lis))
}

// Propose receives a block proposal from the Leader, validates it, and returns a vote
func (s *ValidatorServer) Propose(ctx context.Context, req *pb.ProposeRequest) (*pb.VoteResponse, error) {
	block := protoToBlock(req.Block)

	fmt.Printf("\n[%s] Received proposal: block #%d | order #%d | %s $%s\n",
		s.cfg.NodeName, block.Index, block.OrderID, block.Stock, req.OraclePrice)

	// check 1: chain connectivity
	if err := s.chain.ValidateBlock(block); err != nil {
		reason := fmt.Sprintf("chain validation failed: %v", err)
		// If we are merely behind (missed commits while offline), tell the Leader
		// where our chain ends so it can replay what we missed and re-ask.
		if head := s.chain.HeadIndex(); block.Index > head+1 {
			reason = fmt.Sprintf("%s%d | %s", desyncPrefix, head, reason)
		}
		fmt.Printf("[%s] REJECT: %s\n", s.cfg.NodeName, reason)
		return &pb.VoteResponse{NodeId: s.cfg.NodeName, Approve: false, Reason: reason}, nil
	}

	// check 2: query independent Oracle
	myOracle, err := FetchPrice(s.cfg.OracleURL, block.Stock)
	if err != nil {
		reason := fmt.Sprintf("oracle unreachable: %v", err)
		fmt.Printf("[%s] REJECT: %s\n", s.cfg.NodeName, reason)
		return &pb.VoteResponse{NodeId: s.cfg.NodeName, Approve: false, Reason: reason}, nil
	}

	// check 3: price freshness (< 60 seconds)
	oracleTime, err := time.Parse(time.RFC3339Nano, req.OracleTimestamp)
	if err != nil {
		oracleTime, err = time.Parse(time.RFC3339, req.OracleTimestamp)
	}
	if err != nil || time.Since(oracleTime) > 60*time.Second {
		age := time.Since(oracleTime)
		reason := fmt.Sprintf("stale price: age=%v", age.Round(time.Second))
		fmt.Printf("[%s] REJECT: %s\n", s.cfg.NodeName, reason)
		return &pb.VoteResponse{NodeId: s.cfg.NodeName, Approve: false, Reason: reason}, nil
	}

	// check 4: price divergence < 1%
	proposedPrice, err1 := strconv.ParseFloat(req.OraclePrice, 64)
	myPrice, err2 := strconv.ParseFloat(myOracle.ExecutionPrice, 64)
	if err1 == nil && err2 == nil && myPrice > 0 {
		divergence := math.Abs(proposedPrice-myPrice) / myPrice * 100
		if divergence > 1.0 {
			reason := fmt.Sprintf("price divergence %.2f%% > 1%% (proposed=%.2f, mine=%.2f)",
				divergence, proposedPrice, myPrice)
			fmt.Printf("[%s] REJECT: %s\n", s.cfg.NodeName, reason)
			return &pb.VoteResponse{NodeId: s.cfg.NodeName, Approve: false, Reason: reason}, nil
		}
	}

	// check 5: ECDSA signature verification
	if req.Signature != "" && req.PublicKey != "" && req.SignedMessage != "" {
		if err := verifyECDSA(req.PublicKey, req.SignedMessage, req.Signature); err != nil {
			reason := fmt.Sprintf("ECDSA verification failed: %v", err)
			fmt.Printf("[%s] REJECT: %s\n", s.cfg.NodeName, reason)
			return &pb.VoteResponse{NodeId: s.cfg.NodeName, Approve: false, Reason: reason}, nil
		}
		fmt.Printf("[%s] ECDSA signature valid\n", s.cfg.NodeName)
	}

	fmt.Printf("[%s] APPROVE block #%d (oracle=$%s, mine=$%s)\n",
		s.cfg.NodeName, block.Index, req.OraclePrice, myOracle.ExecutionPrice)
	return &pb.VoteResponse{NodeId: s.cfg.NodeName, Approve: true}, nil
}

// Commit receives an approved block and appends it to the local chain.
// It is also the channel the Leader uses to replay blocks a lagging node missed,
// so it must be idempotent and must never append a block blindly.
func (s *ValidatorServer) Commit(ctx context.Context, req *pb.CommitRequest) (*pb.CommitResponse, error) {
	block := protoToBlock(req.Block)

	// already held — a replayed commit, acknowledge without duplicating
	if block.Index <= s.chain.HeadIndex() {
		fmt.Printf("[%s] Block #%d already present | chain length: %d\n",
			s.cfg.NodeName, block.Index, s.chain.Length())
		return &pb.CommitResponse{
			Status:      "already_present",
			Index:       int32(block.Index),
			ChainLength: int32(s.chain.Length()),
		}, nil
	}

	// the block must connect to our own chain — verify before trusting the Leader
	if err := s.chain.ValidateBlock(block); err != nil {
		fmt.Printf("[%s] Commit REJECTED for block #%d: %v\n", s.cfg.NodeName, block.Index, err)
		return &pb.CommitResponse{
			Status:      fmt.Sprintf("rejected: %v", err),
			Index:       int32(block.Index),
			ChainLength: int32(s.chain.Length()),
		}, nil
	}

	s.chain.Append(block)
	fmt.Printf("[%s] Block #%d committed | chain length: %d\n",
		s.cfg.NodeName, block.Index, s.chain.Length())
	return &pb.CommitResponse{
		Status:      "committed",
		Index:       int32(block.Index),
		ChainLength: int32(s.chain.Length()),
	}, nil
}

// protoToBlock converts a proto Block message to the local Block struct
func protoToBlock(pb *pb.Block) Block {
	return Block{
		Index:     int(pb.Index),
		PrevHash:  pb.PrevHash,
		Timestamp: pb.Timestamp,
		OrderID:   int(pb.OrderId),
		Stock:     pb.Stock,
		OrderType: pb.OrderType,
		Quantity:  pb.Quantity,
		Price:     pb.Price,
		NodeName:  pb.NodeName,
		Signature: pb.Signature,
		PublicKey: pb.PublicKey,
		Hash:      pb.Hash,
	}
}

// verifyECDSA verifies a P-256 ECDSA signature as produced by Python
func verifyECDSA(publicKeyPEM, message, signatureB64 string) error {
	block, _ := pem.Decode([]byte(publicKeyPEM))
	if block == nil {
		return fmt.Errorf("failed to decode PEM block")
	}

	pubInterface, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return fmt.Errorf("failed to parse public key: %v", err)
	}
	pubKey, ok := pubInterface.(*ecdsa.PublicKey)
	if !ok {
		return fmt.Errorf("not an ECDSA public key")
	}

	sigBytes, err := base64.StdEncoding.DecodeString(signatureB64)
	if err != nil {
		return fmt.Errorf("failed to decode signature: %v", err)
	}

	digest := sha256.Sum256([]byte(message))
	if !ecdsa.VerifyASN1(pubKey, digest[:], sigBytes) {
		return fmt.Errorf("signature verification failed")
	}
	return nil
}
