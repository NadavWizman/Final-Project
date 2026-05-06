"""Wallet and portfolio models.

Django holds a *cached* view of balances for quick UI rendering. The
source of truth is the Node cluster — after every confirmed order we
reconcile this cache by calling GetAccount on the Leader.
"""
from __future__ import annotations

from decimal import Decimal

from django.conf import settings
from django.db import models


class Wallet(models.Model):
    user = models.OneToOneField(
        settings.AUTH_USER_MODEL, on_delete=models.CASCADE, related_name="wallet"
    )
    cash_usd = models.DecimalField(max_digits=20, decimal_places=8, default=Decimal("0"))

    def __str__(self) -> str:
        return f"{self.user}: ${self.cash_usd}"


class Position(models.Model):
    wallet = models.ForeignKey(Wallet, on_delete=models.CASCADE, related_name="positions")
    symbol = models.CharField(max_length=16)
    quantity = models.DecimalField(max_digits=20, decimal_places=8, default=Decimal("0"))

    class Meta:
        unique_together = [("wallet", "symbol")]

    def __str__(self) -> str:
        return f"{self.wallet.user} {self.symbol}: {self.quantity}"
