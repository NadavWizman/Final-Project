package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"math"
	"net"
	"strconv"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
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

	// Every RPC must present the shared cluster credential. Without this the
	// Propose/Commit endpoints are open to anyone who can reach the port — a
	// stranger on the network could ask for votes or inject blocks directly.
	srv := grpc.NewServer(grpc.ChainUnaryInterceptor(
		recoveryInterceptor,
		clusterAuthInterceptor(cfg.ClusterSecret),
	))
	pb.RegisterConsensusServiceServer(srv, &ValidatorServer{cfg: cfg, chain: chain})

	fmt.Printf("[%s] gRPC server ready (authenticated)\n", cfg.NodeName)
	log.Fatal(srv.Serve(lis))
}

// authTokenKey is the gRPC metadata key carrying the shared cluster credential.
const authTokenKey = "auth-token"

// recoveryInterceptor turns a panic inside a handler into an Internal error.
// grpc-go does not recover handler panics, so without this one malformed
// request would take the whole Validator process down.
func recoveryInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler) (resp any, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[recovered] panic in %s: %v", info.FullMethod, r)
			err = status.Error(codes.Internal, "internal error")
		}
	}()
	return handler(ctx, req)
}

// clusterAuthInterceptor rejects any RPC that does not present the shared secret.
func clusterAuthInterceptor(secret string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler) (any, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing cluster credential")
		}
		tokens := md.Get(authTokenKey)
		if len(tokens) == 0 || subtle.ConstantTimeCompare([]byte(tokens[0]), []byte(secret)) != 1 {
			return nil, status.Error(codes.Unauthenticated, "invalid cluster credential")
		}
		return handler(ctx, req)
	}
}

// Propose receives a block proposal from the Leader, validates it, and returns a vote
func (s *ValidatorServer) Propose(ctx context.Context, req *pb.ProposeRequest) (*pb.VoteResponse, error) {
	if req.GetBlock() == nil {
		return nil, status.Error(codes.InvalidArgument, "missing block")
	}
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

	// check 4: the COMMITTED price must be within 1% of my own oracle.
	// We validate block.Price — the value that will be written to the chain —
	// not req.OraclePrice. A dishonest leader can put an honest price in the
	// envelope while committing a forged price in the block; checking only the
	// envelope would wave that through.
	committedPrice, err1 := strconv.ParseFloat(block.Price, 64)
	myPrice, err2 := strconv.ParseFloat(myOracle.ExecutionPrice, 64)
	if err1 != nil || err2 != nil || myPrice <= 0 {
		reason := fmt.Sprintf("unusable price (block=%q, mine=%q)", block.Price, myOracle.ExecutionPrice)
		fmt.Printf("[%s] REJECT: %s\n", s.cfg.NodeName, reason)
		return &pb.VoteResponse{NodeId: s.cfg.NodeName, Approve: false, Reason: reason}, nil
	}
	if divergence := math.Abs(committedPrice-myPrice) / myPrice * 100; divergence > 1.0 {
		reason := fmt.Sprintf("price divergence %.2f%% > 1%% (block=%.2f, mine=%.2f)",
			divergence, committedPrice, myPrice)
		fmt.Printf("[%s] REJECT: %s\n", s.cfg.NodeName, reason)
		return &pb.VoteResponse{NodeId: s.cfg.NodeName, Approve: false, Reason: reason}, nil
	}

	// check 5: ECDSA signature — present, valid, AND covering THIS block.
	// The signature material must exist (a missing signature is a rejection, not
	// a skip), and the signed order's trade fields must match the block we are
	// asked to commit. This binds the signature to the payload instead of to a
	// detached envelope the leader controls.
	if req.Signature == "" || req.PublicKey == "" || req.SignedMessage == "" {
		reason := "missing signature material"
		fmt.Printf("[%s] REJECT: %s\n", s.cfg.NodeName, reason)
		return &pb.VoteResponse{NodeId: s.cfg.NodeName, Approve: false, Reason: reason}, nil
	}
	var signed struct {
		OrderType string `json:"order_type"`
		Quantity  string `json:"quantity"`
		Stock     string `json:"stock"`
	}
	if err := json.Unmarshal([]byte(req.SignedMessage), &signed); err != nil {
		reason := fmt.Sprintf("unparseable signed message: %v", err)
		fmt.Printf("[%s] REJECT: %s\n", s.cfg.NodeName, reason)
		return &pb.VoteResponse{NodeId: s.cfg.NodeName, Approve: false, Reason: reason}, nil
	}
	if signed.OrderType != block.OrderType || signed.Quantity != block.Quantity || signed.Stock != block.Stock {
		reason := fmt.Sprintf("signed order does not match block: signed %s %s %s, block %s %s %s",
			signed.OrderType, signed.Quantity, signed.Stock,
			block.OrderType, block.Quantity, block.Stock)
		fmt.Printf("[%s] REJECT: %s\n", s.cfg.NodeName, reason)
		return &pb.VoteResponse{NodeId: s.cfg.NodeName, Approve: false, Reason: reason}, nil
	}
	if err := verifyECDSA(req.PublicKey, req.SignedMessage, req.Signature); err != nil {
		reason := fmt.Sprintf("ECDSA verification failed: %v", err)
		fmt.Printf("[%s] REJECT: %s\n", s.cfg.NodeName, reason)
		return &pb.VoteResponse{NodeId: s.cfg.NodeName, Approve: false, Reason: reason}, nil
	}
	fmt.Printf("[%s] ECDSA signature valid and matches block\n", s.cfg.NodeName)

	fmt.Printf("[%s] APPROVE block #%d (oracle=$%s, mine=$%s)\n",
		s.cfg.NodeName, block.Index, req.OraclePrice, myOracle.ExecutionPrice)
	return &pb.VoteResponse{NodeId: s.cfg.NodeName, Approve: true}, nil
}

// Commit receives an approved block and appends it to the local chain.
// It is also the channel the Leader uses to replay blocks a lagging node missed,
// so it must be idempotent and must never append a block blindly.
func (s *ValidatorServer) Commit(ctx context.Context, req *pb.CommitRequest) (*pb.CommitResponse, error) {
	if req.GetBlock() == nil {
		return nil, status.Error(codes.InvalidArgument, "missing block")
	}
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
