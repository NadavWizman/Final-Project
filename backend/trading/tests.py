from decimal import Decimal
from django.test import TestCase
from django.contrib.auth.models import User
from rest_framework.test import APIClient
from rest_framework import status

from unittest.mock import patch, MagicMock

from .models import Order, Wallet, Position, Stock, UserProfile, CFDPosition, SLTPLevel
from .crypto_utils import generate_key_pair, sign_order, verify_signature, _build_message


def _patch_oracle(test, price='100.00', tolerance='1000'):
    """Neutralise execute_order's independent price re-verification for tests that
    are exercising other logic. Mocks the Oracle to a fixed price and widens the
    tolerance so any execution price passes. Price-verification behaviour has its
    own dedicated tests (OrderPriceVerificationTests)."""
    p1 = patch('trading.views._get_oracle_stub')
    factory = p1.start(); test.addCleanup(p1.stop)
    stub = MagicMock()
    stub.GetPrice.return_value = MagicMock(execution_price=str(price))
    factory.return_value = (stub, MagicMock())

    p2 = patch('trading.views.ORACLE_PRICE_TOLERANCE', Decimal(str(tolerance)))
    p2.start(); test.addCleanup(p2.stop)


# ──────────────────────────────────────────────────────────────────
# 1. Crypto utilities
# ──────────────────────────────────────────────────────────────────

class CryptoUtilsTests(TestCase):

    def test_generate_key_pair_returns_pem_strings(self):
        priv, pub = generate_key_pair()
        self.assertIn('PRIVATE KEY', priv)
        self.assertIn('PUBLIC KEY', pub)

    def test_sign_and_verify_roundtrip(self):
        priv, pub = generate_key_pair()
        order_data = {'stock': 'AAPL', 'order_type': 'BUY', 'quantity': '10', 'nonce': 'abc123'}
        sig = sign_order(priv, order_data)
        self.assertTrue(verify_signature(pub, order_data, sig))

    def test_verify_rejects_tampered_message(self):
        priv, pub = generate_key_pair()
        order_data = {'stock': 'AAPL', 'order_type': 'BUY', 'quantity': '10', 'nonce': 'abc123'}
        sig = sign_order(priv, order_data)
        tampered = {**order_data, 'quantity': '99'}
        self.assertFalse(verify_signature(pub, tampered, sig))

    def test_verify_rejects_wrong_key(self):
        priv, pub = generate_key_pair()
        _, other_pub = generate_key_pair()
        order_data = {'stock': 'AAPL', 'order_type': 'BUY', 'quantity': '5', 'nonce': 'xyz'}
        sig = sign_order(priv, order_data)
        self.assertFalse(verify_signature(other_pub, order_data, sig))

    def test_build_message_is_deterministic(self):
        data = {'stock': 'MSFT', 'order_type': 'SELL', 'quantity': '3', 'nonce': 'n1'}
        self.assertEqual(_build_message(data), _build_message(data))

    def test_build_message_sorts_keys(self):
        d1 = {'stock': 'AAPL', 'order_type': 'BUY',  'quantity': '1', 'nonce': 'x'}
        d2 = {'nonce': 'x',    'quantity': '1',       'order_type': 'BUY', 'stock': 'AAPL'}
        self.assertEqual(_build_message(d1), _build_message(d2))


# ──────────────────────────────────────────────────────────────────
# Helpers
# ──────────────────────────────────────────────────────────────────

def make_user(username='alice', password='pass1234'):
    user = User.objects.create_user(username=username, password=password)
    Wallet.objects.create(user=user, balance=Decimal('10000.00'))
    priv, pub = generate_key_pair()
    UserProfile.objects.create(user=user, ecdsa_private_key=priv, ecdsa_public_key=pub)
    return user


def make_stock(ticker='AAPL', name='Apple Inc.'):
    return Stock.objects.get_or_create(ticker=ticker, defaults={'name': name})[0]


