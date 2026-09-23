from rest_framework import viewsets, status
from rest_framework.decorators import api_view, permission_classes, action
from rest_framework.permissions import IsAuthenticated
from rest_framework.response import Response
from rest_framework.exceptions import ValidationError
from django.utils import timezone
from django.utils.dateparse import parse_datetime
from django.db import transaction, IntegrityError
from django.db.models import F
from django.core.exceptions import ValidationError as DjangoValidationError
from django.contrib.auth.password_validation import validate_password
from datetime import timedelta
from decimal import Decimal, InvalidOperation
from .models import Order, Wallet, Position, Stock, UserProfile, CFDPosition, SLTPLevel, OptionPosition, NodeKey
from .roles import is_consensus_node
from .consensus import QUORUM, count_valid_votes, valid_public_key
from .serializers import OrderSerializer, SLTPLevelSerializer, DepositSerializer
from .crypto_utils import generate_key_pair, sign_order, verify_signature, order_signing_payload
from django.contrib.auth.models import User
from rest_framework.permissions import IsAuthenticated, AllowAny

# ============================================================
# S&P 500 Whitelist — list of tickers allowed for trading
# ============================================================
SP500_TICKERS = {
    'AAPL', 'MSFT', 'GOOGL', 'AMZN', 'NVDA', 'META', 'TSLA', 'BRK.B',
    'UNH', 'LLY', 'JPM', 'XOM', 'V', 'AVGO', 'PG', 'MA', 'HD', 'COST',
    'MRK', 'CVX', 'ABBV', 'ORCL', 'WMT', 'BAC', 'KO', 'PFE', 'NFLX',
    'CRM', 'AMD', 'TMO', 'ACN', 'MCD', 'LIN', 'CSCO', 'TXN', 'ADBE',
    'DHR', 'NEE', 'NKE', 'INTC', 'PM', 'UPS', 'AMGN', 'HON', 'LOW',
    'IBM', 'SBUX', 'QCOM', 'GE', 'CAT', 'GS', 'MS', 'BLK', 'SPGI',
}

# Max allowed gap between the leader-reported execution price and Django's own
# live Oracle lookup at settlement. Wide enough to absorb genuine market movement
# in the seconds since the order was priced, tight enough to catch manipulation.
ORACLE_PRICE_TOLERANCE = Decimal('0.02')  # 2%


_is_consensus_node = is_consensus_node


