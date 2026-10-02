from rest_framework.throttling import SimpleRateThrottle


class _ByAddress(SimpleRateThrottle):
    """Rate limit by client IP — there are no logins at the gateway."""

    def get_cache_key(self, request, view):
        return self.cache_format % {'scope': self.scope, 'ident': self.get_ident(request)}


class AIThrottle(_ByAddress):
    """The Gemini-backed endpoints cost money per call."""
    scope = 'ai'


class TxThrottle(_ByAddress):
    """Forwarding transactions to the chain."""
    scope = 'tx'