# ──────────────────────────────────────────────────────────────────
# 2. Registration
# ──────────────────────────────────────────────────────────────────

class RegistrationTests(TestCase):

    def setUp(self):
        self.client = APIClient()

    def test_register_creates_user_wallet_and_profile(self):
        r = self.client.post('/api/register/', {'username': 'bob', 'password': 'secret99'}, format='json')
        self.assertEqual(r.status_code, status.HTTP_201_CREATED)
        user = User.objects.get(username='bob')
        self.assertEqual(user.wallet.balance, Decimal('10000.00'))
        self.assertIn('PUBLIC KEY', user.profile.ecdsa_public_key)

    def test_register_duplicate_username_rejected(self):
        self.client.post('/api/register/', {'username': 'bob', 'password': 'p1'}, format='json')
        r = self.client.post('/api/register/', {'username': 'bob', 'password': 'p2'}, format='json')
        self.assertEqual(r.status_code, status.HTTP_400_BAD_REQUEST)

    def test_register_missing_fields_rejected(self):
        r = self.client.post('/api/register/', {'username': 'bob'}, format='json')
        self.assertEqual(r.status_code, status.HTTP_400_BAD_REQUEST)


# ──────────────────────────────────────────────────────────────────
# 3. Portfolio
# ──────────────────────────────────────────────────────────────────

class PortfolioTests(TestCase):

    def setUp(self):
        self.user = make_user()
        self.client = APIClient()
        self.client.force_authenticate(user=self.user)

    def test_portfolio_returns_balance_and_empty_holdings(self):
        r = self.client.get('/api/portfolio/')
        self.assertEqual(r.status_code, 200)
        self.assertEqual(r.data['usd_balance'], '10000.00')
        self.assertEqual(r.data['holdings'], [])

    def test_portfolio_requires_auth(self):
        # SilentBasicAuthentication returns no WWW-Authenticate header so browsers
        # never show their native credential dialog. DRF answers 403 rather than
        # 401 when the authenticator supplies no header — that is the intended
        # behaviour here, and the response must carry no challenge.
        anon = APIClient()
        r = anon.get('/api/portfolio/')
        self.assertEqual(r.status_code, status.HTTP_403_FORBIDDEN)
        self.assertNotIn('WWW-Authenticate', r.headers)


# ──────────────────────────────────────────────────────────────────
# 4. Deposit
# ──────────────────────────────────────────────────────────────────

class DepositTests(TestCase):

    def setUp(self):
        self.user = make_user()
        self.client = APIClient()
        self.client.force_authenticate(user=self.user)

    def test_deposit_increases_balance(self):
        r = self.client.post('/api/deposit/', {'amount': '500'}, format='json')
        self.assertEqual(r.status_code, 200)
        self.user.wallet.refresh_from_db()
        self.assertEqual(self.user.wallet.balance, Decimal('10500.00'))

    def test_deposit_negative_amount_rejected(self):
        r = self.client.post('/api/deposit/', {'amount': '-100'}, format='json')
        self.assertEqual(r.status_code, status.HTTP_400_BAD_REQUEST)

    def test_deposit_zero_rejected(self):
        r = self.client.post('/api/deposit/', {'amount': '0'}, format='json')
        self.assertEqual(r.status_code, status.HTTP_400_BAD_REQUEST)


# ──────────────────────────────────────────────────────────────────
# 5. Order creation
# ──────────────────────────────────────────────────────────────────

