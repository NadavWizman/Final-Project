"""Verification of consensus votes.

Each consensus node signs an approval of a block with its Ed25519 node key
(see nodes/nodekey.go). execute_order moves money only when at least
QUORUM distinct nodes signed the exact order, block hash and price being
settled. A single compromised node — the Leader included — therefore cannot
settle anything on its own.
"""
import base64
import re

from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey

from .models import NodeKey
from .roles import CONSENSUS_NODES_GROUP

QUORUM = 2
HASH_RE = re.compile(r'[0-9a-f]{64}')


def vote_message(order_id, block_hash, price) -> bytes:
    """Must match voteMessage() in nodes/nodekey.go byte for byte."""
    return f"tradedesk-vote|{order_id}|{block_hash}|{price}".encode()


def count_valid_votes(order_id, block_hash, price, votes) -> int:
    """Number of distinct consensus nodes whose vote signature verifies."""
    if not isinstance(votes, list) or not HASH_RE.fullmatch(str(block_hash or '')):
        return 0
    message = vote_message(order_id, block_hash, price)
    names = {v.get('node') for v in votes if isinstance(v, dict) and isinstance(v.get('node'), str)}
    keys = {
        k.user.username: k.public_key
        for k in NodeKey.objects.filter(user__username__in=names,
                                        user__groups__name=CONSENSUS_NODES_GROUP,
                                        user__is_active=True).select_related('user')
    }
    approved = set()
    for vote in votes:
        if not isinstance(vote, dict):
            continue
        name, sig = vote.get('node'), vote.get('signature')
        if name in approved or name not in keys or not isinstance(sig, str):
            continue
        try:
            Ed25519PublicKey.from_public_bytes(bytes.fromhex(keys[name])).verify(
                base64.b64decode(sig, validate=True), message)
        except Exception:
            continue
        approved.add(name)
    return len(approved)


def valid_public_key(hex_key) -> bool:
    try:
        Ed25519PublicKey.from_public_bytes(bytes.fromhex(hex_key))
        return len(hex_key) == 64
    except Exception:
        return False
