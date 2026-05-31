package main

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	pb "nodes/oracle"
)

// OracleData holds the price response — same struct as before, used throughout the codebase
type OracleData struct {
	Ticker         string
	ExecutionPrice string
	Timestamp      string
}

// FetchPrice calls the Oracle gRPC service and returns the current price for a ticker
func FetchPrice(oracleAddr, ticker string) (*OracleData, error) {
	conn, err := grpc.NewClient(oracleAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("oracle connection failed: %v", err)
	}
	defer conn.Close()

	client := pb.NewOracleServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := client.GetPrice(ctx, &pb.PriceRequest{Ticker: ticker})
	if err != nil {
		return nil, fmt.Errorf("oracle rpc failed: %v", err)
	}

	return &OracleData{
		Ticker:         resp.Ticker,
		ExecutionPrice: resp.ExecutionPrice,
		Timestamp:      resp.Timestamp,
	}, nil
}
