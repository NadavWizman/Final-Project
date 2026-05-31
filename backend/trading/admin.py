from django.contrib import admin
from .models import Stock, Wallet, Position, Order, OrderApproval, UserProfile

admin.site.register(Stock)
admin.site.register(Wallet)
admin.site.register(Position)
admin.site.register(Order)
admin.site.register(OrderApproval)
admin.site.register(UserProfile)