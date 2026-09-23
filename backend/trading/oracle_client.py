"""Shared gRPC client for the Oracle service.

One channel per process, created on first use and reused: gRPC channels are
thread-safe and multiplex requests, whereas opening a channel per request
(as before) leaked sockets and paid a new connection every time.
"""
import os
import sys
import threading

from django.conf import settings

_ORACLE_DIR = os.path.normpath(os.path.join(os.path.dirname(__file__), '..', '..', 'oracle_service'))
_lock = threading.Lock()
_client = None


def get_stub():
    """(stub, oracle_pb2) for the configured ORACLE_URL."""
    global _client
    with _lock:
        if _client is None:
            if _ORACLE_DIR not in sys.path:
                sys.path.insert(0, _ORACLE_DIR)
            import grpc             # type: ignore
            import oracle_pb2       # type: ignore
            import oracle_pb2_grpc  # type: ignore
            channel = grpc.insecure_channel(getattr(settings, 'ORACLE_URL', '127.0.0.1:8001'))
            _client = (oracle_pb2_grpc.OracleServiceStub(channel), oracle_pb2)
        return _client
