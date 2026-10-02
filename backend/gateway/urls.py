from django.urls import path

from . import market, views

urlpatterns = [
    # the chain
    path('status/',                      views.status_view),
    path('tx/',                          views.tx_view),
    path('tx/<str:tx_hash>/',            views.tx_status_view),
    path('username/<str:name>/',         views.username_view),
    path('portfolio/',                   views.portfolio_view),
    path('orders/',                      views.orders_view),
    path('cfd/',                         views.cfd_view),
    path('options/',                     views.options_view),
    path('sltp/',                        views.sltp_view),
    path('price/<str:ticker>/',          views.price_view),
    path('option-quote/',                views.option_quote_view),
    # display-only market data and AI
    path('history/<str:ticker>/',        market.history_view),
    path('options/chain/<str:ticker>/',  market.option_chain_view),
    path('ai-news/<str:ticker>/',        market.ai_news_view),
    path('ai-chat/<str:ticker>/',        market.ai_chat_view),
]
