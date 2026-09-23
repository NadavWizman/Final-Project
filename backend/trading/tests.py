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


# Ed25519 keys for two voting test nodes. ConsensusClient signs every
# execute_order call with them, the way the Leader forwards real node votes.
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from cryptography.hazmat.primitives import serialization as _ser
import base64 as _b64
import hashlib as _hashlib

_VOTER_KEYS = {'voter-a': Ed25519PrivateKey.generate(), 'voter-b': Ed25519PrivateKey.generate()}


def _raw_public_hex(private_key):
    return private_key.public_key().public_bytes(_ser.Encoding.Raw, _ser.PublicFormat.Raw).hex()


def ensure_voters():
    from .models import NodeKey
    for name, key in _VOTER_KEYS.items():
        if not User.objects.filter(username=name).exists():
            NodeKey.objects.create(user=make_node(name), public_key=_raw_public_hex(key))


def block_hash_for(order_id):
    return _hashlib.sha256(f'block-{order_id}'.encode()).hexdigest()


def votes_for(order_id, block_hash, price, voters=('voter-a', 'voter-b')):
    from .consensus import vote_message
    msg = vote_message(order_id, block_hash, price)
    return [{'node': n, 'signature': _b64.b64encode(_VOTER_KEYS[n].sign(msg)).decode()} for n in voters]


class ConsensusClient(APIClient):
    """APIClient for a node account that attaches a valid quorum proof to
    execute_order calls that don't carry one."""

    def post(self, path, data=None, *args, **kwargs):
        if path.endswith('/execute_order/') and isinstance(data, dict) and 'votes' not in data:
            ensure_voters()
            order_id = int(path.rstrip('/').split('/')[-2])
            block_hash = data.get('block_hash') or block_hash_for(order_id)
            data = {**data, 'block_hash': block_hash,
                    'votes': votes_for(order_id, block_hash, str(data.get('execution_price')))}
        return super().post(path, data, *args, **kwargs)


def make_stock(ticker='AAPL', name='Apple Inc.'):
    return Stock.objects.get_or_create(ticker=ticker, defaults={'name': name})[0]


# ──────────────────────────────────────────────────────────────────
# 2. Registration
# ──────────────────────────────────────────────────────────────────

