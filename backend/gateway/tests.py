"""Gateway tests. The chain is mocked: the gateway only forwards and displays."""
import json
from unittest.mock import patch

from django.test import SimpleTestCase, override_settings
from rest_framework.test import APIClient

from . import chain

ADDR = 'a' * 40
ACCOUNT = {
    'account': {
        'address': ADDR, 'username': 'alice', 'nonce': 3, 'cash': 933936,
        'holdings': {'AAPL': 20000},
        'cfds': [{'id': 7, 'ticker': 'NVDA', 'long': True, 'qty': 30000, 'entry': 23086, 'leverage': 10,
                  'margin': 6926, 'open': True, 'opened_at': 1790000000}],
        'options': [{'id': 9, 'ticker': 'AAPL', 'call': True, 'strike': 34000, 'expiry': '2026-10-16',
                     'contracts': 2, 'premium': 512, 'status': 'OPEN', 'opened_at': 1790000000}],
        'levels': [{'id': 4, 'kind': 'SL', 'ticker': 'AAPL', 'price': 10000, 'qty': 20000},
                   {'id': 5, 'kind': 'TP', 'ticker': 'NVDA', 'cfd': 7, 'price': 30000, 'qty': 30000}],
        'history': [
            {'id': 1, 'kind': 'DEPOSIT', 'status': 'CONFIRMED', 'amount': 1000000, 'time': 1790000000},
            {'id': 2, 'kind': 'STOCK', 'side': 'BUY', 'ticker': 'AAPL', 'qty': 20000, 'status': 'CONFIRMED',
             'price': 33032, 'amount': -66064, 'time': 1790000010},
        ],
    },
    'liquidation': {'7': 21239},
}


def fake_query(path, data=b''):
    if path == '/account':
        return ACCOUNT if data.decode() == ADDR else None
    if path == '/prices':
        return {'prices': {'AAPL': 33032}, 'time': 1790000010}
    if path == '/status':
        return {'chain_id': 'tradedesk-test', 'height': 12}
    return None


@override_settings(NODE_RPC=['http://node'])
@patch.object(chain, 'query', side_effect=fake_query)
class ReadOnlyViewsTests(SimpleTestCase):

    def setUp(self):
        self.c = APIClient()

    def test_portfolio_is_read_from_the_chain(self, _q):
        r = self.c.get(f'/api/portfolio/?address={ADDR}')
        self.assertEqual(r.status_code, 200)
        self.assertEqual((r.data['usd_balance'], r.data['nonce']), ('9339.36', 3))
        self.assertEqual(r.data['holdings'], [{'id': 'AAPL', 'ticker': 'AAPL', 'name': 'Apple Inc.', 'quantity': '2.0000'}])

    def test_orders_newest_first_with_signed_amounts(self, _q):
        r = self.c.get(f'/api/orders/?address={ADDR}')
        self.assertEqual([o['id'] for o in r.data], [2, 1])
        self.assertEqual((r.data[0]['execution_price'], r.data[0]['amount']), ('330.32', '-660.64'))

    def test_cfd_options_and_levels(self, _q):
        cfd = self.c.get(f'/api/cfd/?address={ADDR}').data['cfd_positions'][0]
        self.assertEqual((cfd['direction'], cfd['margin_used'], cfd['liquidation_price']), ('LONG', '69.26', '212.39'))
        opt = self.c.get(f'/api/options/?address={ADDR}').data[0]
        self.assertEqual((opt['contract_type'], opt['strike'], opt['total_cost']), ('CALL', '340.00', '1024.00'))
        self.assertEqual([l['id'] for l in self.c.get(f'/api/sltp/?address={ADDR}&position=AAPL').data], [4])
        self.assertEqual([l['id'] for l in self.c.get(f'/api/sltp/?address={ADDR}&cfd=7').data], [5])

    def test_bad_or_unknown_address(self, _q):
        self.assertEqual(self.c.get('/api/portfolio/?address=nothex').status_code, 400)
        self.assertEqual(self.c.get('/api/portfolio/?address=' + 'b' * 40).status_code, 404)

    def test_price_is_the_chains_verified_median(self, _q):
        r = self.c.get('/api/price/aapl/')
        self.assertEqual((r.data['execution_price'], r.data['verified']), ('330.32', True))
        self.assertEqual(self.c.get('/api/price/SPY/').status_code, 404)


