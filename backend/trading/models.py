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
        ('STOCK',     'Stock'),
        ('CFD',       'CFD'),
        ('CFD_CLOSE', 'CFD Close'),
        ('OPTION',    'Option'),
        ('OPT_CLOSE', 'Option Close'),
        ('OPT_EXER',  'Option Exercise'),
    ]

    user = models.ForeignKey(User, on_delete=models.CASCADE, related_name='orders')
    stock = models.ForeignKey(Stock, on_delete=models.CASCADE)
    order_type = models.CharField(max_length=4, choices=ORDER_TYPES)
    trade_type = models.CharField(max_length=9, choices=TRADE_TYPES, default='STOCK')
    quantity = models.DecimalField(max_digits=15, decimal_places=4)
    leverage = models.IntegerField(null=True, blank=True)
    option_contract_type = models.CharField(max_length=4, null=True, blank=True)
    option_strike        = models.DecimalField(max_digits=15, decimal_places=4, null=True, blank=True)
    option_expiry        = models.DateField(null=True, blank=True)
    position_id          = models.IntegerField(null=True, blank=True)
    status = models.CharField(max_length=10, choices=STATUS_CHOICES, default='DRAFT')
    created_at = models.DateTimeField(auto_now_add=True)
    updated_at = models.DateTimeField(auto_now=True)
    execution_price = models.DecimalField(max_digits=15, decimal_places=4, null=True, blank=True)
    nonce = models.CharField(max_length=100, unique=True, null=True, blank=True)
    limit_price = models.DecimalField(max_digits=15, decimal_places=4, null=True, blank=True)
    # Optional SL/TP to attach to the position this order opens. They are part
    # of the signed order and become SLTPLevel rows when the order settles.
    stop_loss       = models.DecimalField(max_digits=15, decimal_places=4, null=True, blank=True)
    stop_loss_qty   = models.DecimalField(max_digits=15, decimal_places=4, null=True, blank=True)
    take_profit     = models.DecimalField(max_digits=15, decimal_places=4, null=True, blank=True)
    take_profit_qty = models.DecimalField(max_digits=15, decimal_places=4, null=True, blank=True)
    signature = models.TextField(null=True, blank=True)  # ECDSA signature of the order creator
    # hash of the consensus block this order settled under (quorum-certified)
    block_hash = models.CharField(max_length=64, null=True, blank=True)
    reject_reason = models.CharField(max_length=200, blank=True, default='')

    class Meta:
        indexes = [
            models.Index(fields=['user', 'status'], name='order_user_status_idx'),
            models.Index(fields=['status'],          name='order_status_idx'),
        ]

    def __str__(self):
        return f"{self.order_type} {self.quantity} {self.stock_id} ({self.status})"

# 4b. Consensus node signing keys
class NodeKey(models.Model):
    """Ed25519 public key a consensus node signs its votes with.

    Registered by the node itself on first start (trust on first use) and
    fixed afterwards; an admin rotates a key by deleting the row.
    """
    user       = models.OneToOneField(User, on_delete=models.CASCADE, related_name='node_key')
    public_key = models.CharField(max_length=64)   # 32-byte raw key, hex
    created_at = models.DateTimeField(auto_now_add=True)

    def __str__(self):
        return f"NodeKey({self.user.username}: {self.public_key[:12]}…)"


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


# 7. Options positions
class OptionPosition(models.Model):
    CONTRACT_TYPES = [('CALL', 'Call'), ('PUT', 'Put')]
    STATUS_CHOICES = [
        ('OPEN',      'Open'),
        ('CLOSED',    'Closed'),
        ('EXERCISED', 'Exercised'),
        ('EXPIRED',   'Expired'),
    ]

    user          = models.ForeignKey(User, on_delete=models.CASCADE, related_name='option_positions')
    stock         = models.ForeignKey(Stock, on_delete=models.CASCADE)
    contract_type = models.CharField(max_length=4, choices=CONTRACT_TYPES)
    strike        = models.DecimalField(max_digits=15, decimal_places=4)
    expiry        = models.DateField()
    contracts     = models.IntegerField()                  # each contract = 100 shares
    premium_paid  = models.DecimalField(max_digits=15, decimal_places=4)  # per share
    status        = models.CharField(max_length=10, choices=STATUS_CHOICES, default='OPEN')
    opened_at     = models.DateTimeField(auto_now_add=True)
    closed_at     = models.DateTimeField(null=True, blank=True)
    close_premium = models.DecimalField(max_digits=15, decimal_places=4, null=True, blank=True)
    pnl           = models.DecimalField(max_digits=15, decimal_places=4, null=True, blank=True)
    nonce         = models.CharField(max_length=64, unique=True, null=True, blank=True)
    signature     = models.TextField(null=True, blank=True)

    class Meta:
        indexes = [
            models.Index(fields=['user', 'status'], name='opt_user_status_idx'),
        ]

    def __str__(self):
        return f"Option {self.contract_type} {self.stock_id} @{self.strike} exp {self.expiry}"