class OrderCreationTests(TestCase):

    def setUp(self):
        self.user = make_user()
        self.stock = make_stock('AAPL')
        self.client = APIClient()
        self.client.force_authenticate(user=self.user)

    def test_create_order_returns_draft(self):
        r = self.client.post('/api/orders/', {
            'stock': 'AAPL', 'order_type': 'BUY', 'quantity': '5', 'nonce': 'nonce001'
        }, format='json')
        self.assertEqual(r.status_code, status.HTTP_201_CREATED)
        self.assertEqual(r.data['status'], 'DRAFT')

    def test_create_order_unlisted_ticker_rejected(self):
        Stock.objects.get_or_create(ticker='FAKE', defaults={'name': 'Fake Corp'})
        r = self.client.post('/api/orders/', {
            'stock': 'FAKE', 'order_type': 'BUY', 'quantity': '1', 'nonce': 'n2'
        }, format='json')
        self.assertEqual(r.status_code, status.HTTP_400_BAD_REQUEST)

    def test_create_order_zero_quantity_rejected(self):
        r = self.client.post('/api/orders/', {
            'stock': 'AAPL', 'order_type': 'BUY', 'quantity': '0', 'nonce': 'n3'
        }, format='json')
        self.assertEqual(r.status_code, status.HTTP_400_BAD_REQUEST)

    def test_create_order_negative_quantity_rejected(self):
        r = self.client.post('/api/orders/', {
            'stock': 'AAPL', 'order_type': 'BUY', 'quantity': '-1', 'nonce': 'n4'
        }, format='json')
        self.assertEqual(r.status_code, status.HTTP_400_BAD_REQUEST)

    def test_create_order_missing_nonce_rejected(self):
        r = self.client.post('/api/orders/', {
            'stock': 'AAPL', 'order_type': 'BUY', 'quantity': '1'
        }, format='json')
        self.assertEqual(r.status_code, status.HTTP_400_BAD_REQUEST)

    def test_duplicate_nonce_rejected(self):
        self.client.post('/api/orders/', {
            'stock': 'AAPL', 'order_type': 'BUY', 'quantity': '1', 'nonce': 'same'
        }, format='json')
        r = self.client.post('/api/orders/', {
            'stock': 'AAPL', 'order_type': 'BUY', 'quantity': '2', 'nonce': 'same'
        }, format='json')
        self.assertEqual(r.status_code, status.HTTP_400_BAD_REQUEST)

    def test_user_can_only_see_own_orders(self):
        other = make_user('bob')
        other_client = APIClient()
        other_client.force_authenticate(user=other)
        stock = make_stock('MSFT', 'Microsoft')
        other_client.post('/api/orders/', {
            'stock': 'MSFT', 'order_type': 'BUY', 'quantity': '1', 'nonce': 'bob1'
        }, format='json')
        r = self.client.get('/api/orders/')
        self.assertEqual(r.status_code, 200)
        for o in r.data:
            self.assertEqual(o['user'], self.user.id)


# ──────────────────────────────────────────────────────────────────
# 6. Order submission (signing)
# ──────────────────────────────────────────────────────────────────

class OrderSubmitTests(TestCase):

    def setUp(self):
        self.user = make_user()
        self.stock = make_stock('NVDA', 'NVIDIA')
        self.client = APIClient()
        self.client.force_authenticate(user=self.user)
        r = self.client.post('/api/orders/', {
            'stock': 'NVDA', 'order_type': 'BUY', 'quantity': '2', 'nonce': 'sub001'
        }, format='json')
        self.order_id = r.data['id']

    def test_submit_changes_status_to_submitted(self):
        r = self.client.post(f'/api/orders/{self.order_id}/submit/')
        self.assertEqual(r.status_code, 200)
        order = Order.objects.get(pk=self.order_id)
        self.assertEqual(order.status, 'SUBMITTED')
        self.assertIsNotNone(order.signature)

    def test_submit_non_draft_order_fails(self):
        self.client.post(f'/api/orders/{self.order_id}/submit/')
        r = self.client.post(f'/api/orders/{self.order_id}/submit/')
        self.assertEqual(r.status_code, status.HTTP_400_BAD_REQUEST)

    def test_signature_verifies_correctly(self):
        self.client.post(f'/api/orders/{self.order_id}/submit/')
        order = Order.objects.get(pk=self.order_id)
        order_data = {
            'stock':      order.stock.ticker,
            'order_type': order.order_type,
            'quantity':   str(order.quantity),
            'nonce':      order.nonce or '',
        }
        self.assertTrue(verify_signature(
            self.user.profile.ecdsa_public_key, order_data, order.signature
        ))


