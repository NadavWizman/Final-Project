from decimal import Decimal
from django.test import TestCase
from django.contrib.auth.models import User
from rest_framework.test import APIClient
from rest_framework import status

from unittest.mock import patch, MagicMock

from .models import Order, Wallet, Position, Stock, UserProfile, CFDPosition, SLTPLevel, OptionPosition
from .crypto_utils import (generate_key_pair, sign_order, verify_signature, _build_message,
                           order_signing_payload)


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


def make_node(username='node1'):
    from django.contrib.auth.models import Group
    from .roles import CONSENSUS_NODES_GROUP
    node = User.objects.create_user(username=username, password='node-test-pass')
    node.groups.add(Group.objects.get_or_create(name=CONSENSUS_NODES_GROUP)[0])
    return node


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
        r1 = self.client.post('/api/register/', {'username': 'bob', 'password': 'Tr4de-desk-1'}, format='json')
        self.assertEqual(r1.status_code, status.HTTP_201_CREATED)
        r = self.client.post('/api/register/', {'username': 'bob', 'password': 'Tr4de-desk-2'}, format='json')
        self.assertEqual(r.status_code, status.HTTP_400_BAD_REQUEST)
        self.assertEqual(User.objects.filter(username='bob').count(), 1)

    def test_register_weak_password_rejected(self):
        for weak in ('1', 'password', '12345678', 'bob'):
            r = self.client.post('/api/register/', {'username': 'bob', 'password': weak}, format='json')
            self.assertEqual(r.status_code, status.HTTP_400_BAD_REQUEST, weak)
        self.assertFalse(User.objects.filter(username='bob').exists())

    def test_register_invalid_username_rejected(self):
        r = self.client.post('/api/register/', {'username': 'bad name!<>', 'password': 'Tr4de-desk-1'}, format='json')
        self.assertEqual(r.status_code, status.HTTP_400_BAD_REQUEST)

    def test_register_is_atomic(self):
        with patch('trading.views.UserProfile.objects.create', side_effect=RuntimeError('boom')):
            with self.assertRaises(RuntimeError):
                self.client.post('/api/register/', {'username': 'carol', 'password': 'Tr4de-desk-1'}, format='json')
        self.assertFalse(User.objects.filter(username='carol').exists())
        self.assertFalse(Wallet.objects.filter(user__username='carol').exists())

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

    def test_deposit_rejects_non_finite_and_malformed_amounts(self):
        for amount in ('Infinity', 'NaN', 'abc', '', None, '0.001'):
            r = self.client.post('/api/deposit/', {'amount': amount}, format='json')
            self.assertEqual(r.status_code, status.HTTP_400_BAD_REQUEST, amount)
        self.user.wallet.refresh_from_db()
        self.assertEqual(self.user.wallet.balance, Decimal('10000.00'))

    def test_deposit_above_cap_rejected_and_wallet_stays_usable(self):
        r = self.client.post('/api/deposit/', {'amount': '1e30'}, format='json')
        self.assertEqual(r.status_code, status.HTTP_400_BAD_REQUEST)
        r = self.client.post('/api/deposit/', {'amount': '10.50'}, format='json')
        self.assertEqual(r.status_code, 200)
        self.user.wallet.refresh_from_db()
        self.assertEqual(self.user.wallet.balance, Decimal('10010.50'))


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

    def test_orders_cannot_be_modified_or_deleted(self):
        r = self.client.post('/api/orders/', {
            'stock': 'AAPL', 'order_type': 'BUY', 'quantity': '1', 'nonce': 'immut1'
        }, format='json')
        oid = r.data['id']
        self.client.post(f'/api/orders/{oid}/submit/')
        for method in ('patch', 'put'):
            r = getattr(self.client, method)(f'/api/orders/{oid}/', {
                'stock': 'AAPL', 'order_type': 'BUY', 'quantity': '1000', 'nonce': 'immut1',
                'trade_type': 'CFD', 'leverage': 100,
            }, format='json')
            self.assertEqual(r.status_code, status.HTTP_405_METHOD_NOT_ALLOWED)
        self.assertEqual(self.client.delete(f'/api/orders/{oid}/').status_code,
                         status.HTTP_405_METHOD_NOT_ALLOWED)
        order = Order.objects.get(pk=oid)
        self.assertEqual(order.quantity, Decimal('1'))
        self.assertEqual(order.trade_type, 'STOCK')

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
        self.assertTrue(verify_signature(
            self.user.profile.ecdsa_public_key, order_signing_payload(order), order.signature
        ))

    def test_signature_covers_every_trade_field(self):
        self.client.post(f'/api/orders/{self.order_id}/submit/')
        order = Order.objects.get(pk=self.order_id)
        pub = self.user.profile.ecdsa_public_key
        for field, value in (('trade_type', 'CFD'), ('leverage', 100), ('limit_price', Decimal('1')),
                             ('position_id', 7), ('quantity', Decimal('200'))):
            tampered = order_signing_payload(order) | {field: value}
            self.assertFalse(verify_signature(pub, tampered, order.signature), field)

    def test_signed_message_exposed_to_nodes(self):
        self.client.post(f'/api/orders/{self.order_id}/submit/')
        r = self.client.get(f'/api/orders/{self.order_id}/')
        order = Order.objects.get(pk=self.order_id)
        self.assertEqual(r.data['signed_message'].encode(), _build_message(order_signing_payload(order)))
        self.assertIn('"quantity":"2.0000"', r.data['signed_message'])

    def test_nonce_with_json_special_characters_rejected(self):
        for bad in ('a"b', 'a\\b', 'ünï', 'x' * 65, 'has space'):
            r = self.client.post('/api/orders/', {
                'stock': 'NVDA', 'order_type': 'BUY', 'quantity': '1', 'nonce': bad,
            }, format='json')
            self.assertEqual(r.status_code, status.HTTP_400_BAD_REQUEST, bad)


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
        self.node = make_node()
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

    def test_order_altered_after_signing_is_rejected_at_settlement(self):
        # e.g. a direct database edit after consensus approved the signed order
        Order.objects.filter(pk=self.order_id).update(quantity=Decimal('1000'))
        r = self.node_client.post(f'/api/orders/{self.order_id}/execute_order/', {
            'execution_price': '100.00', 'timestamp': _fresh_ts(),
        }, format='json')
        self.assertEqual(r.status_code, status.HTTP_400_BAD_REQUEST)
        self.assertEqual(Order.objects.get(pk=self.order_id).status, 'REJECTED')
        self.user.wallet.refresh_from_db()
        self.assertEqual(self.user.wallet.balance, Decimal('10000.00'))

    def test_admin_staff_is_not_a_consensus_node(self):
        admin = User.objects.create_user(username='admin', password='x', is_staff=True)
        c = APIClient(); c.force_authenticate(user=admin)
        r = c.post(f'/api/orders/{self.order_id}/execute_order/', {
            'execution_price': '100.00', 'timestamp': _fresh_ts()}, format='json')
        self.assertEqual(r.status_code, status.HTTP_403_FORBIDDEN)
        self.assertEqual(Order.objects.get(pk=self.order_id).status, 'SUBMITTED')

    def test_node_lists_every_order(self):
        r = self.node_client.get('/api/orders/')
        self.assertEqual(r.status_code, 200)
        self.assertIn(self.order_id, [o['id'] for o in r.data])

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
        self.node = make_node()
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
        self.node = make_node()
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
            self.user.profile.ecdsa_public_key, order_signing_payload(order), order.signature,
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
        self.node = make_node()
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

    def test_unusable_oracle_price_fails_closed(self):
        # a zero/garbage Oracle price must not skip the divergence check
        for bad in ('0', '', 'nan'):
            with self._oracle(bad):
                r = self._execute('336.00')
            self.assertEqual(r.status_code, status.HTTP_503_SERVICE_UNAVAILABLE, bad)
        self.assertEqual(Order.objects.get(pk=self.order_id).status, 'SUBMITTED')

    def test_second_execution_of_same_order_is_refused(self):
        with self._oracle('336.00'):
            self.assertEqual(self._execute('336.00').status_code, 200)
            r = self._execute('336.00')
        self.assertEqual(r.status_code, status.HTTP_400_BAD_REQUEST)
        self.assertEqual(Position.objects.get(user=self.user).quantity, Decimal('1'))

    def test_oracle_unavailable_fails_closed(self):
        # cannot verify → do not settle; order stays SUBMITTED so it can retry
        with patch('trading.views._get_oracle_stub', side_effect=Exception('oracle down')):
            r = self._execute('336.00')
        self.assertEqual(r.status_code, status.HTTP_503_SERVICE_UNAVAILABLE)
        self.assertEqual(Order.objects.get(pk=self.order_id).status, 'SUBMITTED')


