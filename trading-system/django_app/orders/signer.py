"""ECDSA signing for OrderTx messages.

CRITICAL: the exact bytes we sign here must match the bytes the Go nodes
verify in consensus.ValidateSignedTx. Both sides serialise the OrderTx
with protobuf and sign/verify sha256(serialised).

If you change anything in this module, mirror it in
node/internal/crypto/ecdsa.go and node/internal/consensus/poa.go.
"""
from __future__ import annotations

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric import ec, utils
from cryptography.hazmat.primitives import hashes

from generated import trading_pb2


def load_priv(pem: str) -> ec.EllipticCurvePrivateKey:
    key = serialization.load_pem_private_key(pem.encode(), password=None)
    if not isinstance(key, ec.EllipticCurvePrivateKey):
        raise ValueError("not an ECDSA private key")
    return key


def pub_der(pem: str) -> bytes:
    pub = serialization.load_pem_public_key(pem.encode())
    return pub.public_bytes(
        encoding=serialization.Encoding.DER,
        format=serialization.PublicFormat.SubjectPublicKeyInfo,
    )


def sign_order_tx(tx: trading_pb2.OrderTx, priv_pem: str, pub_pem: str) -> trading_pb2.SignedOrderTx:
    priv = load_priv(priv_pem)
    tx_bytes = tx.SerializeToString(deterministic=True)
    # ECDSA P-256 over sha256(tx_bytes). The ASN.1 DER signature is what Go's
    # ecdsa.VerifyASN1 expects, so we just pass through Python's default.
    signature = priv.sign(tx_bytes, ec.ECDSA(hashes.SHA256()))
    return trading_pb2.SignedOrderTx(
        tx=tx,
        signature=signature,
        pub_key=pub_der(pub_pem),
    )
