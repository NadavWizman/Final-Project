from rest_framework import serializers
from rest_framework.validators import UniqueValidator
from django.contrib.auth.models import User
from .models import Stock, Wallet, Position, Order


# 1. User serializer
class UserSerializer(serializers.ModelSerializer):
    class Meta:
        model = User
        fields = ['id', 'username', 'email']


# 2. Stock serializer
class StockSerializer(serializers.ModelSerializer):
    class Meta:
        model = Stock
        fields = '__all__'


# 3. Wallet serializer
class WalletSerializer(serializers.ModelSerializer):
    class Meta:
        model = Wallet
        fields = ['balance']


# 4. Position serializer
class PositionSerializer(serializers.ModelSerializer):
    class Meta:
        model = Position
        fields = ['id', 'stock', 'quantity']


# 5. Order serializer
class OrderSerializer(serializers.ModelSerializer):
    # user's public key — read automatically from UserProfile
    public_key = serializers.SerializerMethodField()
    # nonce is required at creation and must be unique (replay attack prevention)
    nonce = serializers.CharField(
        required=True,
        allow_blank=False,
        validators=[UniqueValidator(queryset=Order.objects.all())],
    )

    class Meta:
        model = Order
        fields = [
            'id', 'user', 'stock', 'order_type', 'quantity',
            'limit_price', 'status', 'created_at', 'execution_price',
            'nonce', 'signature', 'public_key',
        ]
        read_only_fields = ['id', 'user', 'status', 'created_at', 'execution_price', 'signature', 'public_key']

    def get_public_key(self, obj):
        """Returns the public key of the order owner (from UserProfile)."""
        try:
            return obj.user.profile.ecdsa_public_key
        except Exception:
            return None

    def validate_quantity(self, value):
        if value <= 0:
            raise serializers.ValidationError("Quantity must be greater than 0.")
        return value

    def validate_limit_price(self, value):
        if value is not None and value <= 0:
            raise serializers.ValidationError("Limit price must be greater than 0.")
        return value

    def validate_nonce(self, value):
        if not value:
            raise serializers.ValidationError("A unique nonce is required to prevent replay attacks.")
        return value