class RegistrationTests(TestCase):

    def setUp(self):
        from django.core.cache import cache
        cache.clear()   # registration is rate-limited per client
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

    def test_orders_can_be_filtered_and_limited(self):
        for i in range(3):
            self.client.post('/api/orders/', {'stock': 'AAPL', 'order_type': 'BUY',
                                              'quantity': '1', 'nonce': f'page{i}'}, format='json')
        first = Order.objects.filter(user=self.user).order_by('id').first()
        self.client.post(f'/api/orders/{first.id}/submit/')

        r = self.client.get('/api/orders/?status=SUBMITTED')
        self.assertEqual([o['id'] for o in r.data], [first.id])
        r = self.client.get('/api/orders/?limit=2')
        self.assertEqual(len(r.data), 2)
        self.assertGreater(r.data[0]['id'], r.data[1]['id'])          # newest first
        for bad in ('?limit=0', '?limit=9999', '?limit=x', '?status=NOPE'):
            self.assertEqual(self.client.get('/api/orders/' + bad).status_code, 400, bad)

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
        self.node_client = ConsensusClient()
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


    def test_limit_not_reached_keeps_order_open(self):
        # BUY limit $50 while the market is at $75: the order rests, it is not killed
        r = self.user_client.post('/api/orders/', {
            'stock': 'AAPL', 'order_type': 'BUY', 'quantity': '1',
            'nonce': 'lim001', 'limit_price': '50.00'
        }, format='json')
        oid = r.data['id']
        self.user_client.post(f'/api/orders/{oid}/submit/')
        r2 = self.node_client.post(f'/api/orders/{oid}/execute_order/', {
            'execution_price': '75.00', 'timestamp': _fresh_ts(),
        }, format='json')
        self.assertEqual(r2.status_code, status.HTTP_409_CONFLICT)
        self.assertEqual(Order.objects.get(pk=oid).status, 'SUBMITTED')
        self.user.wallet.refresh_from_db()
        self.assertEqual(self.user.wallet.balance, Decimal('10000.00'))

    def test_limit_reached_fills_at_market(self):
        r = self.user_client.post('/api/orders/', {
            'stock': 'AAPL', 'order_type': 'BUY', 'quantity': '1',
            'nonce': 'lim002', 'limit_price': '80.00'
        }, format='json')
        oid = r.data['id']
        self.user_client.post(f'/api/orders/{oid}/submit/')
        r2 = self.node_client.post(f'/api/orders/{oid}/execute_order/', {
            'execution_price': '75.00', 'timestamp': _fresh_ts(),
        }, format='json')
        self.assertEqual(r2.status_code, 200)
        self.assertEqual(Order.objects.get(pk=oid).execution_price, Decimal('75.0000'))

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
        self.node_client = ConsensusClient()
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
        self.node_client = ConsensusClient()
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
        self.node_client = ConsensusClient()
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

    def test_second_execution_of_same_order_never_settles_twice(self):
        with self._oracle('336.00'):
            self.assertEqual(self._execute('336.00').status_code, 200)
            retry = self._execute('336.00')                       # same certified block
            other = self.node_client.post(f'/api/orders/{self.order_id}/execute_order/', {
                'execution_price': '336.00', 'timestamp': _fresh_ts(),
                'block_hash': block_hash_for(777)}, format='json')  # a different block
        self.assertEqual(retry.data['status'], 'already_executed')
        self.assertEqual(other.status_code, status.HTTP_400_BAD_REQUEST)
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
        self.node_client = ConsensusClient(); self.node_client.force_authenticate(user=self.node)
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

    def test_empty_book_reported_as_nan_uses_last_trade(self):
        # seen live with the market closed: bid/ask are NaN, only lastPrice is set
        import pandas as pd
        from . import views
        nan = float('nan')
        df = pd.DataFrame([{'strike': 90.0, 'bid': nan, 'ask': nan, 'lastPrice': 12.5}])
        with patch('yfinance.Ticker') as T:
            T.return_value.option_chain.return_value = MagicMock(calls=df, puts=df)
            buy = views._option_market_premium('AAPL', self.expiry, 'CALL', Decimal('90'), 'buy')
        self.assertEqual(buy, Decimal('12.5'))
        df = pd.DataFrame([{'strike': 90.0, 'bid': nan, 'ask': nan, 'lastPrice': nan}])
        with patch('yfinance.Ticker') as T:
            T.return_value.option_chain.return_value = MagicMock(calls=df, puts=df)
            self.assertIsNone(views._option_market_premium('AAPL', self.expiry, 'CALL', Decimal('90'), 'buy'))

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


