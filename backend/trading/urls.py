from django.urls import path, include
from rest_framework.routers import DefaultRouter
from .views import (
    OrderViewSet, portfolio_view, register_view, deposit_view,
    price_view, history_view, cfd_positions_view, close_cfd_view,
)

router = DefaultRouter()
router.register(r'orders', OrderViewSet, basename='order')

urlpatterns = [
    path('', include(router.urls)),
    path('portfolio/',                portfolio_view,     name='portfolio'),
    path('register/',                 register_view,      name='register'),
    path('deposit/',                  deposit_view,       name='deposit'),
    path('price/<str:ticker>/',       price_view,         name='price'),
    path('history/<str:ticker>/',     history_view,       name='history'),
    path('cfd/',                      cfd_positions_view, name='cfd-positions'),
    path('cfd/<int:pk>/close/',       close_cfd_view,     name='cfd-close'),
]
