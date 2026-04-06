from django.urls import path, include
from rest_framework.routers import DefaultRouter
from .views import OrderViewSet, portfolio_view

# הראוטר אוטומטית יוצר לנו את כל הכתובות ל-ViewSet של ההזמנות
router = DefaultRouter()
router.register(r'orders', OrderViewSet, basename='order')

urlpatterns = [
    path('', include(router.urls)),
    path('portfolio/', portfolio_view, name='portfolio'),
]