# ============================================================
# 1. Order management
# ============================================================
class OrderViewSet(viewsets.ModelViewSet):
    serializer_class = OrderSerializer
    permission_classes = [IsAuthenticated]
    # Orders are append-only: once created they change only through the
    # submit / execute_order / reject_order lifecycle actions. Allowing PUT,
    # PATCH or DELETE would let a user rewrite a signed order or its history.
    http_method_names = ['get', 'post', 'head', 'options']

    def get_queryset(self):
        # Node (staff) sees all orders — regular user sees only their own.
        # select_related avoids N+1 queries when serializing user and stock fields.
        qs = Order.objects.select_related('user', 'stock', 'user__profile')
        if _is_consensus_node(self.request.user):
            return qs
        return qs.filter(user=self.request.user)

    def perform_create(self, serializer):
        stock = serializer.validated_data['stock']
        # S&P 500 whitelist check
        if stock.ticker not in SP500_TICKERS:
            raise ValidationError(
                {"error": f"Ticker {stock.ticker} is not in the S&P 500 index and is not allowed for trading."}
            )
        # A close/exercise order must reference an open position the user owns,
        # on the same stock the order (and later its signature) names.
        trade_type  = serializer.validated_data.get('trade_type', 'STOCK')
        position_id = serializer.validated_data.get('position_id')
        if trade_type == 'CFD_CLOSE':
            if not CFDPosition.objects.filter(pk=position_id, user=self.request.user,
                                              stock=stock, is_open=True).exists():
                raise ValidationError({"position_id": "No open CFD position with this id for this stock."})
        elif trade_type in ('OPT_CLOSE', 'OPT_EXER'):
            pos = OptionPosition.objects.filter(pk=position_id, user=self.request.user,
                                                stock=stock, status='OPEN').first()
            if pos is None:
                raise ValidationError({"position_id": "No open option position with this id for this stock."})
            if trade_type == 'OPT_EXER' and pos.expiry < timezone.now().date():
                raise ValidationError({"position_id": "This option has expired and can no longer be exercised."})

        # Orders are always created as DRAFT — the user signs and submits via /submit
        serializer.save(user=self.request.user, status='DRAFT')

    # ----------------------------------------------------------------
    # submit — user signs the order and changes status to SUBMITTED
    # ----------------------------------------------------------------
    @action(detail=True, methods=['post'])
    def submit(self, request, pk=None):
        order = self.get_object()

        if order.status != 'DRAFT':
            return Response(
                {"error": f"Only DRAFT orders can be submitted. Current status: {order.status}"},
                status=status.HTTP_400_BAD_REQUEST
            )

        # verify the user has a UserProfile with keys
        try:
            profile = request.user.profile
        except UserProfile.DoesNotExist:
            return Response(
                {"error": "No ECDSA keys found for this user. The account may have been created before this feature was added."},
                status=status.HTTP_400_BAD_REQUEST
            )

        # sign every trade-defining field of the order
        order_data = order_signing_payload(order)
        signature = sign_order(profile.ecdsa_private_key, order_data)

        # self-verify (confirm the signature is valid before saving)
        if not verify_signature(profile.ecdsa_public_key, order_data, signature):
            return Response(
                {"error": "Internal error: the generated signature failed verification."},
                status=status.HTTP_500_INTERNAL_SERVER_ERROR
            )

        order.signature = signature
        order.status = 'SUBMITTED'
        order.save()

        return Response({
            "status":    "submitted",
            "message":   f"Order #{order.id} has been signed and submitted for consensus.",
            "order_id":  order.id,
            "signature": signature[:32] + "...",  # partial display only
        })

    # ----------------------------------------------------------------
    # execute_order — called once by the Leader node after gRPC consensus is reached
    # ----------------------------------------------------------------
    # ----------------------------------------------------------------
    # reject_order — the Leader reports that consensus could not be reached
    # ----------------------------------------------------------------
    @action(detail=True, methods=['post'])
    def reject_order(self, request, pk=None):
        # Only the consensus nodes may reject an order, never a regular user
        if not _is_consensus_node(request.user):
            return Response(
                {"error": "Only consensus nodes may reject orders."},
                status=status.HTTP_403_FORBIDDEN
            )

        reason = str(request.data.get('reason', 'consensus not reached'))[:200]

        with transaction.atomic():
            order = Order.objects.select_for_update().get(pk=self.get_object().pk)

            if order.status != 'SUBMITTED':
                return Response(
                    {"error": f"Only SUBMITTED orders can be rejected. Current status: {order.status}"},
                    status=status.HTTP_400_BAD_REQUEST
                )

            order.status = 'REJECTED'
            order.save()

        return Response({"status": "rejected", "order_id": order.id, "reason": reason})

    @action(detail=True, methods=['post'])
    def execute_order(self, request, pk=None):
        # Only the consensus nodes may settle an order. Without this check a user
        # could execute their own SUBMITTED order directly and skip consensus.
        if not _is_consensus_node(request.user):
            return Response(
                {"error": "Only consensus nodes may execute orders."},
                status=status.HTTP_403_FORBIDDEN
            )

        execution_price_raw = request.data.get('execution_price')
        oracle_timestamp    = request.data.get('timestamp')
        block_hash          = request.data.get('block_hash')

        if not execution_price_raw or not oracle_timestamp:
            return Response(
                {"error": "execution_price and timestamp are required."},
                status=status.HTTP_400_BAD_REQUEST
            )

        try:
            execution_price = Decimal(str(execution_price_raw))
        except InvalidOperation:
            return Response({"error": "Invalid execution price."}, status=status.HTTP_400_BAD_REQUEST)
        if not execution_price.is_finite() or execution_price <= 0:
            return Response({"error": "Invalid execution price."}, status=status.HTTP_400_BAD_REQUEST)

        # Proof of consensus: a quorum of node signatures over exactly this
        # order, block and price. Without it the caller is just one node.
        if count_valid_votes(pk, block_hash, str(execution_price_raw),
                             request.data.get('votes')) < QUORUM:
            return Response(
                {"error": f"Consensus proof missing: at least {QUORUM} valid node votes are required."},
                status=status.HTTP_403_FORBIDDEN,
            )

        snapshot = self.get_object()

        # Idempotent retry: the Leader re-sends the same certified block when it
        # could not tell whether an earlier attempt went through (timeout,
        # crash). Report success instead of refusing, so it can finish the
        # commit. Checked before the staleness check — a retry may be late.
        if snapshot.status == 'CONFIRMED' and snapshot.block_hash == block_hash:
            return Response({"status": "already_executed", "order_id": snapshot.id,
                             "execution_price": str(snapshot.execution_price)})

        # stale-price check
        oracle_time = parse_datetime(str(oracle_timestamp))
        if not oracle_time:
            return Response({"error": "Invalid timestamp format."}, status=status.HTTP_400_BAD_REQUEST)

        age = timezone.now() - oracle_time
        if age > timedelta(seconds=60):
            return Response(
                {"error": f"Stale data: price is {int(age.total_seconds())} seconds old (limit: 60s)."},
                status=status.HTTP_400_BAD_REQUEST
            )

        # ── network I/O first, outside the transaction ─────────────
        # Oracle and option-chain lookups can take seconds; doing them while
        # holding row locks would stall every other writer.
        if snapshot.status != 'SUBMITTED':
            return Response(
                {"error": f"Only SUBMITTED orders can be executed. Current status: {snapshot.status}"},
                status=status.HTTP_400_BAD_REQUEST
            )

        # Independent price re-verification. Django is the component that
        # actually moves money, so it must not take the leader's word for the
        # execution price. The nodes agree the price is honest, but nothing
        # binds that agreed price to what the leader then sends here — a
        # compromised leader could get an honest price approved by consensus
        # and settle at another. Re-query the Oracle and reject a divergent
        # price. Fail closed: if the price can't be verified, don't settle.
        try:
            live_price = _oracle_live_price(snapshot.stock_id)
        except Exception:
            return Response(
                {"error": "Cannot verify execution price: Oracle unavailable."},
                status=status.HTTP_503_SERVICE_UNAVAILABLE,
            )

        option_quote = None
        if snapshot.trade_type in ('OPTION', 'OPT_CLOSE'):
            option_quote = _quote_for_order(snapshot)

        # ── settle under row locks ─────────────────────────────────
        with transaction.atomic():
            order = (Order.objects.select_for_update()
                     .select_related('user__profile', 'stock')
                     .get(pk=snapshot.pk))

            if order.status != 'SUBMITTED':
                return Response(
                    {"error": f"Only SUBMITTED orders can be executed. Current status: {order.status}"},
                    status=status.HTTP_400_BAD_REQUEST
                )

            # The order must still be exactly what the user signed. Consensus
            # verified the signature, but the row could have been altered since.
            profile = getattr(order.user, 'profile', None)
            if not (profile and order.signature and verify_signature(
                    profile.ecdsa_public_key, order_signing_payload(order), order.signature)):
                return _reject(order, "Order no longer matches the user's signature.")

            divergence = abs(execution_price - live_price) / live_price
            if divergence > ORACLE_PRICE_TOLERANCE:
                return _reject(
                    order,
                    f"Execution price ${execution_price} diverges "
                    f"{divergence * 100:.2f}% from live ${live_price} "
                    f"(limit {ORACLE_PRICE_TOLERANCE * 100:.0f}%)."
                )

            # limit-price check
            if order.limit_price:
                if order.order_type == 'BUY' and execution_price > order.limit_price:
                    return _reject(order, f"Market price ${execution_price} exceeds limit price ${order.limit_price}.")
                if order.order_type == 'SELL' and execution_price < order.limit_price:
                    return _reject(order, f"Market price ${execution_price} is below limit price ${order.limit_price}.")

            wallet = Wallet.objects.select_for_update().get(user_id=order.user_id)
            order.block_hash = block_hash
            settle = _SETTLERS.get(order.trade_type, _settle_stock)
            return settle(order, wallet, execution_price, option_quote)


