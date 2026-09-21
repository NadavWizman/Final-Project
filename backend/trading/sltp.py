"""
sltp.py — background thread that monitors multi-level stop-loss and take-profit triggers.
Runs every 15 seconds. Started by TradingConfig.ready() on server startup.

Stock and CFD triggers both create a signed order at status SUBMITTED, so the
close goes through node consensus exactly like a manual trade. Option expiry is
settled directly — it is an expiry event, not a trade the user is authorising.
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
            _check_options()
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
    from .models import SLTPLevel, CFDPosition, Order

    levels = list(
        SLTPLevel.objects.filter(
            cfd_position__isnull=False,
            cfd_position__is_open=True,
            triggered=False,
        ).select_related('cfd_position__user', 'cfd_position__stock', 'cfd_position__user__profile')
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
            from .crypto_utils import sign_order
            with transaction.atomic():
                lv_fresh  = SLTPLevel.objects.select_for_update().get(pk=lv.pk, triggered=False)
                pos_fresh = CFDPosition.objects.select_for_update().get(pk=pos.pk, is_open=True)

                close_qty = min(lv_fresh.quantity, pos_fresh.quantity)
                nonce     = str(uuid.uuid4())
                profile   = pos_fresh.user.profile
                order_data = {
                    'stock':      pos_fresh.stock.ticker,
                    'order_type': 'SELL',
                    'quantity':   str(close_qty),
                    'nonce':      nonce,
                }
                signature = sign_order(profile.ecdsa_private_key, order_data)
                close = Order.objects.create(
                    user=pos_fresh.user, stock=pos_fresh.stock,
                    order_type='SELL', trade_type='CFD_CLOSE',
                    quantity=close_qty, position_id=pos_fresh.pk,
                    nonce=nonce, signature=signature, status='SUBMITTED',
                )

                # Mark the level triggered now, not when the order executes — the
                # monitor runs again in 15s and must not fire this level twice
                # while the close order is still awaiting consensus.
                lv_fresh.triggered    = True
                lv_fresh.triggered_at = timezone.now()
                lv_fresh.save(update_fields=['triggered', 'triggered_at'])

            logger.info('[SLTP] CFD close order #%d for level #%d (%s @%s qty=%s) price=%s',
                        close.id, lv.pk, lv.level_type, lv.price, close_qty, price)
        except (SLTPLevel.DoesNotExist, CFDPosition.DoesNotExist):
            pass
        except Exception:
            logger.exception('[SLTP] Failed to create CFD close for level #%d', lv.pk)


# ── Options expiry ───────────────────────────────────────────

def _check_options():
    from django.db import transaction
    from django.utils import timezone as tz
    from .models import OptionPosition, Wallet

    today = tz.now().date()
    positions = list(
        OptionPosition.objects.filter(status='OPEN', expiry__lt=today)
        .select_related('user', 'stock')
    )
    if not positions:
        return

    tickers = {p.stock.ticker for p in positions}
    prices  = {t: _oracle_price(t) for t in tickers}

    for pos in positions:
        price = prices.get(pos.stock.ticker)
        if price is None:
            continue
        try:
            with transaction.atomic():
                pos_f = OptionPosition.objects.select_for_update().get(pk=pos.pk, status='OPEN')
                total_cost = pos_f.premium_paid * pos_f.contracts * 100

                if pos_f.contract_type == 'CALL':
                    intrinsic = max(Decimal('0'), price - pos_f.strike)
                else:
                    intrinsic = max(Decimal('0'), pos_f.strike - price)

                cash_received = intrinsic * pos_f.contracts * 100
                pnl_val       = cash_received - total_cost
                new_status    = 'EXERCISED' if intrinsic > 0 else 'EXPIRED'

                wallet = Wallet.objects.select_for_update().get(user=pos_f.user)
                wallet.balance += cash_received
                wallet.save()

                pos_f.status        = new_status
                pos_f.pnl           = pnl_val
                pos_f.close_premium = intrinsic
                pos_f.closed_at     = tz.now()
                pos_f.save()

                logger.info('[OPTIONS] Position #%d → %s (intrinsic=%s pnl=%s)',
                            pos.pk, new_status, intrinsic, pnl_val)
        except OptionPosition.DoesNotExist:
            pass
        except Exception:
            logger.exception('[OPTIONS] Failed to handle expiry for position #%d', pos.pk)
