from django.core.management.base import BaseCommand

from trading.sltp import run_forever


class Command(BaseCommand):
    help = (
        'Runs the stop-loss / take-profit, CFD margin and option-expiry monitor in '
        'the foreground. Use this under any server other than `manage.py runserver` '
        '(gunicorn, `runserver --noreload`, …), where the in-process monitor does '
        'not start, and set SLTP_MONITOR=external for the web server.'
    )

    def handle(self, *args, **options):
        self.stdout.write(self.style.SUCCESS('SL/TP monitor running — Ctrl+C to stop.'))
        try:
            run_forever()
        except KeyboardInterrupt:
            self.stdout.write('\nSL/TP monitor stopped.')