# ============================================================
# 1b. Settlement — one function per trade type. Each runs inside the
#     execute_order transaction with the order and wallet rows locked.
# ============================================================
def _reject(order, error, http_status=status.HTTP_400_BAD_REQUEST):
    order.status = 'REJECTED'
    order.block_hash = None
    order.save()
    return Response({"error": error}, status=http_status)


def _confirm(order, execution_price):
    order.execution_price = execution_price
    order.status = 'CONFIRMED'
    order.save()


def _settle_cfd(order, wallet, execution_price, _quote):
    leverage  = order.leverage or 1
    notional  = order.quantity * execution_price
    margin    = (notional / leverage).quantize(Decimal('0.0001'))
    direction = 'LONG' if order.order_type == 'BUY' else 'SHORT'

    if wallet.balance < margin:
        return _reject(order, f"Insufficient margin: need ${margin}, have ${wallet.balance}.")

    wallet.balance -= margin
    wallet.save()
    CFDPosition.objects.create(
        user=order.user, stock=order.stock,
        direction=direction, quantity=order.quantity,
        entry_price=execution_price, leverage=leverage,
        margin_used=margin,
    )
    _confirm(order, execution_price)
    return Response({
        "status":      "success",
        "message":     "CFD position opened after gRPC consensus.",
        "direction":   direction,
        "entry_price": str(execution_price),
        "notional":    str(notional),
        "leverage":    leverage,
        "margin_used": str(margin),
    })


def _settle_cfd_close(order, wallet, execution_price, _quote):
    """Close a CFD position, fully or partially."""
    try:
        pos = CFDPosition.objects.select_for_update().get(
            pk=order.position_id, user=order.user, is_open=True
        )
    except CFDPosition.DoesNotExist:
        return _reject(order, 'CFD position not found or already closed')

    # An SL/TP level may close only part of the position, so honour the
    # order quantity and never close more than the position still holds.
    close_qty = min(order.quantity, pos.quantity) if order.quantity else pos.quantity
    if close_qty <= 0:
        return _reject(order, 'Close quantity must be greater than zero.')

    partial_margin = (pos.margin_used * (close_qty / pos.quantity)).quantize(Decimal('0.0001'))
    pnl = (execution_price - pos.entry_price) * close_qty if pos.direction == 'LONG' \
          else (pos.entry_price - execution_price) * close_qty
    cash_returned = max(partial_margin + pnl, Decimal('0'))

    wallet.balance += cash_returned
    wallet.save()

    pos.quantity    -= close_qty
    pos.margin_used -= partial_margin
    pos.pnl          = (pos.pnl or Decimal('0')) + pnl
    if pos.quantity <= 0:
        pos.is_open     = False
        pos.close_price = execution_price
        pos.closed_at   = timezone.now()
    pos.save()

    _confirm(order, execution_price)
    return Response({
        'status':    'success',
        'message':   'CFD position closed after gRPC consensus.',
        'direction': pos.direction,
        'quantity':  str(close_qty),
        'remaining': str(pos.quantity),
        'pnl':       str(pnl),
    })


def _settle_option_close(order, wallet, execution_price, quote):
    """Close an option position (sell at market premium)."""
    try:
        pos = OptionPosition.objects.select_for_update().get(
            pk=order.position_id, user=order.user, status='OPEN'
        )
    except OptionPosition.DoesNotExist:
        return _reject(order, 'Option position not found or already closed')

    if quote is None:
        # Fail closed: without a market quote we cannot know the time value, and
        # silently settling at intrinsic value would short-change the user. The
        # order stays SUBMITTED so the Leader retries once quotes are back.
        return Response({'error': 'No market quote for this option right now — try again shortly.'},
                        status=status.HTTP_503_SERVICE_UNAVAILABLE)

    # yfinance quotes are not verified by consensus, but the stock price is.
    # An option is never worth less than its intrinsic value, so floor the
    # premium there — a stale or bad quote cannot underpay the user.
    close_premium = max(quote, _intrinsic(pos.contract_type, pos.strike, execution_price))

    total_cost    = pos.premium_paid * pos.contracts * 100
    cash_received = close_premium * pos.contracts * 100
    pnl           = cash_received - total_cost

    wallet.balance += cash_received
    wallet.save()

    pos.status        = 'CLOSED'
    pos.pnl           = pnl
    pos.close_premium = close_premium
    pos.closed_at     = timezone.now()
    pos.save()

    _confirm(order, close_premium)
    return Response({
        'status':  'success',
        'message': 'Option position closed after gRPC consensus.',
        'pnl':     str(pnl),
    })


