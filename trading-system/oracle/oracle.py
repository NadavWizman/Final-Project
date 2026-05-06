"""S&P 500 price oracle.

Two jobs:

  1. Poll Yahoo Finance (yfinance) every POLL_INTERVAL seconds for the
     configured symbol list and cache the latest close.
  2. Expose the cache via:
       - REST: GET /price/<symbol> -> JSON  (called by Django)
       - gRPC: OracleSink.PushPrices to every node (called by us on poll)

Running standalone:
    python oracle.py
"""
from __future__ import annotations

import json
import logging
import os
import sys
import threading
import time
from concurrent import futures
from http.server import BaseHTTPRequestHandler, HTTPServer

import grpc
import yfinance as yf

# generated stubs live next to this file at runtime
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import trading_pb2
import trading_pb2_grpc


SYMBOLS = os.environ.get(
    "ORACLE_SYMBOLS",
    "AAPL,MSFT,GOOGL,AMZN,META,NVDA,TSLA,JPM,V,JNJ,WMT,PG,UNH,HD,MA,XOM,DIS,KO,PEP",
).split(",")

NODE_ADDRS = os.environ.get(
    "NODE_ADDRS", "node-1:9001,node-2:9002,node-3:9003"
).split(",")

POLL_INTERVAL = int(os.environ.get("POLL_INTERVAL_SECS", "15"))
REST_PORT = int(os.environ.get("REST_PORT", "7000"))

logging.basicConfig(level=logging.INFO, format="%(asctime)s [oracle] %(message)s")
log = logging.getLogger("oracle")


class PriceCache:
    def __init__(self) -> None:
        self._prices: dict[str, tuple[str, int]] = {}
        self._lock = threading.Lock()

    def put(self, symbol: str, price: str, ts: int) -> None:
        with self._lock:
            self._prices[symbol] = (price, ts)

    def get(self, symbol: str) -> tuple[str, int] | None:
        with self._lock:
            return self._prices.get(symbol)

    def snapshot(self) -> dict[str, tuple[str, int]]:
        with self._lock:
            return dict(self._prices)


CACHE = PriceCache()


def poll_once() -> None:
    """Fetch latest prices and push them to every node."""
    try:
        data = yf.download(
            tickers=" ".join(SYMBOLS),
            period="1d",
            interval="1m",
            progress=False,
            threads=True,
        )
    except Exception as e:
        log.warning("yfinance download failed: %s", e)
        return

    now = int(time.time())
    updates = []
    for symbol in SYMBOLS:
        try:
            # Multi-ticker frames have a MultiIndex; single ticker doesn't.
            if ("Close", symbol) in data.columns:
                series = data["Close"][symbol].dropna()
            elif "Close" in data.columns:
                series = data["Close"].dropna()
            else:
                continue
            if series.empty:
                continue
            price = f"{float(series.iloc[-1]):.4f}"
        except Exception as e:
            log.warning("bad data for %s: %s", symbol, e)
            continue
        CACHE.put(symbol, price, now)
        updates.append(trading_pb2.PriceUpdate(symbol=symbol, price=price, timestamp=now))

    if not updates:
        return

    req = trading_pb2.PushPricesRequest(prices=updates)
    for addr in NODE_ADDRS:
        try:
            with grpc.insecure_channel(addr) as ch:
                stub = trading_pb2_grpc.OracleSinkStub(ch)
                stub.PushPrices(req, timeout=3.0)
            log.info("pushed %d prices to %s", len(updates), addr)
        except grpc.RpcError as e:
            log.warning("push to %s failed: %s", addr, e.code())


def poll_loop() -> None:
    while True:
        poll_once()
        time.sleep(POLL_INTERVAL)


# --- REST for Django ---------------------------------------------------------

class RestHandler(BaseHTTPRequestHandler):
    def log_message(self, fmt: str, *args) -> None:  # quieter default log
        return

    def do_GET(self) -> None:
        if self.path.startswith("/price/"):
            symbol = self.path[len("/price/"):].upper()
            entry = CACHE.get(symbol)
            if entry is None:
                self.send_response(404)
                self.end_headers()
                self.wfile.write(b'{"error":"no price"}')
                return
            price, ts = entry
            body = json.dumps({"symbol": symbol, "price": price, "timestamp": ts}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if self.path == "/health":
            self.send_response(200)
            self.end_headers()
            self.wfile.write(b"ok")
            return
        self.send_response(404)
        self.end_headers()


def serve_rest() -> None:
    httpd = HTTPServer(("", REST_PORT), RestHandler)
    log.info("REST serving on :%d", REST_PORT)
    httpd.serve_forever()


def main() -> None:
    threading.Thread(target=poll_loop, daemon=True).start()
    serve_rest()


if __name__ == "__main__":
    main()
