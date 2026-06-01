from django.core.management.base import BaseCommand
from django.contrib.auth.models import User

# One entry per node: (username, password, is_leader)
NODE_CREDENTIALS = [
    ('node1', 'node1pass'),
    ('node2', 'node2pass'),
    ('node3', 'node3pass'),
]


class Command(BaseCommand):
    help = (
        'Creates the three blockchain node users (node1, node2, node3) with is_staff=True '
        'so the Leader can authenticate with the Django API. Safe to run multiple times.'
    )

    def handle(self, *args, **options):
        for username, password in NODE_CREDENTIALS:
            if User.objects.filter(username=username).exists():
                self.stdout.write(f'  {username}: already exists — skipped.')
            else:
                User.objects.create_user(
                    username=username,
                    password=password,
                    is_staff=True,
                )
                self.stdout.write(self.style.SUCCESS(f'  {username}: created.'))

        self.stdout.write(self.style.SUCCESS('Node users ready.'))
