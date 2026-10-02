#!/usr/bin/env bash
# Regenerates the gRPC code for the Go nodes and the Python services from the
# .proto files in this directory. Requirements:
#   pip3 install grpcio-tools            (bundles protoc)
#   go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
#   go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2
set -euo pipefail
cd "$(dirname "$0")"
ROOT="$(cd ../.. && pwd)"
export PATH="$PATH:$(go env GOPATH)/bin"

# Go: module "nodes", package nodes/oracle (the nodes fetch signed quotes)
python3 -m grpc_tools.protoc -I . \
    --go_out="$ROOT/nodes/oracle" --go_opt=paths=source_relative \
    --go-grpc_out="$ROOT/nodes/oracle" --go-grpc_opt=paths=source_relative \
    oracle.proto

# Python: the price signers (and the gateway's display-price client)
python3 -m grpc_tools.protoc -I . \
    --python_out="$ROOT/oracle_service" --grpc_python_out="$ROOT/oracle_service" \
    oracle.proto

echo "gRPC code regenerated."
