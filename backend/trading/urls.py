from django.urls import path, include
from rest_framework.routers import DefaultRouter
from .views import OrderViewSet, portfolio_view, register_view, deposit_view

router = DefaultRouter()
router.register(r'orders', OrderViewSet, basename='order')

urlpatterns = [
    path('', include(router.urls)),
    path('portfolio/', portfolio_view, name='portfolio'),
    path('register/',  register_view,  name='register'),
    path('deposit/',   deposit_view,   name='deposit'),
]
