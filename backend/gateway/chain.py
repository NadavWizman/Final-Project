"""Read-only client for the CometBFT nodes' RPC.

The gateway holds no state of its own: every balance, position and order it
shows comes from here, and every transaction it forwards was signed in the
user's browser — the gateway has no key to sign one, and changing one in
transit breaks its signature.
"""
import base64
import json
import urllib.error
import urllib.request

from django.conf import settings


class ChainUnavailable(Exception):
    pass


def _call(method, params):
    """JSON-RPC call to the first node that answers (failover in order)."""
    body = json.dumps({'jsonrpc': '2.0', 'id': 1, 'method': method, 'params': params}).encode()
    last = None
    for url in settings.NODE_RPC:
        req = urllib.request.Request(url, data=body, headers={'Content-Type': 'application/json'})
        try:
            with urllib.request.urlopen(req, timeout=settings.NODE_RPC_TIMEOUT) as r:
                out = json.loads(r.read().decode())
        except (urllib.error.URLError, OSError, ValueError) as e:
            last = e
            continue
        if 'error' in out:
            raise LookupError(out['error'].get('data') or out['error'].get('message'))
        return out['result']
    raise ChainUnavailable(f'no node answered ({last})')


def query(path, data=b''):
    """ABCI query; returns the decoded JSON value, or None when not found."""
    res = _call('abci_query', {'path': path, 'data': data.hex()})['response']
    if res.get('code', 0) != 0:
        return None
    return json.loads(base64.b64decode(res.get('value') or b'e30='))


def broadcast(tx_bytes):
    """Submit a signed transaction; returns (hash_hex, code, log) from CheckTx."""
    res = _call('broadcast_tx_sync', {'tx': base64.b64encode(tx_bytes).decode()})
    return res['hash'], res.get('code', 0), res.get('log', '')


def tx_result(hash_hex):
    """The committed result of a transaction, or None while it is pending."""
    try:
        res = _call('tx', {'hash': base64.b64encode(bytes.fromhex(hash_hex)).decode()})
    except LookupError:
        return None
    r = res['tx_result']
    return {'height': int(res['height']), 'code': r.get('code', 0), 'log': r.get('log', '')}