# ──────────────────────────────────────────────────────────────────
# 7. Order execution (consensus callback)
# ──────────────────────────────────────────────────────────────────

from django.utils import timezone
from datetime import timedelta


def _fresh_ts():
    return timezone.now().isoformat()


class OrderExecutionTests(TestCase):

    def setUp(self):
        self.user = make_user()
        self.stock = make_stock('AAPL')
        self.node = User.objects.create_user(username='node1', password='node1pass', is_staff=True)
        self.node_client = APIClient()
        self.node_client.force_authenticate(user=self.node)
        self.user_client = APIClient()
        self.user_client.force_authenticate(user=self.user)
        _patch_oracle(self)

        r = self.user_client.post('/api/orders/', {
            'stock': 'AAPL', 'order_type': 'BUY', 'quantity': '10', 'nonce': 'exec001'
        }, format='json')
        self.order_id = r.data['id']
        self.user_client.post(f'/api/orders/{self.order_id}/submit/')

    def test_execute_buy_deducts_balance_and_adds_position(self):
        r = self.node_client.post(f'/api/orders/{self.order_id}/execute_order/', {
            'execution_price': '100.00',
            'timestamp': _fresh_ts(),
        }, format='json')
        self.assertEqual(r.status_code, 200)
        self.user.wallet.refresh_from_db()
        self.assertEqual(self.user.wallet.balance, Decimal('9000.00'))
        pos = Position.objects.get(user=self.user, stock=self.stock)
        self.assertEqual(pos.quantity, Decimal('10'))
        order = Order.objects.get(pk=self.order_id)
        self.assertEqual(order.status, 'CONFIRMED')
        self.assertEqual(order.execution_price, Decimal('100.00'))

    def test_execute_with_stale_timestamp_rejected(self):
        old_ts = (timezone.now() - timedelta(seconds=90)).isoformat()
        r = self.node_client.post(f'/api/orders/{self.order_id}/execute_order/', {
            'execution_price': '100.00',
            'timestamp': old_ts,
        }, format='json')
        self.assertEqual(r.status_code, status.HTTP_400_BAD_REQUEST)
        order = Order.objects.get(pk=self.order_id)
        self.assertEqual(order.status, 'SUBMITTED')

    def test_execute_insufficient_balance_rejects_order(self):
        r = self.node_client.post(f'/api/orders/{self.order_id}/execute_order/', {
            'execution_price': '2000.00',  # 10 shares × $2000 = $20k > $10k balance
            'timestamp': _fresh_ts(),
        }, format='json')
        self.assertEqual(r.status_code, status.HTTP_400_BAD_REQUEST)
        order = Order.objects.get(pk=self.order_id)
        self.assertEqual(order.status, 'REJECTED')


    def test_execute_limit_price_exceeded_rejects_buy(self):
        # Create a BUY order with limit_price = $50
        r = self.user_client.post('/api/orders/', {
            'stock': 'AAPL', 'order_type': 'BUY', 'quantity': '1',
            'nonce': 'lim001', 'limit_price': '50.00'
        }, format='json')
        oid = r.data['id']
        self.user_client.post(f'/api/orders/{oid}/submit/')
        r2 = self.node_client.post(f'/api/orders/{oid}/execute_order/', {
            'execution_price': '75.00',  # above limit
            'timestamp': _fresh_ts(),
        }, format='json')
        self.assertEqual(r2.status_code, status.HTTP_400_BAD_REQUEST)
        self.assertEqual(Order.objects.get(pk=oid).status, 'REJECTED')

    def test_execute_sell_adds_balance_and_removes_position(self):
        # Give user a position first
        Position.objects.create(user=self.user, stock=self.stock, quantity=Decimal('5'))
        r = self.user_client.post('/api/orders/', {
            'stock': 'AAPL', 'order_type': 'SELL', 'quantity': '5', 'nonce': 'sell001'
        }, format='json')
        oid = r.data['id']
        self.user_client.post(f'/api/orders/{oid}/submit/')
        r2 = self.node_client.post(f'/api/orders/{oid}/execute_order/', {
            'execution_price': '200.00',
            'timestamp': _fresh_ts(),
        }, format='json')
        self.assertEqual(r2.status_code, 200)
        self.user.wallet.refresh_from_db()
        self.assertEqual(self.user.wallet.balance, Decimal('11000.00'))  # 10k + 5×$200
        self.assertFalse(Position.objects.filter(user=self.user, stock=self.stock).exists())

    def test_execute_sell_insufficient_shares_rejected(self):
        Position.objects.create(user=self.user, stock=self.stock, quantity=Decimal('2'))
        r = self.user_client.post('/api/orders/', {
            'stock': 'AAPL', 'order_type': 'SELL', 'quantity': '5', 'nonce': 'sell002'
        }, format='json')
        oid = r.data['id']
        self.user_client.post(f'/api/orders/{oid}/submit/')
        r2 = self.node_client.post(f'/api/orders/{oid}/execute_order/', {
            'execution_price': '100.00',
            'timestamp': _fresh_ts(),
        }, format='json')
        self.assertEqual(r2.status_code, status.HTTP_400_BAD_REQUEST)
        self.assertEqual(Order.objects.get(pk=oid).status, 'REJECTED')

    def test_execute_missing_fields_rejected(self):
        r = self.node_client.post(f'/api/orders/{self.order_id}/execute_order/', {}, format='json')
        self.assertEqual(r.status_code, status.HTTP_400_BAD_REQUEST)

    def test_regular_user_cannot_execute_orders(self):
        # the order owner must not be able to settle their own order and skip consensus
        r = self.user_client.post(f'/api/orders/{self.order_id}/execute_order/', {
            'execution_price': '100.00',
            'timestamp': _fresh_ts(),
        }, format='json')
        self.assertEqual(r.status_code, status.HTTP_403_FORBIDDEN)
        self.assertEqual(Order.objects.get(pk=self.order_id).status, 'SUBMITTED')
        self.user.wallet.refresh_from_db()
        self.assertEqual(self.user.wallet.balance, Decimal('10000.00'))

    def test_execute_non_submitted_order_rejected(self):
        # Create a fresh DRAFT and try to execute without submitting
        r = self.user_client.post('/api/orders/', {
            'stock': 'AAPL', 'order_type': 'BUY', 'quantity': '1', 'nonce': 'nosubmit'
        }, format='json')
        oid = r.data['id']
        r2 = self.node_client.post(f'/api/orders/{oid}/execute_order/', {
            'execution_price': '100.00',
            'timestamp': _fresh_ts(),
        }, format='json')
        self.assertEqual(r2.status_code, status.HTTP_400_BAD_REQUEST)