class ConsensusProofTests(TestCase):
    """execute_order settles only with a quorum of valid node vote signatures."""

    def setUp(self):
        self.user = make_user()
        make_stock('AAPL')
        self.node = make_node()
        self.raw = APIClient(); self.raw.force_authenticate(user=self.node)
        uc = APIClient(); uc.force_authenticate(user=self.user)
        r = uc.post('/api/orders/', {'stock': 'AAPL', 'order_type': 'BUY', 'quantity': '1',
                                     'nonce': 'proof1'}, format='json')
        self.oid = r.data['id']
        uc.post(f'/api/orders/{self.oid}/submit/')
        ensure_voters()
        _patch_oracle(self)
        self.hash = block_hash_for(self.oid)

    def _execute(self, votes, price='100.00', block_hash=None):
        return self.raw.post(f'/api/orders/{self.oid}/execute_order/', {
            'execution_price': price, 'timestamp': _fresh_ts(),
            'block_hash': block_hash or self.hash, 'votes': votes}, format='json')

    def _status(self):
        return Order.objects.get(pk=self.oid).status

    def test_quorum_settles_and_records_block_hash(self):
        r = self._execute(votes_for(self.oid, self.hash, '100.00'))
        self.assertEqual(r.status_code, 200)
        order = Order.objects.get(pk=self.oid)
        self.assertEqual((order.status, order.block_hash), ('CONFIRMED', self.hash))

    def test_retry_of_settled_block_is_idempotent(self):
        votes = votes_for(self.oid, self.hash, '100.00')
        self.assertEqual(self._execute(votes).status_code, 200)
        balance = Wallet.objects.get(user=self.user).balance
        r = self._execute(votes)             # the Leader's retry after a timeout
        self.assertEqual(r.status_code, 200)
        self.assertEqual(r.data['status'], 'already_executed')
        self.assertEqual(Wallet.objects.get(user=self.user).balance, balance)   # not charged twice

    def test_retry_with_a_different_block_is_refused(self):
        self._execute(votes_for(self.oid, self.hash, '100.00'))
        other = block_hash_for(12345)
        r = self._execute(votes_for(self.oid, other, '100.00'), block_hash=other)
        self.assertEqual(r.status_code, status.HTTP_400_BAD_REQUEST)

    def test_single_node_cannot_settle(self):
        r = self._execute(votes_for(self.oid, self.hash, '100.00', voters=('voter-a',)))
        self.assertEqual(r.status_code, status.HTTP_403_FORBIDDEN)
        self.assertEqual(self._status(), 'SUBMITTED')

    def test_duplicate_votes_from_one_node_count_once(self):
        v = votes_for(self.oid, self.hash, '100.00', voters=('voter-a',))
        self.assertEqual(self._execute(v + v).status_code, status.HTTP_403_FORBIDDEN)

    def test_votes_for_a_different_price_are_rejected(self):
        r = self._execute(votes_for(self.oid, self.hash, '100.00'), price='99.00')
        self.assertEqual(r.status_code, status.HTTP_403_FORBIDDEN)
        self.assertEqual(self._status(), 'SUBMITTED')

    def test_votes_for_a_different_block_are_rejected(self):
        other = block_hash_for(999)
        r = self._execute(votes_for(self.oid, other, '100.00'))
        self.assertEqual(r.status_code, status.HTTP_403_FORBIDDEN)

    def test_votes_from_non_node_accounts_are_ignored(self):
        from .models import NodeKey
        User.objects.get(username='voter-b').groups.clear()
        r = self._execute(votes_for(self.oid, self.hash, '100.00'))
        self.assertEqual(r.status_code, status.HTTP_403_FORBIDDEN)
        self.assertTrue(NodeKey.objects.filter(user__username='voter-b').exists())

    def test_garbage_votes_rejected(self):
        for votes in (None, 'x', [1, 2], [{'node': 'voter-a', 'signature': '!!'}]):
            self.assertEqual(self._execute(votes).status_code, status.HTTP_403_FORBIDDEN)


class GoVoteCompatibilityTests(TestCase):
    """A vote signed by the Go node code (nodekey.go:signVote, fixed seed 0..31)
    must verify here — this pins the cross-language vote format."""

    GO_PUB  = '03a107bff3ce10be1d70dd18e74bc09967e4d6309ba50d5f1ddc8664125531b8'
    GO_HASH = 'ab' + '0' * 62
    GO_SIG  = '7Sugab3pVdIAGvRsEO3GcYP+fEvuiWs4ycLiVWRPcCg8h7+KlnqCBgcLogMhn8Nm9n7KI0avRH04eThXX467DA=='

    def test_go_signed_vote_verifies(self):
        from .consensus import count_valid_votes
        from .models import NodeKey
        NodeKey.objects.create(user=make_node('go-node'), public_key=self.GO_PUB)
        votes = [{'node': 'go-node', 'signature': self.GO_SIG}]
        self.assertEqual(count_valid_votes(42, self.GO_HASH, '187.25', votes), 1)
        self.assertEqual(count_valid_votes(42, self.GO_HASH, '187.26', votes), 0)