def _settle_option_exercise(order, wallet, execution_price, _quote):
    """Exercise an option (cash-settled at intrinsic value)."""
    try:
        pos = OptionPosition.objects.select_for_update().get(
            pk=order.position_id, user=order.user, status='OPEN'
        )
    except OptionPosition.DoesNotExist:
        return _reject(order, 'Option position not found or already closed')
    if pos.expiry < timezone.now().date():
        return _reject(order, 'Option has expired and can no longer be exercised.')

    intrinsic = _intrinsic(pos.contract_type, pos.strike, execution_price)
    if intrinsic <= 0:
        return _reject(order, 'Option is out of the money (intrinsic value ≤ 0)')

    total_cost    = pos.premium_paid * pos.contracts * 100
    cash_received = intrinsic * pos.contracts * 100
    pnl           = cash_received - total_cost

    wallet.balance += cash_received
    wallet.save()

    pos.status        = 'EXERCISED'
    pos.pnl           = pnl
    pos.close_premium = intrinsic
    pos.closed_at     = timezone.now()
    pos.save()

    _confirm(order, execution_price)
    return Response({
        'status':    'success',
        'message':   'Option exercised after gRPC consensus.',
        'intrinsic': str(intrinsic),
        'pnl':       str(pnl),
    })


def _settle_option_open(order, wallet, execution_price, premium):
    """Buy an option — the premium is fetched server-side, never from the leader."""
    if premium is None:
        return _reject(
            order,
            f'No market price for {order.stock_id} {order.option_contract_type} '
            f'@{order.option_strike} exp {order.option_expiry}',
            http_status=status.HTTP_503_SERVICE_UNAVAILABLE,
        )
    # Floor at intrinsic value from the consensus-verified stock price, so a
    # stale quote below intrinsic cannot sell the user free money.
    premium    = max(premium, _intrinsic(order.option_contract_type, order.option_strike, execution_price))
    contracts  = int(order.quantity)
    total_cost = premium * contracts * 100

    if wallet.balance < total_cost:
        return _reject(order, f'Insufficient funds: need ${total_cost:.2f}, have ${wallet.balance:.2f}')
    wallet.balance -= total_cost
    wallet.save()

    OptionPosition.objects.create(
        user=order.user, stock=order.stock,
        contract_type=order.option_contract_type,
        strike=order.option_strike,
        expiry=order.option_expiry,
        contracts=contracts,
        premium_paid=premium,
    )
    _confirm(order, premium)
    return Response({
        'status':     'success',
        'message':    'Option position opened after gRPC consensus.',
        'premium':    str(premium),
        'total_cost': str(total_cost),
        'contracts':  contracts,
    })


def _settle_stock(order, wallet, execution_price, _quote):
    total_value = order.quantity * execution_price

    if order.order_type == 'BUY':
        if wallet.balance < total_value:
            return _reject(order, f"Insufficient balance: ${wallet.balance} < ${total_value}.")
        wallet.balance -= total_value
        position, _ = Position.objects.select_for_update().get_or_create(user=order.user, stock=order.stock)
        position.quantity += order.quantity
        position.save()
    else:
        try:
            position = Position.objects.select_for_update().get(user=order.user, stock=order.stock)
        except Position.DoesNotExist:
            return _reject(order, "No holdings found for this stock.")
        if position.quantity < order.quantity:
            return _reject(order, f"Insufficient shares: {position.quantity} < {order.quantity}.")
        position.quantity -= order.quantity
        if position.quantity == 0:
            position.delete()
        else:
            position.save()
        wallet.balance += total_value

    wallet.save()
    _confirm(order, execution_price)
    return Response({
        "status":          "success",
        "message":         "Order executed after gRPC consensus.",
        "execution_price": str(execution_price),
        "total_value":     str(total_value),
    })


_SETTLERS = {
    'CFD':       _settle_cfd,
    'CFD_CLOSE': _settle_cfd_close,
    'OPTION':    _settle_option_open,
    'OPT_CLOSE': _settle_option_close,
    'OPT_EXER':  _settle_option_exercise,
    'STOCK':     _settle_stock,
}


def _oracle_live_price(ticker):
    """Live price from the Oracle. Raises if it is unavailable or unusable."""
    stub, oracle_pb2 = _get_oracle_stub()
    resp = stub.GetPrice(oracle_pb2.PriceRequest(ticker=ticker), timeout=5)
    price = Decimal(str(resp.execution_price))
    if not price.is_finite() or price <= 0:
        raise ValueError(f"unusable oracle price {resp.execution_price!r}")
    return price


def _option_market_premium(ticker, expiry, contract_type, strike, side):
    """Per-share market premium for one option contract from yfinance, or None.

    Uses the bid/ask mid when both sides are quoted. With a one-sided book it
    falls back to the side the trade actually crosses (ask for a buy, bid for
    a sell) and only then to the last trade — never to half of one side.
    """
    try:
        import yfinance as yf
        chain = yf.Ticker(ticker).option_chain(expiry.isoformat())
        df    = chain.calls if contract_type == 'CALL' else chain.puts
        row   = df[(df['strike'] - float(strike)).abs() < 0.01]
        if row.empty:
            return None
        row  = row.iloc[0]
        bid  = float(row.get('bid',       0) or 0)
        ask  = float(row.get('ask',       0) or 0)
        last = float(row.get('lastPrice', 0) or 0)
        if bid > 0 and ask > 0:
            price = (bid + ask) / 2
        else:
            price = (ask if side == 'buy' else bid) or last
        if not price or price <= 0:
            return None
        return Decimal(str(round(price, 4)))
    except Exception:
        return None


def _intrinsic(contract_type, strike, stock_price):
    """Per-share intrinsic value of an option at a given stock price."""
    value = stock_price - strike if contract_type == 'CALL' else strike - stock_price
    return max(Decimal('0'), value)


def _quote_for_order(order):
    """Market premium an OPTION / OPT_CLOSE order would settle at, or None."""
    if order.trade_type == 'OPTION':
        return _option_market_premium(order.stock_id, order.option_expiry,
                                      order.option_contract_type, order.option_strike, 'buy')
    pos = OptionPosition.objects.filter(pk=order.position_id, user=order.user).first()
    if pos is None:
        return None
    return _option_market_premium(pos.stock_id, pos.expiry, pos.contract_type, pos.strike, 'sell')