class OrderConsensusRejectionTests(TestCase):
    """The Leader marks an order REJECTED when consensus cannot be reached,
    so it leaves the SUBMITTED queue instead of being re-proposed forever."""

    def setUp(self):
        self.user = make_user()
        make_stock('AAPL')
        self.node = User.objects.create_user(username='node1', password='node1pass', is_staff=True)
        self.node_client = APIClient()
        self.node_client.force_authenticate(user=self.node)
        self.user_client = APIClient()
        self.user_client.force_authenticate(user=self.user)
        _patch_oracle(self)

        r = self.user_client.post('/api/orders/', {
            'stock': 'AAPL', 'order_type': 'BUY', 'quantity': '10', 'nonce': 'rej001'
        }, format='json')
        self.order_id = r.data['id']
        self.user_client.post(f'/api/orders/{self.order_id}/submit/')

    def test_node_can_reject_after_failed_consensus(self):
        r = self.node_client.post(f'/api/orders/{self.order_id}/reject_order/', {
            'reason': 'consensus not reached (1/3 approvals)',
        }, format='json')
        self.assertEqual(r.status_code, 200)
        order = Order.objects.get(pk=self.order_id)
        self.assertEqual(order.status, 'REJECTED')

    def test_rejection_leaves_balance_untouched(self):
        self.node_client.post(f'/api/orders/{self.order_id}/reject_order/', {}, format='json')
        self.user.wallet.refresh_from_db()
        self.assertEqual(self.user.wallet.balance, Decimal('10000.00'))
        self.assertFalse(Position.objects.filter(user=self.user).exists())

    def test_regular_user_cannot_reject_orders(self):
        r = self.user_client.post(f'/api/orders/{self.order_id}/reject_order/', {}, format='json')
        self.assertEqual(r.status_code, status.HTTP_403_FORBIDDEN)
        order = Order.objects.get(pk=self.order_id)
        self.assertEqual(order.status, 'SUBMITTED')

    def test_cannot_reject_an_already_confirmed_order(self):
        self.node_client.post(f'/api/orders/{self.order_id}/execute_order/', {
            'execution_price': '100.00', 'timestamp': _fresh_ts(),
        }, format='json')
        r = self.node_client.post(f'/api/orders/{self.order_id}/reject_order/', {}, format='json')
        self.assertEqual(r.status_code, status.HTTP_400_BAD_REQUEST)
        order = Order.objects.get(pk=self.order_id)
        self.assertEqual(order.status, 'CONFIRMED')


