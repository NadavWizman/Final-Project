from rest_framework import serializers
from django.contrib.auth.models import User
from .models import Stock, Wallet, Position, Order

# 1. מתרגם למשתמש (מחזיר רק פרטים בסיסיים, בלי סיסמאות!)
class UserSerializer(serializers.ModelSerializer):
    class Meta:
        model = User
        fields = ['id', 'username', 'email']

# 2. מתרגם למניות
class StockSerializer(serializers.ModelSerializer):
    class Meta:
        model = Stock
        fields = '__all__'  # קיצור דרך שאומר "קח את כל השדות מהטבלה"

# 3. מתרגם לארנק
class WalletSerializer(serializers.ModelSerializer):
    class Meta:
        model = Wallet
        fields = ['balance']

# 4. מתרגם להחזקות (Positions)
class PositionSerializer(serializers.ModelSerializer):
    class Meta:
        model = Position
        fields = ['id', 'stock', 'quantity']

# 5. מתרגם להזמנות (Orders) - הלב של המערכת!
class OrderSerializer(serializers.ModelSerializer):
    class Meta:
        model = Order
        fields = ['id', 'user', 'stock', 'order_type', 'quantity', 'limit_price', 'status', 'created_at', 'execution_price', 'nonce']
        # הורדנו את ה-nonce מכאן כדי שהמשתמש יוכל לשלוח אותו:
        read_only_fields = ['id', 'user', 'status', 'created_at', 'execution_price']
        
    def validate_quantity(self, value):
        if value <= 0:
            raise serializers.ValidationError("הכמות חייבת להיות גדולה מ-0.")
        return value

    def validate_limit_price(self, value):
        if value is not None and value <= 0:
            raise serializers.ValidationError("מחיר גבול חייב להיות גדול מ-0.")
        return value

    # --- התוספת החדשה שלנו ---
    def validate_nonce(self, value):
        if not value:
            raise serializers.ValidationError("חובה לספק nonce ייחודי למניעת מתקפות שחזור (Replay Attacks).")
        return value