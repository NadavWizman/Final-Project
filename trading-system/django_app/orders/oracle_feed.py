"""Thin HTTP client for the Python oracle service.

The oracle exposes a REST endpoint at /price/<symbol> returning the latest
quote + timestamp. Django calls this during order submission to stamp the
OrderTx with a fresh price.
"""
from __future__ import annotations

import os
from dataclasses import dataclass

import urllib.request
import urllib.error
import json


ORACLE_URL = os.environ.get("ORACLE_URL", "http://oracle:7000")


@dataclass
class Quote:
    symbol: str
    price: str
    timestamp: int


def fetch_quote(symbol: str) -> Quote:
    url = f"{ORACLE_URL}/price/{symbol}"
    try:
        with urllib.request.urlopen(url, timeout=5) as resp:  # nosec B310
            payload = json.loads(resp.read().decode())
    except urllib.error.URLError as e:
        raise RuntimeError(f"oracle unreachable: {e}") from e
    return Quote(symbol=payload["symbol"], price=payload["price"], timestamp=payload["timestamp"])