class CFDPartialCloseTests(TestCase):
    """CFD_CLOSE honours the order quantity so an SL/TP level can close part of
    a position. A manual close sends the full quantity and closes everything."""

    def setUp(self):
        self.user = make_user()
        self.stock = make_stock('AAPL')
        self.node = User.objects.create_user(username='node1', password='node1pass', is_staff=True)
        self.node_client = APIClient()
        self.node_client.force_authenticate(user=self.node)
        self.user_client = APIClient()
        self.user_client.force_authenticate(user=self.user)
        _patch_oracle(self)

        # entry $100 x 10 shares at 10x leverage -> $1000 notional, $100 margin
        self.pos = CFDPosition.objects.create(
            user=self.user, stock=self.stock, direction='LONG',
            quantity=Decimal('10'), entry_price=Decimal('100'),
            leverage=10, margin_used=Decimal('100'),
        )
        self.user.wallet.balance = Decimal('9900.00')   # margin already deducted
        self.user.wallet.save()

    def _close(self, qty, price, nonce):
        r = self.user_client.post('/api/orders/', {
            'stock': 'AAPL', 'order_type': 'SELL', 'trade_type': 'CFD_CLOSE',
            'quantity': str(qty), 'nonce': nonce, 'position_id': self.pos.id,
        }, format='json')
        oid = r.data['id']
        self.user_client.post(f'/api/orders/{oid}/submit/')
        return self.node_client.post(f'/api/orders/{oid}/execute_order/', {
            'execution_price': str(price), 'timestamp': _fresh_ts(),
        }, format='json')

    def test_partial_close_returns_proportional_margin_and_keeps_position_open(self):
        # close 4 of 10 at $110 -> margin 40 back, P&L (110-100)*4 = 40
        r = self._close(4, '110.00', 'cfdpart1')
        self.assertEqual(r.status_code, 200)
        self.pos.refresh_from_db()
        self.assertTrue(self.pos.is_open)
        self.assertEqual(self.pos.quantity, Decimal('6.0000'))
        self.assertEqual(self.pos.margin_used, Decimal('60.0000'))
        self.user.wallet.refresh_from_db()
        self.assertEqual(self.user.wallet.balance, Decimal('9980.00'))

    def test_closing_the_remainder_marks_position_closed(self):
        self._close(4, '110.00', 'cfdpart2')
        r = self._close(6, '110.00', 'cfdpart3')
        self.assertEqual(r.status_code, 200)
        self.pos.refresh_from_db()
        self.assertFalse(self.pos.is_open)
        self.assertEqual(self.pos.quantity, Decimal('0.0000'))
        self.assertEqual(self.pos.close_price, Decimal('110.0000'))
        # total P&L across both closes: (110-100)*10 = 100
        self.assertEqual(self.pos.pnl, Decimal('100.0000'))

    def test_close_quantity_is_capped_at_position_size(self):
        r = self._close(50, '110.00', 'cfdpart4')
        self.assertEqual(r.status_code, 200)
        self.pos.refresh_from_db()
        self.assertFalse(self.pos.is_open)
        self.assertEqual(self.pos.quantity, Decimal('0.0000'))

    def test_short_close_profits_when_price_falls(self):
        short = CFDPosition.objects.create(
            user=self.user, stock=self.stock, direction='SHORT',
            quantity=Decimal('5'), entry_price=Decimal('100'),
            leverage=10, margin_used=Decimal('50'),
        )
        r = self.user_client.post('/api/orders/', {
            'stock': 'AAPL', 'order_type': 'SELL', 'trade_type': 'CFD_CLOSE',
            'quantity': '5', 'nonce': 'cfdshort1', 'position_id': short.id,
        }, format='json')
        oid = r.data['id']
        self.user_client.post(f'/api/orders/{oid}/submit/')
        self.node_client.post(f'/api/orders/{oid}/execute_order/', {
            'execution_price': '90.00', 'timestamp': _fresh_ts(),
        }, format='json')
        short.refresh_from_db()
        # SHORT profits as price falls: (100-90)*5 = 50
        self.assertEqual(short.pnl, Decimal('50.0000'))


