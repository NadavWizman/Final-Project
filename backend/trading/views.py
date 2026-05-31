from rest_framework import viewsets, status
from rest_framework.decorators import api_view, permission_classes, action
from rest_framework.permissions import IsAuthenticated
from rest_framework.response import Response
from rest_framework.exceptions import ValidationError
from django.utils import timezone
from django.utils.dateparse import parse_datetime
from django.db import IntegrityError, transaction
from datetime import timedelta
from decimal import Decimal, InvalidOperation
from .models import Order, Wallet, Position, Stock, OrderApproval, UserProfile
from .serializers import OrderSerializer
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
        # Node (staff) sees all orders — regular user sees only their own
        if self.request.user.is_staff:
            return Order.objects.all()
        return Order.objects.filter(user=self.request.user)

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
    # execute_order — called by Execution nodes after consensus
    # ----------------------------------------------------------------
    @action(detail=True, methods=['post'])
    def execute_order(self, request, pk=None):
        order = self.get_object()

        if order.status != 'SUBMITTED':
            return Response(
                {"error": "Only SUBMITTED orders can be executed."},
                status=status.HTTP_400_BAD_REQUEST
            )

        execution_price_raw = request.data.get('execution_price')
        oracle_timestamp    = request.data.get('timestamp')
        node_name           = request.user.username

        if not execution_price_raw or not oracle_timestamp:
            return Response(
                {"error": "execution_price and timestamp are required."},
                status=status.HTTP_400_BAD_REQUEST
            )

        # parse price
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
            order.status = 'REJECTED'
            order.save()
            return Response(
                {"error": f"Stale data: price is {int(age.total_seconds())} seconds old (limit: 60s)."},
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

        # record this node's vote
        try:
            OrderApproval.objects.create(
                order=order,
                node_name=node_name,
                execution_price=execution_price
            )
        except IntegrityError:
            return Response(
                {"error": "This node has already approved this order."},
                status=status.HTTP_400_BAD_REQUEST
            )
        except Exception as e:
            return Response(
                {"error": f"Internal error: {str(e)}"},
                status=status.HTTP_500_INTERNAL_SERVER_ERROR
            )

        total_approvals = order.approvals.count()

        if total_approvals < 2:
            return Response({
                "status": "pending_consensus",
                "message": f"Vote recorded ({total_approvals}/3). Waiting for more nodes.",
                "approvals": total_approvals
            })

        # consensus reached — execute with lock to prevent race condition
        with transaction.atomic():
            order.refresh_from_db()
            if order.status != 'SUBMITTED':
                return Response({
                    "status": "already_processed",
                    "message": "This order has already been processed by another node."
                })

            total_value = order.quantity * execution_price
            wallet = order.user.wallet

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
            "status": "success",
            "message": f"Consensus reached! ({total_approvals}/3 approvals). Order executed.",
            "execution_price": str(execution_price),
            "total_value": str(total_value),
            "approvals": total_approvals
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
        {"ticker": pos.stock.ticker, "name": pos.stock.name, "quantity": str(pos.quantity)}
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