# ============================================================
# 1c. Node key registration
# ============================================================
@api_view(['POST'])
@permission_classes([IsAuthenticated])
def node_key_view(request):
    """A consensus node registers the Ed25519 key it signs votes with.

    Trust on first use: the first key registered for a node is kept. A
    different key later is refused (409) until an admin deletes the old one.
    """
    if not is_consensus_node(request.user):
        return Response({"error": "Only consensus nodes may register keys."},
                        status=status.HTTP_403_FORBIDDEN)
    public_key = str(request.data.get('public_key', '')).lower()
    if not valid_public_key(public_key):
        return Response({"error": "public_key must be a 32-byte Ed25519 key in hex."},
                        status=status.HTTP_400_BAD_REQUEST)
    key, created = NodeKey.objects.get_or_create(user=request.user, defaults={'public_key': public_key})
    if key.public_key != public_key:
        return Response({"error": "A different key is already registered for this node. "
                                  "An admin must remove it before a new key can be registered."},
                        status=status.HTTP_409_CONFLICT)
    return Response({"status": "registered" if created else "unchanged"},
                    status=status.HTTP_201_CREATED if created else status.HTTP_200_OK)


# ============================================================
# 2. Portfolio view
# ============================================================
@api_view(['GET'])
@permission_classes([IsAuthenticated])
def portfolio_view(request):
    try:
        wallet = Wallet.objects.get(user=request.user)
    except Wallet.DoesNotExist:
        return Response({"error": "Wallet not found."}, status=status.HTTP_404_NOT_FOUND)

    positions = Position.objects.filter(user=request.user).select_related('stock')
    holdings = [
        {"id": pos.id, "ticker": pos.stock.ticker, "name": pos.stock.name, "quantity": str(pos.quantity)}
        for pos in positions
    ]

    return Response({
        "username":        request.user.username,
        "usd_balance":     str(wallet.balance),
        "holdings":        holdings,
        "total_positions": len(holdings),
    })


# ============================================================
# 3. User registration
# ============================================================
@api_view(['POST'])
@permission_classes([AllowAny])
def register_view(request):
    username = request.data.get('username')
    password = request.data.get('password')

    if not isinstance(username, str) or not isinstance(password, str) or not username or not password:
        return Response({"error": "username and password are required."}, status=status.HTTP_400_BAD_REQUEST)

    username = username.strip()
    try:
        User._meta.get_field('username').run_validators(username)
    except DjangoValidationError as e:
        return Response({"error": " ".join(e.messages)}, status=status.HTTP_400_BAD_REQUEST)
    try:
        validate_password(password, user=User(username=username))
    except DjangoValidationError as e:
        return Response({"error": " ".join(e.messages)}, status=status.HTTP_400_BAD_REQUEST)

    if User.objects.filter(username=username).exists():
        return Response({"error": "Username already exists."}, status=status.HTTP_400_BAD_REQUEST)

    # User, wallet and keys are created together or not at all, so a failure
    # half-way never leaves an account without a wallet or signing keys.
    private_pem, public_pem = generate_key_pair()
    try:
        with transaction.atomic():
            user = User.objects.create_user(username=username, password=password)
            Wallet.objects.create(user=user, balance=Decimal('10000.00'))
            UserProfile.objects.create(
                user=user,
                ecdsa_private_key=private_pem,
                ecdsa_public_key=public_pem,
            )
    except IntegrityError:
        # two concurrent registrations raced past the exists() check
        return Response({"error": "Username already exists."}, status=status.HTTP_400_BAD_REQUEST)

    return Response(
        {
            "message":    f"User '{username}' created with a $10,000 wallet and ECDSA keys.",
            "username":   username,
            "public_key": public_pem,   # exposed to the user — private key is stored server-side only
        },
        status=status.HTTP_201_CREATED
    )


# ============================================================
# 4. Wallet deposit
# ============================================================
@api_view(['POST'])
@permission_classes([IsAuthenticated])
def deposit_view(request):
    serializer = DepositSerializer(data=request.data)
    if not serializer.is_valid():
        return Response({"error": "Invalid amount.", "details": serializer.errors},
                        status=status.HTTP_400_BAD_REQUEST)
    amount = serializer.validated_data['amount']

    # Atomic increment in SQL — a read-modify-write in Python would lose
    # updates that settle concurrently (orders, SL/TP, option expiry).
    with transaction.atomic():
        Wallet.objects.get_or_create(user=request.user, defaults={'balance': Decimal('0.00')})
        Wallet.objects.filter(user=request.user).update(balance=F('balance') + amount)
    wallet = Wallet.objects.get(user=request.user)

    return Response({"message": f"Successfully deposited ${amount}.", "new_balance": str(wallet.balance)})


# ============================================================
# 5. Price proxy — lets the browser fetch live prices via gRPC oracle
# ============================================================

@api_view(['GET'])
@permission_classes([IsAuthenticated])
def history_view(_request, ticker):
    try:
        import yfinance as yf
        period   = _request.GET.get('period', '1mo')
        interval = _request.GET.get('interval', '1d')

        valid_periods   = {'1d','5d','1mo','3mo','6mo','ytd','1y','2y','5y','10y','max'}
        valid_intervals = {'1m','2m','5m','15m','30m','60m','1h','1d','5d','1wk','1mo','3mo'}

        if period   not in valid_periods:   period   = '1mo'
        if interval not in valid_intervals: interval = '1d'

        # yfinance hard limits: cap period to what each interval supports
        if interval == '1m' and period not in {'1d','5d'}:
            period = '5d'
        elif interval in {'2m','5m','15m','30m','60m','1h'} and period not in {'1d','5d','1mo','3mo','6mo'}:
            period = '1mo'

        data = yf.Ticker(ticker.upper()).history(period=period, interval=interval)
        if data.empty:
            return Response({"error": f"No data for {ticker}"}, status=status.HTTP_404_NOT_FOUND)
        prices = [
            {
                "date":  str(row.Index.date()),
                "open":  round(float(row.Open),  2),
                "high":  round(float(row.High),  2),
                "low":   round(float(row.Low),   2),
                "close": round(float(row.Close), 2),
            }
            for row in data.itertuples()
        ]
        return Response({"ticker": ticker.upper(), "prices": prices})
    except Exception as e:
        return Response({"error": str(e)}, status=status.HTTP_503_SERVICE_UNAVAILABLE)


