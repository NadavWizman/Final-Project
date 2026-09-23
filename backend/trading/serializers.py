import re
from decimal import Decimal

from django.utils import timezone
from rest_framework import serializers
from rest_framework.validators import UniqueValidator
from django.contrib.auth.models import User
from .models import Stock, Wallet, Position, Order, SLTPLevel
from .crypto_utils import canonical_order_message

NONCE_RE = re.compile(r'[A-Za-z0-9_-]{1,64}')


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
    # the exact canonical message the signature covers, so the nodes verify the
    # signature over these bytes instead of re-building the JSON themselves
    signed_message = serializers.SerializerMethodField()
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
            'stop_loss', 'stop_loss_qty', 'take_profit', 'take_profit_qty',
            'option_contract_type', 'option_strike', 'option_expiry',
            'position_id',
            'status', 'created_at', 'execution_price',
            'nonce', 'signature', 'public_key', 'signed_message', 'block_hash',
        ]
        read_only_fields = ['id', 'user', 'status', 'created_at', 'execution_price',
                            'signature', 'public_key', 'signed_message', 'block_hash']

    def get_public_key(self, obj):
        """Returns the public key of the order owner (from UserProfile)."""
        try:
            return obj.user.profile.ecdsa_public_key
        except Exception:
            return None

    def get_signed_message(self, obj):
        return canonical_order_message(obj) if obj.signature else None

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
        order_type = data.get('order_type')
        leverage   = data.get('leverage')
        quantity   = data.get('quantity')
        if trade_type == 'CFD' and not leverage:
            raise serializers.ValidationError({"leverage": "Leverage is required for CFD orders."})
        if trade_type != 'CFD' and leverage:
            raise serializers.ValidationError({"leverage": "Leverage only applies to CFD orders."})
        if trade_type == 'OPTION':
            if order_type != 'BUY':
                raise serializers.ValidationError({"order_type": "Only buying option contracts is supported."})
            if not data.get('option_contract_type') or data['option_contract_type'] not in ('CALL', 'PUT'):
                raise serializers.ValidationError({"option_contract_type": "Must be CALL or PUT for OPTION orders."})
            if not data.get('option_strike') or data['option_strike'] <= 0:
                raise serializers.ValidationError({"option_strike": "Required and must be > 0 for OPTION orders."})
            if not data.get('option_expiry'):
                raise serializers.ValidationError({"option_expiry": "Required for OPTION orders."})
            if data['option_expiry'] < timezone.now().date():
                raise serializers.ValidationError({"option_expiry": "This option has already expired."})
        if trade_type in ('OPTION', 'OPT_CLOSE', 'OPT_EXER'):
            if quantity is not None and quantity != quantity.to_integral_value():
                raise serializers.ValidationError({"quantity": "Options trade in whole contracts."})
        if trade_type in ('CFD_CLOSE', 'OPT_CLOSE', 'OPT_EXER'):
            if not data.get('position_id'):
                raise serializers.ValidationError({"position_id": "Required for close/exercise orders."})
            if order_type != 'SELL':
                raise serializers.ValidationError({"order_type": "Close/exercise orders must be SELL."})
        self._validate_sltp(data, trade_type, order_type, quantity)
        if trade_type != 'OPTION' and any(data.get(f) for f in ('option_contract_type', 'option_strike', 'option_expiry')):
            raise serializers.ValidationError({"trade_type": "Option fields only apply to OPTION orders."})
        return data

    @staticmethod
    def _validate_sltp(data, trade_type, order_type, quantity):
        """SL/TP may be attached to an order that opens a position: a stock BUY
        or a CFD. Quantities default to the whole order."""
        sl, tp = data.get('stop_loss'), data.get('take_profit')
        if sl is None and tp is None:
            if data.get('stop_loss_qty') is not None or data.get('take_profit_qty') is not None:
                raise serializers.ValidationError({"stop_loss": "A quantity was given without a price."})
            return
        opens_position = trade_type == 'CFD' or (trade_type == 'STOCK' and order_type == 'BUY')
        if not opens_position:
            raise serializers.ValidationError(
                {"stop_loss": "SL/TP can only be attached to a stock BUY or a CFD order."})
        for price_f, qty_f in (('stop_loss', 'stop_loss_qty'), ('take_profit', 'take_profit_qty')):
            price = data.get(price_f)
            if price is None:
                if data.get(qty_f) is not None:
                    raise serializers.ValidationError({qty_f: f"{qty_f} requires {price_f}."})
                continue
            if price <= 0:
                raise serializers.ValidationError({price_f: "Must be greater than 0."})
            qty = data.get(qty_f)
            if qty is None:
                data[qty_f] = quantity
            elif qty <= 0 or (quantity is not None and qty > quantity):
                raise serializers.ValidationError({qty_f: "Must be between 0 and the order quantity."})
        if sl is not None and tp is not None:
            long_side = order_type == 'BUY'
            if (long_side and sl >= tp) or (not long_side and sl <= tp):
                raise serializers.ValidationError(
                    {"stop_loss": "Stop loss must be on the losing side of the take profit."})

    def validate_nonce(self, value):
        # Restricted charset: the nonce is embedded in the signed JSON message, and
        # quotes, backslashes or non-ASCII would make the message ambiguous.
        if not NONCE_RE.fullmatch(value or ''):
            raise serializers.ValidationError(
                "Nonce must be 1-64 characters of letters, digits, '-' or '_'.")
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
