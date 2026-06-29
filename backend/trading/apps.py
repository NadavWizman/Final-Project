import os
from django.apps import AppConfig


class TradingConfig(AppConfig):
    name = 'trading'

    def ready(self):
        # Start only in the live server process, not the reloader parent or management commands.
        if os.environ.get('RUN_MAIN') != 'true':
            return
        from .sltp import start_monitor
        start_monitor()
