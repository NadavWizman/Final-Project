"""
Management command: create_test_users
Creates N test users (default 1000) for load testing.

Usage:
    python manage.py create_test_users          # creates testuser_0001..testuser_1000
    python manage.py create_test_users --count 500
    python manage.py create_test_users --flush  # delete all test users first

Credentials: username=testuser_NNNN  password=testpass123
Each user gets:
  - $100,000 wallet
  - ECDSA key pair
  - 100 shares each of AAPL, MSFT, GOOGL (so sell actions work immediately)
"""

from django.core.management.base import BaseCommand
from django.db import transaction
from decimal import Decimal


SEED_TICKERS   = ['AAPL', 'MSFT', 'GOOGL']
SEED_QUANTITY  = Decimal('100')
WALLET_BALANCE = Decimal('100000.00')
PASSWORD       = 'testpass123'


class Command(BaseCommand):
    help = 'Create test users for load testing (default: 1000)'

    def add_arguments(self, parser):
        parser.add_argument('--count', type=int, default=1000,
                            help='Number of test users to create (default: 1000)')
        parser.add_argument('--flush', action='store_true',
                            help='Delete all existing testuser_NNNN accounts first')

    def handle(self, *args, **options):
        from django.contrib.auth.models import User
        from trading.models import Wallet, UserProfile, Stock, Position
        from trading.crypto_utils import generate_key_pair

        count = options['count']

        if options['flush']:
            deleted, _ = User.objects.filter(username__startswith='testuser_').delete()
            self.stdout.write(self.style.WARNING(f'Flushed {deleted} existing test users.'))

        # Ensure seed stocks exist
        for ticker in SEED_TICKERS:
            Stock.objects.get_or_create(ticker=ticker, defaults={'name': ticker})

        created = skipped = 0

        for i in range(1, count + 1):
            username = f'testuser_{i:04d}'

            if User.objects.filter(username=username).exists():
                skipped += 1
                continue

            try:
                with transaction.atomic():
                    user = User.objects.create_user(username=username, password=PASSWORD)

                    Wallet.objects.create(user=user, balance=WALLET_BALANCE)

                    private_pem, public_pem = generate_key_pair()
                    UserProfile.objects.create(
                        user=user,
                        ecdsa_private_key=private_pem,
                        ecdsa_public_key=public_pem,
                    )

                    for ticker in SEED_TICKERS:
                        stock = Stock.objects.get(ticker=ticker)
                        Position.objects.create(
                            user=user, stock=stock, quantity=SEED_QUANTITY
                        )

                created += 1
            except Exception as e:
                self.stdout.write(self.style.ERROR(f'  Failed {username}: {e}'))
                continue

            if created % 100 == 0:
                self.stdout.write(f'  Created {created}/{count}...')

        self.stdout.write(self.style.SUCCESS(
            f'\nDone. {created} users created, {skipped} already existed.'
        ))
        self.stdout.write(f'  Username pattern : testuser_0001 … testuser_{count:04d}')
        self.stdout.write(f'  Password         : {PASSWORD}')
        self.stdout.write(f'  Wallet balance   : ${WALLET_BALANCE:,.2f}')
        self.stdout.write(f'  Seed holdings    : 100 shares each of {", ".join(SEED_TICKERS)}')
