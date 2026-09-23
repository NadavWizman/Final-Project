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

# Go: module "nodes", packages nodes/consensus and nodes/oracle
for proto in consensus oracle; do
    python3 -m grpc_tools.protoc -I . \
        --go_out="$ROOT/nodes/$proto" --go_opt=paths=source_relative \
        --go-grpc_out="$ROOT/nodes/$proto" --go-grpc_opt=paths=source_relative \
        "$proto.proto"
done

# Python: the Oracle service and its Django client
python3 -m grpc_tools.protoc -I . \
    --python_out="$ROOT/oracle_service" --grpc_python_out="$ROOT/oracle_service" \
    oracle.proto

echo "gRPC code regenerated."
