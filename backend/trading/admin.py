from django.contrib import admin
from .models import Stock, Wallet, Position, Order, UserProfile


@admin.register(Stock)
class StockAdmin(admin.ModelAdmin):
    list_display  = ('ticker', 'name')
    search_fields = ('ticker', 'name')
    ordering      = ('ticker',)


@admin.register(Wallet)
class WalletAdmin(admin.ModelAdmin):
    list_display  = ('user', 'balance')
    search_fields = ('user__username',)
    ordering      = ('-balance',)


@admin.register(Position)
class PositionAdmin(admin.ModelAdmin):
    list_display  = ('user', 'stock', 'quantity')
    search_fields = ('user__username', 'stock__ticker')
    list_filter   = ('stock',)


@admin.register(Order)
class OrderAdmin(admin.ModelAdmin):
    list_display   = ('id', 'user', 'order_type', 'stock', 'quantity',
                      'status', 'execution_price', 'created_at')
    list_filter    = ('status', 'order_type', 'stock')
    search_fields  = ('user__username', 'stock__ticker', 'nonce')
    readonly_fields = ('signature', 'nonce', 'created_at', 'updated_at')
    ordering       = ('-created_at',)
    date_hierarchy = 'created_at'


@admin.register(UserProfile)
class UserProfileAdmin(admin.ModelAdmin):
    list_display  = ('user',)
    search_fields = ('user__username',)
    readonly_fields = ('ecdsa_private_key', 'ecdsa_public_key')
