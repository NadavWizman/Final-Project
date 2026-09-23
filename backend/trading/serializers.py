from decimal import Decimal

from rest_framework import serializers
from rest_framework.validators import UniqueValidator
from django.contrib.auth.models import User
from .models import Stock, Wallet, Position, Order, SLTPLevel


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
            'id', 'user', 'stock', 'order_type', 'trade_type', 'quantity',
            'leverage', 'limit_price',
            'option_contract_type', 'option_strike', 'option_expiry',
            'position_id',
            'status', 'created_at', 'execution_price',
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

    def validate_leverage(self, value):
        if value is not None and (value < 2 or value > 100):
            raise serializers.ValidationError("Leverage must be between 2 and 100.")
        return value

    def validate_limit_price(self, value):
        if value is not None and value <= 0:
            raise serializers.ValidationError("Limit price must be greater than 0.")
        return value

    def validate(self, data):
        trade_type = data.get('trade_type', 'STOCK')
        leverage   = data.get('leverage')
        if trade_type == 'CFD' and not leverage:
            raise serializers.ValidationError({"leverage": "Leverage is required for CFD orders."})
        if trade_type == 'STOCK' and leverage:
            raise serializers.ValidationError({"leverage": "Leverage only applies to CFD orders."})
        if trade_type == 'OPTION':
            if not data.get('option_contract_type') or data['option_contract_type'] not in ('CALL', 'PUT'):
                raise serializers.ValidationError({"option_contract_type": "Must be CALL or PUT for OPTION orders."})
            if not data.get('option_strike') or data['option_strike'] <= 0:
                raise serializers.ValidationError({"option_strike": "Required and must be > 0 for OPTION orders."})
            if not data.get('option_expiry'):
                raise serializers.ValidationError({"option_expiry": "Required for OPTION orders."})
        if trade_type in ('CFD_CLOSE', 'OPT_CLOSE', 'OPT_EXER'):
            if not data.get('position_id'):
                raise serializers.ValidationError({"position_id": "Required for close/exercise orders."})
        return data

    def validate_nonce(self, value):
        if not value:
            raise serializers.ValidationError("A unique nonce is required to prevent replay attacks.")
        return value


# 6. Deposit serializer
MAX_DEPOSIT = Decimal('1000000.00')


class DepositSerializer(serializers.Serializer):
    # DecimalField rejects NaN/Infinity and values that would overflow the
    # wallet's DecimalField(max_digits=15, decimal_places=2).
    amount = serializers.DecimalField(
        max_digits=12, decimal_places=2,
        min_value=Decimal('0.01'), max_value=MAX_DEPOSIT,
    )


# 7. SLTPLevel serializer
class SLTPLevelSerializer(serializers.ModelSerializer):
    class Meta:
        model = SLTPLevel
        fields = ['id', 'level_type', 'price', 'quantity', 'triggered', 'triggered_at', 'created_at']
