from django.urls import path
from . import views

urlpatterns = [
    path("orders/", views.orders_collection, name="orders_collection"),
    path("orders/<uuid:order_id>/", views.order_detail, name="order_detail"),
    path("orders/<uuid:order_id>/submit/", views.submit_order, name="order_submit"),
]
