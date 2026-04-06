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
        fields = ['id', 'user', 'stock', 'order_type', 'quantity', 'limit_price', 'status', 'created_at', 'execution_price', 'nonce']        # אנחנו לא נותנים למשתמש לקבוע בעצמו את מחיר הביצוע, לכן הוספנו אותו לקריאה בלבד
        read_only_fields = ['id', 'user', 'status', 'created_at', 'execution_price']