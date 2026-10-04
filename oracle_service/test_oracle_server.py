"""Unit tests for the oracle signer (network sources are mocked).

    cd oracle_service && python3 -m unittest -v
"""
import unittest
from decimal import Decimal
from unittest.mock import MagicMock, patch

import pandas as pd
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

import oracle_server as o


def _signer(fetch, name='yahoo'):
    key = Ed25519PrivateKey.generate()
    return o.OracleServicer(name, fetch, key), key.public_key()


def _quote(svc, ticker):
    ctx = MagicMock()
    return svc.SignQuote(MagicMock(ticker=ticker), ctx), ctx


class SignedQuoteTests(unittest.TestCase):

    def test_quote_signature_covers_the_exact_message(self):
        svc, pub = _signer(lambda t: (Decimal('330.325'), ''))
        q, _ = _quote(svc, 'aapl')
        self.assertEqual((q.source, q.ticker, q.price_cents), ('yahoo', 'AAPL', 33033))  # half-up
        pub.verify(q.signature, f'tradedesk-quote|yahoo|AAPL|33033|{q.timestamp}'.encode())
        with self.assertRaises(Exception):  # a changed price no longer verifies
            pub.verify(q.signature, f'tradedesk-quote|yahoo|AAPL|33034|{q.timestamp}'.encode())

    def test_fixed_source_signs_under_its_registered_name(self):
        svc, pub = _signer(o.fixed('1.00'), name='cnbc')
        q, _ = _quote(svc, 'MSFT')
        self.assertEqual((q.source, q.price_cents), ('cnbc', 100))
        pub.verify(q.signature, f'tradedesk-quote|cnbc|MSFT|100|{q.timestamp}'.encode())

    def test_quotes_are_cached_briefly(self):
        calls = []
        svc, _ = _signer(lambda t: calls.append(t) or (Decimal('10'), ''))
        _quote(svc, 'AAPL')
        _quote(svc, 'AAPL')
        self.assertEqual(len(calls), 1)

    def test_source_failure_aborts_without_a_quote(self):
        def broken(t):
            raise LookupError('down')
        svc, _ = _signer(broken)
        ctx = MagicMock()
        ctx.abort.side_effect = RuntimeError('aborted')
        with self.assertRaises(RuntimeError):
            svc.SignQuote(MagicMock(ticker='AAPL'), ctx)
        self.assertEqual(ctx.abort.call_args.args[0], o.grpc.StatusCode.NOT_FOUND)


class SourceParsingTests(unittest.TestCase):

    def test_yahoo_skips_incomplete_bars_and_maps_class_shares(self):
        idx = pd.date_range('2026-09-21 20:00', periods=2, freq='D', tz='UTC')
        df = pd.DataFrame({'Close': [186.5, float('nan')]}, index=idx)
        with patch('yfinance.Ticker') as T:
            T.return_value.history.return_value = df
            price, _ = o.yahoo('BRK.B')
        T.assert_called_once_with('BRK-B')
        self.assertEqual(price, Decimal('186.5'))

    def test_nasdaq_and_cnbc_parse_prices(self):
        with patch.object(o, '_get_json', return_value={'data': {'primaryData': {'lastSalePrice': '$1,330.32'}}}):
            self.assertEqual(o.nasdaq('AAPL')[0], Decimal('1330.32'))
        with patch.object(o, '_get_json', return_value={'FormattedQuoteResult': {'FormattedQuote': [{'last': '330.32'}]}}):
            self.assertEqual(o.cnbc('AAPL')[0], Decimal('330.32'))
        with patch.object(o, '_get_json', return_value={'data': None}):
            with self.assertRaises(LookupError):
                o.nasdaq('AAPL')

    def test_tradingview_takes_the_exchange_that_has_the_symbol(self):
        reply = {'data': [{'s': 'NASDAQ:JPM', 'd': [None]}, {'s': 'NYSE:JPM', 'd': [332.38]}]}
        with patch.object(o, '_get_json', return_value=reply) as g:
            self.assertEqual(o.tradingview('JPM')[0], Decimal('332.38'))
        self.assertEqual(g.call_args.kwargs['body']['symbols']['tickers'], ['NASDAQ:JPM', 'NYSE:JPM', 'AMEX:JPM'])
        with patch.object(o, '_get_json', return_value={'data': []}):
            with self.assertRaises(LookupError):
                o.tradingview('JPM')

    def test_google_reads_the_price_next_to_its_own_symbol(self):
        page = ('...["MSFT","NASDAQ"],"Microsoft",0,"USD",[512.8,1]...'
                '["AAPL","NASDAQ"],"Apple Inc",0,"USD",[333.69,3.37,1.02]...')
        with patch.object(o, '_get_text', return_value=page):
            self.assertEqual(o.google('AAPL')[0], Decimal('333.69'))
        with patch.object(o, '_get_text', return_value='<html>consent page</html>'):
            with self.assertRaises(LookupError):
                o.google('AAPL')

    def test_malformed_ticker_is_refused_before_any_request(self):
        servicer, _ = _signer(MagicMock(side_effect=AssertionError('fetched')))
        ctx = MagicMock()
        ctx.abort.side_effect = RuntimeError('aborted')
        with self.assertRaises(RuntimeError):
            servicer.SignQuote(MagicMock(ticker='../../admin'), ctx)
        self.assertEqual(ctx.abort.call_args.args[0], o.grpc.StatusCode.INVALID_ARGUMENT)


if __name__ == '__main__':
    unittest.main()