# ============================================================
# 6. CFD positions list
# ============================================================
@api_view(['GET'])
@permission_classes([IsAuthenticated])
def cfd_positions_view(request):
    positions = CFDPosition.objects.filter(user=request.user).select_related('stock').order_by('-opened_at')
    data = [
        {
            "id":          pos.id,
            "stock":       pos.stock.ticker,
            "direction":   pos.direction,
            "quantity":    str(pos.quantity),
            "entry_price": str(pos.entry_price),
            "leverage":    pos.leverage,
            "margin_used": str(pos.margin_used),
            "is_open":     pos.is_open,
            "opened_at":   pos.opened_at,
            "close_price": str(pos.close_price) if pos.close_price else None,
            "pnl":         str(pos.pnl)         if pos.pnl         is not None else None,
            "closed_at":   pos.closed_at,
        }
        for pos in positions
    ]
    return Response({"cfd_positions": data})




def _get_oracle_stub():
    """Returns a gRPC stub for the Oracle service, importing from the correct path."""
    import sys, os
    oracle_path = os.path.normpath(
        os.path.join(os.path.dirname(__file__), '..', '..', 'oracle_service')
    )
    if oracle_path not in sys.path:
        sys.path.insert(0, oracle_path)
    import grpc                   # type: ignore
    import oracle_pb2             # type: ignore
    import oracle_pb2_grpc        # type: ignore
    from django.conf import settings
    oracle_url = getattr(settings, 'ORACLE_URL', '127.0.0.1:8001')
    channel = grpc.insecure_channel(oracle_url)
    return oracle_pb2_grpc.OracleServiceStub(channel), oracle_pb2


@api_view(['GET', 'POST'])
@permission_classes([IsAuthenticated])
def sltp_view(request):
    """List or create SLTPLevel records for a stock position or CFD position."""
    if request.method == 'GET':
        position_id = request.query_params.get('position')
        cfd_id      = request.query_params.get('cfd')
        if position_id:
            levels = SLTPLevel.objects.filter(
                position__id=position_id, position__user=request.user
            )
        elif cfd_id:
            levels = SLTPLevel.objects.filter(
                cfd_position__id=cfd_id, cfd_position__user=request.user
            )
        else:
            return Response({'error': 'Provide ?position=<id> or ?cfd=<id>'}, status=status.HTTP_400_BAD_REQUEST)
        return Response(SLTPLevelSerializer(levels, many=True).data)

    # POST — create a new level
    position_id = request.data.get('position_id')
    cfd_id      = request.data.get('cfd_id')
    level_type  = request.data.get('level_type')
    raw_price   = request.data.get('price')
    raw_qty     = request.data.get('quantity')

    if level_type not in ('SL', 'TP'):
        return Response({'error': 'level_type must be SL or TP'}, status=status.HTTP_400_BAD_REQUEST)
    try:
        price    = Decimal(str(raw_price))
        quantity = Decimal(str(raw_qty))
        if price <= 0 or quantity <= 0:
            raise ValueError
    except (InvalidOperation, TypeError, ValueError):
        return Response({'error': 'price and quantity must be positive numbers'}, status=status.HTTP_400_BAD_REQUEST)

    if position_id:
        try:
            pos = Position.objects.get(pk=position_id, user=request.user)
        except Position.DoesNotExist:
            return Response({'error': 'Position not found'}, status=status.HTTP_404_NOT_FOUND)
        from django.db.models import Sum
        used = SLTPLevel.objects.filter(
            position=pos, level_type=level_type, triggered=False
        ).aggregate(total=Sum('quantity'))['total'] or Decimal('0')
        if used + quantity > pos.quantity:
            return Response(
                {'error': f'Total {level_type} quantity ({used + quantity}) exceeds holding ({pos.quantity})'},
                status=status.HTTP_400_BAD_REQUEST,
            )
        level = SLTPLevel.objects.create(position=pos, level_type=level_type, price=price, quantity=quantity)

    elif cfd_id:
        try:
            cfd = CFDPosition.objects.get(pk=cfd_id, user=request.user, is_open=True)
        except CFDPosition.DoesNotExist:
            return Response({'error': 'CFD position not found'}, status=status.HTTP_404_NOT_FOUND)
        from django.db.models import Sum
        used = SLTPLevel.objects.filter(
            cfd_position=cfd, level_type=level_type, triggered=False
        ).aggregate(total=Sum('quantity'))['total'] or Decimal('0')
        if used + quantity > cfd.quantity:
            return Response(
                {'error': f'Total {level_type} quantity ({used + quantity}) exceeds position ({cfd.quantity})'},
                status=status.HTTP_400_BAD_REQUEST,
            )
        level = SLTPLevel.objects.create(cfd_position=cfd, level_type=level_type, price=price, quantity=quantity)

    else:
        return Response({'error': 'Provide position_id or cfd_id'}, status=status.HTTP_400_BAD_REQUEST)

    return Response(SLTPLevelSerializer(level).data, status=status.HTTP_201_CREATED)