class CFDStopLossRoutingTests(TestCase):
    """A CFD stop-loss now creates a signed order for consensus instead of
    settling straight to the wallet."""

    def setUp(self):
        self.user = make_user()
        self.stock = make_stock('AAPL')
        self.pos = CFDPosition.objects.create(
            user=self.user, stock=self.stock, direction='LONG',
            quantity=Decimal('10'), entry_price=Decimal('100'),
            leverage=10, margin_used=Decimal('100'),
        )
        self.level = SLTPLevel.objects.create(
            cfd_position=self.pos, level_type='SL',
            price=Decimal('95'), quantity=Decimal('4'),
        )

    def _run_monitor_at(self, price):
        from . import sltp
        with patch.object(sltp, '_oracle_price', return_value=Decimal(price)):
            sltp._check_cfds()

    def test_trigger_creates_submitted_cfd_close_order(self):
        self._run_monitor_at('90.00')       # below the $95 stop
        order = Order.objects.get(trade_type='CFD_CLOSE', user=self.user)
        self.assertEqual(order.status, 'SUBMITTED')
        self.assertEqual(order.quantity, Decimal('4.0000'))
        self.assertEqual(order.position_id, self.pos.id)
        self.assertIsNotNone(order.signature)

    def test_order_signature_verifies(self):
        self._run_monitor_at('90.00')
        order = Order.objects.get(trade_type='CFD_CLOSE', user=self.user)
        self.assertTrue(verify_signature(
            self.user.profile.ecdsa_public_key,
            {'stock': 'AAPL', 'order_type': 'SELL',
             'quantity': str(order.quantity), 'nonce': order.nonce},
            order.signature,
        ))

    def test_wallet_and_position_untouched_until_consensus_executes(self):
        before = self.user.wallet.balance
        self._run_monitor_at('90.00')
        self.user.wallet.refresh_from_db()
        self.pos.refresh_from_db()
        self.assertEqual(self.user.wallet.balance, before)
        self.assertEqual(self.pos.quantity, Decimal('10.0000'))
        self.assertTrue(self.pos.is_open)

    def test_level_marked_triggered_so_it_does_not_fire_twice(self):
        self._run_monitor_at('90.00')
        self.level.refresh_from_db()
        self.assertTrue(self.level.triggered)
        self._run_monitor_at('90.00')       # monitor runs again 15s later
        self.assertEqual(Order.objects.filter(trade_type='CFD_CLOSE').count(), 1)

    def test_level_does_not_trigger_above_stop_price(self):
        self._run_monitor_at('99.00')
        self.assertFalse(Order.objects.filter(trade_type='CFD_CLOSE').exists())
        self.level.refresh_from_db()
        self.assertFalse(self.level.triggered)


