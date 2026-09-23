"""Unit tests for the Oracle service (Yahoo Finance is mocked).

    cd oracle_service && python3 -m unittest -v
"""
import unittest
from unittest.mock import MagicMock, patch

import pandas as pd

import oracle_server as o


def _history(*closes):
    index = pd.date_range('2026-09-21 20:00', periods=len(closes), freq='D', tz='UTC')
    return pd.DataFrame({'Close': list(closes)}, index=index)


class OracleServerTests(unittest.TestCase):

    def _get(self, svc, ticker):
        ctx = MagicMock()
        return svc.GetPrice(o.oracle_pb2.PriceRequest(ticker=ticker), ctx), ctx

    def test_class_shares_use_yahoo_dash_symbol(self):
        with patch.object(o.yf, 'Ticker') as T:
            T.return_value.history.return_value = _history(410.0)
            resp, _ = self._get(o.OracleServicer(), 'brk.b')
        T.assert_called_once_with('BRK-B')
        self.assertEqual((resp.ticker, resp.execution_price), ('BRK.B', '410.0'))

    def test_market_time_is_the_data_time(self):
        with patch.object(o.yf, 'Ticker') as T:
            T.return_value.history.return_value = _history(100.0, 187.254)
            resp, _ = self._get(o.OracleServicer(), 'AAPL')
        self.assertEqual(resp.execution_price, '187.25')
        self.assertTrue(resp.market_time.startswith('2026-09-22T20:00:00'))
        self.assertNotEqual(resp.timestamp, resp.market_time)

    def test_quotes_are_cached_briefly(self):
        with patch.object(o.yf, 'Ticker') as T:
            T.return_value.history.return_value = _history(100.0)
            svc = o.OracleServicer()
            self._get(svc, 'AAPL')
            self._get(svc, 'AAPL')
        self.assertEqual(T.call_count, 1)

    def test_no_data_is_not_found(self):
        with patch.object(o.yf, 'Ticker') as T:
            T.return_value.history.return_value = pd.DataFrame({'Close': []})
            _, ctx = self._get(o.OracleServicer(), 'ZZZZ')
        ctx.set_code.assert_called_once_with(o.grpc.StatusCode.NOT_FOUND)


if __name__ == '__main__':
    unittest.main()