@api_view(['DELETE'])
@permission_classes([IsAuthenticated])
def sltp_delete_view(request, pk):
    """Delete an untriggered SLTPLevel owned by the requesting user."""
    try:
        level = SLTPLevel.objects.select_related('position__user', 'cfd_position__user').get(pk=pk)
        owner = level.position.user if level.position_id else level.cfd_position.user
        if owner != request.user:
            raise SLTPLevel.DoesNotExist
    except SLTPLevel.DoesNotExist:
        return Response({'error': 'Level not found'}, status=status.HTTP_404_NOT_FOUND)
    level.delete()
    return Response(status=status.HTTP_204_NO_CONTENT)


# ============================================================
# 8. Options chain (expiry dates + calls/puts via yfinance)
# ============================================================
@api_view(['GET'])
@permission_classes([IsAuthenticated])
def option_chain_view(request, ticker):
    """GET /options/chain/<ticker>/ — no ?expiry → expiry list; with ?expiry → full chain."""
    ticker = ticker.upper()
    expiry = request.query_params.get('expiry')
    try:
        import yfinance as yf
        t = yf.Ticker(ticker)
        if not expiry:
            return Response({'expiries': list(t.options)})
        chain = t.option_chain(expiry)
        COLS = ['strike', 'bid', 'ask', 'lastPrice', 'volume', 'openInterest', 'impliedVolatility', 'inTheMoney']

        def safe_df(df):
            available = [c for c in COLS if c in df.columns]
            out = df[available].fillna(0)
            rows = []
            for _, row in out.iterrows():
                record = {}
                for col in available:
                    val = row[col]
                    record[col] = bool(val) if col == 'inTheMoney' else float(val)
                rows.append(record)
            return rows

        return Response({'calls': safe_df(chain.calls), 'puts': safe_df(chain.puts)})
    except Exception as e:
        return Response({'error': str(e)}, status=status.HTTP_503_SERVICE_UNAVAILABLE)


# ============================================================
# 9. Option positions — list
# ============================================================
@api_view(['GET'])
@permission_classes([IsAuthenticated])
def option_positions_view(request):
    positions = list(
        OptionPosition.objects.filter(user=request.user)
        .select_related('stock')
        .order_by('-opened_at')
    )
    data = []
    for p in positions:
        total_cost = p.premium_paid * p.contracts * 100
        data.append({
            'id':            p.id,
            'stock':         p.stock.ticker,
            'contract_type': p.contract_type,
            'strike':        str(p.strike),
            'expiry':        p.expiry.isoformat(),
            'contracts':     p.contracts,
            'premium_paid':  str(p.premium_paid),
            'total_cost':    str(total_cost),
            'status':        p.status,
            'opened_at':     p.opened_at.isoformat(),
            'closed_at':     p.closed_at.isoformat() if p.closed_at else None,
            'close_premium': str(p.close_premium) if p.close_premium else None,
            'pnl':           str(p.pnl) if p.pnl is not None else None,
        })
    return Response(data)






@api_view(['GET'])
@permission_classes([IsAuthenticated])
def price_view(_request, ticker):
    try:
        stub, pb = _get_oracle_stub()
        resp = stub.GetPrice(pb.PriceRequest(ticker=ticker.upper()), timeout=5)
        return Response({
            "ticker":          resp.ticker,
            "execution_price": resp.execution_price,
            "timestamp":       resp.timestamp,
        })
    except Exception as e:
        return Response({"error": str(e)}, status=status.HTTP_503_SERVICE_UNAVAILABLE)


# ── AI News ──────────────────────────────────────────────────────────────────

_AI_NEWS_CACHE = {}   # ticker -> (timestamp, result)
_AI_NEWS_TTL   = 300  # seconds

_COMPANY_NAMES = {
    'AAPL':'Apple','MSFT':'Microsoft','GOOGL':'Alphabet (Google)','GOOG':'Alphabet (Google)','AMZN':'Amazon',
    'NVDA':'NVIDIA','META':'Meta','TSLA':'Tesla','NFLX':'Netflix','AMD':'AMD',
    'INTC':'Intel','JPM':'JPMorgan Chase','V':'Visa','MA':'Mastercard',
    'KO':'Coca-Cola','BAC':'Bank of America','QCOM':'Qualcomm',
    'AMGN':'Amgen','GS':'Goldman Sachs','HD':'Home Depot','IBM':'IBM',
    'JNJ':'Johnson & Johnson','MCD':'McDonald\'s','PG':'Procter & Gamble',
}

