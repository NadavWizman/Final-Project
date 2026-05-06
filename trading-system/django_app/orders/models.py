"""Order lifecycle model.

States:
  DRAFT       — created via POST /orders/, not yet sent to the cluster.
  SUBMITTED   — forwarded to the Leader; waiting for consensus.
  CONFIRMED   — Leader returned CONFIRMED (committed by >= 2/3 nodes).
  REJECTED    — Node-side validation or consensus failure.
"""
from __future__ import annotations

import uuid
from decimal import Decimal

from django.conf import settings
from django.db import models


class Order(models.Model):
    class Side(models.TextChoices):
        BUY = "BUY", "Buy"
        SELL = "SELL", "Sell"

    class Status(models.TextChoices):
        DRAFT = "DRAFT"
        SUBMITTED = "SUBMITTED"
        CONFIRMED = "CONFIRMED"
        REJECTED = "REJECTED"

    order_id = models.UUIDField(primary_key=True, default=uuid.uuid4, editable=False)
    user = models.ForeignKey(settings.AUTH_USER_MODEL, on_delete=models.PROTECT, related_name="orders")
    symbol = models.CharField(max_length=16)
    side = models.CharField(max_length=4, choices=Side.choices)
    quantity = models.DecimalField(max_digits=20, decimal_places=8)
    # Price at submission time, copied from the oracle response.
    price = models.DecimalField(max_digits=20, decimal_places=8, null=True, blank=True)
    price_timestamp = models.BigIntegerField(null=True, blank=True)  # unix seconds
    # User's worst acceptable price. NULL = market order (no limit).
    #   BUY:  oracle_price MUST be <= limit_price
    #   SELL: oracle_price MUST be >= limit_price
    limit_price = models.DecimalField(max_digits=20, decimal_places=8, null=True, blank=True)

    nonce = models.BigIntegerField(null=True, blank=True)
    status = models.CharField(max_length=16, choices=Status.choices, default=Status.DRAFT)
    rejection_reason = models.TextField(blank=True, default="")
    block_index = models.BigIntegerField(null=True, blank=True)
    block_hash = models.CharField(max_length=128, blank=True, default="")

    created_at = models.DateTimeField(auto_now_add=True)
    submitted_at = models.DateTimeField(null=True, blank=True)
    settled_at = models.DateTimeField(null=True, blank=True)

    class Meta:
        ordering = ["-created_at"]

    def __str__(self) -> str:
        return f"{self.order_id} {self.side} {self.quantity} {self.symbol} [{self.status}]"
