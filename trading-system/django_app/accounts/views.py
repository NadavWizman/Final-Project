from rest_framework import status
from rest_framework.decorators import api_view, permission_classes
from rest_framework.permissions import IsAuthenticated
from rest_framework.response import Response

from orders.grpc_client import NodeClient


@api_view(["GET"])
@permission_classes([IsAuthenticated])
def portfolio(request):
    """Live portfolio pulled from the Leader node.

    We prefer the node over the local Django cache for correctness — the
    node's state is authoritative.
    """
    user = request.user
    client = NodeClient.default()
    snapshot = client.get_account(user.external_id)
    return Response(
        {
            "user_id": user.external_id,
            "cash": snapshot.cash,
            "positions": dict(snapshot.positions),
            "last_nonce": snapshot.last_nonce,
        },
        status=status.HTTP_200_OK,
    )
