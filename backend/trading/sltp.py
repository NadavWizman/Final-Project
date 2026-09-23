"""
sltp.py — background thread that monitors multi-level stop-loss and take-profit triggers.
Runs every 15 seconds — inside `manage.py runserver` (TradingConfig.ready), or
as its own process via `manage.py run_sltp_monitor` for any other server.

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
    run_forever()


def run_forever(interval=15):
    """Check every SL/TP level, CFD margin and option expiry every `interval` s."""
    from django.db import close_old_connections
    while True:
        try:
            close_old_connections()
            run_once()
        except Exception:
            logger.exception('[SLTP] Unhandled error in monitor loop')
        time.sleep(interval)


def run_once():
    _check_stocks()
    _check_cfds()
    _check_options()


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


def _create_signed_order(**fields):
    """Create an order already signed with the owner's key, at SUBMITTED, so it
    goes through node consensus exactly like a manual trade."""
    from .models import Order
    from .crypto_utils import sign_order, order_signing_payload

    order = Order(nonce=uuid.uuid4().hex, status='SUBMITTED', **fields)
    order.signature = sign_order(order.user.profile.ecdsa_private_key,
                                 order_signing_payload(order))
    order.save()
    return order


def _expiry_price(ticker, expiry):
    """Closing price of `ticker` on the expiry date (or the last trading day
    before it). Falls back to the live Oracle price only if the expiry was the
    previous day and history is unavailable; otherwise returns None so the
    position is retried rather than settled at a wrong price."""
    from datetime import timedelta
    try:
        import yfinance as yf
        hist = yf.Ticker(ticker.replace('.', '-')).history(
            start=(expiry - timedelta(days=6)).isoformat(),
            end=(expiry + timedelta(days=1)).isoformat(),
        )
        if not hist.empty:
            return Decimal(str(round(float(hist['Close'].iloc[-1]), 4)))
    except Exception as e:
        logger.warning('[OPTIONS] History lookup for %s on %s failed: %s', ticker, expiry, e)
    if (timezone.now().date() - expiry).days <= 1:
        return _oracle_price(ticker)
    return None


# ── stock SL/TP ──────────────────────────────────────────────────

def _check_stocks():
    from django.db import transaction
    from .models import SLTPLevel, Position

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
            with transaction.atomic():
                lv_f  = SLTPLevel.objects.select_for_update().get(pk=lv.pk, triggered=False)
                pos_f = Position.objects.select_for_update().get(pk=lv_f.position_id)

                # Never sell more than is actually held and not already on its
                # way out: holdings shrink after manual sells, and a sibling
                # level (e.g. the TP after an SL fired) may already be pending.
                qty = min(lv_f.quantity, pos_f.quantity - _pending_close_qty(
                    user=pos_f.user, stock=pos_f.stock, trade_type='STOCK'))

                lv_f.triggered    = True
                lv_f.triggered_at = timezone.now()
                lv_f.save(update_fields=['triggered', 'triggered_at'])

                if qty <= 0:
                    logger.info('[SLTP] Level #%d reached but no unsold shares remain — nothing to sell', lv.pk)
                    continue
                sell = _create_signed_order(
                    user=pos_f.user, stock=pos_f.stock,
                    order_type='SELL', trade_type='STOCK', quantity=qty,
                )
            logger.info('[SLTP] Stock auto-sell #%d for level #%d (%s @%s qty=%s) price=%s',
                        sell.id, lv.pk, lv.level_type, lv.price, qty, price)
        except (SLTPLevel.DoesNotExist, Position.DoesNotExist):
            pass
        except Exception:
            logger.exception('[SLTP] Failed to create auto-sell for level #%d', lv.pk)


def _pending_close_qty(**order_filter):
    """Quantity already in SUBMITTED SELL/close orders matching the filter."""
    from django.db.models import Sum
    from .models import Order
    return Order.objects.filter(status='SUBMITTED', order_type='SELL', **order_filter) \
        .aggregate(total=Sum('quantity'))['total'] or Decimal('0')


# ── CFD SL/TP ────────────────────────────────────────────────────

def _check_cfds():
    from django.db import transaction
    from .models import SLTPLevel, CFDPosition

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
            with transaction.atomic():
                lv_fresh  = SLTPLevel.objects.select_for_update().get(pk=lv.pk, triggered=False)
                pos_fresh = CFDPosition.objects.select_for_update().get(pk=pos.pk, is_open=True)

                close_qty = min(lv_fresh.quantity, pos_fresh.quantity - _pending_close_qty(
                    trade_type='CFD_CLOSE', position_id=pos_fresh.pk))

                # Mark the level triggered now, not when the order executes — the
                # monitor runs again in 15s and must not fire this level twice
                # while the close order is still awaiting consensus.
                lv_fresh.triggered    = True
                lv_fresh.triggered_at = timezone.now()
                lv_fresh.save(update_fields=['triggered', 'triggered_at'])

                if close_qty <= 0:
                    logger.info('[SLTP] CFD level #%d reached but the position is already closing', lv.pk)
                    continue
                close = _create_signed_order(
                    user=pos_fresh.user, stock=pos_fresh.stock,
                    order_type='SELL', trade_type='CFD_CLOSE',
                    quantity=close_qty, position_id=pos_fresh.pk,
                )

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

    for pos in positions:
        # Settle at the underlying's close ON the expiry date, not at today's
        # price — the monitor (or the Oracle) may have been down for days.
        price = _expiry_price(pos.stock.ticker, pos.expiry)
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