class NodeKeyRegistrationTests(TestCase):

    def setUp(self):
        self.node = make_node()
        self.client = APIClient(); self.client.force_authenticate(user=self.node)
        self.key = _raw_public_hex(Ed25519PrivateKey.generate())

    def test_first_registration_then_idempotent(self):
        self.assertEqual(self.client.post('/api/node-key/', {'public_key': self.key}, format='json').status_code, 201)
        self.assertEqual(self.client.post('/api/node-key/', {'public_key': self.key}, format='json').status_code, 200)

    def test_different_key_refused_after_first_use(self):
        self.client.post('/api/node-key/', {'public_key': self.key}, format='json')
        other = _raw_public_hex(Ed25519PrivateKey.generate())
        self.assertEqual(self.client.post('/api/node-key/', {'public_key': other}, format='json').status_code, 409)

    def test_regular_user_cannot_register(self):
        c = APIClient(); c.force_authenticate(user=make_user())
        self.assertEqual(c.post('/api/node-key/', {'public_key': self.key}, format='json').status_code, 403)

    def test_malformed_key_rejected(self):
        for bad in ('', 'zz', 'ab' * 31, 'ab' * 33):
            self.assertEqual(self.client.post('/api/node-key/', {'public_key': bad}, format='json').status_code, 400)


class StockStopLossTests(TestCase):
    """Stock SL/TP levels sell only shares that are still held and unsold."""

    def setUp(self):
        self.user = make_user()
        self.stock = make_stock('AAPL')
        self.pos = Position.objects.create(user=self.user, stock=self.stock, quantity=Decimal('10'))

    def _level(self, kind, price, qty):
        return SLTPLevel.objects.create(position=self.pos, level_type=kind,
                                        price=Decimal(price), quantity=Decimal(qty))

    def _run_at(self, price):
        from . import sltp
        with patch.object(sltp, '_oracle_price', return_value=Decimal(price)):
            sltp._check_stocks()

    def _sells(self):
        return list(Order.objects.filter(user=self.user, order_type='SELL').values_list('quantity', flat=True))

    def test_trigger_creates_signed_sell(self):
        self._level('SL', '95', '4')
        self._run_at('90')
        order = Order.objects.get(user=self.user)
        self.assertEqual((order.status, order.quantity), ('SUBMITTED', Decimal('4.0000')))
        self.assertTrue(verify_signature(self.user.profile.ecdsa_public_key,
                                         order_signing_payload(order), order.signature))

    def test_quantity_capped_at_current_holding(self):
        lv = self._level('SL', '95', '10')
        self.pos.quantity = Decimal('3')          # user sold 7 shares manually
        self.pos.save()
        self._run_at('90')
        self.assertEqual(self._sells(), [Decimal('3.0000')])
        lv.refresh_from_db()
        self.assertTrue(lv.triggered)

    def test_shares_already_pending_sale_are_not_sold_twice(self):
        # at $90 both the stop (<= 95) and the target (>= 80) are reached;
        # together they must not sell more than the 10 shares held
        self._level('SL', '95', '10')
        self._level('TP', '80', '10')
        self._run_at('90')
        self.assertEqual(sum(self._sells()), Decimal('10'))

    def test_nothing_left_marks_level_without_ordering(self):
        lv = self._level('SL', '95', '5')
        self.pos.quantity = Decimal('0')
        self.pos.save()
        self._run_at('90')
        self.assertEqual(self._sells(), [])
        lv.refresh_from_db()
        self.assertTrue(lv.triggered)


