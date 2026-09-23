"""Oracle gRPC service — live stock prices from Yahoo Finance.

    ORACLE_LISTEN   address to bind (default 127.0.0.1:8001; use 0.0.0.0:8001
                    only when nodes run on other hosts)
    ORACLE_CACHE_S  seconds a quote is reused (default 3)
"""
import math
import os
import sys
import threading
import time
from concurrent import futures
from datetime import datetime, timezone

import grpc
import yfinance as yf

sys.path.insert(0, os.path.dirname(__file__))
import oracle_pb2
import oracle_pb2_grpc

LISTEN   = os.getenv('ORACLE_LISTEN', '127.0.0.1:8001')
CACHE_S  = float(os.getenv('ORACLE_CACHE_S', '3'))


def yahoo_symbol(ticker):
    """Yahoo writes class shares with a dash: BRK.B -> BRK-B."""
    return ticker.upper().replace('.', '-')


class OracleServicer(oracle_pb2_grpc.OracleServiceServicer):
    """Serves the latest price. A short cache means the Leader, both Validators
    and Django — which all ask for the same ticker within a second or two —
    see the same quote instead of hammering Yahoo and disagreeing by a tick."""

    def __init__(self):
        self._cache = {}              # ticker -> (fetched_at, price, market_time)
        self._lock = threading.Lock()

    def _quote(self, ticker):
        now = time.monotonic()
        with self._lock:
            hit = self._cache.get(ticker)
            if hit and now - hit[0] < CACHE_S:
                return hit[1], hit[2]
        # 5 days of history so the last close is available before the open,
        # on weekends and on holidays, when a 1-day window is empty.
        data = yf.Ticker(yahoo_symbol(ticker)).history(period='5d')
        # Yahoo can append an incomplete bar whose Close is NaN (e.g. before the
        # open); use the most recent bar that actually has a price.
        closes = data['Close'].dropna() if 'Close' in data else data
        closes = closes[closes > 0] if len(closes) else closes
        if len(closes) == 0:
            return None, None
        price = round(float(closes.iloc[-1]), 2)
        market_time = closes.index[-1].to_pydatetime().astimezone(timezone.utc).isoformat()
        with self._lock:
            self._cache[ticker] = (now, price, market_time)
        return price, market_time

    def GetPrice(self, request, context):
        ticker = request.ticker.upper()
        try:
            price, market_time = self._quote(ticker)
            if price is None or not math.isfinite(price) or price <= 0:
                context.set_code(grpc.StatusCode.NOT_FOUND)
                context.set_details(f"No data found for {ticker}")
                return oracle_pb2.PriceResponse()

            return oracle_pb2.PriceResponse(
                ticker=ticker,
                execution_price=str(price),
                timestamp=datetime.now(timezone.utc).isoformat(),
                market_time=market_time,
            )

        except Exception as e:
            context.set_code(grpc.StatusCode.INTERNAL)
            context.set_details(str(e))
            return oracle_pb2.PriceResponse()


def serve():
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=10))
    oracle_pb2_grpc.add_OracleServiceServicer_to_server(OracleServicer(), server)
    server.add_insecure_port(LISTEN)
    server.start()
    print(f"Oracle gRPC service running on {LISTEN}")
    server.wait_for_termination()


if __name__ == '__main__':
    serve()
