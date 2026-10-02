"""Oracle signer — one price source, every quote signed with that source's key.

The network uses three independent sources (Yahoo, Nasdaq, CNBC by default).
Each runs its own signer process with its own Ed25519 key; the public keys are
fixed in the genesis file. The block proposer collects one signed quote per
source and the validators take the exact median, so one wrong or lying source
cannot move the execution price, and a quote changed in transit fails its
signature.

    python3 oracle_server.py --source yahoo  --key ../testnet/oracles/yahoo.key  --port 8001
    python3 oracle_server.py --source nasdaq --key ../testnet/oracles/nasdaq.key --port 8002
    python3 oracle_server.py --source cnbc   --key ../testnet/oracles/cnbc.key   --port 8003

    # a dishonest source (demo): signs a fixed price under a registered name
    python3 oracle_server.py --source fixed --price 1.00 --name cnbc --key ../testnet/oracles/cnbc.key --port 8003

Signed bytes: "tradedesk-quote|<name>|<ticker>|<price_cents>|<unix_seconds>".
"""
import argparse
import json
import math
import os
import sys
import threading
import time
import urllib.parse
import urllib.request
from concurrent import futures
from datetime import datetime, timezone
from decimal import Decimal, ROUND_HALF_UP

import grpc
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

sys.path.insert(0, os.path.dirname(__file__))
import oracle_pb2
import oracle_pb2_grpc

CACHE_S = float(os.getenv('ORACLE_CACHE_S', '3'))
UA = {'User-Agent': 'Mozilla/5.0 (TradeDesk oracle)', 'Accept': 'application/json'}


def _get_json(url, timeout=6):
    req = urllib.request.Request(url, headers=UA)
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return json.loads(r.read().decode())


def _money(text):
    """'$330.32' / '330.32' → Decimal, or None."""
    if text is None:
        return None
    try:
        v = Decimal(str(text).replace('$', '').replace(',', '').strip())
    except Exception:
        return None
    return v if v.is_finite() and v > 0 else None


# ── sources ─────────────────────────────────────────────────────────
# Each returns (price: Decimal, market_time: str) or raises.

def yahoo(ticker):
    import yfinance as yf
    data = yf.Ticker(ticker.replace('.', '-')).history(period='5d')
    # Yahoo can append an incomplete bar whose Close is NaN; use the last real one
    closes = data['Close'].dropna() if 'Close' in data else data
    closes = closes[closes > 0] if len(closes) else closes
    if len(closes) == 0:
        raise LookupError('no data')
    price = Decimal(str(float(closes.iloc[-1])))
    return price, closes.index[-1].to_pydatetime().astimezone(timezone.utc).isoformat()


def nasdaq(ticker):
    url = f'https://api.nasdaq.com/api/quote/{urllib.parse.quote(ticker)}/info?assetclass=stocks'
    d = _get_json(url)
    price = _money(((d.get('data') or {}).get('primaryData') or {}).get('lastSalePrice'))
    if price is None:
        raise LookupError('no data')
    return price, (d['data']['primaryData'].get('lastTradeTimestamp') or '')


def cnbc(ticker):
    url = ('https://quote.cnbc.com/quote-html-webservice/restQuote/symbolType/symbol?symbols='
           + urllib.parse.quote(ticker) + '&requestMethod=itv&noform=1&partnerId=2&fund=1&exthrs=1&output=json')
    q = _get_json(url)['FormattedQuoteResult']['FormattedQuote'][0]
    price = _money(q.get('last'))
    if price is None:
        raise LookupError('no data')
    return price, q.get('last_time', '')


def fixed(price_text):
    price = _money(price_text)
    if price is None:
        raise SystemExit('--price must be a positive number')
    return lambda ticker: (price, datetime.now(timezone.utc).isoformat())


SOURCES = {'yahoo': yahoo, 'nasdaq': nasdaq, 'cnbc': cnbc}


class OracleServicer(oracle_pb2_grpc.OracleServiceServicer):

    def __init__(self, name, fetch, key):
        self.name, self.fetch, self.key = name, fetch, key
        self._cache = {}              # ticker -> (fetched_at, price, market_time)
        self._lock = threading.Lock()

    def _quote(self, ticker):
        now = time.monotonic()
        with self._lock:
            hit = self._cache.get(ticker)
            if hit and now - hit[0] < CACHE_S:
                return hit[1], hit[2]
        price, market_time = self.fetch(ticker)
        with self._lock:
            self._cache[ticker] = (now, price, market_time)
        return price, market_time

    def _price_or_abort(self, request, context):
        ticker = request.ticker.upper()
        try:
            return ticker, self._quote(ticker)
        except Exception as e:
            context.abort(grpc.StatusCode.NOT_FOUND, f'{self.name}: no price for {ticker} ({e})')

    def SignQuote(self, request, context):
        ticker, (price, _) = self._price_or_abort(request, context)
        cents = int((price * 100).quantize(Decimal('1'), rounding=ROUND_HALF_UP))
        ts = int(time.time())
        msg = f'tradedesk-quote|{self.name}|{ticker}|{cents}|{ts}'.encode()
        return oracle_pb2.SignedQuote(source=self.name, ticker=ticker, price_cents=cents,
                                      timestamp=ts, signature=self.key.sign(msg))

    def GetPrice(self, request, context):
        ticker, (price, market_time) = self._price_or_abort(request, context)
        return oracle_pb2.PriceResponse(
            ticker=ticker, execution_price=str(price.quantize(Decimal('0.01'))),
            timestamp=datetime.now(timezone.utc).isoformat(), market_time=market_time)


def load_key(path):
    with open(path) as f:
        seed = bytes.fromhex(f.read().strip())
    return Ed25519PrivateKey.from_private_bytes(seed)


def main(argv=None):
    p = argparse.ArgumentParser(description='TradeDesk oracle signer (one price source).')
    p.add_argument('--source', required=True, choices=sorted(SOURCES) + ['fixed'])
    p.add_argument('--key', required=True, help='Ed25519 seed file written by tradedesk-node init')
    p.add_argument('--port', type=int, required=True)
    p.add_argument('--name', help='name registered in the genesis file (default: the source)')
    p.add_argument('--price', help='price for --source fixed (demo of a lying source)')
    p.add_argument('--host', default=os.getenv('ORACLE_HOST', '127.0.0.1'))
    a = p.parse_args(argv)

    fetch = fixed(a.price) if a.source == 'fixed' else SOURCES[a.source]
    name = a.name or a.source
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=16))
    oracle_pb2_grpc.add_OracleServiceServicer_to_server(OracleServicer(name, fetch, load_key(a.key)), server)
    server.add_insecure_port(f'{a.host}:{a.port}')
    server.start()
    label = f'fixed ${a.price} (dishonest demo source)' if a.source == 'fixed' else a.source
    print(f'Oracle signer "{name}" ({label}) on {a.host}:{a.port}', flush=True)
    server.wait_for_termination()


if __name__ == '__main__':
    main()
