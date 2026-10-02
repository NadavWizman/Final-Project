"""Load test for TradeDesk on its CometBFT network.

Every simulated user is a real wallet: it creates a P-256 key, registers on
the chain, and signs every order itself — exactly what the browser does. The
gateway only forwards. Besides the HTTP timings, each transaction reports an
"end-to-end" timing: from sending it until it is in a committed (final) block.

    pip3 install locust
    ./run.sh start
    locust -f locustfile.py --host http://127.0.0.1:8000
    # or headless, with CSV output:
    locust -f locustfile.py --host http://127.0.0.1:8000 --headless -u 50 -r 10 -t 2m --csv results

In the "end-to-end" rows, P50/P95 are the latency to finality; requests/s in
those rows is the settled-orders throughput.
"""
import base64
import hashlib
import json
import random
import time

from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.hazmat.primitives.asymmetric.utils import decode_dss_signature
from locust import HttpUser, between, events, task

TICKERS = ['AAPL', 'MSFT', 'GOOGL', 'AMZN', 'NVDA', 'META', 'JPM', 'V', 'KO', 'AMD']
FINALITY_TIMEOUT = 60
P256_N = 0xffffffff00000000ffffffffffffffffbce6faada7179e84f3b9cac2fc632551


class Trader(HttpUser):
    wait_time = between(0.5, 2)

    def on_start(self):
        self.key = ec.generate_private_key(ec.SECP256R1())
        self.pub = self.key.public_key().public_bytes(
            serialization.Encoding.X962, serialization.PublicFormat.UncompressedPoint)
        self.addr = hashlib.sha256(self.pub).hexdigest()[:40]
        self.nonce = 0
        self.chain = self.client.get('/api/status/', name='/api/status/').json()['chain_id']
        self.holdings, self.cfds = {}, []
        self.send({'type': 'register', 'username': 'load_' + self.addr[:12]}, 'register')

    # ── signing and settlement ───────────────────────────────────────
    def send(self, msg, label):
        msg = {**msg, 'chain': self.chain, 'from': self.addr, 'nonce': str(self.nonce)}
        raw = json.dumps(msg).encode()
        r, s = decode_dss_signature(self.key.sign(raw, ec.ECDSA(hashes.SHA256())))
        s = min(s, P256_N - s)   # the chain accepts only the low-S form
        env = {'msg': raw.decode(),
               'sig': base64.b64encode(r.to_bytes(32, 'big') + s.to_bytes(32, 'big')).decode()}
        if msg['type'] == 'register':
            env['pubkey'] = base64.b64encode(self.pub).decode()

        start = time.perf_counter()
        resp = self.client.post('/api/tx/', json=env, name='/api/tx/ (submit)')
        if resp.status_code != 202:
            if 'nonce' in resp.text:   # resynchronise with the chain
                self.nonce = self.client.get(f'/api/portfolio/?address={self.addr}',
                                             name='/api/portfolio/').json().get('nonce', self.nonce)
            return None
        self.nonce += 1
        tx_hash = resp.json()['hash']

        while time.perf_counter() - start < FINALITY_TIMEOUT:
            st = self.client.get(f'/api/tx/{tx_hash}/', name='/api/tx/{hash}/ (poll)').json()
            if st.get('status') == 'committed':
                ok = st.get('code') == 0 and not st.get('log', '').startswith('REJECTED')
                events.request.fire(request_type='end-to-end', name=label,
                                    response_time=(time.perf_counter() - start) * 1000,
                                    response_length=0, exception=None if ok else Exception(st.get('log')),
                                    context={})
                return st
            time.sleep(0.25)
        events.request.fire(request_type='end-to-end', name=label,
                            response_time=FINALITY_TIMEOUT * 1000, response_length=0,
                            exception=TimeoutError('not final'), context={})
        return None

    def order(self, label, **order):
        return self.send({'type': 'order', 'order': order}, label)

    def refresh(self):
        p = self.client.get(f'/api/portfolio/?address={self.addr}', name='/api/portfolio/')
        if p.status_code == 200:
            self.holdings = {h['ticker']: float(h['quantity']) for h in p.json()['holdings']}
        c = self.client.get(f'/api/cfd/?address={self.addr}', name='/api/cfd/')
        if c.status_code == 200:
            self.cfds = [x for x in c.json()['cfd_positions'] if x['is_open']]

    # ── the mix of actions ───────────────────────────────────────────
    @task(5)
    def view_portfolio(self):
        self.refresh()

    @task(2)
    def view_orders(self):
        self.client.get(f'/api/orders/?address={self.addr}&limit=50', name='/api/orders/')

    @task(4)
    def buy_stock(self):
        self.order('buy stock', kind='STOCK', side='BUY', ticker=random.choice(TICKERS),
                   qty=str(random.randint(1, 3)))

    @task(2)
    def sell_stock(self):
        if not self.holdings:
            return self.refresh()
        t = random.choice(list(self.holdings))
        self.order('sell stock', kind='STOCK', side='SELL', ticker=t, qty=f'{self.holdings[t]:.4f}')
        self.holdings.pop(t, None)

    @task(2)
    def open_cfd(self):
        self.order('open CFD', kind='CFD', side=random.choice(['BUY', 'SELL']),
                   ticker=random.choice(TICKERS), qty=str(random.randint(1, 5)),
                   leverage=str(random.choice([2, 5, 10])))

    @task(1)
    def close_cfd(self):
        if not self.cfds:
            return self.refresh()
        c = self.cfds.pop()
        self.order('close CFD', kind='CFD_CLOSE', side='SELL', ticker=c['stock'],
                   qty=c['quantity'], position=str(c['id']))
