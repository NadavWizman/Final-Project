"""The Django gateway: a read-only window onto the chain.

Django no longer holds keys, balances or orders and moves no money. It
serves the web app, forwards transactions the browser has already signed,
and shows state it reads from the nodes. If it were compromised, the worst
it could do is show wrong numbers — it cannot sign for a user, and a
transaction it alters fails its signature at every node.
"""
import json
import re
from datetime import datetime, timezone

from rest_framework import status
from rest_framework.decorators import api_view, throttle_classes
from rest_framework.response import Response

from . import chain
from .catalog import SP500_STOCKS
from .throttles import TxThrottle

ADDRESS_RE = re.compile(r'[0-9a-f]{40}')
HASH_RE = re.compile(r'[0-9A-Fa-f]{64}')
MAX_TX_BYTES = 4096


def _unavailable():
    return Response({'error': 'The chain is unavailable right now. Please try again.'},
                    status=status.HTTP_503_SERVICE_UNAVAILABLE)


def _money(cents):
    """Integer cents from the chain → decimal string for the UI."""
    if cents is None:
        return None
    sign = '-' if cents < 0 else ''
    cents = abs(int(cents))
    return f'{sign}{cents // 100}.{cents % 100:02d}'


def _qty(units):
    """Quantity in 1/10000 share → decimal string."""
    units = int(units or 0)
    return f'{units // 10000}.{units % 10000:04d}'


def _iso(ts):
    return datetime.fromtimestamp(int(ts), timezone.utc).isoformat() if ts else None


def _account(address):
    """The account view from the chain, or a Response for an error."""
    if not ADDRESS_RE.fullmatch(address or ''):
        return None, Response({'error': 'address must be 40 hex characters'}, status=400)
    try:
        view = chain.query('/account', address.encode())
    except chain.ChainUnavailable:
        return None, _unavailable()
    if view is None:
        return None, Response({'error': 'account not found'}, status=404)
    return view, None


# ── chain status and transactions ────────────────────────────────────

@api_view(['GET'])
def status_view(_request):
    try:
        s = chain.query('/status')
    except chain.ChainUnavailable:
        return _unavailable()
    return Response(s)


@api_view(['POST'])
@throttle_classes([TxThrottle])
def tx_view(request):
    """Forward a transaction signed in the browser. The gateway does not and
    cannot change it: the nodes verify the signature over the exact bytes."""
    env = request.data
    if not isinstance(env, dict) or not isinstance(env.get('msg'), str) or not isinstance(env.get('sig'), str):
        return Response({'error': 'expected {msg, sig[, pubkey]}'}, status=400)
    allowed = {'msg', 'sig', 'pubkey'}
    tx = json.dumps({k: env[k] for k in ('msg', 'sig', 'pubkey') if k in env and env[k]},
                    separators=(',', ':')).encode()
    if set(env) - allowed or len(tx) > MAX_TX_BYTES:
        return Response({'error': 'malformed transaction'}, status=400)
    try:
        tx_hash, code, log = chain.broadcast(tx)
    except chain.ChainUnavailable:
        return _unavailable()
    except LookupError as e:
        return Response({'error': str(e)[:200]}, status=400)
    if code != 0:
        return Response({'error': log or 'rejected by the node', 'hash': tx_hash}, status=400)
    return Response({'hash': tx_hash, 'status': 'pending'}, status=202)


@api_view(['GET'])
def tx_status_view(_request, tx_hash):
    if not HASH_RE.fullmatch(tx_hash):
        return Response({'error': 'bad hash'}, status=400)
    try:
        r = chain.tx_result(tx_hash)
    except chain.ChainUnavailable:
        return _unavailable()
    if r is None:
        return Response({'status': 'pending'})
    return Response({'status': 'committed', **r})


@api_view(['GET'])
def username_view(_request, name):
    try:
        r = chain.query('/username', name.encode())
    except chain.ChainUnavailable:
        return _unavailable()
    if r is None:
        return Response({'error': 'not registered'}, status=404)
    return Response(r)


# ── views of one account, in the shapes the web app uses ─────────────

@api_view(['GET'])
def portfolio_view(request):
    view, err = _account(request.query_params.get('address'))
    if err:
        return err
    a = view['account']
    holdings = [
        {'id': t, 'ticker': t, 'name': SP500_STOCKS.get(t, t), 'quantity': _qty(q)}
        for t, q in sorted((a.get('holdings') or {}).items())
    ]
    return Response({
        'address': a['address'], 'username': a['username'], 'nonce': a['nonce'],
        'usd_balance': _money(a['cash']), 'holdings': holdings, 'total_positions': len(holdings),
    })


