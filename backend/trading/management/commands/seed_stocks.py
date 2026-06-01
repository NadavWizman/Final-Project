from django.core.management.base import BaseCommand
from trading.models import Stock

# Full name mapping for every ticker in the SP500 whitelist (views.py)
SP500_STOCKS = {
    'AAPL':  'Apple Inc.',
    'MSFT':  'Microsoft Corporation',
    'GOOGL': 'Alphabet Inc.',
    'AMZN':  'Amazon.com Inc.',
    'NVDA':  'NVIDIA Corporation',
    'META':  'Meta Platforms Inc.',
    'TSLA':  'Tesla Inc.',
    'BRK.B': 'Berkshire Hathaway Inc.',
    'UNH':   'UnitedHealth Group Inc.',
    'LLY':   'Eli Lilly and Company',
    'JPM':   'JPMorgan Chase & Co.',
    'XOM':   'Exxon Mobil Corporation',
    'V':     'Visa Inc.',
    'AVGO':  'Broadcom Inc.',
    'PG':    'Procter & Gamble Co.',
    'MA':    'Mastercard Inc.',
    'HD':    'The Home Depot Inc.',
    'COST':  'Costco Wholesale Corporation',
    'MRK':   'Merck & Co. Inc.',
    'CVX':   'Chevron Corporation',
    'ABBV':  'AbbVie Inc.',
    'ORCL':  'Oracle Corporation',
    'WMT':   'Walmart Inc.',
    'BAC':   'Bank of America Corporation',
    'KO':    'The Coca-Cola Company',
    'PFE':   'Pfizer Inc.',
    'NFLX':  'Netflix Inc.',
    'CRM':   'Salesforce Inc.',
    'AMD':   'Advanced Micro Devices Inc.',
    'TMO':   'Thermo Fisher Scientific Inc.',
    'ACN':   'Accenture plc',
    'MCD':   "McDonald's Corporation",
    'LIN':   'Linde plc',
    'CSCO':  'Cisco Systems Inc.',
    'TXN':   'Texas Instruments Inc.',
    'ADBE':  'Adobe Inc.',
    'DHR':   'Danaher Corporation',
    'NEE':   'NextEra Energy Inc.',
    'NKE':   'Nike Inc.',
    'INTC':  'Intel Corporation',
    'PM':    'Philip Morris International Inc.',
    'UPS':   'United Parcel Service Inc.',
    'AMGN':  'Amgen Inc.',
    'HON':   'Honeywell International Inc.',
    'LOW':   "Lowe's Companies Inc.",
    'IBM':   'International Business Machines Corporation',
    'SBUX':  'Starbucks Corporation',
    'QCOM':  'Qualcomm Inc.',
    'GE':    'GE Aerospace',
    'CAT':   'Caterpillar Inc.',
    'GS':    'The Goldman Sachs Group Inc.',
    'MS':    'Morgan Stanley',
    'BLK':   'BlackRock Inc.',
    'SPGI':  'S&P Global Inc.',
}


class Command(BaseCommand):
    help = 'Populates the Stock table with S&P 500 tickers. Safe to run multiple times.'

    def handle(self, *args, **options):
        created = 0
        for ticker, name in SP500_STOCKS.items():
            _, was_created = Stock.objects.get_or_create(
                ticker=ticker,
                defaults={'name': name},
            )
            if was_created:
                created += 1

        total = len(SP500_STOCKS)
        self.stdout.write(self.style.SUCCESS(
            f'Done. {created} stocks created, {total - created} already existed ({total} total).'
        ))
