# syntax=docker/dockerfile:1.6
FROM golang:1.22-alpine AS build
WORKDIR /src

# Install protoc so we can generate proto stubs inside the image. This keeps
# the host contract-free: just `docker compose build` and everything's wired.
RUN apk add --no-cache protobuf protobuf-dev git \
    && go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.34.2 \
    && go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.4.0

ENV PATH="/go/bin:${PATH}"

COPY proto/ /src/proto/
COPY node/  /src/

# Generate Go code from proto/trading.proto. With `paths=source_relative` and
# go_package=trading/proto;tradingpb, the generated files land at
# /src/proto/trading.pb.go and /src/proto/trading_grpc.pb.go, which our code
# imports as `tradingpb "trading/proto"`.
RUN protoc -I /src \
    --go_out=/src --go_opt=paths=source_relative \
    --go-grpc_out=/src --go-grpc_opt=paths=source_relative \
    /src/proto/trading.proto \
 && go mod tidy \
 && CGO_ENABLED=0 go build -o /out/node ./cmd/node

# --- runtime image ---
FROM alpine:3.20
RUN adduser -D -u 10001 app
WORKDIR /app
COPY --from=build /out/node /app/node
USER app
ENTRYPOINT ["/app/node"]
