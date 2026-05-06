from django.contrib import admin
from django.contrib.auth.admin import UserAdmin

from .models import User


@admin.register(User)
class CustomUserAdmin(UserAdmin):
    list_display = ("username", "external_id", "nonce", "is_staff")
    fieldsets = UserAdmin.fieldsets + (
        ("Trading", {"fields": ("external_id", "priv_key_pem", "pub_key_pem", "nonce")}),
    )
