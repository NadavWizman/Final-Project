#!/usr/bin/env python3
"""
Generate ECDSA P-256 keypairs for the three authority nodes and for demo
users. Writes PEM files into ./keys/ and prints a JSON snippet you can drop
into node configs / Django fixtures.

Run once before the first `docker compose up`:

    python3 scripts/gen_keys.py
"""
from __future__ import annotations

import json
import os
from pathlib import Path

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric import ec


def gen() -> tuple[bytes, bytes]:
    key = ec.generate_private_key(ec.SECP256R1())
    priv_pem = key.private_bytes(
        encoding=serialization.Encoding.PEM,
        format=serialization.PrivateFormat.PKCS8,
        encryption_algorithm=serialization.NoEncryption(),
    )
    pub_pem = key.public_key().public_bytes(
        encoding=serialization.Encoding.PEM,
        format=serialization.PublicFormat.SubjectPublicKeyInfo,
    )
    return priv_pem, pub_pem


def main() -> None:
    out = Path(__file__).resolve().parent.parent / "keys"
    out.mkdir(exist_ok=True)

    authorities = []
    for i, (node_id, port) in enumerate(
        [("node-1", 9001), ("node-2", 9002), ("node-3", 9003)]
    ):
        priv, pub = gen()
        (out / f"{node_id}.key.pem").write_bytes(priv)
        (out / f"{node_id}.pub.pem").write_bytes(pub)
        authorities.append(
            {
                "node_id": node_id,
                "pub_key_pem": pub.decode(),
                "address": f"{node_id}:{port}",
            }
        )

    users = {}
    for user_id in ["alice", "bob"]:
        priv, pub = gen()
        (out / f"user-{user_id}.key.pem").write_bytes(priv)
        (out / f"user-{user_id}.pub.pem").write_bytes(pub)
        users[user_id] = {
            "priv_pem": priv.decode(),
            "pub_pem": pub.decode(),
        }

    bundle = {"authorities": authorities, "users": users}
    (out / "bundle.json").write_text(json.dumps(bundle, indent=2))
    print(f"Wrote keys to {out}/")
    print("Copy the public keys from keys/bundle.json into node configs "
          "and Django fixtures as needed.")


if __name__ == "__main__":
    main()
