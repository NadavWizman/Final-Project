from rest_framework import serializers

from .models import Order


class OrderSerializer(serializers.ModelSerializer):
    class Meta:
        model = Order
        fields = [
            "order_id", "symbol", "side", "quantity", "limit_price",
            "price", "price_timestamp", "nonce", "status", "rejection_reason",
            "block_index", "block_hash",
            "created_at", "submitted_at", "settled_at",
        ]
        read_only_fields = [
            "order_id", "price", "price_timestamp", "nonce", "status",
            "rejection_reason", "block_index", "block_hash",
            "created_at", "submitted_at", "settled_at",
        ]


class OrderCreateSerializer(serializers.Serializer):
    symbol = serializers.CharField(max_length=16)
    side = serializers.ChoiceField(choices=Order.Side.choices)
    quantity = serializers.DecimalField(max_digits=20, decimal_places=8, min_value=0)
    # Optional. Omit for a market order.
    limit_price = serializers.DecimalField(
        max_digits=20, decimal_places=8, min_value=0, required=False, allow_null=True
    )

    def validate_symbol(self, value: str) -> str:
        from django.conf import settings
        if value not in settings.SP500_WHITELIST:
            raise serializers.ValidationError(f"{value} is not in the S&P 500 whitelist")
        return value