@api_view(['GET'])
def orders_view(request):
    view, err = _account(request.query_params.get('address'))
    if err:
        return err
    try:
        limit = min(max(int(request.query_params.get('limit', 200)), 1), 500)
    except ValueError:
        return Response({'error': 'limit must be a number'}, status=400)
    rows = []
    for r in reversed((view['account'].get('history') or [])[-limit:]):
        rows.append({
            'id': r['id'], 'stock': r.get('ticker', ''), 'order_type': r.get('side') or 'BUY',
            'trade_type': r['kind'], 'quantity': _qty(r.get('qty')), 'status': r['status'],
            'execution_price': _money(r['price']) if r.get('price') else None,
            'amount': _money(r.get('amount') or 0), 'reject_reason': r.get('reason', ''),
            'created_at': _iso(r.get('time')), 'height': r.get('height'),
        })
    return Response(rows)


@api_view(['GET'])
def cfd_view(request):
    view, err = _account(request.query_params.get('address'))
    if err:
        return err
    liq = view.get('liquidation') or {}
    rows = []
    for c in sorted(view['account'].get('cfds') or [], key=lambda c: -c['id']):
        rows.append({
            'id': c['id'], 'stock': c['ticker'], 'direction': 'LONG' if c.get('long') else 'SHORT',
            'quantity': _qty(c['qty']), 'entry_price': _money(c['entry']), 'leverage': c['leverage'],
            'margin_used': _money(c['margin']), 'is_open': c.get('open', False),
            'opened_at': _iso(c.get('opened_at')), 'closed_at': _iso(c.get('closed_at')),
            'close_price': _money(c['close_px']) if c.get('close_px') else None,
            'pnl': _money(c.get('pnl', 0)) if not c.get('open') else None,
            'liquidation_price': _money(liq.get(str(c['id']))) if c.get('open') else None,
        })
    return Response({'cfd_positions': rows})


@api_view(['GET'])
def options_view(request):
    view, err = _account(request.query_params.get('address'))
    if err:
        return err
    rows = []
    for o in sorted(view['account'].get('options') or [], key=lambda o: -o['id']):
        rows.append({
            'id': o['id'], 'stock': o['ticker'], 'contract_type': 'CALL' if o.get('call') else 'PUT',
            'strike': _money(o['strike']), 'expiry': o['expiry'], 'contracts': o['contracts'],
            'premium_paid': _money(o['premium']), 'total_cost': _money(o['premium'] * 100 * o['contracts']),
            'status': o['status'], 'opened_at': _iso(o.get('opened_at')), 'closed_at': _iso(o.get('closed_at')),
            'close_premium': _money(o['close_px']) if o.get('close_px') else None,
            'pnl': _money(o.get('pnl', 0)) if o['status'] != 'OPEN' else None,
        })
    return Response(rows)


@api_view(['GET'])
def sltp_view(request):
    view, err = _account(request.query_params.get('address'))
    if err:
        return err
    ticker, cfd = request.query_params.get('position'), request.query_params.get('cfd')
    rows = []
    for lv in sorted(view['account'].get('levels') or [], key=lambda l: (l['kind'], l['price'])):
        if cfd and str(lv.get('cfd', 0)) != cfd:
            continue
        if ticker and (lv.get('cfd') or lv.get('ticker') != ticker):
            continue
        rows.append({
            'id': lv['id'], 'level_type': lv['kind'], 'price': _money(lv['price']),
            'quantity': _qty(lv['qty']), 'triggered': lv.get('triggered', False),
            'triggered_at': _iso(lv.get('at')),
        })
    return Response(rows)


# ── prices ───────────────────────────────────────────────────────────

@api_view(['GET'])
def price_view(_request, ticker):
    """The last execution price the chain verified for a ticker (the median of
    the signed oracle quotes in its latest priced block)."""
    ticker = ticker.upper()
    if ticker not in SP500_STOCKS:
        return Response({'error': 'not a listed ticker'}, status=404)
    try:
        p = chain.query('/prices')
    except chain.ChainUnavailable:
        return _unavailable()
    cents = (p or {}).get('prices', {}).get(ticker)
    if cents is None:
        # no block has priced it yet: fall back to an unsigned display quote
        from .market import display_price
        return display_price(ticker)
    return Response({'ticker': ticker, 'execution_price': _money(cents),
                     'timestamp': _iso(p.get('time')), 'verified': True})


@api_view(['GET'])
def option_quote_view(request):
    q = {k: request.query_params.get(k, '') for k in ('ticker', 'type', 'strike', 'expiry')}
    try:
        r = chain.query('/option_quote', json.dumps(
            {'Ticker': q['ticker'].upper(), 'Type': q['type'].upper(), 'Strike': q['strike'], 'Expiry': q['expiry']}).encode())
    except chain.ChainUnavailable:
        return _unavailable()
    if r is None:
        return Response({'error': 'no verified price yet for this ticker, or invalid strike/expiry'}, status=404)
    return Response({'premium': _money(r['premium']), 'underlying': _money(r['underlying'])})
