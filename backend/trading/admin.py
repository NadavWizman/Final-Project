from django.contrib import admin
from .models import (Stock, Wallet, Position, Order, UserProfile, NodeKey,
                     CFDPosition, OptionPosition, SLTPLevel)


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
    list_display   = ('id', 'user', 'order_type', 'trade_type', 'stock', 'quantity',
                      'status', 'execution_price', 'created_at')
    list_filter    = ('status', 'order_type', 'trade_type', 'stock')
    search_fields  = ('user__username', 'stock__ticker', 'nonce')
    readonly_fields = ('signature', 'nonce', 'block_hash', 'reject_reason', 'created_at', 'updated_at')
    ordering       = ('-created_at',)
    date_hierarchy = 'created_at'


@admin.register(UserProfile)
class UserProfileAdmin(admin.ModelAdmin):
    list_display  = ('user',)
    search_fields = ('user__username',)
    readonly_fields = ('ecdsa_private_key', 'ecdsa_public_key')


@admin.register(NodeKey)
class NodeKeyAdmin(admin.ModelAdmin):
    list_display    = ('user', 'public_key', 'created_at')
    readonly_fields = ('user', 'public_key', 'created_at')


@admin.register(CFDPosition)
class CFDPositionAdmin(admin.ModelAdmin):
    list_display  = ('id', 'user', 'stock', 'direction', 'quantity', 'entry_price',
                     'leverage', 'margin_used', 'is_open', 'pnl')
    list_filter   = ('is_open', 'direction', 'stock')
    search_fields = ('user__username', 'stock__ticker')


@admin.register(OptionPosition)
class OptionPositionAdmin(admin.ModelAdmin):
    list_display  = ('id', 'user', 'stock', 'contract_type', 'strike', 'expiry',
                     'contracts', 'premium_paid', 'status', 'pnl')
    list_filter   = ('status', 'contract_type', 'stock')
    search_fields = ('user__username', 'stock__ticker')


@admin.register(SLTPLevel)
class SLTPLevelAdmin(admin.ModelAdmin):
    list_display = ('id', 'level_type', 'price', 'quantity', 'position', 'cfd_position',
                    'triggered', 'triggered_at')
    list_filter  = ('level_type', 'triggered')
