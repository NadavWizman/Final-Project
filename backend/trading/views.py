from rest_framework import viewsets, status
from rest_framework.decorators import api_view, permission_classes, action
from rest_framework.permissions import IsAuthenticated
from rest_framework.response import Response
from rest_framework.exceptions import ValidationError
from django.utils import timezone
from django.utils.dateparse import parse_datetime
from django.db import transaction
from datetime import timedelta
from decimal import Decimal, InvalidOperation
from .models import Order, Wallet, Position, Stock, UserProfile, CFDPosition, SLTPLevel
from .serializers import OrderSerializer, SLTPLevelSerializer
from .crypto_utils import generate_key_pair, sign_order, verify_signature
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


# ============================================================
# 1. Order management
# ============================================================
class OrderViewSet(viewsets.ModelViewSet):
    serializer_class = OrderSerializer
    permission_classes = [IsAuthenticated]

    def get_queryset(self):
        # Node (staff) sees all orders — regular user sees only their own.
        # select_related avoids N+1 queries when serializing user and stock fields.
        qs = Order.objects.select_related('user', 'stock', 'user__profile')
        if self.request.user.is_staff:
            return qs
        return qs.filter(user=self.request.user)

    def perform_create(self, serializer):
        stock = serializer.validated_data['stock']
        # S&P 500 whitelist check
        if stock.ticker not in SP500_TICKERS:
            raise ValidationError(
                {"error": f"Ticker {stock.ticker} is not in the S&P 500 index and is not allowed for trading."}
            )
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

        # sign the order
        order_data = {
            "stock":      order.stock.ticker,
            "order_type": order.order_type,
            "quantity":   str(order.quantity),
            "nonce":      order.nonce or "",
        }

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
    @action(detail=True, methods=['post'])
    def execute_order(self, request, pk=None):
        execution_price_raw = request.data.get('execution_price')
        oracle_timestamp    = request.data.get('timestamp')

        if not execution_price_raw or not oracle_timestamp:
            return Response(
                {"error": "execution_price and timestamp are required."},
                status=status.HTTP_400_BAD_REQUEST
            )

        try:
            execution_price = Decimal(str(execution_price_raw))
        except InvalidOperation:
            return Response({"error": "Invalid execution price."}, status=status.HTTP_400_BAD_REQUEST)

        # stale-price check
        oracle_time = parse_datetime(oracle_timestamp)
        if not oracle_time:
            return Response({"error": "Invalid timestamp format."}, status=status.HTTP_400_BAD_REQUEST)

        age = timezone.now() - oracle_time
        if age > timedelta(seconds=60):
            return Response(
                {"error": f"Stale data: price is {int(age.total_seconds())} seconds old (limit: 60s)."},
                status=status.HTTP_400_BAD_REQUEST
            )

        with transaction.atomic():
            order = self.get_object()
            order.refresh_from_db()

            if order.status != 'SUBMITTED':
                return Response(
                    {"error": f"Only SUBMITTED orders can be executed. Current status: {order.status}"},
                    status=status.HTTP_400_BAD_REQUEST
                )

            # limit-price check
            if order.limit_price:
                if order.order_type == 'BUY' and execution_price > order.limit_price:
                    order.status = 'REJECTED'
                    order.save()
                    return Response(
                        {"error": f"Market price ${execution_price} exceeds limit price ${order.limit_price}."},
                        status=status.HTTP_400_BAD_REQUEST
                    )
                elif order.order_type == 'SELL' and execution_price < order.limit_price:
                    order.status = 'REJECTED'
                    order.save()
                    return Response(
                        {"error": f"Market price ${execution_price} is below limit price ${order.limit_price}."},
                        status=status.HTTP_400_BAD_REQUEST
                    )

            wallet = order.user.wallet

            if order.trade_type == 'CFD':
                leverage    = order.leverage or 1
                notional    = order.quantity * execution_price
                margin      = (notional / leverage).quantize(Decimal('0.0001'))
                direction   = 'LONG' if order.order_type == 'BUY' else 'SHORT'

                if wallet.balance < margin:
                    order.status = 'REJECTED'
                    order.save()
                    return Response(
                        {"error": f"Insufficient margin: need ${margin}, have ${wallet.balance}."},
                        status=status.HTTP_400_BAD_REQUEST
                    )

                wallet.balance -= margin
                wallet.save()
                CFDPosition.objects.create(
                    user=order.user, stock=order.stock,
                    direction=direction, quantity=order.quantity,
                    entry_price=execution_price, leverage=leverage,
                    margin_used=margin,
                )
                order.execution_price = execution_price
                order.status = 'CONFIRMED'
                order.save()

                return Response({
                    "status":        "success",
                    "message":       "CFD position opened after gRPC consensus.",
                    "direction":     direction,
                    "entry_price":   str(execution_price),
                    "notional":      str(notional),
                    "leverage":      leverage,
                    "margin_used":   str(margin),
                })

            # --- regular stock order ---
            total_value = order.quantity * execution_price

            if order.order_type == 'BUY':
                if wallet.balance < total_value:
                    order.status = 'REJECTED'
                    order.save()
                    return Response(
                        {"error": f"Insufficient balance: ${wallet.balance} < ${total_value}."},
                        status=status.HTTP_400_BAD_REQUEST
                    )
                wallet.balance -= total_value
                position, _ = Position.objects.get_or_create(user=order.user, stock=order.stock)
                position.quantity += order.quantity
                position.save()

            elif order.order_type == 'SELL':
                try:
                    position = Position.objects.get(user=order.user, stock=order.stock)
                except Position.DoesNotExist:
                    order.status = 'REJECTED'
                    order.save()
                    return Response(
                        {"error": "No holdings found for this stock."},
                        status=status.HTTP_400_BAD_REQUEST
                    )
                if position.quantity < order.quantity:
                    order.status = 'REJECTED'
                    order.save()
                    return Response(
                        {"error": f"Insufficient shares: {position.quantity} < {order.quantity}."},
                        status=status.HTTP_400_BAD_REQUEST
                    )
                position.quantity -= order.quantity
                if position.quantity == 0:
                    position.delete()
                else:
                    position.save()
                wallet.balance += total_value

            wallet.save()
            order.execution_price = execution_price
            order.status = 'CONFIRMED'
            order.save()

        return Response({
            "status":          "success",
            "message":         "Order executed after gRPC consensus.",
            "execution_price": str(execution_price),
            "total_value":     str(total_value),
        })


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

    if not username or not password:
        return Response({"error": "username and password are required."}, status=status.HTTP_400_BAD_REQUEST)

    if User.objects.filter(username=username).exists():
        return Response({"error": "Username already exists."}, status=status.HTTP_400_BAD_REQUEST)

    user = User.objects.create_user(username=username, password=password)
    Wallet.objects.create(user=user, balance=Decimal('10000.00'))

    # generate an ECDSA key pair for every new user
    private_pem, public_pem = generate_key_pair()
    UserProfile.objects.create(
        user=user,
        ecdsa_private_key=private_pem,
        ecdsa_public_key=public_pem,
    )

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
    amount_raw = request.data.get('amount')
    if not amount_raw:
        return Response({"error": "amount is required."}, status=status.HTTP_400_BAD_REQUEST)

    try:
        amount = Decimal(str(amount_raw))
        if amount <= 0:
            raise ValueError()
    except Exception:
        return Response({"error": "Invalid amount."}, status=status.HTTP_400_BAD_REQUEST)

    wallet, _ = Wallet.objects.get_or_create(user=request.user, defaults={'balance': Decimal('0.00')})
    wallet.balance += amount
    wallet.save()

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


