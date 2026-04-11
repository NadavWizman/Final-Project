from django.db import models
from django.contrib.auth.models import User

# 1. טבלת נכסים/מניות (קטלוג המניות המורשות)
class Stock(models.Model):
    ticker = models.CharField(max_length=10, primary_key=True) # למשל: AAPL
    name = models.CharField(max_length=100)

    def __str__(self):
        return self.ticker

# 2. טבלת ארנק דולרי - קשר של 1:1 למשתמש
class Wallet(models.Model):
    user = models.OneToOneField(User, on_delete=models.CASCADE, related_name='wallet')
    balance = models.DecimalField(max_digits=15, decimal_places=2, default=0.00)

    def __str__(self):
        return f"Wallet of {self.user.username} - {self.balance} USD"

# 3. טבלת החזקות מניות (Positions)
class Position(models.Model):
    user = models.ForeignKey(User, on_delete=models.CASCADE, related_name='positions')
    stock = models.ForeignKey(Stock, on_delete=models.CASCADE)
    quantity = models.DecimalField(max_digits=15, decimal_places=4, default=0.00)

    class Meta:
        unique_together = ('user', 'stock') # מונע כפילות: לכל משתמש שורה אחת לכל מניה

# 4. טבלת הזמנות (Orders) וניהול מחזור חיים 
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

    user = models.ForeignKey(User, on_delete=models.CASCADE, related_name='orders')
    stock = models.ForeignKey(Stock, on_delete=models.CASCADE)
    order_type = models.CharField(max_length=4, choices=ORDER_TYPES)
    quantity = models.DecimalField(max_digits=15, decimal_places=4)
    status = models.CharField(max_length=10, choices=STATUS_CHOICES, default='SUBMITED')
    created_at = models.DateTimeField(auto_now_add=True)
    updated_at = models.DateTimeField(auto_now=True)
    execution_price = models.DecimalField(max_digits=15, decimal_places=4, null=True, blank=True)
    nonce = models.CharField(max_length=100, unique=True, null=True, blank=True)
    limit_price = models.DecimalField(max_digits=15, decimal_places=4, null=True, blank=True)

    
    def __str__(self):
        return f"{self.order_type} {self.quantity} {self.stock_id} ({self.status})"