class OptionExpiryTests(TestCase):

    def test_expired_option_settles_at_expiry_close(self):
        from . import sltp
        user = make_user()
        stock = make_stock('AAPL')
        expiry = timezone.now().date() - timedelta(days=10)
        pos = OptionPosition.objects.create(
            user=user, stock=stock, contract_type='CALL', strike=Decimal('100'),
            expiry=expiry, contracts=1, premium_paid=Decimal('2'))
        with patch.object(sltp, '_expiry_price', return_value=Decimal('105')) as ep, \
             patch.object(sltp, '_oracle_price', return_value=Decimal('150')):
            sltp._check_options()
        ep.assert_called_once_with('AAPL', expiry)
        pos.refresh_from_db()
        self.assertEqual((pos.status, pos.close_premium), ('EXERCISED', Decimal('5.0000')))
        self.assertEqual(Wallet.objects.get(user=user).balance, Decimal('10500.00'))

    def test_old_expiry_without_history_is_not_settled_at_todays_price(self):
        from . import sltp
        with patch('yfinance.Ticker', side_effect=Exception('offline')), \
             patch.object(sltp, '_oracle_price', return_value=Decimal('150')):
            self.assertIsNone(sltp._expiry_price('AAPL', timezone.now().date() - timedelta(days=10)))


class CFDLiquidationTests(TestCase):

    def setUp(self):
        self.user = make_user()
        self.stock = make_stock('AAPL')
        # $100 entry x 10 at 10x → $100 margin; liquidation at a $80 loss = $92
        self.pos = CFDPosition.objects.create(
            user=self.user, stock=self.stock, direction='LONG', quantity=Decimal('10'),
            entry_price=Decimal('100'), leverage=10, margin_used=Decimal('100'))

    def _run_at(self, price):
        from . import sltp
        with patch.object(sltp, '_oracle_price', return_value=Decimal(price)):
            sltp._check_cfd_margins()

    def test_liquidation_price(self):
        from .sltp import liquidation_price
        self.assertEqual(liquidation_price(self.pos), Decimal('92'))
        short = CFDPosition(direction='SHORT', quantity=Decimal('10'), entry_price=Decimal('100'),
                            margin_used=Decimal('100'))
        self.assertEqual(liquidation_price(short), Decimal('108'))

    def test_no_liquidation_above_threshold(self):
        self._run_at('93')
        self.assertFalse(Order.objects.filter(trade_type='CFD_CLOSE').exists())

    def test_breach_creates_one_signed_full_close(self):
        self._run_at('91')
        self._run_at('90')          # next monitor pass: already closing, no duplicate
        close = Order.objects.get(trade_type='CFD_CLOSE')
        self.assertEqual((close.quantity, close.position_id, close.status),
                         (Decimal('10.0000'), self.pos.id, 'SUBMITTED'))

    def test_api_reports_liquidation_price(self):
        c = APIClient(); c.force_authenticate(user=self.user)
        self.assertEqual(c.get('/api/cfd/').data['cfd_positions'][0]['liquidation_price'], '92.00')


