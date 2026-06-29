from decimal import Decimal
from django.db import models
from django.contrib.auth.models import User


# 0. User profile — holds ECDSA keys
class UserProfile(models.Model):
    user = models.OneToOneField(User, on_delete=models.CASCADE, related_name='profile')
    ecdsa_private_key = models.TextField()   # PEM — stored server-side, never exposed via API
    ecdsa_public_key  = models.TextField()   # PEM — sent to nodes for signature verification

    def __str__(self):
        return f"Profile({self.user.username})"


# 1. Stocks table (catalog of allowed tickers)
class Stock(models.Model):
    ticker = models.CharField(max_length=10, primary_key=True) # e.g. AAPL
    name = models.CharField(max_length=100)

    def __str__(self):
        return self.ticker

# 2. Dollar wallet table - 1:1 relationship with User
class Wallet(models.Model):
    user = models.OneToOneField(User, on_delete=models.CASCADE, related_name='wallet')
    balance = models.DecimalField(max_digits=15, decimal_places=2, default=0.00)

    def __str__(self):
        return f"Wallet of {self.user.username} - {self.balance} USD"

# 3. Stock holdings table (Positions)
class Position(models.Model):
    user = models.ForeignKey(User, on_delete=models.CASCADE, related_name='positions')
    stock = models.ForeignKey(Stock, on_delete=models.CASCADE)
    quantity = models.DecimalField(max_digits=15, decimal_places=4, default=Decimal('0'))

    class Meta:
        unique_together = ('user', 'stock')
        indexes = [
            models.Index(fields=['user'], name='position_user_idx'),
        ]

# 4. Orders table and lifecycle management
class Order(models.Model):
    ORDER_TYPES = [
        ('BUY', 'Buy'),
        ('SELL', 'Sell'),
    ]

    STATUS_CHOICES = [
        ('DRAFT', 'Draft'),
        ('SUBMITTED', 'Submitted'),
        ('CONFIRMED', 'Confirmed'),
        ('REJECTED', 'Rejected'),
    ]

    TRADE_TYPES = [
        ('STOCK', 'Stock'),
        ('CFD',   'CFD'),
    ]

    user = models.ForeignKey(User, on_delete=models.CASCADE, related_name='orders')
    stock = models.ForeignKey(Stock, on_delete=models.CASCADE)
    order_type = models.CharField(max_length=4, choices=ORDER_TYPES)
    trade_type = models.CharField(max_length=5, choices=TRADE_TYPES, default='STOCK')
    quantity = models.DecimalField(max_digits=15, decimal_places=4)
    leverage = models.IntegerField(null=True, blank=True)  # CFD only — e.g. 5 means 5x
    status = models.CharField(max_length=10, choices=STATUS_CHOICES, default='DRAFT')
    created_at = models.DateTimeField(auto_now_add=True)
    updated_at = models.DateTimeField(auto_now=True)
    execution_price = models.DecimalField(max_digits=15, decimal_places=4, null=True, blank=True)
    nonce = models.CharField(max_length=100, unique=True, null=True, blank=True)
    limit_price = models.DecimalField(max_digits=15, decimal_places=4, null=True, blank=True)
    stop_loss   = models.DecimalField(max_digits=15, decimal_places=4, null=True, blank=True)
    take_profit = models.DecimalField(max_digits=15, decimal_places=4, null=True, blank=True)
    sltp_triggered = models.BooleanField(default=False)  # prevents double-fire of SL/TP auto-sell
    signature = models.TextField(null=True, blank=True)  # ECDSA signature of the order creator


    class Meta:
        indexes = [
            models.Index(fields=['user', 'status'], name='order_user_status_idx'),
            models.Index(fields=['status'],          name='order_status_idx'),
        ]

    def __str__(self):
        return f"{self.order_type} {self.quantity} {self.stock_id} ({self.status})"

# 5. CFD open positions
class CFDPosition(models.Model):
    DIRECTION_CHOICES = [
        ('LONG',  'Long'),
        ('SHORT', 'Short'),
    ]

    user        = models.ForeignKey(User,  on_delete=models.CASCADE, related_name='cfd_positions')
    stock       = models.ForeignKey(Stock, on_delete=models.CASCADE)
    direction   = models.CharField(max_length=5, choices=DIRECTION_CHOICES)
    quantity    = models.DecimalField(max_digits=15, decimal_places=4)
    entry_price = models.DecimalField(max_digits=15, decimal_places=4)
    leverage    = models.IntegerField()
    margin_used = models.DecimalField(max_digits=15, decimal_places=4)
    stop_loss   = models.DecimalField(max_digits=15, decimal_places=4, null=True, blank=True)
    take_profit = models.DecimalField(max_digits=15, decimal_places=4, null=True, blank=True)
    is_open     = models.BooleanField(default=True)
    opened_at   = models.DateTimeField(auto_now_add=True)
    closed_at   = models.DateTimeField(null=True, blank=True)
    close_price = models.DecimalField(max_digits=15, decimal_places=4, null=True, blank=True)
    pnl         = models.DecimalField(max_digits=15, decimal_places=4, null=True, blank=True)

    class Meta:
        indexes = [
            models.Index(fields=['user', 'is_open'], name='cfd_user_open_idx'),
        ]

    def __str__(self):
        return f"CFD {self.direction} {self.quantity} {self.stock_id} @{self.entry_price} x{self.leverage}"


# 6. Multi-level Stop Loss / Take Profit
class SLTPLevel(models.Model):
    TYPE_CHOICES = [('SL', 'Stop Loss'), ('TP', 'Take Profit')]

    # Exactly one of these is set per level
    position     = models.ForeignKey('Position',    null=True, blank=True, on_delete=models.CASCADE, related_name='sltp_levels')
    cfd_position = models.ForeignKey('CFDPosition', null=True, blank=True, on_delete=models.CASCADE, related_name='sltp_levels')

    level_type   = models.CharField(max_length=2, choices=TYPE_CHOICES)
    price        = models.DecimalField(max_digits=15, decimal_places=4)
    quantity     = models.DecimalField(max_digits=15, decimal_places=4)
    triggered    = models.BooleanField(default=False)
    triggered_at = models.DateTimeField(null=True, blank=True)
    created_at   = models.DateTimeField(auto_now_add=True)

    class Meta:
        ordering = ['level_type', 'price']

    def __str__(self):
        target = f"pos#{self.position_id}" if self.position_id else f"cfd#{self.cfd_position_id}"
        return f"{self.level_type} {self.quantity}@{self.price} ({target})"


# 7. Node consensus approvals table
class OrderApproval(models.Model):
    # link to the specific order
    order = models.ForeignKey(Order, on_delete=models.CASCADE, related_name='approvals')
    # name of the node that approved (i.e. the username it authenticated with)
    node_name = models.CharField(max_length=50)
    # price this node received from the oracle
    execution_price = models.DecimalField(max_digits=15, decimal_places=4)
    timestamp = models.DateTimeField(auto_now_add=True)

    class Meta:
        # prevents the same node from voting twice on the same order
        unique_together = ('order', 'node_name')
        indexes = [
            models.Index(fields=['order'], name='approval_order_idx'),
        ]

    def __str__(self):
        return f"Node {self.node_name} approved Order {self.order.id} at {self.execution_price}"
