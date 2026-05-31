import sys
import os
from concurrent import futures
from datetime import datetime, timezone

import grpc
import yfinance as yf

sys.path.insert(0, os.path.dirname(__file__))
import oracle_pb2
import oracle_pb2_grpc


class OracleServicer(oracle_pb2_grpc.OracleServiceServicer):

    def GetPrice(self, request, context):
        ticker = request.ticker
        try:
            stock = yf.Ticker(ticker)
            data = stock.history(period='1d')

            if data.empty:
                context.set_code(grpc.StatusCode.NOT_FOUND)
                context.set_details(f"No data found for {ticker}")
                return oracle_pb2.PriceResponse()

            price = round(float(data['Close'].iloc[-1]), 2)
            timestamp = datetime.now(timezone.utc).isoformat()

            return oracle_pb2.PriceResponse(
                ticker=ticker,
                execution_price=str(price),
                timestamp=timestamp,
            )

        except Exception as e:
            context.set_code(grpc.StatusCode.INTERNAL)
            context.set_details(str(e))
            return oracle_pb2.PriceResponse()


def serve():
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=10))
    oracle_pb2_grpc.add_OracleServiceServicer_to_server(OracleServicer(), server)
    server.add_insecure_port("0.0.0.0:8001")
    server.start()
    print("Oracle gRPC service running on port 8001")
    server.wait_for_termination()


if __name__ == '__main__':
    serve()
