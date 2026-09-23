package main

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	pb "nodes/consensus"
)

func TestProposeAndCommitRejectMissingBlock(t *testing.T) {
	v := newTestValidator(t, "node2")
	if _, err := v.Propose(context.Background(), &pb.ProposeRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("Propose(nil block) error = %v, want InvalidArgument", err)
	}
	if _, err := v.Commit(context.Background(), &pb.CommitRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("Commit(nil block) error = %v, want InvalidArgument", err)
	}
}

func TestValidateBlockSurvivesShortHashes(t *testing.T) {
	c := newTestChain(t, "node2")
	for _, b := range []Block{
		{Index: 1, PrevHash: "", Hash: ""},
		{Index: 1, PrevHash: c.Head().Hash, Hash: "ab"},
	} {
		if err := c.ValidateBlock(b); err == nil {
			t.Fatalf("ValidateBlock accepted malformed block %+v", b)
		}
	}
}

func TestRecoveryInterceptorConvertsPanic(t *testing.T) {
	info := &grpc.UnaryServerInfo{FullMethod: "/test/Panic"}
	_, err := recoveryInterceptor(context.Background(), nil, info,
		func(ctx context.Context, req any) (any, error) { panic("boom") })
	if status.Code(err) != codes.Internal {
		t.Fatalf("error = %v, want Internal", err)
	}
}
