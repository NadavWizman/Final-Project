"""
sltp.py — background thread that monitors multi-level stop-loss and take-profit triggers.
Runs every 15 seconds. Started by TradingConfig.ready() on server startup.
"""

import logging
import threading
import time
import uuid
from decimal import Decimal

from django.utils import timezone

logger = logging.getLogger(__name__)
_started = False


def start_monitor():
    global _started
    if _started:
        return
    _started = True
    t = threading.Thread(target=_loop, daemon=True)
    t.start()
    logger.info('[SLTP] Monitor started')


def _loop():
    time.sleep(5)
    while True:
        try:
            from django.db import close_old_connections
            close_old_connections()
            _check_stocks()
            _check_cfds()
        except Exception:
            logger.exception('[SLTP] Unhandled error in monitor loop')
        time.sleep(15)


# ── price helper ─────────────────────────────────────────────────

def _oracle_price(ticker) -> Decimal | None:
    try:
        import sys, os
        oracle_path = os.path.normpath(
            os.path.join(os.path.dirname(__file__), '..', '..', 'oracle_service')
        )
        if oracle_path not in sys.path:
            sys.path.insert(0, oracle_path)
        import grpc
        import oracle_pb2
        import oracle_pb2_grpc
        from django.conf import settings
        url = getattr(settings, 'ORACLE_URL', '127.0.0.1:8001')
        ch  = grpc.insecure_channel(url)
        stub = oracle_pb2_grpc.OracleServiceStub(ch)
        resp = stub.GetPrice(oracle_pb2.PriceRequest(ticker=ticker), timeout=5)
        ch.close()
        return Decimal(str(resp.execution_price))
    except Exception as e:
        logger.warning('[SLTP] Oracle error for %s: %s', ticker, e)
        return None


# ── stock SL/TP ──────────────────────────────────────────────────

def _check_stocks():
    from .models import SLTPLevel, Order

    levels = list(
        SLTPLevel.objects.filter(
            position__isnull=False,
            triggered=False,
        ).select_related('position__user', 'position__stock', 'position__user__profile')
    )
    if not levels:
        return

    tickers = {lv.position.stock.ticker for lv in levels}
    prices  = {t: _oracle_price(t) for t in tickers}

    for lv in levels:
        price = prices.get(lv.position.stock.ticker)
        if price is None:
            continue

        triggered = (lv.level_type == 'SL' and price <= lv.price) or \
                    (lv.level_type == 'TP' and price >= lv.price)
        if not triggered:
            continue

        try:
            from .crypto_utils import sign_order
            nonce = str(uuid.uuid4())
            profile = lv.position.user.profile
            order_data = {
                'stock':      lv.position.stock.ticker,
                'order_type': 'SELL',
                'quantity':   str(lv.quantity),
                'nonce':      nonce,
            }
            signature = sign_order(profile.ecdsa_private_key, order_data)
            sell = Order.objects.create(
                user=lv.position.user, stock=lv.position.stock,
                order_type='SELL', trade_type='STOCK',
                quantity=lv.quantity, nonce=nonce,
                signature=signature, status='SUBMITTED',
            )
            lv.triggered    = True
            lv.triggered_at = timezone.now()
            lv.save(update_fields=['triggered', 'triggered_at'])
            logger.info('[SLTP] Stock auto-sell #%d for level #%d (%s @%s qty=%s) price=%s',
                        sell.id, lv.id, lv.level_type, lv.price, lv.quantity, price)
        except Exception:
            logger.exception('[SLTP] Failed to create auto-sell for level #%d', lv.id)


# ── CFD SL/TP ────────────────────────────────────────────────────

def _check_cfds():
    from django.db import transaction
    from .models import SLTPLevel, CFDPosition, Wallet

    levels = list(
        SLTPLevel.objects.filter(
            cfd_position__isnull=False,
            cfd_position__is_open=True,
            triggered=False,
        ).select_related('cfd_position__user', 'cfd_position__stock')
    )
    if not levels:
        return

    tickers = {lv.cfd_position.stock.ticker for lv in levels}
    prices  = {t: _oracle_price(t) for t in tickers}

    for lv in levels:
        pos   = lv.cfd_position
        price = prices.get(pos.stock.ticker)
        if price is None:
            continue

        if pos.direction == 'LONG':
            triggered = (lv.level_type == 'SL' and price <= lv.price) or \
                        (lv.level_type == 'TP' and price >= lv.price)
        else:
            triggered = (lv.level_type == 'SL' and price >= lv.price) or \
                        (lv.level_type == 'TP' and price <= lv.price)

        if not triggered:
            continue

        try:
            with transaction.atomic():
                lv_fresh  = SLTPLevel.objects.select_for_update().get(pk=lv.pk, triggered=False)
                pos_fresh = CFDPosition.objects.select_for_update().get(pk=pos.pk, is_open=True)

                close_qty      = min(lv_fresh.quantity, pos_fresh.quantity)
                partial_margin = pos_fresh.margin_used * (close_qty / pos_fresh.quantity)
                pnl = (price - pos_fresh.entry_price) * close_qty if pos_fresh.direction == 'LONG' \
                      else (pos_fresh.entry_price - price) * close_qty

                wallet = Wallet.objects.select_for_update().get(user=pos_fresh.user)
                wallet.balance += partial_margin + pnl
                if wallet.balance < Decimal('0'):
                    wallet.balance = Decimal('0')
                wallet.save()

                pos_fresh.quantity    -= close_qty
                pos_fresh.margin_used -= partial_margin
                if pos_fresh.quantity <= 0:
                    pos_fresh.is_open     = False
                    pos_fresh.close_price = price
                    pos_fresh.pnl         = (pos_fresh.pnl or Decimal('0')) + pnl
                    pos_fresh.closed_at   = timezone.now()
                pos_fresh.save()

                lv_fresh.triggered    = True
                lv_fresh.triggered_at = timezone.now()
                lv_fresh.save(update_fields=['triggered', 'triggered_at'])

            logger.info('[SLTP] CFD level #%d triggered (%s @%s qty=%s) P&L=%s',
                        lv.pk, lv.level_type, lv.price, close_qty, pnl)
        except (SLTPLevel.DoesNotExist, CFDPosition.DoesNotExist):
            pass
        except Exception:
            logger.exception('[SLTP] Failed to trigger CFD level #%d', lv.pk)
