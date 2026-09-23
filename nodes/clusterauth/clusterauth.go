// Package clusterauth authenticates node-to-node gRPC calls with a shared
// cluster secret without ever sending the secret itself.
//
// Each call carries "auth-token: <unix-seconds>:<hex HMAC-SHA256(secret, method|unix-seconds)>".
// The receiver recomputes the MAC for the method being called and accepts it
// only within MaxSkew of its own clock, so a captured token is useless for
// another RPC and expires quickly.
package clusterauth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// MetadataKey is the gRPC metadata key carrying the token.
const MetadataKey = "auth-token"

// MaxSkew is how far a token's timestamp may differ from the receiver's clock.
const MaxSkew = 30 * time.Second

// Token returns the credential for calling method at time now.
func Token(secret, method string, now time.Time) string {
	ts := strconv.FormatInt(now.Unix(), 10)
	return ts + ":" + mac(secret, method, ts)
}

// Verify checks a token presented for method.
func Verify(secret, method, token string, now time.Time) error {
	ts, sig, ok := strings.Cut(token, ":")
	if !ok {
		return errors.New("malformed token")
	}
	unix, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return errors.New("malformed token timestamp")
	}
	if skew := now.Sub(time.Unix(unix, 0)); skew > MaxSkew || skew < -MaxSkew {
		return fmt.Errorf("token expired or clock skew too large (%s)", skew.Round(time.Second))
	}
	want := mac(secret, method, ts)
	if !hmac.Equal([]byte(sig), []byte(want)) {
		return errors.New("invalid token")
	}
	return nil
}

func mac(secret, method, ts string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(method + "|" + ts))
	return hex.EncodeToString(m.Sum(nil))
}

// ClientInterceptor attaches a fresh token to every outgoing unary call.
func ClientInterceptor(secret string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		ctx = metadata.AppendToOutgoingContext(ctx, MetadataKey, Token(secret, method, time.Now()))
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// ServerInterceptor rejects any call that does not carry a valid token.
func ServerInterceptor(secret string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler) (any, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		tokens := md.Get(MetadataKey)
		if len(tokens) == 0 {
			return nil, status.Error(codes.Unauthenticated, "missing cluster credential")
		}
		if err := Verify(secret, info.FullMethod, tokens[0], time.Now()); err != nil {
			return nil, status.Error(codes.Unauthenticated, "invalid cluster credential: "+err.Error())
		}
		return handler(ctx, req)
	}
}
