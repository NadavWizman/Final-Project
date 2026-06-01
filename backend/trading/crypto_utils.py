"""
crypto_utils.py — ECDSA helpers for the trading system
=======================================================
Uses the cryptography library (P-256 / SECP256R1)
"""

import base64
import json

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

    order_data must contain: stock, order_type, quantity, nonce
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


# private helper
def _build_message(order_data: dict) -> bytes:
    """Builds the message to sign — key-sorted JSON."""
    payload = {
        "stock":      str(order_data.get("stock", "")),
        "order_type": str(order_data.get("order_type", "")),
        "quantity":   str(order_data.get("quantity", "")),
        "nonce":      str(order_data.get("nonce", "")),
    }
    return json.dumps(payload, sort_keys=True, separators=(',', ':')).encode()
