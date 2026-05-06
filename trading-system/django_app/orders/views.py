"""Order lifecycle endpoints.

  POST /api/orders/               -> create draft
  POST /api/orders/<id>/submit/   -> sign + send to Leader -> confirmed/rejected
  GET  /api/orders/               -> list my orders
  GET  /api/orders/<id>/          -> detail
"""
from __future__ import annotations

import time
from decimal import Decimal

from django.db import transaction
from django.shortcuts import get_object_or_404
from django.utils import timezone
from rest_framework import status
from rest_framework.decorators import api_view, permission_classes
from rest_framework.permissions import IsAuthenticated
from rest_framework.response import Response

from generated import trading_pb2
from .grpc_client import NodeClient
from .models import Order
from .oracle_feed import fetch_quote
from .serializers import OrderCreateSerializer, OrderSerializer
from .signer import sign_order_tx


@api_view(["GET", "POST"])
@permission_classes([IsAuthenticated])
def orders_collection(request):
    if request.method == "GET":
        qs = Order.objects.filter(user=request.user)
        return Response(OrderSerializer(qs, many=True).data)

    # POST — create draft.
    payload = OrderCreateSerializer(data=request.data)
    payload.is_valid(raise_exception=True)
    order = Order.objects.create(
        user=request.user,
        symbol=payload.validated_data["symbol"],
        side=payload.validated_data["side"],
        quantity=payload.validated_data["quantity"],
        limit_price=payload.validated_data.get("limit_price"),
        status=Order.Status.DRAFT,
    )
    return Response(OrderSerializer(order).data, status=status.HTTP_201_CREATED)


@api_view(["GET"])
@permission_classes([IsAuthenticated])
def order_detail(request, order_id):
    order = get_object_or_404(Order, order_id=order_id, user=request.user)
    return Response(OrderSerializer(order).data)


@api_view(["POST"])
@permission_classes([IsAuthenticated])
def submit_order(request, order_id):
    """Sign the order and ship it to the Leader node.

    This is the hot path. The sequence is:
      1. Lock the row + user for nonce safety.
      2. Fetch a fresh oracle quote.
      3. Optimistic local balance check (UX only — node is authoritative).
      4. Bump user.nonce, build OrderTx, sign.
      5. gRPC SubmitOrder.
      6. Persist result.
    """
    user = request.user

    with transaction.atomic():
        order = (
            Order.objects.select_for_update()
            .get(order_id=order_id, user=user, status=Order.Status.DRAFT)
        )

        # 1. Fresh price.
        quote = fetch_quote(order.symbol)

        # 1b. Limit-price gate. The Node will re-check this — failing here just
        # gives the user a clean error without burning a consensus round.
        _check_limit_price(order, Decimal(quote.price))

        # 2. Balance sanity (best-effort; Node is the real check).
        _optimistic_balance_check(user, order, Decimal(quote.price))

        # 3. Nonce.
        # Using select_for_update on the user row prevents concurrent
        # submissions from stepping on the same nonce.
        user_locked = type(user).objects.select_for_update().get(pk=user.pk)
        user_locked.nonce += 1
        user_locked.save(update_fields=["nonce"])
        nonce = user_locked.nonce

        # 4. Build + sign.
        tx = trading_pb2.OrderTx(
            order_id=str(order.order_id),
            user_id=user.external_id,
            symbol=order.symbol,
            side=(trading_pb2.BUY if order.side == Order.Side.BUY else trading_pb2.SELL),
            quantity=str(order.quantity),
            price=quote.price,
            price_timestamp=quote.timestamp,
            nonce=nonce,
            submitted_at=int(time.time()),
            limit_price=(str(order.limit_price) if order.limit_price is not None else ""),
        )
        signed = sign_order_tx(tx, user.priv_key_pem, user.pub_key_pem)

        order.price = Decimal(quote.price)
        order.price_timestamp = quote.timestamp
        order.nonce = nonce
        order.status = Order.Status.SUBMITTED
        order.submitted_at = timezone.now()
        order.save()

    # 5. gRPC (outside the txn so we don't hold DB locks over the network).
    client = NodeClient.default()
    try:
        resp = client.submit_order(signed)
    except Exception as e:  # grpc.RpcError or anything else
        order.status = Order.Status.REJECTED
        order.rejection_reason = f"leader unreachable: {e}"
        order.settled_at = timezone.now()
        order.save()
        return Response(OrderSerializer(order).data, status=status.HTTP_502_BAD_GATEWAY)

    # 6. Persist outcome.
    if resp.status == trading_pb2.CONFIRMED:
        order.status = Order.Status.CONFIRMED
        order.block_index = resp.block_idx
        order.block_hash = resp.block_hash.hex()
    else:
        order.status = Order.Status.REJECTED
        order.rejection_reason = resp.reason or "unknown"
    order.settled_at = timezone.now()
    order.save()

    return Response(OrderSerializer(order).data)


def _check_limit_price(order: Order, oracle_price: Decimal) -> None:
    """Reject the submit if the oracle price violates the user's limit.

    BUY  with limit L: oracle MUST be <= L (don't pay above L).
    SELL with limit L: oracle MUST be >= L (don't sell below L).
    """
    from rest_framework.exceptions import ValidationError

    if order.limit_price is None:
        return  # market order
    limit = Decimal(order.limit_price)
    if order.side == Order.Side.BUY and oracle_price > limit:
        raise ValidationError(
            f"buy limit violated: oracle price ${oracle_price} > limit ${limit}"
        )
    if order.side == Order.Side.SELL and oracle_price < limit:
        raise ValidationError(
            f"sell limit violated: oracle price ${oracle_price} < limit ${limit}"
        )


def _optimistic_balance_check(user, order: Order, price: Decimal) -> None:
    """Best-effort check against Django's cached wallet.

    Raises ValidationError-ish to pre-empt obvious failures. Not the source
    of truth — the Node re-checks and wins any disagreement.
    """
    from rest_framework.exceptions import ValidationError

    wallet = getattr(user, "wallet", None)
    if wallet is None:
        return
    notional = price * order.quantity
    if order.side == Order.Side.BUY:
        if wallet.cash_usd < notional:
            raise ValidationError(f"insufficient cash: have ${wallet.cash_usd} need ${notional}")
    else:
        pos = wallet.positions.filter(symbol=order.symbol).first()
        have = pos.quantity if pos else Decimal("0")
        if have < order.quantity:
            raise ValidationError(f"insufficient shares: have {have} need {order.quantity}")
