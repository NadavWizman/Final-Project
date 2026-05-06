"""gRPC client wrapper for talking to the Leader node.

Keeps the channel alive for the Django process lifetime. For multi-worker
deployments (gunicorn/uwsgi) each worker gets its own channel.
"""
from __future__ import annotations

from dataclasses import dataclass
from typing import Mapping, Optional

import grpc
from django.conf import settings

from generated import trading_pb2, trading_pb2_grpc


@dataclass
class AccountSnapshot:
    user_id: str
    cash: str
    positions: Mapping[str, str]
    last_nonce: int


class NodeClient:
    _instance: Optional["NodeClient"] = None

    def __init__(self, address: str):
        self.address = address
        self.channel = grpc.insecure_channel(address)
        self.trading = trading_pb2_grpc.TradingServiceStub(self.channel)

    @classmethod
    def default(cls) -> "NodeClient":
        if cls._instance is None:
            cls._instance = cls(settings.LEADER_NODE_ADDR)
        return cls._instance

    def submit_order(self, signed_tx: trading_pb2.SignedOrderTx) -> trading_pb2.SubmitOrderResponse:
        req = trading_pb2.SubmitOrderRequest(signed_tx=signed_tx)
        return self.trading.SubmitOrder(req, timeout=10.0)

    def get_account(self, user_id: str) -> AccountSnapshot:
        resp = self.trading.GetAccount(
            trading_pb2.GetAccountRequest(user_id=user_id), timeout=5.0
        )
        return AccountSnapshot(
            user_id=resp.user_id,
            cash=resp.cash,
            positions=dict(resp.positions),
            last_nonce=resp.last_nonce,
        )
