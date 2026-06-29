from rest_framework.authentication import BasicAuthentication


class SilentBasicAuthentication(BasicAuthentication):
    """BasicAuthentication that omits the WWW-Authenticate header.
    Without that header, browsers won't show their native credential dialog."""

    def authenticate_header(self, request):
        return None
