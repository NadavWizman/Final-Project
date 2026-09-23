package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/sha256"
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
	"google.golang.org/grpc/status"
	"nodes/clusterauth"
	pb "nodes/consensus"
)

// desyncPrefix marks a rejection caused purely by the Validator lagging behind.
// The Validator emits "DESYNC head=<n> | ..." and the Leader parses <n> to know
// which committed blocks to replay. Both sides must agree on this format.
const desyncPrefix = "DESYNC head="

// ValidatorServer implements the gRPC ConsensusServiceServer interface
type ValidatorServer struct {
	pb.UnimplementedConsensusServiceServer
	cfg     Config
	chain   *Chain
	django  *DjangoClient
	key     ed25519.PrivateKey                       // signs this node's approvals
	priceFn func(ticker string) (*OracleData, error) // test hook; nil = live Oracle
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
		clusterauth.ServerInterceptor(cfg.ClusterSecret),
	))
	node, err := newNode(cfg, chain)
	if err != nil {
		log.Fatalf("[%s] %v", cfg.NodeName, err)
	}
	pb.RegisterConsensusServiceServer(srv, node)

	fmt.Printf("[%s] gRPC server ready (authenticated)\n", cfg.NodeName)
	log.Fatal(srv.Serve(lis))
}

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

// Propose receives a block proposal from the Leader, validates it, and returns a vote
func (s *ValidatorServer) Propose(ctx context.Context, req *pb.ProposeRequest) (*pb.VoteResponse, error) {
	if req.GetBlock() == nil {
		return nil, status.Error(codes.InvalidArgument, "missing block")
	}
	block := protoToBlock(req.Block)

	fmt.Printf("\n[%s] Received proposal: block #%d | order #%d | %s $%s\n",
		s.cfg.NodeName, block.Index, block.OrderID, block.Stock, block.Price)

	if reason := s.evaluate(block, req); reason != "" {
		fmt.Printf("[%s] REJECT: %s\n", s.cfg.NodeName, reason)
		return &pb.VoteResponse{NodeId: s.cfg.NodeName, Approve: false, Reason: reason}, nil
	}

	fmt.Printf("[%s] APPROVE block #%d (price $%s)\n", s.cfg.NodeName, block.Index, block.Price)
	return &pb.VoteResponse{NodeId: s.cfg.NodeName, Approve: true,
		VoteSignature: signVote(s.key, block)}, nil
}

// newNode builds the consensus logic shared by Leader and Validators: chain,
// Django access and the node's signing key (registered with Django in the
// background, so the node can start before the backend).
func newNode(cfg Config, chain *Chain) (*ValidatorServer, error) {
	key, err := loadOrCreateNodeKey(cfg.NodeName)
	if err != nil {
		return nil, err
	}
	dj := NewDjangoClient(cfg)
	go registerKeyUntilDone(dj, cfg.NodeName, key.Public().(ed25519.PublicKey))
	return &ValidatorServer{cfg: cfg, chain: chain, django: dj, key: key}, nil
}

// evaluate runs every check on a proposed block and returns the reason for
// rejecting it, or "" to approve. Nothing the Leader sends is trusted on its
// own: the order, its signature and the user's public key are re-read from
// Django with this node's own credentials, and the price is re-read from the
// Oracle.
func (s *ValidatorServer) evaluate(block Block, req *pb.ProposeRequest) string {
	// check 1: chain connectivity
	if err := s.chain.ValidateBlock(block); err != nil {
		reason := fmt.Sprintf("chain validation failed: %v", err)
		// If we are merely behind (missed commits while offline), tell the Leader
		// where our chain ends so it can replay what we missed and re-ask.
		if head := s.chain.HeadIndex(); block.Index > head+1 {
			reason = fmt.Sprintf("%s%d | %s", desyncPrefix, head, reason)
		}
		return reason
	}

	// check 2: the order exists in Django, is awaiting consensus, and the block,
	// the envelope and Django all agree on it. The public key comes from Django,
	// never from the Leader — otherwise any key pair would "verify".
	order, err := s.django.Order(block.OrderID)
	if err != nil {
		return fmt.Sprintf("cannot load order #%d from Django: %v", block.OrderID, err)
	}
	if order.Status != "SUBMITTED" {
		return fmt.Sprintf("order #%d is %s, not SUBMITTED", order.ID, order.Status)
	}
	if order.Stock != block.Stock || order.OrderType != block.OrderType || order.Quantity != block.Quantity {
		return fmt.Sprintf("block does not match order #%d: order %s %s %s, block %s %s %s",
			order.ID, order.OrderType, order.Quantity, order.Stock,
			block.OrderType, block.Quantity, block.Stock)
	}
	if order.Signature == "" || order.PublicKey == "" || order.SignedMessage == "" {
		return "order has no signature material"
	}
	if block.Signature != order.Signature || block.PublicKey != order.PublicKey ||
		req.Signature != order.Signature || req.PublicKey != order.PublicKey ||
		req.SignedMessage != order.SignedMessage {
		return "signature, public key or signed message differs from the order in Django"
	}

	// check 3: ECDSA signature valid, and the signed trade fields match the block
	if err := verifyECDSA(order.PublicKey, order.SignedMessage, order.Signature); err != nil {
		return fmt.Sprintf("ECDSA verification failed: %v", err)
	}
	var signed struct {
		OrderType string `json:"order_type"`
		Quantity  string `json:"quantity"`
		Stock     string `json:"stock"`
	}
	if err := json.Unmarshal([]byte(order.SignedMessage), &signed); err != nil {
		return fmt.Sprintf("unparseable signed message: %v", err)
	}
	if signed.OrderType != block.OrderType || signed.Quantity != block.Quantity || signed.Stock != block.Stock {
		return fmt.Sprintf("signed order does not match block: signed %s %s %s, block %s %s %s",
			signed.OrderType, signed.Quantity, signed.Stock,
			block.OrderType, block.Quantity, block.Stock)
	}

	// check 4: query the Oracle independently
	myOracle, err := s.price(block.Stock)
	if err != nil {
		return fmt.Sprintf("oracle unreachable: %v", err)
	}

	// check 5: the Leader's price quote is fresh (< 60 seconds)
	oracleTime, err := time.Parse(time.RFC3339Nano, req.OracleTimestamp)
	if err != nil {
		oracleTime, err = time.Parse(time.RFC3339, req.OracleTimestamp)
	}
	if err != nil {
		return fmt.Sprintf("unparseable price timestamp %q", req.OracleTimestamp)
	}
	if age := time.Since(oracleTime); age > 60*time.Second || age < -60*time.Second {
		return fmt.Sprintf("stale price: age=%v", age.Round(time.Second))
	}

	// check 6: the COMMITTED price must be within 1% of my own oracle.
	// We validate block.Price — the value that will be written to the chain —
	// not req.OraclePrice, which a dishonest Leader could set independently.
	committedPrice, err1 := strconv.ParseFloat(block.Price, 64)
	myPrice, err2 := strconv.ParseFloat(myOracle.ExecutionPrice, 64)
	if err1 != nil || err2 != nil || myPrice <= 0 || committedPrice <= 0 ||
		math.IsNaN(committedPrice) || math.IsInf(committedPrice, 0) {
		return fmt.Sprintf("unusable price (block=%q, mine=%q)", block.Price, myOracle.ExecutionPrice)
	}
	if divergence := math.Abs(committedPrice-myPrice) / myPrice * 100; divergence > 1.0 {
		return fmt.Sprintf("price divergence %.2f%% > 1%% (block=%.2f, mine=%.2f)",
			divergence, committedPrice, myPrice)
	}
	return ""
}