class OrderPriceVerificationTests(TestCase):
    """Django independently re-verifies the execution price against the Oracle
    before settling, so a leader cannot settle at a price consensus never saw."""

    def setUp(self):
        self.user = make_user()
        make_stock('AAPL')
        self.node = User.objects.create_user(username='node1', password='node1pass', is_staff=True)
        self.node_client = APIClient()
        self.node_client.force_authenticate(user=self.node)
        self.user_client = APIClient()
        self.user_client.force_authenticate(user=self.user)

        r = self.user_client.post('/api/orders/', {
            'stock': 'AAPL', 'order_type': 'BUY', 'quantity': '1', 'nonce': 'pv001'
        }, format='json')
        self.order_id = r.data['id']
        self.user_client.post(f'/api/orders/{self.order_id}/submit/')

    def _oracle(self, price):
        stub = MagicMock()
        stub.GetPrice.return_value = MagicMock(execution_price=str(price))
        return patch('trading.views._get_oracle_stub', return_value=(stub, MagicMock()))

    def _execute(self, price):
        return self.node_client.post(f'/api/orders/{self.order_id}/execute_order/', {
            'execution_price': str(price), 'timestamp': _fresh_ts(),
        }, format='json')

    def test_honest_price_executes(self):
        with self._oracle('336.00'):
            r = self._execute('336.00')
        self.assertEqual(r.status_code, 200)
        self.assertEqual(Order.objects.get(pk=self.order_id).status, 'CONFIRMED')

    def test_small_movement_within_tolerance_executes(self):
        # ~0.6% below the live price — genuine drift, must not be rejected
        with self._oracle('336.00'):
            r = self._execute('338.00')
        self.assertEqual(r.status_code, 200)
        self.assertEqual(Order.objects.get(pk=self.order_id).status, 'CONFIRMED')

    def test_underpriced_settlement_is_rejected(self):
        # the attack: consensus saw ~$336, the leader tries to settle at $1
        with self._oracle('336.00'):
            r = self._execute('1.00')
        self.assertEqual(r.status_code, status.HTTP_400_BAD_REQUEST)
        self.assertIn('diverges', r.data['error'])
        self.assertEqual(Order.objects.get(pk=self.order_id).status, 'REJECTED')

    def test_overpriced_settlement_is_rejected(self):
        with self._oracle('336.00'):
            r = self._execute('900.00')
        self.assertEqual(r.status_code, status.HTTP_400_BAD_REQUEST)
        self.assertEqual(Order.objects.get(pk=self.order_id).status, 'REJECTED')

    def test_oracle_unavailable_fails_closed(self):
        # cannot verify → do not settle; order stays SUBMITTED so it can retry
        with patch('trading.views._get_oracle_stub', side_effect=Exception('oracle down')):
            r = self._execute('336.00')
        self.assertEqual(r.status_code, status.HTTP_503_SERVICE_UNAVAILABLE)
        self.assertEqual(Order.objects.get(pk=self.order_id).status, 'SUBMITTED')