class OptionOrderValidationTests(TestCase):

    def setUp(self):
        self.user = make_user()
        self.stock = make_stock('AAPL')
        self.client = APIClient()
        self.client.force_authenticate(user=self.user)
        self.future = (timezone.now().date() + timedelta(days=30)).isoformat()

    def _option(self, **overrides):
        body = {
            'stock': 'AAPL', 'order_type': 'BUY', 'trade_type': 'OPTION', 'quantity': '1',
            'nonce': overrides.pop('nonce', 'opt-n1'), 'option_contract_type': 'CALL',
            'option_strike': '100', 'option_expiry': self.future,
        }
        body.update(overrides)
        return self.client.post('/api/orders/', body, format='json')

    def test_valid_option_order_accepted(self):
        self.assertEqual(self._option().status_code, status.HTTP_201_CREATED)

    def test_fractional_contracts_rejected(self):
        self.assertEqual(self._option(quantity='0.5').status_code, status.HTTP_400_BAD_REQUEST)

    def test_selling_options_rejected(self):
        self.assertEqual(self._option(order_type='SELL').status_code, status.HTTP_400_BAD_REQUEST)

    def test_expired_option_rejected(self):
        self.assertEqual(self._option(option_expiry='2020-01-17').status_code, status.HTTP_400_BAD_REQUEST)

    def test_close_requires_owned_open_position_on_same_stock(self):
        other = make_user('bob')
        pos = OptionPosition.objects.create(
            user=other, stock=self.stock, contract_type='CALL', strike=Decimal('100'),
            expiry=timezone.now().date() + timedelta(days=5), contracts=1, premium_paid=Decimal('2'),
        )
        r = self.client.post('/api/orders/', {
            'stock': 'AAPL', 'order_type': 'SELL', 'trade_type': 'OPT_CLOSE',
            'quantity': '1', 'nonce': 'close-x', 'position_id': pos.id,
        }, format='json')
        self.assertEqual(r.status_code, status.HTTP_400_BAD_REQUEST)

    def test_cannot_exercise_expired_option(self):
        pos = OptionPosition.objects.create(
            user=self.user, stock=self.stock, contract_type='CALL', strike=Decimal('100'),
            expiry=timezone.now().date() - timedelta(days=1), contracts=1, premium_paid=Decimal('2'),
        )
        r = self.client.post('/api/orders/', {
            'stock': 'AAPL', 'order_type': 'SELL', 'trade_type': 'OPT_EXER',
            'quantity': '1', 'nonce': 'exer-x', 'position_id': pos.id,
        }, format='json')
        self.assertEqual(r.status_code, status.HTTP_400_BAD_REQUEST)