// price looks up the live price through the configured Oracle.
func (s *ValidatorServer) price(ticker string) (*OracleData, error) {
	if s.priceFn != nil {
		return s.priceFn(ticker)
	}
	return FetchPrice(s.cfg.OracleURL, ticker)
}

// Commit receives an approved block and appends it to the local chain.
// It is also the channel the Leader uses to replay blocks a lagging node missed,
// so it must be idempotent and must never append a block blindly: the block
// has to extend this node's chain AND be certified by Django — its order must
// be CONFIRMED under exactly this block hash, which Django records only after
// verifying a quorum of signed votes. Holding the cluster secret is therefore
// not enough to inject a block.
func (s *ValidatorServer) Commit(ctx context.Context, req *pb.CommitRequest) (*pb.CommitResponse, error) {
	if req.GetBlock() == nil {
		return nil, status.Error(codes.InvalidArgument, "missing block")
	}
	block := protoToBlock(req.Block)
	respond := func(state string) *pb.CommitResponse {
		return &pb.CommitResponse{Status: state, Index: int32(block.Index), ChainLength: int32(s.chain.Length())}
	}

	// already held — a replayed commit. Acknowledge it only if it is the very
	// same block; a different block at that index is a fork, not a duplicate.
	if existing, ok := s.chain.BlockAt(block.Index); ok {
		if existing.Hash == block.Hash {
			fmt.Printf("[%s] Block #%d already present | chain length: %d\n",
				s.cfg.NodeName, block.Index, s.chain.Length())
			return respond("already_present"), nil
		}
		fmt.Printf("[%s] Commit REJECTED: block #%d conflicts with the block we hold\n",
			s.cfg.NodeName, block.Index)
		return respond(fmt.Sprintf("rejected: conflicts with local block #%d", block.Index)), nil
	}

	// the block must connect to our own chain — verify before trusting the Leader
	if err := s.chain.ValidateBlock(block); err != nil {
		fmt.Printf("[%s] Commit REJECTED for block #%d: %v\n", s.cfg.NodeName, block.Index, err)
		return respond(fmt.Sprintf("rejected: %v", err)), nil
	}

	if err := s.certified(block); err != nil {
		fmt.Printf("[%s] Commit REJECTED for block #%d: %v\n", s.cfg.NodeName, block.Index, err)
		return respond(fmt.Sprintf("rejected: %v", err)), nil
	}

	if err := s.chain.Append(block); err != nil {
		fmt.Printf("[%s] Commit FAILED for block #%d: %v\n", s.cfg.NodeName, block.Index, err)
		return respond(fmt.Sprintf("rejected: %v", err)), nil
	}
	fmt.Printf("[%s] Block #%d committed | chain length: %d\n",
		s.cfg.NodeName, block.Index, s.chain.Length())
	return respond("committed"), nil
}

// certified reports whether Django settled the block's order under this block.
func (s *ValidatorServer) certified(b Block) error {
	order, err := s.django.Order(b.OrderID)
	if err != nil {
		return fmt.Errorf("cannot confirm block with Django: %v", err)
	}
	if order.Status != "CONFIRMED" || order.BlockHash != b.Hash {
		return fmt.Errorf("block is not certified: order #%d is %s under block %s",
			order.ID, order.Status, short(order.BlockHash))
	}
	return nil
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
