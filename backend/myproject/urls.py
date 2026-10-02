"""TradeDesk gateway: the web app and a read-only API onto the chain."""
from django.urls import include, path
from django.views.generic import TemplateView

urlpatterns = [
    path('',     TemplateView.as_view(template_name='index.html'), name='home'),
    path('api/', include('gateway.urls')),
]