# ============================================================
# 7. CFD close position
# ============================================================
@api_view(['POST'])
@permission_classes([IsAuthenticated])
def close_cfd_view(request, pk):
    try:
        pos = CFDPosition.objects.get(pk=pk, user=request.user, is_open=True)
    except CFDPosition.DoesNotExist:
        return Response({"error": "Open CFD position not found."}, status=status.HTTP_404_NOT_FOUND)

    try:
        stub, pb = _get_oracle_stub()
        resp = stub.GetPrice(pb.PriceRequest(ticker=pos.stock.ticker), timeout=5)
        close_price = Decimal(str(resp.execution_price))
    except Exception as e:
        return Response({"error": f"Oracle unavailable: {e}"}, status=status.HTTP_503_SERVICE_UNAVAILABLE)

    if pos.direction == 'LONG':
        pnl = (close_price - pos.entry_price) * pos.quantity
    else:
        pnl = (pos.entry_price - close_price) * pos.quantity

    with transaction.atomic():
        wallet = request.user.wallet
        wallet.balance += pos.margin_used + pnl
        if wallet.balance < 0:
            wallet.balance = Decimal('0')
        wallet.save()

        pos.is_open     = False
        pos.close_price = close_price
        pos.pnl         = pnl
        pos.closed_at   = timezone.now()
        pos.save()

    return Response({
        "status":           "closed",
        "stock":            pos.stock.ticker,
        "direction":        pos.direction,
        "entry_price":      str(pos.entry_price),
        "close_price":      str(close_price),
        "pnl":              str(pnl),
        "margin_returned":  str(pos.margin_used),
        "new_balance":      str(wallet.balance),
    })


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