@api_view(['GET'])
@permission_classes([IsAuthenticated])
def ai_news_view(_request, ticker):
    import time, json
    import yfinance as yf
    from django.conf import settings

    ticker = ticker.upper()

    # simple in-process cache
    cached = _AI_NEWS_CACHE.get(ticker)
    if cached and time.time() - cached[0] < _AI_NEWS_TTL:
        return Response(cached[1])

    if not settings.GEMINI_API_KEY:
        return Response(
            {"error": "GEMINI_API_KEY not configured. Add it to your .env file."},
            status=status.HTTP_503_SERVICE_UNAVAILABLE,
        )

    # 1. Yahoo Finance direct news (new nested structure: item['content']['title'])
    yf_items = []
    try:
        news = yf.Ticker(ticker).news or []
        for item in news[:12]:
            content = item.get('content') or item  # handle both old and new yfinance formats
            title = content.get('title', '')
            url   = (content.get('canonicalUrl') or content.get('clickThroughUrl') or {}).get('url', '') \
                    or content.get('link', content.get('url', ''))
            if title:
                yf_items.append(f"- {title}  [URL: {url}]")
    except Exception:
        pass

    # 2. DuckDuckGo broader macro / sector news (with timeout)
    ddg_items = []
    try:
        from duckduckgo_search import DDGS
        with DDGS(timeout=8) as ddgs:
            for r in ddgs.news(f"{ticker} stock market news sector", max_results=10):
                title = r.get('title', '')
                body  = (r.get('body') or '')[:200]
                url   = r.get('url', '')
                if title:
                    ddg_items.append(f"- {title}: {body}  [URL: {url}]")
    except Exception:
        pass

    if not yf_items and not ddg_items:
        return Response({"points": []})

    # 3. Gemini synthesis — retry up to 3 times on transient 503 errors
    from google import genai as google_genai
    from google.genai import errors as genai_errors
    client = google_genai.Client(api_key=settings.GEMINI_API_KEY)

    news_block = ""
    if yf_items:
        news_block += "DIRECT STOCK NEWS (Yahoo Finance):\n" + "\n".join(yf_items) + "\n\n"
    if ddg_items:
        news_block += "BROADER MARKET / MACRO SEARCH RESULTS:\n" + "\n".join(ddg_items)

    prompt = f"""You are a concise market analyst helping a retail investor decide whether news is worth reading right now.

Stock ticker: {ticker}

Your tasks:
1. Pick the 4-6 most relevant and recent items for a {ticker} investor.
2. Include BOTH direct news (about {ticker} itself) AND indirect news (sector trends, macro events, government/regulatory actions, competitor moves) — but only if there is a plausible reason it could affect {ticker}'s price or outlook.
3. For indirect items, briefly state WHY it matters to {ticker} in one phrase.
4. Discard anything clearly outdated, duplicate, or irrelevant.

Return ONLY this JSON (no markdown, no explanation):
{{
  "points": [
    {{"text": "one-sentence summary", "url": "source URL", "indirect": false}},
    {{"text": "one-sentence summary", "url": "source URL", "indirect": true, "reason": "why it affects {ticker}"}}
  ]
}}

NEWS DATA:
{news_block}"""

    last_err = None
    for attempt in range(3):
        try:
            resp = client.models.generate_content(
                model='gemini-flash-lite-latest',
                contents=prompt,
            )
            raw = resp.text.strip()
            if raw.startswith('```'):
                raw = raw.split('```')[1]
                if raw.startswith('json'):
                    raw = raw[4:]
            result = json.loads(raw.strip())
            break
        except genai_errors.ServerError:
            last_err = "503"
            time.sleep(2 ** attempt)  # 1s, 2s, 4s
        except Exception as e:
            return Response({"error": str(e)}, status=status.HTTP_503_SERVICE_UNAVAILABLE)
    else:
        return Response({"error": "Gemini is busy, try again in a moment."}, status=status.HTTP_503_SERVICE_UNAVAILABLE)

    _AI_NEWS_CACHE[ticker] = (time.time(), result)
    return Response(result)


# ── AI Chat ───────────────────────────────────────────────────────────────────

@api_view(['POST'])
@permission_classes([IsAuthenticated])
def ai_chat_view(request, ticker):
    import time
    from django.conf import settings

    ticker   = ticker.upper()
    question = (request.data.get('question') or '').strip()
    history  = request.data.get('history') or []   # [{role, text}, ...]

    if not question:
        return Response({"error": "No question provided."}, status=status.HTTP_400_BAD_REQUEST)
    if not settings.GEMINI_API_KEY:
        return Response({"error": "GEMINI_API_KEY not configured."}, status=status.HTTP_503_SERVICE_UNAVAILABLE)

    company = _COMPANY_NAMES.get(ticker, ticker)

    from google import genai as google_genai
    from google.genai import errors as genai_errors, types as genai_types
    client = google_genai.Client(api_key=settings.GEMINI_API_KEY)

    # Fetch live market data to ground the AI's answers in real numbers
    market_context = ''
    try:
        import yfinance as yf
        fi = yf.Ticker(ticker).fast_info

        def _fmt_cap(v):
            if not v: return 'N/A'
            if v >= 1e12: return f'${v/1e12:.2f}T'
            if v >= 1e9:  return f'${v/1e9:.2f}B'
            return f'${v/1e6:.2f}M'

        lines = ['\n\nCURRENT LIVE MARKET DATA (always use this, not your training data):']
        if fi.last_price:      lines.append(f'- Price: ${fi.last_price:.2f}')
        if fi.market_cap:      lines.append(f'- Market cap: {_fmt_cap(fi.market_cap)}')
        if fi.day_high and fi.day_low:
            lines.append(f"- Today's range: ${fi.day_low:.2f} – ${fi.day_high:.2f}")
        if fi.year_high and fi.year_low:
            lines.append(f'- 52-week range: ${fi.year_low:.2f} – ${fi.year_high:.2f}')
        market_context = '\n'.join(lines)
    except Exception:
        pass

    system_instruction = (
        f"You are a concise stock market analyst assistant. "
        f"The user is currently viewing the stock {ticker} ({company}). "
        f"Whenever the user says 'they', 'the company', 'it', 'their', 'them', or any ambiguous pronoun, "
        f"they are referring to {company} ({ticker}). "
        f"Answer questions about this company and its stock concisely. "
        f"Keep responses under 150 words unless more detail is clearly needed."
        f"{market_context}"
    )

    # Build conversation: history (up to 10 prior messages) + current question
    contents = []
    for msg in history[-10:]:
        role = 'user' if msg.get('role') == 'user' else 'model'
        contents.append({'role': role, 'parts': [{'text': msg['text']}]})
    contents.append({'role': 'user', 'parts': [{'text': question}]})

    for attempt in range(3):
        try:
            resp = client.models.generate_content(
                model='gemini-flash-lite-latest',
                contents=contents,
                config=genai_types.GenerateContentConfig(
                    system_instruction=system_instruction,
                ),
            )
            return Response({"answer": resp.text.strip()})
        except genai_errors.ServerError:
            time.sleep(2 ** attempt)
        except Exception as e:
            return Response({"error": str(e)}, status=status.HTTP_503_SERVICE_UNAVAILABLE)

    return Response({"error": "Gemini is busy, try again in a moment."}, status=status.HTTP_503_SERVICE_UNAVAILABLE)
