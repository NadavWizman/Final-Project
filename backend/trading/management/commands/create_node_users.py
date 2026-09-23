from django.contrib.auth.models import Group, User
from django.core.management.base import BaseCommand

from trading.roles import CONSENSUS_NODES_GROUP

# One entry per node: (username, password)
NODE_CREDENTIALS = [
    ('node1', 'node1pass'),
    ('node2', 'node2pass'),
    ('node3', 'node3pass'),
]


class Command(BaseCommand):
    help = (
        'Creates the three blockchain node users (node1, node2, node3) in the '
        f'"{CONSENSUS_NODES_GROUP}" group so the nodes can authenticate with the '
        'Django API. Safe to run multiple times.'
    )

    def handle(self, *args, **options):
        group, _ = Group.objects.get_or_create(name=CONSENSUS_NODES_GROUP)
        for username, password in NODE_CREDENTIALS:
            user = User.objects.filter(username=username).first()
            if user:
                self.stdout.write(f'  {username}: already exists — ensured node role.')
            else:
                user = User.objects.create_user(username=username, password=password)
                self.stdout.write(self.style.SUCCESS(f'  {username}: created.'))
            user.groups.add(group)

        self.stdout.write(self.style.SUCCESS('Node users ready.'))
