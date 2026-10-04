"""Security headers for every response.

The signing keys are not in this page — they live in the TradeDesk Wallet
extension, which shows and signs each transaction itself. The policy below is
defence in depth: page scripts may talk only to this origin (connect-src,
img-src, form-action), no plug-ins, no framing. The single inline script and
the inline handlers need 'unsafe-inline'.
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