class OrderSLTPTests(TestCase):
    """SL/TP given with an order are signed with it and attached on settlement."""

    def setUp(self):
        self.user = make_user()
        self.stock = make_stock('AAPL')
        self.uc = APIClient(); self.uc.force_authenticate(user=self.user)
        self.node_client = ConsensusClient(); self.node_client.force_authenticate(user=make_node())
        _patch_oracle(self)

    def _place(self, **extra):
        body = {'stock': 'AAPL', 'order_type': 'BUY', 'quantity': '10', 'nonce': extra.pop('nonce', 'sl1')}
        body.update(extra)
        return self.uc.post('/api/orders/', body, format='json')

    def _settle(self, oid):
        self.uc.post(f'/api/orders/{oid}/submit/')
        return self.node_client.post(f'/api/orders/{oid}/execute_order/', {
            'execution_price': '100.00', 'timestamp': _fresh_ts()}, format='json')

    def test_stock_buy_levels_created_on_settlement(self):
        r = self._place(stop_loss='90', take_profit='120', take_profit_qty='4')
        self.assertEqual(r.status_code, 201, r.data)
        self.assertEqual(SLTPLevel.objects.count(), 0)          # nothing before consensus
        self.assertEqual(self._settle(r.data['id']).status_code, 200)
        levels = {l.level_type: l for l in SLTPLevel.objects.filter(position__user=self.user)}
        self.assertEqual((levels['SL'].price, levels['SL'].quantity), (Decimal('90'), Decimal('10')))
        self.assertEqual((levels['TP'].price, levels['TP'].quantity), (Decimal('120'), Decimal('4')))

    def test_cfd_levels_attach_to_the_new_position(self):
        r = self._place(nonce='sl2', trade_type='CFD', leverage=5, order_type='SELL',
                        stop_loss='110', take_profit='80')
        self.assertEqual(r.status_code, 201, r.data)
        self._settle(r.data['id'])
        cfd = CFDPosition.objects.get(user=self.user)
        self.assertEqual(cfd.sltp_levels.count(), 2)

    def test_sltp_is_covered_by_the_signature(self):
        r = self._place(stop_loss='90')
        self.uc.post(f"/api/orders/{r.data['id']}/submit/")
        order = Order.objects.get(pk=r.data['id'])
        tampered = order_signing_payload(order) | {'stop_loss': Decimal('1')}
        self.assertFalse(verify_signature(self.user.profile.ecdsa_public_key, tampered, order.signature))

    def test_invalid_sltp_rejected(self):
        cases = [
            {'stop_loss': '120', 'take_profit': '110'},             # SL above TP on a long
            {'stop_loss': '90', 'stop_loss_qty': '11'},              # more than the order
            {'stop_loss_qty': '5'},                                  # quantity without price
            {'order_type': 'SELL', 'stop_loss': '90'},               # stock SELL opens nothing
            {'stop_loss': '-1'},
        ]
        for i, extra in enumerate(cases):
            self.assertEqual(self._place(nonce=f'bad{i}', **extra).status_code, 400, extra)


class MarketDataAndAIEndpointTests(TestCase):

    def setUp(self):
        from django.core.cache import cache
        cache.clear()
        self.user = make_user()
        self.client = APIClient(); self.client.force_authenticate(user=self.user)

    def test_unlisted_tickers_never_reach_data_sources(self):
        with patch('yfinance.Ticker') as T, patch('trading.views._get_oracle_stub') as stub:
            for url in ('/api/price/SPY/', '/api/history/%3Cscript%3E/', '/api/options/chain/XYZ/',
                        '/api/ai-news/NOPE/'):
                self.assertEqual(self.client.get(url).status_code, 404, url)
            r = self.client.post('/api/ai-chat/NOPE/', {'question': 'hi'}, format='json')
            self.assertEqual(r.status_code, 404)
        T.assert_not_called()
        stub.assert_not_called()

    def test_data_source_errors_are_not_echoed(self):
        with patch('trading.views._get_oracle_stub', side_effect=Exception('secret internal path /etc/x')):
            r = self.client.get('/api/price/AAPL/')
        self.assertEqual(r.status_code, 503)
        self.assertNotIn('/etc/x', r.data['error'])

    def test_chat_input_validation(self):
        with self.settings(GEMINI_API_KEY='k'):
            long_q = self.client.post('/api/ai-chat/AAPL/', {'question': 'x' * 1001}, format='json')
            bad_hist = self.client.post('/api/ai-chat/AAPL/', {'question': 'hi', 'history': 'x'}, format='json')
        self.assertEqual(long_q.status_code, 400)
        self.assertEqual(bad_hist.status_code, 400)

    def test_chat_ignores_malformed_history_entries(self):
        import sys, types
        genai = types.ModuleType('google.genai')
        client = MagicMock()
        client.models.generate_content.return_value = MagicMock(text=' answer ')
        genai.Client = MagicMock(return_value=client)
        genai.errors = types.SimpleNamespace(ServerError=type('ServerError', (Exception,), {}))
        genai.types = types.SimpleNamespace(GenerateContentConfig=MagicMock())
        import yfinance  # noqa: F401 — import heavy deps before patching sys.modules
        modules = {'google.genai': genai,
                   'google.genai.errors': genai.errors, 'google.genai.types': genai.types}
        history = [{'role': 'user'}, 'junk', 5, {'role': 'model', 'text': 'ok'}]
        with patch.dict(sys.modules, modules), self.settings(GEMINI_API_KEY='k'), \
             patch('yfinance.Ticker', side_effect=Exception('offline')):
            r = self.client.post('/api/ai-chat/AAPL/', {'question': 'hi', 'history': history}, format='json')
        self.assertEqual(r.status_code, 200, r.data)
        self.assertEqual(r.data['answer'], 'answer')
        sent = client.models.generate_content.call_args.kwargs['contents']
        self.assertEqual(len(sent), 2)          # the one valid history entry + the question

    def test_registration_is_rate_limited(self):
        anon = APIClient()
        codes = [anon.post('/api/register/', {'username': f'u{i}', 'password': 'Tr4de-desk-1'},
                           format='json').status_code for i in range(25)]
        self.assertIn(429, codes)


