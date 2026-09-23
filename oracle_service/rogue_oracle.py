"""
rogue_oracle.py — a deliberately dishonest Oracle, for demonstrating consensus.

This exists to show what happens when a Leader node is fed a manipulated price.
It speaks the same gRPC interface as oracle_server.py but reports a price of the
operator's choosing instead of the real market price.

Demo:
    Terminal A   python3 oracle_server.py                  # honest, port 8001
    Terminal B   python3 rogue_oracle.py                   # dishonest, port 8002
    Terminal C   ORACLE_URL=127.0.0.1:8002 NODE_NAME=node1 ... ./nodes_bin

The Leader now sees the fake price; the Validators still query the honest Oracle
on 8001. The prices disagree by more than 1%, so both Validators reject the
block and consensus fails 1/3 — the trade never executes.

    ROGUE_PRICE=1.00   fixed price to report   (default 1.00)
    ROGUE_PORT=8002    port to listen on       (default 8002)
"""
import os
import sys
from concurrent import futures
from datetime import datetime, timezone

import grpc

sys.path.insert(0, os.path.dirname(__file__))
import oracle_pb2
import oracle_pb2_grpc

FAKE_PRICE = os.getenv("ROGUE_PRICE", "1.00")
PORT = os.getenv("ROGUE_PORT", "8002")


class RogueOracleServicer(oracle_pb2_grpc.OracleServiceServicer):

    def GetPrice(self, request, context):
        # a fresh timestamp, so the Validators' staleness check passes and the
        # rejection is unambiguously about the price itself
        timestamp = datetime.now(timezone.utc).isoformat()
        print(f"  [ROGUE] {request.ticker} -> ${FAKE_PRICE}  (fabricated)")
        return oracle_pb2.PriceResponse(
            ticker=request.ticker,
            execution_price=FAKE_PRICE,
            timestamp=timestamp,
            market_time=timestamp,
        )


def serve():
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=10))
    oracle_pb2_grpc.add_OracleServiceServicer_to_server(RogueOracleServicer(), server)
    server.add_insecure_port(f"127.0.0.1:{PORT}")
    server.start()
    print("=" * 52)
    print("  ROGUE ORACLE — reports a fabricated price")
    print(f"  Listening on : {PORT}")
    print(f"  Fake price   : ${FAKE_PRICE}")
    print("=" * 52)
    server.wait_for_termination()


if __name__ == '__main__':
    serve()
