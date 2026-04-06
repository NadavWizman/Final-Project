
from django.contrib import admin
from .models import Stock, Wallet, Position, Order

# כאן אנחנו אומרות ל-Django להציג את הטבלאות במסך הניהול
admin.site.register(Stock)
admin.site.register(Wallet)
admin.site.register(Position)
admin.site.register(Order)