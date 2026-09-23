"""
crypto_utils.py — ECDSA helpers for the trading system
=======================================================
Uses the cryptography library (P-256 / SECP256R1)
"""

import base64
import json
from decimal import Decimal

from cryptography.hazmat.backends import default_backend
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec


# generate a key pair
def generate_key_pair():
    """
    Returns (private_pem, public_pem) — two PEM strings.
    Called once when a user registers.
    """
    private_key = ec.generate_private_key(ec.SECP256R1(), default_backend())
    public_key = private_key.public_key()

    private_pem = private_key.private_bytes(
        encoding=serialization.Encoding.PEM,
        format=serialization.PrivateFormat.PKCS8,
        encryption_algorithm=serialization.NoEncryption()
    ).decode()

    public_pem = public_key.public_bytes(
        encoding=serialization.Encoding.PEM,
        format=serialization.PublicFormat.SubjectPublicKeyInfo
    ).decode()

    return private_pem, public_pem


# sign an order
def sign_order(private_pem: str, order_data: dict) -> str:
    """
    Signs the order fields with the user's private key.
    Returns a Base64-encoded signature.

    order_data holds the fields in SIGNED_ORDER_FIELDS (missing ones sign as "")
    """
    private_key = serialization.load_pem_private_key(
        private_pem.encode(),
        password=None,
        backend=default_backend()
    )

    # the message to sign — critical fields in deterministic order
    message = _build_message(order_data)

    # DER signature with SHA-256
    signature_bytes = private_key.sign(message, ec.ECDSA(hashes.SHA256()))
    return base64.b64encode(signature_bytes).decode()


# verify a signature
def verify_signature(public_pem: str, order_data: dict, signature_b64: str) -> bool:
    """
    Verifies that the signature belongs to the given public key.
    Returns True if valid, False otherwise.
    """
    try:
        public_key = serialization.load_pem_public_key(
            public_pem.encode(),
            backend=default_backend()
        )
        message = _build_message(order_data)
        signature_bytes = base64.b64decode(signature_b64)
        public_key.verify(signature_bytes, message, ec.ECDSA(hashes.SHA256()))
        return True
    except Exception:
        return False


# ── canonical order payload ─────────────────────────────────────
# Every field that decides what a trade does or how much money moves is signed,
# so none of them can be changed after the user authorised the order.
SIGNED_ORDER_FIELDS = (
    "stock", "order_type", "trade_type", "quantity", "nonce", "leverage",
    "limit_price", "position_id", "option_contract_type", "option_strike",
    "option_expiry",
)

_FOUR_DP = Decimal("0.0001")


def _canon(value) -> str:
    """Deterministic string form of a field value (Decimals fixed to 4 dp)."""
    if value is None:
        return ""
    if isinstance(value, (Decimal, float)):
        return format(Decimal(str(value)).quantize(_FOUR_DP), "f")
    if hasattr(value, "isoformat"):
        return value.isoformat()
    return str(value)


def order_signing_payload(order) -> dict:
    """The dict that is signed for an Order model instance."""
    return {
        "stock":                order.stock_id,
        "order_type":           order.order_type,
        "trade_type":           order.trade_type,
        "quantity":             order.quantity,
        "nonce":                order.nonce,
        "leverage":             order.leverage,
        "limit_price":          order.limit_price,
        "position_id":          order.position_id,
        "option_contract_type": order.option_contract_type,
        "option_strike":        order.option_strike,
        "option_expiry":        order.option_expiry,
    }


def canonical_order_message(order) -> str:
    """The exact message signed for an order — shared with the nodes verbatim."""
    return _build_message(order_signing_payload(order)).decode()


# private helper
def _build_message(order_data: dict) -> bytes:
    """Builds the message to sign — key-sorted compact JSON of every signed field."""
    payload = {k: _canon(order_data.get(k)) for k in SIGNED_ORDER_FIELDS}
    return json.dumps(payload, sort_keys=True, separators=(',', ':')).encode()
