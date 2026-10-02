"""Django settings for the TradeDesk gateway.

The gateway has no database: it keeps no users, keys, balances or orders.
Everything it shows is read from the chain; everything it submits was
signed in the user's browser.
"""
import secrets
import sys
import warnings
from pathlib import Path

from decouple import config, Csv
from django.core.exceptions import ImproperlyConfigured

BASE_DIR = Path(__file__).resolve().parent.parent

DEBUG         = config('DEBUG', default=False, cast=bool)
ALLOWED_HOSTS = config('ALLOWED_HOSTS', default='127.0.0.1,localhost', cast=Csv())

_RUNNING_TESTS = sys.argv[1:2] == ['test']
SECRET_KEY = config('SECRET_KEY', default='')
if SECRET_KEY in ('', 'replace-with-a-long-random-string'):
    if not (DEBUG or _RUNNING_TESTS):
        raise ImproperlyConfigured(
            'SECRET_KEY is not set. Run setup.sh or put a long random SECRET_KEY in backend/.env.')
    SECRET_KEY = secrets.token_urlsafe(50)
    warnings.warn('SECRET_KEY not set — using a temporary random key.')

# CometBFT RPC of the nodes, tried in order (any honest node will do).
NODE_RPC = config('NODE_RPC', default='http://127.0.0.1:26657,http://127.0.0.1:26667,'
                  'http://127.0.0.1:26677,http://127.0.0.1:26687', cast=Csv())
NODE_RPC_TIMEOUT = config('NODE_RPC_TIMEOUT', default=5, cast=float)

# An oracle signer, used only for display prices before the chain has one.
ORACLE_DISPLAY_URL = config('ORACLE_DISPLAY_URL', default='127.0.0.1:8001')

# Gemini — a fixed model version, so answers do not change under us.
GEMINI_API_KEY = config('GEMINI_API_KEY', default='')
GEMINI_MODEL   = config('GEMINI_MODEL', default='gemini-2.5-flash-lite')

INSTALLED_APPS = [
    'django.contrib.staticfiles',
    'rest_framework',
    'gateway',
]

MIDDLEWARE = [
    'django.middleware.security.SecurityMiddleware',
    'django.middleware.common.CommonMiddleware',
    'django.middleware.clickjacking.XFrameOptionsMiddleware',
    'gateway.security.SecurityHeadersMiddleware',
]

ROOT_URLCONF = 'myproject.urls'

TEMPLATES = [{
    'BACKEND': 'django.template.backends.django.DjangoTemplates',
    'DIRS': [BASE_DIR / 'templates'],
    'APP_DIRS': True,
    'OPTIONS': {'context_processors': ['django.template.context_processors.request']},
}]

WSGI_APPLICATION = 'myproject.wsgi.application'

DATABASES = {}   # nothing to store: the chain is the only source of truth

LANGUAGE_CODE = 'en-us'
TIME_ZONE = 'UTC'
USE_I18N = True
USE_TZ = True
STATIC_URL = 'static/'

REST_FRAMEWORK = {
    'DEFAULT_AUTHENTICATION_CLASSES': [],
    'DEFAULT_PERMISSION_CLASSES': ['rest_framework.permissions.AllowAny'],
    'UNAUTHENTICATED_USER': None,
    # Rate limits count the client's own address. X-Forwarded-For is trusted
    # only through this many reverse proxies (0: never), or anyone could
    # dodge the limits by sending a different header each time.
    'NUM_PROXIES': config('NUM_PROXIES', default=0, cast=int),
    'DEFAULT_THROTTLE_RATES': {
        'ai': config('THROTTLE_AI', default='120/hour'),
        'tx': config('THROTTLE_TX', default='600/minute'),
    },
}
