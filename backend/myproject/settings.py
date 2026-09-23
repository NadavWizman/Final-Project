import secrets
import sys
import warnings
from pathlib import Path

from decouple import config, Csv
from django.core.exceptions import ImproperlyConfigured

BASE_DIR = Path(__file__).resolve().parent.parent

# Read from .env (or environment variables). See .env.example for the full list;
# setup.sh generates backend/.env with a random SECRET_KEY.
DEBUG         = config('DEBUG', default=False, cast=bool)
ALLOWED_HOSTS = config('ALLOWED_HOSTS', default='127.0.0.1,localhost', cast=Csv())

_PLACEHOLDER_KEYS = {'', 'replace-with-a-long-random-string'}
SECRET_KEY = config('SECRET_KEY', default='')
_RUNNING_TESTS = sys.argv[1:2] == ['test']
if SECRET_KEY in _PLACEHOLDER_KEYS:
    if not (DEBUG or _RUNNING_TESTS):
        raise ImproperlyConfigured(
            'SECRET_KEY is not set. Run setup.sh or put a long random SECRET_KEY in backend/.env.')
    # Development/tests only: a per-process random key, never a value published in the repo.
    SECRET_KEY = secrets.token_urlsafe(50)
    warnings.warn('SECRET_KEY not set — using a temporary random key (sessions reset on restart).')

# Oracle gRPC address used by the price_view proxy
ORACLE_URL = config('ORACLE_URL', default='127.0.0.1:8001')

# Gemini API key for the AI news overview feature
GEMINI_API_KEY = config('GEMINI_API_KEY', default='')


# Application definition

INSTALLED_APPS = [
    'django.contrib.admin',
    'django.contrib.auth',
    'django.contrib.contenttypes',
    'django.contrib.sessions',
    'django.contrib.messages',
    'django.contrib.staticfiles',
    'trading',
    'rest_framework',
]

MIDDLEWARE = [
    'django.middleware.security.SecurityMiddleware',
    'django.contrib.sessions.middleware.SessionMiddleware',
    'django.middleware.common.CommonMiddleware',
    'django.middleware.csrf.CsrfViewMiddleware',
    'django.contrib.auth.middleware.AuthenticationMiddleware',
    'django.contrib.messages.middleware.MessageMiddleware',
    'django.middleware.clickjacking.XFrameOptionsMiddleware',
]

ROOT_URLCONF = 'myproject.urls'

TEMPLATES = [
    {
        'BACKEND': 'django.template.backends.django.DjangoTemplates',
        'DIRS': [BASE_DIR / 'templates'],
        'APP_DIRS': True,
        'OPTIONS': {
            'context_processors': [
                'django.template.context_processors.request',
                'django.contrib.auth.context_processors.auth',
                'django.contrib.messages.context_processors.messages',
            ],
        },
    },
]

WSGI_APPLICATION = 'myproject.wsgi.application'


# Database
# https://docs.djangoproject.com/en/6.0/ref/settings/#databases

DATABASES = {
    'default': {
        'ENGINE': 'django.db.backends.sqlite3',
        'NAME': BASE_DIR / 'db.sqlite3',
        'OPTIONS': {
            # SQLite ignores SELECT ... FOR UPDATE. IMMEDIATE transactions take the
            # write lock up front, so two settlements can never interleave their
            # read-modify-write of the same wallet; `timeout` makes a contending
            # writer wait instead of failing with "database is locked".
            'transaction_mode': 'IMMEDIATE',
            'timeout': 20,
        },
    }
}


# Password validation
# https://docs.djangoproject.com/en/6.0/ref/settings/#auth-password-validators

AUTH_PASSWORD_VALIDATORS = [
    {
        'NAME': 'django.contrib.auth.password_validation.UserAttributeSimilarityValidator',
    },
    {
        'NAME': 'django.contrib.auth.password_validation.MinimumLengthValidator',
    },
    {
        'NAME': 'django.contrib.auth.password_validation.CommonPasswordValidator',
    },
    {
        'NAME': 'django.contrib.auth.password_validation.NumericPasswordValidator',
    },
]


# Internationalization
# https://docs.djangoproject.com/en/6.0/topics/i18n/

LANGUAGE_CODE = 'en-us'

TIME_ZONE = 'UTC'

USE_I18N = True

USE_TZ = True


# Static files (CSS, JavaScript, Images)
# https://docs.djangoproject.com/en/6.0/howto/static-files/

STATIC_URL = 'static/'
REST_FRAMEWORK = {
    'DEFAULT_AUTHENTICATION_CLASSES': [
        'trading.auth.SilentBasicAuthentication',
        'rest_framework.authentication.SessionAuthentication',
    ],
    'DEFAULT_PERMISSION_CLASSES': [
        'rest_framework.permissions.IsAuthenticated',
    ],
}
