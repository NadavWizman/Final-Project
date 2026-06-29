"""
Locust load test for the TradeDesk trading platform.

Setup:
    1. pip install locust
    2. python manage.py create_test_users --count 1000
    3. Start the Django server + blockchain nodes
    4. locust -f locustfile.py --host http://127.0.0.1:8000

Then open http://localhost:8089, set users=1000, spawn rate=50, start.

Action weights (approximate real-world mix):
    - Read portfolio / orders     (frequent, cheap)
    - Buy stock                   (moderate)
    - Sell stock                  (moderate, requires holdings)
    - Open CFD                    (occasional)
    - Close CFD                   (occasional, requires open position)
"""

import random
import uuid

from locust import HttpUser, between, task


TICKERS = [
    'AAPL', 'MSFT', 'GOOGL', 'AMZN', 'NVDA',
    'META', 'TSLA', 'AMD',   'NFLX', 'QCOM',
    'JPM',  'V',    'MA',    'KO',   'BAC',
]
LEVERAGES = [2, 5, 10]


def _nonce():
    return str(uuid.uuid4())


class TradingUser(HttpUser):
    """Simulates one authenticated trader making random actions."""

    wait_time = between(0.5, 2)

    # ── lifecycle ──────────────────────────────────────────────

    def on_start(self):
        num = random.randint(1, 1000)
        self.username = f'testuser_{num:04d}'
        self.auth     = (self.username, 'testpass123')
        # local cache — refreshed lazily on each relevant task
        self._holdings    = {}   # ticker → quantity
        self._open_cfds   = []   # list of position dicts
        self._refresh_portfolio()

    # ── helpers ────────────────────────────────────────────────

    def _refresh_portfolio(self):
        r = self.client.get('/api/portfolio/', auth=self.auth,
                            name='/api/portfolio/')
        if r.status_code == 200:
            data = r.json()
            self._holdings = {
                h['ticker']: float(h['quantity'])
                for h in data.get('holdings', [])
            }

    def _refresh_cfds(self):
        r = self.client.get('/api/cfd/', auth=self.auth, name='/api/cfd/')
        if r.status_code == 200:
            self._open_cfds = [
                p for p in r.json().get('cfd_positions', []) if p['is_open']
            ]

    def _create_and_submit(self, body, tag):
        """POST /orders/ then /orders/{id}/submit/ — the consensus flow."""
        r = self.client.post('/api/orders/', json=body, auth=self.auth,
                             name=f'/api/orders/ [{tag}]')
        if r.status_code == 201:
            order_id = r.json()['id']
            self.client.post(f'/api/orders/{order_id}/submit/', json={},
                             auth=self.auth,
                             name='/api/orders/{id}/submit/')

    # ── read tasks (high frequency) ────────────────────────────

    @task(5)
    def view_portfolio(self):
        r = self.client.get('/api/portfolio/', auth=self.auth,
                            name='/api/portfolio/')
        if r.status_code == 200:
            data = r.json()
            self._holdings = {
                h['ticker']: float(h['quantity'])
                for h in data.get('holdings', [])
            }

    @task(3)
    def view_orders(self):
        self.client.get('/api/orders/', auth=self.auth, name='/api/orders/')

    @task(2)
    def view_cfd_positions(self):
        r = self.client.get('/api/cfd/', auth=self.auth, name='/api/cfd/')
        if r.status_code == 200:
            self._open_cfds = [
                p for p in r.json().get('cfd_positions', []) if p['is_open']
            ]

    # ── stock tasks ────────────────────────────────────────────

    @task(4)
    def buy_stock(self):
        ticker = random.choice(TICKERS)
        qty    = random.randint(1, 10)
        self._create_and_submit({
            'stock': ticker, 'order_type': 'BUY',
            'trade_type': 'STOCK', 'quantity': qty, 'nonce': _nonce(),
        }, 'BUY')

    @task(2)
    def sell_stock(self):
        if not self._holdings:
            self._refresh_portfolio()
            return
        ticker = random.choice(list(self._holdings.keys()))
        owned  = self._holdings[ticker]
        qty    = max(1, int(owned * random.uniform(0.1, 0.3)))
        self._create_and_submit({
            'stock': ticker, 'order_type': 'SELL',
            'trade_type': 'STOCK', 'quantity': qty, 'nonce': _nonce(),
        }, 'SELL')

    # ── CFD tasks ──────────────────────────────────────────────

    @task(2)
    def open_cfd(self):
        ticker   = random.choice(TICKERS)
        qty      = random.randint(1, 20)
        leverage = random.choice(LEVERAGES)
        side     = random.choice(['BUY', 'SELL'])
        self._create_and_submit({
            'stock': ticker, 'order_type': side,
            'trade_type': 'CFD', 'quantity': qty,
            'leverage': leverage, 'nonce': _nonce(),
        }, 'CFD')

    @task(1)
    def close_cfd(self):
        self._refresh_cfds()
        if not self._open_cfds:
            return
        pos = random.choice(self._open_cfds)
        self._create_and_submit({
            'stock': pos['stock'], 'order_type': 'SELL',
            'trade_type': 'CFD_CLOSE', 'quantity': pos['quantity'],
            'position_id': pos['id'], 'nonce': _nonce(),
        }, 'CFD_CLOSE')

    # ── price checks (lightweight reads) ───────────────────────

    @task(2)
    def check_price(self):
        ticker = random.choice(TICKERS)
        self.client.get(f'/api/price/{ticker}/', auth=self.auth,
                        name='/api/price/{ticker}/')