@override_settings(NODE_RPC=['http://node'])
class TransactionForwardingTests(SimpleTestCase):

    def setUp(self):
        self.c = APIClient()

    def test_signed_envelope_is_forwarded_byte_for_byte(self):
        env = {'msg': '{"type":"order","nonce":"3"}', 'sig': 'c2ln'}
        with patch.object(chain, 'broadcast', return_value=('AB' * 32, 0, '')) as b:
            r = self.c.post('/api/tx/', env, format='json')
        self.assertEqual(r.status_code, 202)
        self.assertEqual(json.loads(b.call_args.args[0]), env)   # nothing added or changed

    def test_node_refusal_is_reported(self):
        with patch.object(chain, 'broadcast', return_value=('AB' * 32, 1, 'wrong nonce: expected 4, got 3')):
            r = self.c.post('/api/tx/', {'msg': '{}', 'sig': 'x'}, format='json')
        self.assertEqual((r.status_code, r.data['error']), (400, 'wrong nonce: expected 4, got 3'))

    def test_malformed_or_oversized_transactions_are_refused(self):
        for body in ({'msg': 1, 'sig': 'x'}, {'sig': 'x'}, {'msg': 'x', 'sig': 'y', 'extra': 1},
                     {'msg': 'x' * 5000, 'sig': 'y'}):
            self.assertEqual(self.c.post('/api/tx/', body, format='json').status_code, 400, body)

    def test_chain_outage_is_503(self):
        with patch.object(chain, 'broadcast', side_effect=chain.ChainUnavailable('down')):
            r = self.c.post('/api/tx/', {'msg': '{}', 'sig': 'x'}, format='json')
        self.assertEqual(r.status_code, 503)

    def test_tx_status(self):
        with patch.object(chain, 'tx_result', return_value=None):
            self.assertEqual(self.c.get('/api/tx/' + 'ab' * 32 + '/').data['status'], 'pending')
        with patch.object(chain, 'tx_result', return_value={'height': 9, 'code': 0, 'log': 'CONFIRMED'}):
            r = self.c.get('/api/tx/' + 'ab' * 32 + '/')
        self.assertEqual((r.data['status'], r.data['log']), ('committed', 'CONFIRMED'))
        self.assertEqual(self.c.get('/api/tx/nothex/').status_code, 400)


class GatewayHasNoStateTests(SimpleTestCase):

    def test_no_database_and_no_money_code(self):
        from django.conf import settings
        # an empty DATABASES becomes Django's dummy backend, which refuses every query
        self.assertEqual(settings.DATABASES['default']['ENGINE'], 'django.db.backends.dummy')
        self.assertNotIn('trading', settings.INSTALLED_APPS)
        import gateway.views as v
        for name in dir(v):   # no settlement entry points remain in the gateway
            self.assertNotIn('execute', name.lower())
            self.assertNotIn('settle', name.lower())


class SecurityTests(SimpleTestCase):

    def setUp(self):
        from django.core.cache import cache
        cache.clear()
        self.c = APIClient()

    def test_forwarded_for_header_does_not_dodge_the_rate_limit(self):
        from .throttles import TxThrottle
        codes = []
        with patch.object(TxThrottle, 'THROTTLE_RATES', {'tx': '2/minute'}), \
             patch.object(chain, 'broadcast', return_value=('AB' * 32, 0, '')):
            for i in range(4):
                r = self.c.post('/api/tx/', {'msg': '{}', 'sig': 'x'}, format='json',
                                HTTP_X_FORWARDED_FOR=f'10.0.0.{i}')
                codes.append(r.status_code)
        self.assertEqual(codes, [202, 202, 429, 429])

    def test_page_has_a_content_security_policy(self):
        r = self.c.get('/')
        csp = r.headers.get('Content-Security-Policy', '')
        for directive in ("connect-src 'self'", "img-src 'self' data:", "frame-ancestors 'none'",
                          "object-src 'none'"):
            self.assertIn(directive, csp)

    def test_ai_news_output_is_reduced_to_the_expected_shape(self):
        from .market import _clean_news
        out = _clean_news({'points': [
            {'text': 'ok', 'url': 'https://example.com/a', 'indirect': False},
            {'text': 'x' * 1000, 'url': 'javascript:alert(1)', 'indirect': True, 'reason': 'r'},
            {'text': 5}, 'junk'] + [{'text': 't', 'url': 'http://e.com'}] * 20, 'extra': '<script>'})
        self.assertEqual(set(out), {'points', 'disclaimer'})
        self.assertEqual(len(out['points']), 8)
        self.assertEqual(out['points'][0]['url'], 'https://example.com/a')
        self.assertEqual((out['points'][1]['url'], len(out['points'][1]['text'])), ('', 400))