class SmallFixesTests(TestCase):

    def setUp(self):
        self.user = make_user()
        self.stock = make_stock('AAPL')
        self.client = APIClient(); self.client.force_authenticate(user=self.user)

    def test_triggered_level_cannot_be_deleted(self):
        pos = Position.objects.create(user=self.user, stock=self.stock, quantity=Decimal('5'))
        lv = SLTPLevel.objects.create(position=pos, level_type='SL', price=Decimal('90'),
                                      quantity=Decimal('5'), triggered=True)
        self.assertEqual(self.client.delete(f'/api/sltp/{lv.id}/').status_code, 400)
        self.assertTrue(SLTPLevel.objects.filter(pk=lv.id).exists())

    def test_reject_reason_is_stored_and_exposed(self):
        r = self.client.post('/api/orders/', {'stock': 'AAPL', 'order_type': 'BUY', 'quantity': '1',
                                              'nonce': 'rr1'}, format='json')
        self.client.post(f"/api/orders/{r.data['id']}/submit/")
        node = APIClient(); node.force_authenticate(user=make_node())
        node.post(f"/api/orders/{r.data['id']}/reject_order/", {'reason': 'consensus not reached (1 approvals)'},
                  format='json')
        self.assertEqual(self.client.get(f"/api/orders/{r.data['id']}/").data['reject_reason'],
                         'consensus not reached (1 approvals)')

    def test_intraday_history_keeps_the_time(self):
        import pandas as pd
        idx = pd.to_datetime(['2026-09-22 14:30', '2026-09-22 14:35']).tz_localize('UTC')
        df = pd.DataFrame({'Open': [1, 2], 'High': [1, 2], 'Low': [1, 2], 'Close': [1, 2]}, index=idx)
        with patch('yfinance.Ticker') as T:
            T.return_value.history.return_value = df
            r = self.client.get('/api/history/AAPL/?period=1d&interval=5m')
        dates = [p['date'] for p in r.data['prices']]
        self.assertEqual(len(set(dates)), 2)
        self.assertIn('14:35', dates[1])

    def test_history_skips_incomplete_bars(self):
        import pandas as pd
        idx = pd.to_datetime(['2026-09-21', '2026-09-22']).tz_localize('UTC')
        nan = float('nan')
        df = pd.DataFrame({'Open': [1.0, nan], 'High': [1.0, nan], 'Low': [1.0, nan],
                           'Close': [1.0, nan]}, index=idx)
        with patch('yfinance.Ticker') as T:
            T.return_value.history.return_value = df
            r = self.client.get('/api/history/AAPL/')
        self.assertEqual(r.status_code, 200)
        self.assertEqual([p['date'] for p in r.data['prices']], ['2026-09-21'])

    def test_oracle_channel_is_reused(self):
        from . import oracle_client
        oracle_client._client = None
        with patch('grpc.insecure_channel') as ch:
            first, second = oracle_client.get_stub(), oracle_client.get_stub()
        self.assertIs(first, second)
        ch.assert_called_once()
        oracle_client._client = None
