import logging
import os
import sys

from django.apps import AppConfig

logger = logging.getLogger(__name__)


class TradingConfig(AppConfig):
    name = 'trading'

    def ready(self):
        # The SL/TP monitor runs inside the development server for convenience.
        # Every other deployment (gunicorn, `runserver --noreload`, …) must run
        # `manage.py run_sltp_monitor` as its own process and set
        # SLTP_MONITOR=external, so exactly one monitor is active.
        if os.environ.get('SLTP_MONITOR', 'auto').lower() in ('external', 'off', '0', 'false'):
            return
        if sys.argv[1:2] != ['runserver']:
            return
        # With the autoreloader, ready() also runs in the watcher parent; start
        # only in the child that serves requests (RUN_MAIN) — or directly when
        # the reloader is disabled.
        if '--noreload' not in sys.argv and os.environ.get('RUN_MAIN') != 'true':
            return
        from .sltp import start_monitor
        start_monitor()
