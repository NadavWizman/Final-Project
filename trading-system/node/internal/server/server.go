// Package server wires the consensus engine to the three gRPC services
// defined in proto/trading.proto.
package server

import (
	"context"
	"log"

	"github.com/shopspring/decimal"

	"trading/internal/consensus"
	"trading/internal/oracleclient"
	"trading/internal/state"
	tradingpb "trading/proto"
)

// Server implements TradingService, ConsensusService, and OracleSink.
type Server struct {
	tradingpb.UnimplementedTradingServiceServer
	tradingpb.UnimplementedConsensusServiceServer
	tradingpb.UnimplementedOracleSinkServer

	Engine *consensus.Engine
	State  *state.State
	Oracle *oracleclient.Cache
}

// ---------------------------------------------------------------------------
// TradingService — called by Django (or any client) on the Leader only.
// ---------------------------------------------------------------------------

func (s *Server) SubmitOrder(ctx context.Context, req *tradingpb.SubmitOrderRequest) (*tradingpb.SubmitOrderResponse, error) {
	block, err := s.Engine.RunLeaderRound(ctx, req.SignedTx)
	if err != nil {
		log.Printf("[rpc] SubmitOrder rejected: %v", err)
		return &tradingpb.SubmitOrderResponse{
			OrderId: req.SignedTx.GetTx().GetOrderId(),
			Status:  tradingpb.OrderStatus_REJECTED,
			Reason:  err.Error(),
		}, nil
	}
	return &tradingpb.SubmitOrderResponse{
		OrderId:   req.SignedTx.Tx.OrderId,
		Status:    tradingpb.OrderStatus_CONFIRMED,
		BlockIdx:  block.Index,
		BlockHash: block.Hash,
	}, nil
}

func (s *Server) GetAccount(ctx context.Context, req *tradingpb.GetAccountRequest) (*tradingpb.GetAccountResponse, error) {
	acc, ok := s.State.Snapshot(req.UserId)
	if !ok {
		return &tradingpb.GetAccountResponse{UserId: req.UserId}, nil
	}
	positions := make(map[string]string, len(acc.Positions))
	for sym, qty := range acc.Positions {
		positions[sym] = qty.String()
	}
	return &tradingpb.GetAccountResponse{
		UserId:    acc.UserID,
		Cash:      acc.Cash.String(),
		Positions: positions,
		LastNonce: acc.LastNonce,
	}, nil
}

// ---------------------------------------------------------------------------
// ConsensusService — internal, node-to-node.
// ---------------------------------------------------------------------------

func (s *Server) ProposeBlock(ctx context.Context, req *tradingpb.ProposeBlockRequest) (*tradingpb.ProposeBlockResponse, error) {
	return s.Engine.HandleProposal(req.Block)
}

func (s *Server) CommitBlock(ctx context.Context, req *tradingpb.CommitBlockRequest) (*tradingpb.CommitBlockResponse, error) {
	if err := s.Engine.HandleCommit(req.Block); err != nil {
		log.Printf("[rpc] CommitBlock failed: %v", err)
		return &tradingpb.CommitBlockResponse{Ok: false}, nil
	}
	return &tradingpb.CommitBlockResponse{Ok: true}, nil
}

// ---------------------------------------------------------------------------
// OracleSink — oracle pushes price updates here.
// ---------------------------------------------------------------------------

func (s *Server) PushPrices(ctx context.Context, req *tradingpb.PushPricesRequest) (*tradingpb.PushPricesResponse, error) {
	for _, p := range req.Prices {
		price, err := decimal.NewFromString(p.Price)
		if err != nil {
			log.Printf("[oracle] bad price for %s: %v", p.Symbol, err)
			continue
		}
		s.Oracle.Put(oracleclient.Quote{
			Symbol:    p.Symbol,
			Price:     price,
			Timestamp: p.Timestamp,
		})
	}
	return &tradingpb.PushPricesResponse{Ok: true}, nil
}
