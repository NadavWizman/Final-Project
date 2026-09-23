import os
from pathlib import Path

from django.conf import settings
from django.contrib.auth.models import Group, User
from django.core.management.base import BaseCommand, CommandError

from trading.roles import CONSENSUS_NODES_GROUP

NODE_USERNAMES = ('node1', 'node2', 'node3')
DEFAULT_ENV_FILE = Path(settings.BASE_DIR).parent / 'nodes' / '.env'


def _read_env_file(path):
    values = {}
    if path.exists():
        for line in path.read_text().splitlines():
            line = line.strip()
            if not line or line.startswith('#') or '=' not in line:
                continue
            key, val = line.split('=', 1)
            values[key.strip().removeprefix('export ').strip()] = val.strip().strip('"\'')
    return values


class Command(BaseCommand):
    help = (
        'Creates the three blockchain node users (node1, node2, node3) in the '
        f'"{CONSENSUS_NODES_GROUP}" group, with the passwords from nodes/.env '
        '(NODE1_PASS, NODE2_PASS, NODE3_PASS — environment variables take '
        'precedence). Existing node users get their password synced, so Django '
        'always matches what the nodes present. Safe to run multiple times.'
    )

    def add_arguments(self, parser):
        parser.add_argument('--env-file', default=str(DEFAULT_ENV_FILE),
                            help='File holding NODE1_PASS..NODE3_PASS (default: nodes/.env)')

    def handle(self, *args, **options):
        file_values = _read_env_file(Path(options['env_file']))
        group, _ = Group.objects.get_or_create(name=CONSENSUS_NODES_GROUP)

        for username in NODE_USERNAMES:
            key = f'{username.upper()}_PASS'
            password = os.environ.get(key) or file_values.get(key)
            if not password or password == 'replace-me':
                raise CommandError(
                    f'No password for {username}: set {key} in {options["env_file"]} '
                    f'or the environment (setup.sh generates it).')

            user = User.objects.filter(username=username).first()
            if user:
                user.set_password(password)
                if not user.is_superuser:
                    user.is_staff = False   # nodes need no admin access
                user.save(update_fields=['password', 'is_staff'])
                self.stdout.write(f'  {username}: already exists — password synced.')
            else:
                user = User.objects.create_user(username=username, password=password)
                self.stdout.write(self.style.SUCCESS(f'  {username}: created.'))
            user.groups.add(group)

        self.stdout.write(self.style.SUCCESS('Node users ready.'))
