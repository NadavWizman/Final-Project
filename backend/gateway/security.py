"""Security headers for every response.

The page holds the user's signing keys, so its Content-Security-Policy
limits where a script may send data: only back to this origin. Even if text
from the chain or the news were ever rendered as HTML, an injected script
could not upload a key to another site (connect-src, img-src, form-action).
The single inline script and the inline handlers need 'unsafe-inline'.
"""

CSP = '; '.join([
    "default-src 'self'",
    "script-src 'self' 'unsafe-inline'",
    "style-src 'self' 'unsafe-inline'",
    "img-src 'self' data:",
    "connect-src 'self'",
    "font-src 'self'",
    "object-src 'none'",
    "base-uri 'none'",
    "form-action 'self'",
    "frame-ancestors 'none'",
])


class SecurityHeadersMiddleware:
    def __init__(self, get_response):
        self.get_response = get_response

    def __call__(self, request):
        response = self.get_response(request)
        response.setdefault('Content-Security-Policy', CSP)
        response.setdefault('Permissions-Policy', 'camera=(), microphone=(), geolocation=()')
        return response