class OptionSettlementTests(TestCase):
    """Option premiums come from yfinance (mocked here); the stock price is the
    consensus-verified execution price."""

    def setUp(self):
        self.user = make_user()
        self.stock = make_stock('AAPL')
        self.node = make_node()
        self.node_client = APIClient(); self.node_client.force_authenticate(user=self.node)
        self.user_client = APIClient(); self.user_client.force_authenticate(user=self.user)
        _patch_oracle(self)
        self.expiry = timezone.now().date() + timedelta(days=30)

    def _submit(self, body):
        r = self.user_client.post('/api/orders/', body, format='json')
        self.assertEqual(r.status_code, 201, r.data)
        self.user_client.post(f"/api/orders/{r.data['id']}/submit/")
        return r.data['id']

    def _execute(self, oid, price='100.00'):
        return self.node_client.post(f'/api/orders/{oid}/execute_order/', {
            'execution_price': price, 'timestamp': _fresh_ts()}, format='json')

    def _buy_call(self, nonce='ob1'):
        return self._submit({
            'stock': 'AAPL', 'order_type': 'BUY', 'trade_type': 'OPTION', 'quantity': '2',
            'nonce': nonce, 'option_contract_type': 'CALL', 'option_strike': '90',
            'option_expiry': self.expiry.isoformat()})

    def test_buy_charges_market_premium(self):
        oid = self._buy_call()
        with patch('trading.views._option_market_premium', return_value=Decimal('12.50')):
            r = self._execute(oid)
        self.assertEqual(r.status_code, 200)
        self.user.wallet.refresh_from_db()
        self.assertEqual(self.user.wallet.balance, Decimal('10000') - Decimal('12.50') * 200)

    def test_buy_premium_floored_at_intrinsic(self):
        # stale quote of $1 for a call $10 in the money
        oid = self._buy_call('ob2')
        with patch('trading.views._option_market_premium', return_value=Decimal('1.00')):
            self._execute(oid)
        self.assertEqual(OptionPosition.objects.get(user=self.user).premium_paid, Decimal('10.0000'))

    def test_close_without_quote_fails_closed_and_stays_submitted(self):
        pos = OptionPosition.objects.create(
            user=self.user, stock=self.stock, contract_type='CALL', strike=Decimal('90'),
            expiry=self.expiry, contracts=1, premium_paid=Decimal('5'))
        oid = self._submit({'stock': 'AAPL', 'order_type': 'SELL', 'trade_type': 'OPT_CLOSE',
                            'quantity': '1', 'nonce': 'oc1', 'position_id': pos.id})
        with patch('trading.views._option_market_premium', return_value=None):
            r = self._execute(oid)
        self.assertEqual(r.status_code, status.HTTP_503_SERVICE_UNAVAILABLE)
        self.assertEqual(Order.objects.get(pk=oid).status, 'SUBMITTED')
        pos.refresh_from_db()
        self.assertEqual(pos.status, 'OPEN')

    def test_one_sided_book_uses_the_crossed_side(self):
        import pandas as pd
        from . import views
        df = pd.DataFrame([{'strike': 90.0, 'bid': 0.0, 'ask': 4.0, 'lastPrice': 3.0}])
        chain = MagicMock(calls=df, puts=df)
        with patch('yfinance.Ticker') as T:
            T.return_value.option_chain.return_value = chain
            buy = views._option_market_premium('AAPL', self.expiry, 'CALL', Decimal('90'), 'buy')
            sell = views._option_market_premium('AAPL', self.expiry, 'CALL', Decimal('90'), 'sell')
        self.assertEqual(buy, Decimal('4.0'))    # not half the ask
        self.assertEqual(sell, Decimal('3.0'))   # no bid → last trade
